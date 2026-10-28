package main

// Diagnostik KEX untuk SSH sing-box — BUKAN perbaikan, melainkan alat
// ukur agar perbaikan sing-box tidak ditebak.
//
// Latar: Teskan jalan di mihomo tapi gagal di sing-box 1.14.3 dengan
// akun/proxy/payload/jembatan yang sama persis (terbukti 2026-10-27
// 03:29 WIB). Gagalnya di tahap sesudah KEX: core mengirim 1461 byte
// (banner + KEXINIT), server membalas 2394 byte (banner + KEXINIT +
// host key), lalu koneksi putus sesudah ~1 detik. Itu pola
// ketidakcocokan algoritma, bukan payload/framing.
//
// Yang dicatat di sini, sekali per server per start daemon:
//   - banner SSH server dan banner SSH core (versi klien sing-box
//     yang acak itu ikut terlihat di sini),
//   - daftar algoritma KEXINIT dari MASING-MASING sisi: kex,
//     host-key, cipher, dan MAC, kedua arah bila berbeda.
//
// Yang TIDAK PERNAH dicatat: cookie KEXINIT, host key itu sendiri,
// user/password, atau byte sesi apa pun. Parser hanya mengambil
// daftar nama algoritma (string dipisah koma) dari paket KEXINIT
// pertama; sisanya dibuang.
//
// Cara kerja, sengaja dibuat tidak bisa merusak relay:
//   - Tap relay hanya menyalin 8192 byte PERTAMA tiap arah ke buffer
//     (io.Tee lewat tapReader) — jalur data relay tidak diubah,
//     tidak ada byte yang ditahan atau disisipkan.
//   - Parsing dilakukan SESUDAH relay selesai, dari buffer itu.
//     Gagal parse = tidak ada baris diagnostik, relay tetap jalan
//     seperti biasa.
//   - Tes ping (sshTestPing) juga mencoba membaca KEXINIT server
//     sesudah banner; gagal baca di situ TIDAK menggagalkan ping,
//     karena banner saja sudah cukup untuk ping.

import (
	"encoding/binary"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// sshKEXInfo: daftar algoritma dari satu paket KEXINIT (msg type 20).
type sshKEXInfo struct {
	Banner    string
	KEX       []string
	HostKey   []string
	CipherC2S []string
	CipherS2C []string
	MACC2S    []string
	MACS2C    []string
}

// parseSSHHandshake: banner + paket KEXINIT pertama dari data mentah
// SSH (sudah tanpa frame WS — tap relay dan tes ping sama-sama membaca
// dari koneksi yang sudah di-unframe).
func parseSSHHandshake(data []byte) (sshKEXInfo, error) {
	var out sshKEXInfo
	nl := indexByte(data, '\n')
	if nl < 0 {
		return out, fmt.Errorf("belum ada banner SSH")
	}
	out.Banner = strings.TrimSpace(string(data[:nl]))
	if !strings.HasPrefix(out.Banner, "SSH-") {
		return out, fmt.Errorf("bukan banner SSH: %q", truncateForLog(out.Banner, 60))
	}
	rest := data[nl+1:]
	if len(rest) < 5 {
		return out, fmt.Errorf("paket KEXINIT belum lengkap")
	}
	pktLen := binary.BigEndian.Uint32(rest[0:4])
	if pktLen < 16 || pktLen > 35000 {
		return out, fmt.Errorf("panjang paket KEXINIT tidak wajar: %d", pktLen)
	}
	if uint64(len(rest)) < uint64(4)+uint64(pktLen) {
		return out, fmt.Errorf("paket KEXINIT belum lengkap")
	}
	padLen := int(rest[4])
	payloadLen := int(pktLen) - padLen - 1
	if payloadLen <= 0 || 5+payloadLen > len(rest) {
		return out, fmt.Errorf("panjang payload KEXINIT tidak wajar")
	}
	payload := rest[5 : 5+payloadLen]
	if len(payload) < 1 || payload[0] != 20 {
		return out, fmt.Errorf("paket pertama bukan KEXINIT (type %d)", firstByte(payload))
	}
	off := 1 + 16 // type + cookie (cookie TIDAK dibaca isinya)
	readList := func() ([]string, error) {
		if off+4 > len(payload) {
			return nil, fmt.Errorf("daftar algoritma terpotong")
		}
		l := int(binary.BigEndian.Uint32(payload[off : off+4]))
		off += 4
		if l < 0 || off+l > len(payload) {
			return nil, fmt.Errorf("panjang daftar algoritma tidak wajar")
		}
		s := string(payload[off : off+l])
		off += l
		if s == "" {
			return []string{}, nil
		}
		return strings.Split(s, ","), nil
	}
	var err error
	// Urutan RFC 4253 §7.1, tepat 10 daftar.
	if out.KEX, err = readList(); err != nil {
		return out, err
	}
	if out.HostKey, err = readList(); err != nil {
		return out, err
	}
	if out.CipherC2S, err = readList(); err != nil {
		return out, err
	}
	if out.CipherS2C, err = readList(); err != nil {
		return out, err
	}
	if out.MACC2S, err = readList(); err != nil {
		return out, err
	}
	if out.MACS2C, err = readList(); err != nil {
		return out, err
	}
	// 4 daftar sisanya (kompresi x2, bahasa x2) tidak dipakai untuk
	// memilih cipher/mac/kex sing-box, jadi dilewati tanpa disimpan.
	for i := 0; i < 4; i++ {
		if _, err = readList(); err != nil {
			return out, err
		}
	}
	return out, nil
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func firstByte(b []byte) int {
	if len(b) == 0 {
		return -1
	}
	return int(b[0])
}

func truncateForLog(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// tapReader: pembungkus Read yang menghitung byte DAN menyalin
// 8192 byte pertama ke buffer, tanpa mengubah aliran sama sekali.
// Sesudah 8192 byte, penyalinan berhenti tapi penghitungan jalan terus,
// jadi relay trafik besar (mis. YouTube lewat SSH) tidak membuat
// buffer membengkak.
type tapReader struct {
	r   io.Reader
	n   int64
	buf []byte
}

func (t *tapReader) Read(b []byte) (int, error) {
	n, err := t.r.Read(b)
	t.n += int64(n)
	if n > 0 && len(t.buf) < 8192 {
		take := n
		if len(t.buf)+take > 8192 {
			take = 8192 - len(t.buf)
		}
		t.buf = append(t.buf, b[:take]...)
	}
	return n, err
}

// recordSSHKEX: tulis diagnostik KEX satu sisi ke log/ssh.log, sekali
// per server per start daemon. Gagal parse = diam saja, bukan error.
func (a *App) recordSSHKEX(name, serverID, side string, data []byte) {
	info, err := parseSSHHandshake(data)
	if err != nil {
		return
	}
	key := serverID + "/" + side
	a.sshLogMu.Lock()
	if a.sshKexLogged == nil {
		a.sshKexLogged = map[string]bool{}
	}
	if a.sshKexLogged[key] {
		a.sshLogMu.Unlock()
		return
	}
	a.sshKexLogged[key] = true
	if a.logDir != "" && a.sshLogW == nil {
		a.sshLogW = newCapLogWriter(filepath.Join(a.logDir, "ssh.log"))
	}
	w := a.sshLogW
	a.sshLogMu.Unlock()
	if w == nil {
		return
	}
	join := func(list []string) string {
		if len(list) == 0 {
			return "(kosong)"
		}
		return strings.Join(list, ", ")
	}
	cipher := join(info.CipherC2S)
	if !equalStringLists(info.CipherC2S, info.CipherS2C) {
		cipher = "c2s: " + join(info.CipherC2S) + " | s2c: " + join(info.CipherS2C)
	}
	mac := join(info.MACC2S)
	if !equalStringLists(info.MACC2S, info.MACS2C) {
		mac = "c2s: " + join(info.MACC2S) + " | s2c: " + join(info.MACS2C)
	}
	ts := time.Now().Format("15:04:05")
	lines := fmt.Sprintf("[%s] %s: KEX %s — banner: %s\n"+
		"[%s] %s: KEX %s — kex: %s\n"+
		"[%s] %s: KEX %s — hostkey: %s\n"+
		"[%s] %s: KEX %s — cipher: %s\n"+
		"[%s] %s: KEX %s — mac: %s\n",
		ts, name, side, info.Banner,
		ts, name, side, join(info.KEX),
		ts, name, side, join(info.HostKey),
		ts, name, side, cipher,
		ts, name, side, mac)
	_, _ = w.Write([]byte(lines))
}

func equalStringLists(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
