package main

import (
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DNS stub: relay UDP sederhana 127.0.0.1:5353 -> upstream (default 8.8.8.8).
// iptables me-REDIRECT semua UDP/53 ke sini (kecuali uid 0), sehingga DNS
// aplikasi tidak bisa dibajak/dirusak DNS ISP — selalu jawab dari upstream
// yang kita pilih. Backend sendiri jalan sebagai root (uid 0) sehingga
// koneksi ke upstream-nya tidak kena REDIRECT (langsung).
//
// Paket diteruskan apa adanya (cocok untuk query/respons jenis apa pun). Parsing
// DNS hanya minimal, untuk tiga hal agar koneksi terasa tanpa delay:
//   - cache jawaban (TTL asli, dibatasi) -> nama yang sama tak ditanya ulang
//   - AAAA dijawab kosong seketika bila IPv6 diblokir (tak ada gunanya menunggu)
//   - query yang tak berbalas dikirim ulang cepat (500ms), bukan menunggu 5 detik

const dnsStubPort = 5353

// dnsStubUp = 1 bila stub benar-benar sedang listen. Rule REDIRECT UDP/53 hanya
// dipasang bila stub siap (kalau port 5353 bentrok, DNS tidak boleh dimatikan).
var dnsStubUp int32

func dnsStubReady() bool { return atomic.LoadInt32(&dnsStubUp) == 1 }

func (a *App) startDNSStub() {
	go a.dnsStubLoop()
}

func (a *App) dnsStubLoop() {
	pc, err := net.ListenPacket("udp", "127.0.0.1:"+itoa(dnsStubPort))
	if err != nil {
		a.logErrf("dns stub gagal listen: %v (DNS hijack mode iptables dinonaktifkan)", err)
		return
	}
	atomic.StoreInt32(&dnsStubUp, 1)
	defer func() {
		atomic.StoreInt32(&dnsStubUp, 0)
		pc.Close()
	}()
	a.logf("dns stub listen 127.0.0.1:%d", dnsStubPort)
	buf := make([]byte, 4096)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond) // jangan busy-loop bila error berulang
			continue
		}
		if !a.cfg().DNSHijack {
			continue
		}
		q := make([]byte, n)
		copy(q, buf[:n])
		go a.forwardDNS(pc, addr, q)
	}
}

const (
	dnsCacheMax    = 512
	dnsCacheMinTTL = 5 * time.Second // TTL lebih kecil dari ini tidak di-cache
	dnsCacheMaxTTL = 2 * time.Minute // batas atas agar jawaban tak basah terlalu lama
	dnsRetryAfter  = 500 * time.Millisecond
	dnsTimeout     = 3 * time.Second
)

type dnsCacheEntry struct {
	resp   []byte
	expire time.Time
}

var (
	dnsCacheMu sync.Mutex
	dnsCache   = map[string]dnsCacheEntry{}
)

func dnsCacheGet(key string) []byte {
	dnsCacheMu.Lock()
	defer dnsCacheMu.Unlock()
	e, ok := dnsCache[key]
	if !ok {
		return nil
	}
	if time.Now().After(e.expire) {
		delete(dnsCache, key)
		return nil
	}
	out := make([]byte, len(e.resp))
	copy(out, e.resp)
	return out
}

func dnsCachePut(key string, resp []byte, ttl time.Duration) {
	dnsCacheMu.Lock()
	defer dnsCacheMu.Unlock()
	if len(dnsCache) >= dnsCacheMax {
		now := time.Now()
		for k, e := range dnsCache {
			if now.After(e.expire) {
				delete(dnsCache, k)
			}
		}
		for k := range dnsCache {
			if len(dnsCache) < dnsCacheMax {
				break
			}
			delete(dnsCache, k)
		}
	}
	c := make([]byte, len(resp))
	copy(c, resp)
	dnsCache[key] = dnsCacheEntry{resp: c, expire: time.Now().Add(ttl)}
}

// dnsQuestion membaca query standar (1 pertanyaan). key = nama (huruf kecil) + tipe +
// kelas; qend = offset akhir bagian pertanyaan. ok=false bila bukan query standar
// (paket tetap diteruskan apa adanya, hanya tanpa cache/trik di atas).
func dnsQuestion(q []byte) (key string, qtype uint16, qend int, ok bool) {
	if len(q) < 12 || q[2]&0x80 != 0 || q[2]&0x78 != 0 {
		return "", 0, 0, false
	}
	if binary.BigEndian.Uint16(q[4:6]) != 1 {
		return "", 0, 0, false
	}
	i := 12
	for {
		if i >= len(q) {
			return "", 0, 0, false
		}
		l := int(q[i])
		if l == 0 {
			i++
			break
		}
		if l&0xC0 != 0 {
			return "", 0, 0, false
		}
		i += 1 + l
	}
	if i+4 > len(q) {
		return "", 0, 0, false
	}
	kb := make([]byte, i+4-12)
	copy(kb, q[12:i+4])
	for j := 0; j < i-12; j++ { // hanya bagian nama; panjang label (<=63) tak kena
		if c := kb[j]; c >= 'A' && c <= 'Z' {
			kb[j] = c + 32
		}
	}
	return string(kb), binary.BigEndian.Uint16(q[i : i+2]), i + 4, true
}

// dnsEmptyReply: jawaban NOERROR tanpa record (untuk AAAA saat IPv6 diblokir).
func dnsEmptyReply(q []byte, qend int) []byte {
	r := make([]byte, qend)
	copy(r, q[:qend])
	r[2] = 0x80 | (q[2] & 0x01) // QR=1, opcode 0, salin RD
	r[3] = 0x80                 // RA=1, RCODE=0
	for i := 6; i < 12; i++ {   // AN/NS/AR = 0
		r[i] = 0
	}
	return r
}

// dnsSkipName melompati nama (label / pointer kompresi) mulai offset i.
// Mengembalikan offset setelah nama, atau -1 bila paket rusak.
func dnsSkipName(b []byte, i int) int {
	for {
		if i >= len(b) {
			return -1
		}
		l := int(b[i])
		switch {
		case l == 0:
			return i + 1
		case l&0xC0 == 0xC0:
			if i+2 > len(b) {
				return -1
			}
			return i + 2
		case l&0xC0 != 0:
			return -1
		default:
			i += 1 + l
		}
	}
}

// dnsRespTTL: TTL terkecil di bagian jawaban. ok=false bila respons tak layak
// di-cache (bukan NOERROR, jawaban kosong, terpotong/TC, rusak, atau TTL terlalu kecil).
func dnsRespTTL(r []byte) (time.Duration, bool) {
	if len(r) < 12 || r[2]&0x80 == 0 || r[2]&0x02 != 0 || r[3]&0x0F != 0 {
		return 0, false
	}
	qd := int(binary.BigEndian.Uint16(r[4:6]))
	an := int(binary.BigEndian.Uint16(r[6:8]))
	if an == 0 {
		return 0, false
	}
	i := 12
	for n := 0; n < qd; n++ {
		i = dnsSkipName(r, i)
		if i < 0 || i+4 > len(r) {
			return 0, false
		}
		i += 4
	}
	min := uint32(0xFFFFFFFF)
	for n := 0; n < an; n++ {
		i = dnsSkipName(r, i)
		if i < 0 || i+10 > len(r) {
			return 0, false
		}
		ttl := binary.BigEndian.Uint32(r[i+4 : i+8])
		rdlen := int(binary.BigEndian.Uint16(r[i+8 : i+10]))
		i += 10 + rdlen
		if i > len(r) {
			return 0, false
		}
		if ttl < min {
			min = ttl
		}
	}
	d := time.Duration(min) * time.Second
	if min > uint32(dnsCacheMaxTTL/time.Second) {
		d = dnsCacheMaxTTL
	}
	if d < dnsCacheMinTTL {
		return 0, false
	}
	return d, true
}

// dnsExchange: kirim query ke upstream; bila belum ada balasan setelah dnsRetryAfter,
// kirim ulang lewat soket baru (menangani paket UDP hilang di jaringan seluler).
// Balasan pertama yang valid (ID cocok) menang. nil bila semua gagal.
func dnsExchange(q []byte, up string) []byte {
	ch := make(chan []byte, 2) // buffered: goroutine tak pernah tertahan
	ask := func() {
		c, err := net.DialTimeout("udp", up, 2*time.Second)
		if err != nil {
			ch <- nil
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(dnsTimeout))
		if _, err := c.Write(q); err != nil {
			ch <- nil
			return
		}
		buf := make([]byte, 4096)
		for {
			n, err := c.Read(buf)
			if err != nil || n == 0 {
				ch <- nil
				return
			}
			if n >= 12 && buf[0] == q[0] && buf[1] == q[1] && buf[2]&0x80 != 0 {
				out := make([]byte, n)
				copy(out, buf[:n])
				ch <- out
				return
			}
		}
	}
	go ask()
	pending := 1
	retried := false
	timer := time.NewTimer(dnsRetryAfter)
	defer timer.Stop()
	for pending > 0 {
		select {
		case r := <-ch:
			pending--
			if r != nil {
				return r
			}
			if !retried { // gagal cepat (mis. soket error): coba sekali lagi segera
				retried = true
				pending++
				go ask()
			}
		case <-timer.C:
			if !retried {
				retried = true
				pending++
				go ask()
			}
		}
	}
	return nil
}

func (a *App) forwardDNS(pc net.PacketConn, addr net.Addr, q []byte) {
	st := a.cfg()
	up := strings.TrimSpace(st.DNSUpstream)
	if up == "" {
		up = "8.8.8.8"
	}
	if !strings.Contains(up, ":") {
		up += ":53"
	}
	key, qtype, qend, ok := dnsQuestion(q)
	if ok {
		// IPv6 diblokir -> jawaban AAAA tak berguna; balas kosong seketika agar
		// aplikasi langsung pakai IPv4 (tanpa menunggu upstream).
		if qtype == 28 && st.BlockIPv6 {
			_, _ = pc.WriteTo(dnsEmptyReply(q, qend), addr)
			return
		}
		key = up + "|" + key // ganti upstream -> cache otomatis tak terpakai
		if r := dnsCacheGet(key); r != nil && len(r) >= qend {
			copy(r[0:2], q[0:2])         // ID query ini
			copy(r[12:qend], q[12:qend]) // pertanyaan persis (huruf besar/kecil 0x20)
			_, _ = pc.WriteTo(r, addr)
			return
		}
	}
	resp := dnsExchange(q, up)
	if resp == nil {
		return
	}
	if ok {
		if ttl, cacheable := dnsRespTTL(resp); cacheable {
			dnsCachePut(key, resp, ttl)
		}
	}
	_, _ = pc.WriteTo(resp, addr)
}

// mihomoDNSReady: true bila DNS mihomo (127.0.0.1:mihomoDNSPort) menjawab query.
// Dipakai sebelum REDIRECT UDP/53 diarahkan ke sana, agar DNS tak pernah mati.
func mihomoDNSReady() bool {
	// query A untuk "ttc-probe.test" (fake-ip menjawab instan tanpa upstream)
	q := []byte{0x7a, 0x31, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	for _, l := range []string{"ttc-probe", "test"} {
		q = append(q, byte(len(l)))
		q = append(q, l...)
	}
	q = append(q, 0x00, 0x00, 0x01, 0x00, 0x01)
	c, err := net.DialTimeout("udp", "127.0.0.1:"+itoa(mihomoDNSPort), time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(700 * time.Millisecond))
	if _, err := c.Write(q); err != nil {
		return false
	}
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	return err == nil && n >= 12 && buf[0] == q[0] && buf[1] == q[1] && buf[2]&0x80 != 0
}
