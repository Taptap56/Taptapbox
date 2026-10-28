// badvpn.go — relay UDP via BadVPN udpgw untuk server SSH.
//
// Masalah: outbound SSH di sing-box/mihomo tidak mendukung UDP, jadi
// trafik UDP aplikasi (game, QUIC) yang dirutekan ke server SSH selalu
// gagal. Relay ini menutup lubang itu TANPA mengganti mesin TUN core:
//
//   aplikasi --UDP--> core (TUN/TProxy) --rule UDP--> SOCKS5 lokal
//     (daemon, port acak 127.0.0.1) --udpgw lewat TCP--> core mixed
//     (SOCKS CONNECT 127.0.0.1:<udpgwPort>) --rule TCP--> outbound SSH
//     --terowongan SSH (lewat jembatan payload bila ada)--> udpgw server
//     --UDP--> internet, dan sebaliknya.
//
// Syarat yang TIDAK bisa ditawar: server SSH harus menjalankan daemon
// badvpn-udpgw (umumnya listen 127.0.0.1:7300 di sisi server). Bila
// server tidak menjalankannya, dial udpgw gagal dan log menjelaskan
// hal itu secara eksplisit; paket UDP di-drop (sama seperti hari ini,
// bukan regresi — UDP lewat SSH memang sudah gagal sebelumnya).
//
// Relay HANYA aktif bila: toggle BadVPN nyala + mode OTOMATIS (bukan
// config manual) + server terpilih berprotokol SSH. Bila server terpilih
// bukan SSH, UDP tetap ditangani native oleh core (VMESS/VLESS/dll
// sudah mendukung UDP) dan relay tidak dipakai walau listener-nya jalan.
// Di mode iptables (REDIRECT TCP saja), UDP tidak lewat core sehingga
// relay tidak berlaku — itu perilaku mode tersebut, bukan bug relay.
// Port SOCKS/UDP relay dipilih acak (bind :0) agar tidak tabrakan
// dengan jembatan SSH (15411+) maupun port lain; config core yang
// di-generate membaca port itu dari relay yang sudah jalan.

package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const badvpnTag = "taptap-udp-badvpn"

// badvpnDefaultPort = port udpgw di sisi server bila user tidak mengisi.
const badvpnDefaultPort = 7300

// badvpnRelay = server SOCKS5-UDP lokal + client udpgw + pemetaan aliran.
type badvpnRelay struct {
	a         *App
	socksPort int
	udpPort   int
	udpgwPort int
	mixedPort int
	sshID     string // server SSH yang dilayani (ganti server = relay baru)

	tcpLn   net.Listener
	udpConn *net.UDPConn
	stopCh  chan struct{}
	stopped int32

	mu            sync.Mutex // melindungi flows/rev/nextID
	flows         map[string]uint16
	rev           map[uint16]bvFlow
	nextID        uint16
	unknownLogged bool

	clientMu     sync.Mutex
	client       *udpgwClient
	dialing      bool
	lastDialFail time.Time
	lastFailLog  time.Time
	clientUp     bool
}

type bvFlow struct {
	src     *net.UDPAddr
	dstIP   net.IP
	dstPort int
}

// badvpnUdpgwPort: port udpgw server dari pengaturan (0 = default 7300).
func badvpnUdpgwPort(s Settings) int {
	if s.BadvpnUdpgwPort > 0 {
		return s.BadvpnUdpgwPort
	}
	return badvpnDefaultPort
}

// startBadvpn menyalakan relay bila syarat terpenuhi; bila tidak, relay
// lama (bila ada) dimatikan dan fungsi diam-diam tidak mengaktifkan.
// Dipanggil dari startCore SEBELUM core di-generate, agar config core
// bisa memakai port relay. Aman dipanggil berulang (idempotent).
func (a *App) startBadvpn() {
	if a.store == nil {
		return
	}
	a.store.mu.RLock()
	st := a.store.Settings
	sel := a.store.findServer(st.SelectedID)
	a.store.mu.RUnlock()
	if !st.BadvpnEnabled || st.ManualConfig || sel == nil || !sel.Enabled || sel.Proto != "ssh" {
		a.stopBadvpn()
		return
	}
	// Relay lama yang masih cocok (server & port udpgw sama) dipakai
	// terus — penting agar startCore ganda ("core sudah berjalan")
	// tidak membunuh relay yang sedang dipakai core yang jalan.
	a.bvMu.Lock()
	if a.bv != nil && a.bv.sshID == sel.ID && a.bv.udpgwPort == badvpnUdpgwPort(st) {
		a.bvMu.Unlock()
		return
	}
	a.bvMu.Unlock()
	a.stopBadvpn()
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		a.logWarnf("badvpn: gagal listen SOCKS: %v", err)
		return
	}
	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		_ = tcpLn.Close()
		return
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		_ = tcpLn.Close()
		a.logWarnf("badvpn: gagal listen UDP: %v", err)
		return
	}
	r := &badvpnRelay{
		a:         a,
		socksPort: tcpLn.Addr().(*net.TCPAddr).Port,
		udpPort:   udpConn.LocalAddr().(*net.UDPAddr).Port,
		udpgwPort: badvpnUdpgwPort(st),
		mixedPort: a.mixedSetting(),
		sshID:     sel.ID,
		tcpLn:     tcpLn,
		udpConn:   udpConn,
		stopCh:    make(chan struct{}),
		flows:     map[string]uint16{},
		rev:       map[uint16]bvFlow{},
		nextID:    1,
	}
	a.bvMu.Lock()
	a.bv = r
	a.bvMu.Unlock()
	go r.acceptLoop()
	go r.udpLoop()
	a.logf("badvpn: aktif untuk SSH %q (SOCKS 127.0.0.1:%d, UDP 127.0.0.1:%d, udpgw server :%d)", sel.Name, r.socksPort, r.udpPort, r.udpgwPort)
}

// stopBadvpn mematikan relay (bila ada). Dipanggil dari stopCore
// SETELAH core berhenti, dan dari startBadvpn sebelum bind ulang.
func (a *App) stopBadvpn() {
	a.bvMu.Lock()
	r := a.bv
	a.bvMu.Unlock()
	if r != nil {
		r.stop() // set stopped dulu agar reader tidak me-log "putus" palsu
	}
	a.bvMu.Lock()
	if a.bv == r {
		a.bv = nil
	}
	a.bvMu.Unlock()
}

func (r *badvpnRelay) stop() {
	if !atomic.CompareAndSwapInt32(&r.stopped, 0, 1) {
		return
	}
	close(r.stopCh)
	_ = r.tcpLn.Close()
	_ = r.udpConn.Close()
	r.clientMu.Lock()
	c := r.client
	r.client = nil
	r.clientMu.Unlock()
	if c != nil {
		c.close()
	}
}

// badvpnCfg: port relay untuk generator config. ok=false bila relay
// tidak jalan (config di-generate tanpa aturan BadVPN sama sekali).
func (a *App) badvpnCfg() (socksPort, udpgwPort int, ok bool) {
	a.bvMu.Lock()
	defer a.bvMu.Unlock()
	if a.bv == nil {
		return 0, 0, false
	}
	return a.bv.socksPort, a.bv.udpgwPort, true
}

// badvpnSelectedSSH: server terpilih berprotokol SSH yang berhasil
// di-build (tag outbound-nya). Dipakai generator config; pemanggil
// harus memegang store.mu (read), seperti buildServers.
func (a *App) badvpnSelectedSSH(built []builtServer) (tag string, ok bool) {
	sel := a.store.findServer(a.store.Settings.SelectedID)
	if sel == nil || sel.Proto != "ssh" {
		return "", false
	}
	for _, b := range built {
		if b.ID == sel.ID && b.Ob != nil {
			return b.Tag, true
		}
	}
	return "", false
}

// ---------- server SOCKS5 (hanya UDP ASSOCIATE) ----------

func (r *badvpnRelay) acceptLoop() {
	for {
		c, err := r.tcpLn.Accept()
		if err != nil {
			return // listener ditutup saat stop
		}
		go r.handleSocksConn(c)
	}
}

func (r *badvpnRelay) handleSocksConn(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	// Handshake: VER, NMETHODS, METHODS... -> pilih "tanpa auth" (0x00).
	hdr := make([]byte, 2)
	if _, err := readFull(c, hdr); err != nil {
		return
	}
	if hdr[0] != 0x05 || hdr[1] == 0 {
		return
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := readFull(c, methods); err != nil {
		return
	}
	noAuth := false
	for _, m := range methods {
		if m == 0x00 {
			noAuth = true
		}
	}
	if !noAuth {
		_, _ = c.Write([]byte{0x05, 0xff})
		return
	}
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	// Request: VER, CMD, RSV, ATYP, ADDR, PORT.
	req, err := readSocksRequest(c)
	if err != nil {
		return
	}
	if req.cmd != 0x03 { // hanya UDP ASSOCIATE; CONNECT dkk ditolak
		_, _ = c.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	// Balasan sukses: alamat relay UDP (127.0.0.1:udpPort).
	rep := []byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, byte(r.udpPort >> 8), byte(r.udpPort)}
	if _, err := c.Write(rep); err != nil {
		return
	}
	// Tahan koneksi selama sesi UDP core hidup; core menutupnya saat
	// sesi berakhir. Deadline dihapus agar sesi game panjang tidak putus.
	_ = c.SetDeadline(time.Time{})
	buf := make([]byte, 1)
	for {
		_, err := c.Read(buf)
		if err != nil {
			return
		}
	}
}

type socksRequest struct {
	cmd  byte
	atyp byte
	host string
	port int
}

func readSocksRequest(c net.Conn) (socksRequest, error) {
	var r socksRequest
	hdr := make([]byte, 4)
	if _, err := readFull(c, hdr); err != nil {
		return r, err
	}
	if hdr[0] != 0x05 {
		return r, fmt.Errorf("badvpn: versi SOCKS %d", hdr[0])
	}
	r.cmd, r.atyp = hdr[1], hdr[3]
	switch r.atyp {
	case 0x01:
		b := make([]byte, 6)
		if _, err := readFull(c, b); err != nil {
			return r, err
		}
		r.host = net.IP(b[0:4]).String()
		r.port = int(binary.BigEndian.Uint16(b[4:6]))
	case 0x03:
		lb := make([]byte, 1)
		if _, err := readFull(c, lb); err != nil {
			return r, err
		}
		b := make([]byte, int(lb[0])+2)
		if _, err := readFull(c, b); err != nil {
			return r, err
		}
		r.host = string(b[:len(b)-2])
		r.port = int(binary.BigEndian.Uint16(b[len(b)-2:]))
	case 0x04:
		b := make([]byte, 18)
		if _, err := readFull(c, b); err != nil {
			return r, err
		}
		r.host = net.IP(b[0:16]).String()
		r.port = int(binary.BigEndian.Uint16(b[16:18]))
	default:
		return r, fmt.Errorf("badvpn: ATYP %d tidak didukung", r.atyp)
	}
	return r, nil
}

func readFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := c.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// ---------- loop UDP: core -> udpgw ----------

func (r *badvpnRelay) udpLoop() {
	buf := make([]byte, 65535)
	for {
		n, src, err := r.udpConn.ReadFromUDP(buf)
		if err != nil {
			return // conn ditutup saat stop
		}
		if n == 0 {
			continue
		}
		r.handleUDPPacket(append([]byte(nil), buf[:n]...), src)
	}
}

// socksUDP = header UDP SOCKS5 + payload.
type socksUDP struct {
	frag    byte
	ip      net.IP
	domain  string
	port    int
	payload []byte
}

func parseSocksUDP(pkt []byte) (socksUDP, error) {
	var s socksUDP
	if len(pkt) < 4 {
		return s, fmt.Errorf("badvpn: paket UDP SOCKS terlalu pendek")
	}
	// RSV 2 byte (harus 0) + FRAG + ATYP.
	s.frag = pkt[2]
	atyp := pkt[3]
	rest := pkt[4:]
	switch atyp {
	case 0x01:
		if len(rest) < 6 {
			return s, fmt.Errorf("badvpn: alamat IPv4 terpotong")
		}
		s.ip = net.IP(append([]byte(nil), rest[0:4]...))
		s.port = int(binary.BigEndian.Uint16(rest[4:6]))
		s.payload = append([]byte(nil), rest[6:]...)
	case 0x04:
		if len(rest) < 18 {
			return s, fmt.Errorf("badvpn: alamat IPv6 terpotong")
		}
		s.ip = net.IP(append([]byte(nil), rest[0:16]...))
		s.port = int(binary.BigEndian.Uint16(rest[16:18]))
		s.payload = append([]byte(nil), rest[18:]...)
	case 0x03:
		if len(rest) < 1 {
			return s, fmt.Errorf("badvpn: domain terpotong")
		}
		dl := int(rest[0])
		if len(rest) < 1+dl+2 {
			return s, fmt.Errorf("badvpn: domain terpotong")
		}
		s.domain = string(rest[1 : 1+dl])
		s.port = int(binary.BigEndian.Uint16(rest[1+dl : 1+dl+2]))
		s.payload = append([]byte(nil), rest[1+dl+2:]...)
	default:
		return s, fmt.Errorf("badvpn: ATYP UDP %d tidak didukung", atyp)
	}
	return s, nil
}

func buildSocksUDP(ip net.IP, port int, payload []byte) []byte {
	var out []byte
	out = append(out, 0x00, 0x00, 0x00)
	if ip4 := ip.To4(); ip4 != nil {
		out = append(out, 0x01)
		out = append(out, ip4...)
	} else {
		out = append(out, 0x04)
		out = append(out, ip.To16()...)
	}
	out = append(out, byte(port>>8), byte(port))
	return append(out, payload...)
}

func (r *badvpnRelay) handleUDPPacket(pkt []byte, src *net.UDPAddr) {
	s, err := parseSocksUDP(pkt)
	if err != nil {
		return // paket rusak: buang diam-diam, core akan retransmit bila perlu
	}
	if s.frag != 0 {
		return // fragmentasi SOCKS tidak didukung; game tidak memakainya
	}
	dst := s.ip
	if dst == nil && s.domain != "" {
		// udpgw tidak mengenal domain: resolve dulu. Gagal -> drop + log
		// sekali per domain (bukan per paket, agar log tidak banjir).
		// (Jarang terjadi: core biasanya sudah mengirim IP hasil DNS-nya.)
		rip, err := net.ResolveIPAddr("ip", s.domain)
		if err != nil || rip == nil {
			r.a.logOnce("badvpn|dns|"+s.domain, "badvpn: gagal resolve %s: %v", s.domain, err)
			return
		}
		dst = rip.IP
	}
	if dst == nil || len(s.payload) == 0 || len(s.payload) > udpgwMaxPayload {
		return
	}
	cl := r.getClient()
	if cl == nil {
		return // dial sedang berjalan/gagal: paket di-drop, game retransmit
	}
	key := src.String() + "|" + dst.String() + ":" + strconv.Itoa(s.port)
	r.mu.Lock()
	connID, ok := r.flows[key]
	if !ok {
		connID = r.nextID
		r.nextID++
		if r.nextID == 0 {
			r.nextID = 1 // 0 dicadangkan untuk keepalive
		}
		// Bila ID terpakai ulang untuk tujuan lain, pakai flag rebind
		// agar server membuang socket lama dengan ID itu.
		r.flows[key] = connID
		if old, dup := r.rev[connID]; dup && (old.dstIP.String() != dst.String() || old.dstPort != s.port) {
			delete(r.flows, old.src.String()+"|"+old.dstIP.String()+":"+strconv.Itoa(old.dstPort))
			r.rev[connID] = bvFlow{src: src, dstIP: dst, dstPort: s.port}
			r.mu.Unlock()
			_ = cl.send(udpgwFlagRebind, connID, dst, s.port, s.payload)
			return
		}
		r.rev[connID] = bvFlow{src: src, dstIP: dst, dstPort: s.port}
	}
	r.mu.Unlock()
	_ = cl.send(0, connID, dst, s.port, s.payload)
}

// getClient: client udpgw yang hidup, atau picu dial (non-blokir).
func (r *badvpnRelay) getClient() *udpgwClient {
	r.clientMu.Lock()
	cl := r.client
	if cl != nil {
		r.clientMu.Unlock()
		return cl
	}
	if r.dialing || time.Since(r.lastDialFail) < 5*time.Second {
		r.clientMu.Unlock()
		return nil
	}
	r.dialing = true
	r.clientMu.Unlock()
	go r.dialUdpgw()
	return nil
}

func (r *badvpnRelay) dialUdpgw() {
	defer func() {
		r.clientMu.Lock()
		r.dialing = false
		r.clientMu.Unlock()
	}()
	if atomic.LoadInt32(&r.stopped) == 1 {
		return
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(r.mixedPort), 5*time.Second)
	if err != nil {
		r.dialFailed(fmt.Errorf("gagal dial mixed core 127.0.0.1:%d: %v", r.mixedPort, err))
		return
	}
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	if err := socks5Connect(conn, "127.0.0.1", r.udpgwPort); err != nil {
		_ = conn.Close()
		r.dialFailed(fmt.Errorf("gagal CONNECT udpgw 127.0.0.1:%d lewat core: %v (core harus jalan & rule TCP udpgw harus mengarah ke SSH)", r.udpgwPort, err))
		return
	}
	_ = conn.SetDeadline(time.Time{})
	var cl *udpgwClient
	cl = newUdpgwClient(conn, r.onUdpgwMessage, func(err error) { r.clientDead(cl, err) })
	r.clientMu.Lock()
	if atomic.LoadInt32(&r.stopped) == 1 {
		r.clientMu.Unlock()
		cl.close()
		return
	}
	r.client = cl
	r.clientUp = true
	r.clientMu.Unlock()
	r.a.logf("badvpn: udpgw terhubung lewat SSH (server :%d) — UDP game kini lewat BadVPN", r.udpgwPort)
	go cl.run()
}

func (r *badvpnRelay) dialFailed(err error) {
	r.clientMu.Lock()
	r.lastDialFail = time.Now()
	throttle := time.Since(r.lastFailLog) < 60*time.Second
	if !throttle {
		r.lastFailLog = time.Now()
	}
	wasUp := r.clientUp
	r.clientUp = false
	r.clientMu.Unlock()
	if wasUp || !throttle {
		r.a.logWarnf("badvpn: GAGAL terhubung ke udpgw — %v. Periksa apakah server SSH menjalankan badvpn-udpgw di port %d; selama gagal, UDP lewat SSH tetap tidak jalan.", err, r.udpgwPort)
	}
}

func (r *badvpnRelay) clientDead(cl *udpgwClient, err error) {
	r.clientMu.Lock()
	if r.client == cl {
		r.client = nil
	}
	r.clientMu.Unlock()
	// Pesan error baca (EOF saat core berhenti) tidak perlu diributkan
	// bila relay memang sedang dimatikan.
	if atomic.LoadInt32(&r.stopped) == 0 {
		r.a.logWarnf("badvpn: koneksi udpgw putus (%v) — dial ulang otomatis saat ada paket UDP berikutnya", err)
	}
}

// onUdpgwMessage: balasan server -> kirim balik ke core sebagai UDP SOCKS5.
func (r *badvpnRelay) onUdpgwMessage(m udpgwMessage) {
	if len(m.Payload) == 0 {
		return
	}
	r.mu.Lock()
	fl, ok := r.rev[m.ConnID]
	r.mu.Unlock()
	if !ok {
		r.mu.Lock()
		logged := r.unknownLogged
		r.unknownLogged = true
		r.mu.Unlock()
		if !logged {
			r.a.logWarnf("badvpn: balasan udpgw connID %d tanpa aliran dikenal — diabaikan", m.ConnID)
		}
		return
	}
	pkt := buildSocksUDP(m.IP, m.Port, m.Payload)
	_, _ = r.udpConn.WriteToUDP(pkt, fl.src)
}

// socks5Connect: handshake + CONNECT SOCKS5 tanpa auth di atas conn TCP.
// Dipakai relay untuk mencapai udpgw lewat mixed inbound core.
func socks5Connect(conn net.Conn, host string, port int) error {
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	rep := make([]byte, 2)
	if _, err := readFull(conn, rep); err != nil {
		return err
	}
	if rep[0] != 0x05 || rep[1] != 0x00 {
		return fmt.Errorf("badvpn: SOCKS menolak auth (ver=%d method=%d)", rep[0], rep[1])
	}
	ip := net.ParseIP(host)
	var req []byte
	if ip4 := ip.To4(); ip4 != nil {
		req = []byte{0x05, 0x01, 0x00, 0x01}
		req = append(req, ip4...)
	} else if ip16 := ip.To16(); ip16 != nil {
		req = []byte{0x05, 0x01, 0x00, 0x04}
		req = append(req, ip16...)
	} else {
		req = []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
		req = append(req, host...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		return err
	}
	hdr := make([]byte, 4)
	if _, err := readFull(conn, hdr); err != nil {
		return err
	}
	if hdr[0] != 0x05 || hdr[1] != 0x00 {
		return fmt.Errorf("badvpn: SOCKS CONNECT gagal (rep=%d)", hdr[1])
	}
	switch hdr[3] {
	case 0x01:
		if _, err := readFull(conn, make([]byte, 6)); err != nil {
			return err
		}
	case 0x03:
		lb := make([]byte, 1)
		if _, err := readFull(conn, lb); err != nil {
			return err
		}
		if _, err := readFull(conn, make([]byte, int(lb[0])+2)); err != nil {
			return err
		}
	case 0x04:
		if _, err := readFull(conn, make([]byte, 18)); err != nil {
			return err
		}
	default:
		return fmt.Errorf("badvpn: ATYP balasan %d", hdr[3])
	}
	return nil
}
