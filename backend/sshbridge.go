// sshbridge.go — jembatan SSH gaya HTTP Custom untuk TaptapBox.
//
// Masalah: core sing-box/mihomo punya outbound SSH polos (TCP langsung ke
// server:port, tanpa payload, tanpa proxy jarak jauh, tanpa WS). Profil SSH
// HTTP Custom justru bekerja sebaliknya: TCP dulu ke proxy jarak jauh,
// kirim payload HTTP mentah (biasanya request upgrade WebSocket), lalu
// SSH berjalan DI DALAM frame WebSocket (bila payloadnya upgrade WS),
// atau dipipa mentah sesudah header respons dibaca (payload non-WS).
//
// Solusi di sini, agar jalan di KEDUA core tanpa mengubah core:
//   - Server SSH yang memakai payload ditulis ulang di config jadi
//     server=127.0.0.1, port=jembatan server itu (lihat genbox.go/mihomo.go
//     + buildServers di util.go). Tiap server SSH payload punya port
//     sendiri (15411 + peringkat ID terurut), jadi url-test, fallback,
//     load-balance, tes delay, dan pilihan di zashboard semuanya dialirkan
//     lewat profil masing-masing — bukan lewat profil server terpilih saja.
//     Core tetap bicara SSH polos, tapi ke jembatan lokal ini.
//   - Daemon mendengarkan sshBridgePort. Tiap koneksi dari core, jembatan
//     dial ke proxy jarak jauh, opsional TLS (SNI), kirim payload yang
//     sudah disubstitusi, baca header respons, lalu:
//       * payload mengandung "Upgrade: websocket" -> frame WS (klien
//         memakai mask, seperti WS standar; server tidak perlu mask),
//       * selain itu -> pipa mentah (header respons sudah dikonsumsi,
//         jadi tidak tercampur ke banner SSH).
//   - Profil jembatan mengikuti SEMUA server SSH payload yang aktif,
//     bukan hanya server terpilih. Jembatan disinkronkan saat core
//     dinyalakan (startCore) dan dihentikan saat core berhenti (stopCore).
//     Pindah server lewat WebUI/zashboard selagi core jalan TIDAK perlu
//     menyinkronkan ulang, karena jembatan server tujuan sudah jalan.
//   - SlowDNS TIDAK ada di sini: sesuai temuan sebelumnya, SlowDNS adalah
//     terowongan mandiri (bukan pelengkap WS), jadi bukan bagian profil SSH.
//   - HCR juga bukan SSH: itu transport tersendiri di HTTP Custom, jadi
//     tidak masuk form SSH ini.
//
// Batasan v1 yang jujur:
//   - Port jembatan dihitung dari ID server yang terurut, jadi menambah /
//     menghapus server SSH payload bisa menggeser port server lain.
//     Itu aman karena port dihitung ulang bersamaan dengan config saat
//     core dinyalakan — tapi jangan menyinkronkan jembatan di tengah
//     jalan tanpa regenerate config (lihat syncSSHbridge).
//   - "Enhanced" dicatat sebagai flag, tapi perilaku wire-nya sama dengan
//     mode payload: payload dikirim mentah seperti ditulis. Tidak ada
//     protokol rahasia "Enhanced" yang bisa ditiru dari aplikasi tertutup.
//   - "Versi SNI" hanya disimpan (satu-satunya opsi di contoh user adalah
//     Default); tidak mengubah handshake TLS Go.

package main

import (
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// sshBridgePort: port loopback jembatan SSH. Tidak bentrok dengan
// WebUI 5056, mixed 10809, maupun tproxy 15359.
const sshBridgePort = 15411

// sshDefaultUA: pengganti placeholder [ua] bila user tidak menulis UA
// sendiri di payload. UA generik, bukan UA khusus HTTP Custom.
const sshDefaultUA = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36"

// sshDefaultPayload: template WS yang sama dengan contoh CloudFront user
// (GET / + upgrade websocket). Disimpan WebUI sebagai nilai awal saat
// membuat server SSH baru, bukan hardcode di backend.
const sshDefaultPayload = "GET / HTTP/1.1[crlf]Host: [host][crlf]Connection: Upgrade[crlf]User-Agent: [ua][crlf]Upgrade: websocket[crlf][crlf]"

// sshBridgeProfile: satu profil SSH payload yang sedang aktif.
type sshBridgeProfile struct {
	ServerID    string // ID server di store (untuk ssh.log)
	ServerName  string // nama server di store (untuk ssh.log)
	SSHHost     string // address SSH (p["server"])
	RemoteProxy string // p["ssh_proxy"], mis. "1.2.3.4:443" / "sushiroll.example:80"
	Payload     string // p["ssh_payload"] (mentah, berisi placeholder)
	UseTLS      bool   // p["ssh_tls"] == "1"
	SNI         string // p["ssh_sni"] (kosong = SSHHost)
	Insecure    bool   // p["ssh_insecure"] == "1"
	Raw         bool   // p["ssh_mode"]=="raw" atau p["ssh_raw"]=="1": sesudah
	// payload, pipa mentah TANPA frame WS (gaya injector). Bila false,
	// payload upgrade WS diframe sebagai WebSocket standar.
}

func (p sshBridgeProfile) String() string {
	mode := "ws"
	if p.Raw {
		mode = "raw"
	}
	return fmt.Sprintf("ssh->%s tls=%v sni=%s mode=%s", p.RemoteProxy, p.UseTLS, p.SNI, mode)
}

// sshPayloadProfile: true bila server ini SSH payload yang aktif.
// Syarat: proto ssh, ssh_payload_on=1, proxy jarak jauh terisi, payload terisi.
func sshPayloadProfile(sv *Server) (sshBridgeProfile, bool) {
	if sv == nil || sv.Proto != "ssh" {
		return sshBridgeProfile{}, false
	}
	p := sv.Params
	if p["ssh_payload_on"] != "1" {
		return sshBridgeProfile{}, false
	}
	proxy := strings.TrimSpace(p["ssh_proxy"])
	payload := p["ssh_payload"]
	if proxy == "" || strings.TrimSpace(payload) == "" {
		return sshBridgeProfile{}, false
	}
	sni := strings.TrimSpace(p["ssh_sni"])
	if sni == "" {
		sni = strings.TrimSpace(p["server"])
	}
	return sshBridgeProfile{
		ServerID:    sv.ID,
		ServerName:  sv.Name,
		SSHHost:     strings.TrimSpace(p["server"]),
		RemoteProxy: proxy,
		Payload:     payload,
		UseTLS:      p["ssh_tls"] == "1",
		SNI:         sni,
		Insecure:    p["ssh_insecure"] == "1" || strings.EqualFold(p["ssh_insecure"], "true"),
		Raw:         p["ssh_mode"] == "raw" || p["ssh_raw"] == "1",
	}, true
}

// expandSSHPayload mengganti placeholder template payload.
// [host] -> host SSH, [protocol] -> "HTTP/1.1", [ua] -> UA default,
// [crlf] -> CR+LF. Bila template tidak diakhiri baris kosong, ditambah
// "\r\n\r\n" supaya server tahu header selesai.
func expandSSHPayload(template, sshHost string) string {
	out := template
	out = strings.ReplaceAll(out, "[host]", sshHost)
	out = strings.ReplaceAll(out, "[protocol]", "HTTP/1.1")
	out = strings.ReplaceAll(out, "[ua]", sshDefaultUA)
	out = strings.ReplaceAll(out, "[crlf]", "\r\n")
	// Normalisasi akhir baris: template tempelan bisa berisi \n saja.
	out = strings.ReplaceAll(out, "\r\n", "\n")
	out = strings.ReplaceAll(out, "\n", "\r\n")
	if !strings.HasSuffix(out, "\r\n\r\n") {
		out = strings.TrimRight(out, "\r\n") + "\r\n\r\n"
	}
	return out
}

// isWSUpgrade: true bila payload (sesudah substitusi) meminta upgrade WS.
func isWSUpgrade(payload string) bool {
	return strings.Contains(strings.ToLower(payload), "upgrade: websocket")
}

// addWSHandshakeHeaders menambahkan Sec-WebSocket-Key + Version 13 ke
// payload upgrade WS yang belum memilikinya. Handshake WS standar WAJIB
// header ini; tanpa itu server WS ketat membalas 400 / tidak membalas
// 101 sama sekali, dan payload contoh HTTP Custom memang tidak menulisnya
// (aplikasi injector menambahkannya sendiri di balik layar).
// Mode pipa mentah (Raw) TIDAK ditambah, karena payload harus terkirim
// persis seperti ditulis.
func addWSHandshakeHeaders(payload string) string {
	if !isWSUpgrade(payload) {
		return payload
	}
	if strings.Contains(strings.ToLower(payload), "sec-websocket-key") {
		return payload
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return payload
	}
	body := strings.TrimSuffix(payload, "\r\n\r\n")
	return body + "\r\nSec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key[:]) + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
}

// sshBridgeDial: dial ke proxy jarak jauh, TLS bila diminta, kirim payload,
// baca header respons, kembalikan koneksi siap pipa (WS bila upgrade WS)
// beserta daftar status respons (untuk ssh.log). Satu kali percobaan
// saja — percobaan ulang otomatis ada di dialSSHWithRetry, bukan di sini,
// supaya tes satu percobaan tidak ikut menunggu backoff.
func sshBridgeDial(prof sshBridgeProfile) (net.Conn, []string, error) {
	statuses := []string{}
	proxy := prof.RemoteProxy
	if _, _, err := net.SplitHostPort(proxy); err != nil {
		// proxy tanpa port: 443 bila TLS, 80 bila tidak
		if prof.UseTLS {
			proxy += ":443"
		} else {
			proxy += ":80"
		}
	}
	d := &net.Dialer{Timeout: sshDialTimeout}
	raw, err := d.Dial("tcp", proxy)
	if err != nil {
		return nil, statuses, err
	}
	var conn net.Conn = raw
	if prof.UseTLS {
		tlsConn := tls.Client(raw, &tls.Config{
			ServerName:         prof.SNI,
			InsecureSkipVerify: prof.Insecure, // #nosec G402 — atas permintaan user (checkbox di WebUI)
		})
		if err := tlsConn.Handshake(); err != nil {
			_ = raw.Close()
			return nil, statuses, err
		}
		conn = tlsConn
	}
	// Batas waktu hanya untuk handshake payload, bukan untuk sesi SSH.
	// 5 detik (dulu 15): lihat sshPayloadDeadline — proxy sehat
	// menjawab dalam <2 detik, dan 15 detik per percobaan membuat
	// retry timeout tidak mungkin muat dalam connect_timeout core.
	_ = conn.SetDeadline(time.Now().Add(sshPayloadDeadline))
	payload := expandSSHPayload(prof.Payload, prof.SSHHost)
	if !prof.Raw {
		// Mode WS: lengkapi handshake standar (lihat addWSHandshakeHeaders).
		// Key ditambahkan ke request TERAKHIR (request upgrade WS), bukan
		// ke request depan, karena TrimSuffix hanya menyentuh akhir payload.
		payload = addWSHandshakeHeaders(payload)
	}
	// Payload Enhanced HTTP Custom bisa berisi DUA request sekaligus,
	// mis. HEAD ke host depan (balas 301) lalu BMOVE upgrade WS ke host
	// SSH (balas 101) — terbukti di log HTTP Custom user 2026-10-27
	// 02:02:29. Kode lama hanya membaca SATU respons (301) lalu gagal di
	// kedua mode, padahal respons finalnya 101. Karena itu payload
	// dipecah per request, dikirim satu per satu, dan satu respons
	// dibaca per request; status yang dipakai adalah respons TERAKHIR,
	// kecuali respons final (101/2xx) sudah datang lebih awal — kalau
	// koneksi sudah upgrade, request sisa tidak boleh dikirim lagi
	// karena akan mengotori stream WS/SSH.
	requests := splitSSHPayloadRequests(payload)
	br := bufio.NewReader(conn)
	status := ""
	for i, req := range requests {
		if _, err := io.WriteString(conn, req); err != nil {
			_ = conn.Close()
			return nil, statuses, err
		}
		status, err = readHTTPResponseHeaders(br)
		if err != nil {
			_ = conn.Close()
			return nil, statuses, err
		}
		statuses = append(statuses, status)
		code := httpStatusCode(status)
		if i < len(requests)-1 && (code == "101" || strings.HasPrefix(code, "2")) {
			break
		}
	}
	// Deteksi framing dari data pertama server. Server SSH selalu bicara
	// duluan (banner "SSH-2.0-..."). Bila yang datang "SSH-" berarti server
	// memipa SSH mentah sesudah 101 (umum di proxy ws-ssh), dan membungkus
	// dalam frame WS justru merusak sesi. Bila yang datang frame WS (0x81/
	// 0x82 tanpa mask), framing WS memang dibutuhkan. Hasil deteksi menang
	// atas pilihan dropdown; bila tak ada data dalam 2 detik, pilihan
	// dropdown dipakai seperti sebelumnya.
	// 2 detik (dulu 4): server yang kirim banner langsung biasanya
	// datang dalam <1 detik lewat proxy; menunggu 4 detik hanya
	// memperpanjang dial jembatan dan mendorong core sing-box ke
	// timeout-nya (pola intermiten 2026-10-27). Server yang menunggu
	// banner klien dulu tidak dirugikan: fallback dropdown-nya sama
	// dengan perilaku lama, hanya lebih cepat 2 detik.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	first, _ := br.Peek(4)
	detected := detectSSHFraming(first)
	_ = conn.SetDeadline(time.Time{})
	// br mungkin sudah membaca byte lebih dari header (buffered); byte itu
	// milik stream berikutnya, jadi harus ikut terbawa, bukan dibuang.
	// Untuk WS: frame pertama bisa sudah terbaca sebagian ke buffer.
	// Untuk pipa mentah: byte buffered adalah awal banner SSH.
	leftover := br.Buffered()
	var up net.Conn = conn
	if leftover > 0 {
		peek, _ := br.Peek(leftover)
		up = &prefixConn{Conn: conn, prefix: append([]byte(nil), peek...)}
		// Konsumsi byte yang sudah di-peek dari buffer agar tidak dobel.
		_, _ = br.Discard(leftover)
	}
	useWS := isWSUpgrade(payload) && !prof.Raw
	if detected == "ws" {
		useWS = true
	} else if detected == "raw" {
		useWS = false
	}
	if useWS {
		if detected == "" && httpStatusCode(status) != "101" {
			_ = up.Close()
			return nil, statuses, fmt.Errorf("upgrade websocket ditolak: %s (semua respons: %s)", status, strings.Join(statuses, " | "))
		}
		return &sshWSConn{Conn: up}, statuses, nil
	}
	// Pipa mentah: payload non-WS, atau payload WS dalam mode Raw.
	// Terima 2xx (CONNECT) maupun 101 tanpa framing.
	// Status lain tetap dicoba dipipa? Tidak — gagalkan supaya tidak
	// mengotori sesi SSH dengan halaman error proxy.
	code := httpStatusCode(status)
	if detected == "" && !strings.HasPrefix(code, "2") && !strings.HasPrefix(code, "1") {
		_ = up.Close()
		return nil, statuses, fmt.Errorf("proxy menolak payload: %s (semua respons: %s)", status, strings.Join(statuses, " | "))
	}
	return up, statuses, nil
}

// detectSSHFraming menebak framing dari 4 byte pertama dari server:
// "raw" bila banner SSH polos, "ws" bila frame WS tanpa mask (FIN +
// teks/biner), "" bila belum bisa dipastikan.
func detectSSHFraming(first []byte) string {
	if len(first) < 4 {
		return ""
	}
	if strings.HasPrefix(string(first), "SSH-") { // awalan, bukan persis 4 byte: Peek bisa mengembalikan lebih
		return "raw"
	}
	if (first[0] == 0x81 || first[0] == 0x82) && first[1]&0x80 == 0 && first[1] > 0 {
		return "ws"
	}
	return ""
}

// splitSSHPayloadRequests memecah payload yang sudah diekspansi menjadi
// blok-blok request, masing-masing diakhiri "\r\n\r\n". Blok kosong
// (akhir payload, baris kosong ganda) dibuang. Payload satu request
// menghasilkan satu blok — perilaku lama tidak berubah.
func splitSSHPayloadRequests(payload string) []string {
	parts := strings.Split(payload, "\r\n\r\n")
	out := []string{}
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, p+"\r\n\r\n")
	}
	if len(out) == 0 {
		return []string{payload}
	}
	return out
}

// httpStatusCode: "HTTP/1.1 101 ..." -> "101" ("" bila tak bisa diparse).
func httpStatusCode(status string) string {
	f := strings.Fields(status)
	if len(f) >= 2 {
		return f[1]
	}
	return ""
}

// readHTTPResponseHeaders membaca baris status + header sampai baris kosong.
// Mengembalikan baris status (mis. "HTTP/1.1 101 Switching Protocols").
func readHTTPResponseHeaders(br *bufio.Reader) (string, error) {
	var status string
	for i := 0; i < 64; i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			return "", err
		}
		trim := strings.TrimRight(line, "\r\n")
		if i == 0 {
			status = trim
		}
		if trim == "" {
			return status, nil
		}
	}
	return "", fmt.Errorf("header respons proxy terlalu panjang")
}

// prefixConn: koneksi yang mengembalikan prefix dulu sebelum stream asli.
// Dipakai untuk byte yang sudah terlanjur dibaca bufio saat baca header.
type prefixConn struct {
	net.Conn
	prefix []byte
	off    int
}

func (c *prefixConn) Read(b []byte) (int, error) {
	if c.off < len(c.prefix) {
		n := copy(b, c.prefix[c.off:])
		c.off += n
		return n, nil
	}
	return c.Conn.Read(b)
}

// sshWSConn: koneksi WebSocket sisi klien, framing manual (stdlib saja).
// Tulis: satu frame biner per Write, FIN=1, opcode=2, ter-mask.
// Baca: gabungkan frame sampai FIN, tangani ping (balas pong) dan close.
type sshWSConn struct {
	net.Conn
	rbuf []byte // sisa payload frame yang belum dibaca pemanggil
	roff int
}

func (w *sshWSConn) Read(b []byte) (int, error) {
	if w.roff < len(w.rbuf) {
		n := copy(b, w.rbuf[w.roff:])
		w.roff += n
		if w.roff >= len(w.rbuf) {
			w.rbuf = nil
			w.roff = 0
		}
		return n, nil
	}
	for {
		op, payload, err := readSSHWSFrame(w.Conn)
		if err != nil {
			return 0, err
		}
		switch op {
		case 0x9: // ping -> pong
			_ = w.writeSSHWSFrame(0xA, payload)
			continue
		case 0x8: // close
			_ = w.writeSSHWSFrame(0x8, nil)
			return 0, io.EOF
		case 0x2, 0x1, 0x0: // biner/teks/kontinuasi: SSH adalah stream, jadi
			// pemecahan frame WS tidak masalah — tiap Read mengembalikan
			// satu frame, dan lapisan SSH menyambungnya kembali.
			w.rbuf = append(w.rbuf, payload...)
			n := copy(b, w.rbuf)
			w.roff = n
			if w.roff >= len(w.rbuf) {
				w.rbuf = nil
				w.roff = 0
			}
			return n, nil
		default:
			continue
		}
	}
}

func (w *sshWSConn) Write(b []byte) (int, error) {
	if err := w.writeSSHWSFrame(0x2, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (w *sshWSConn) writeSSHWSFrame(op byte, payload []byte) error {
	var hdr [14]byte
	hdr[0] = 0x80 | op // FIN + opcode
	maskBit := byte(0x80)
	n := len(payload)
	var hlen int
	switch {
	case n < 126:
		hdr[1] = maskBit | byte(n)
		hlen = 2
	case n < 65536:
		hdr[1] = maskBit | 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
		hlen = 4
	default:
		hdr[1] = maskBit | 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(n))
		hlen = 10
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	copy(hdr[hlen:hlen+4], mask[:])
	hlen += 4
	if _, err := w.Conn.Write(hdr[:hlen]); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	_, err := w.Conn.Write(masked)
	return err
}

// readSSHWSFrame membaca satu frame WS dari koneksi (server -> klien).
// Mengembalikan opcode dan payload (sudah di-unmask bila termask).
func readSSHWSFrame(r io.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	op := h[0] & 0x0F
	masked := h[1]&0x80 != 0
	length := int64(h[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(ext[:]))
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	if length < 0 || length > 64<<20 {
		return 0, nil, fmt.Errorf("ukuran frame WS tidak wajar: %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return op, payload, nil
}

// sshTestPing: ping untuk server SSH.
//   - SSH polos (tanpa payload): TCP ping langsung ke host, seperti
//     protokol lain — host itu memang tujuan sebenarnya.
//   - SSH payload: TCP ping langsung SALAH, karena host SSH bukan
//     tujuan yang bisa didial langsung — jalurnya lewat proxy +
//     payload. Karena itu tes ini dial langsung lewat sshBridgeDial
//     (tanpa perlu core jalan), lalu membaca banner SSH server
//     ("SSH-2.0-..."). Banner membuktikan: proxy terhubung, payload
//     diterima (101), framing WS/pipa cocok, DAN ujungnya memang
//     server SSH. Yang tidak dibuktikan banner: user/password benar
//     atau tidak — itu baru ketahuan saat core login SSH.
//     Mengembalikan ms (dari mulai dial sampai banner diterima) atau -1.
//     Hasil tes juga dicatat di ssh.log agar bisa dibaca di Logs > SSH.
func (a *App) sshTestPing(serverID, host, port string) int {
	a.store.mu.RLock()
	sv := a.store.findServer(serverID)
	var prof sshBridgeProfile
	var ok bool
	if sv != nil {
		_, profs := a.sshBridgeAssignmentsLocked()
		prof, ok = profs[serverID]
	}
	a.store.mu.RUnlock()
	if sv == nil {
		return -1
	}
	if !ok {
		return tcpPing(host, port, 4*time.Second)
	}
	name := prof.ServerName
	if name == "" {
		name = prof.SSHHost
	}
	start := time.Now()
	conn, resp, attempts, err := dialSSHWithRetry(prof, sshBridgeDial)
	if err != nil {
		a.recordSSHLog(sshLogEntry{
			Time: time.Now().Format("15:04:05"), ServerID: serverID, Name: name,
			Proxy: prof.RemoteProxy, Mode: sshModeName(prof), Responses: resp,
			OK: false, Attempts: attempts, Error: err.Error(),
		})
		return -1
	}
	defer conn.Close()
	// Banner SSH dikirim server segera sesudah koneksi jadi; 5 detik
	// cukup, karena dial proxy sudah selesai di titik ini.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(strings.TrimSpace(line), "SSH-") {
		errMsg := "proxy terhubung tapi server tidak mengirim banner SSH — framing WS/pipa mungkin tidak cocok, atau ujung proxy bukan server SSH"
		if err != nil {
			errMsg = errMsg + " (" + err.Error() + ")"
		} else {
			pv := line
			if len(pv) > 60 {
				pv = pv[:60]
			}
			errMsg = errMsg + fmt.Sprintf(" — yang diterima: %q", pv)
		}
		a.recordSSHLog(sshLogEntry{
			Time: time.Now().Format("15:04:05"), ServerID: serverID, Name: name,
			Proxy: prof.RemoteProxy, Mode: sshModeName(prof), Responses: resp,
			OK: false, Attempts: attempts, Error: errMsg,
		})
		return -1
	}
	ms := int(time.Since(start).Milliseconds())
	if ms < 1 {
		ms = 1
	}
	a.recordSSHLog(sshLogEntry{
		Time: time.Now().Format("15:04:05"), ServerID: serverID, Name: name,
		Proxy: prof.RemoteProxy, Mode: sshModeName(prof), Responses: resp,
		OK: true, Attempts: attempts,
	})
	// Diagnostik KEX server (opsional): baca paket KEXINIT sesudah
	// banner. Gagal baca TIDAK menggagalkan ping — banner saja sudah
	// cukup untuk ping, dan server yang menunggu KEXINIT klien dulu
	// tidak akan mengirim paket ini sekarang. Deadline 2 detik
	// (dulu 5): Dropbear mengirim KEXINIT langsung, jadi menunggu
	// 5 detik hanya memperlambat kembalinya hasil tes ping ke WebUI.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var hdr [4]byte
	if _, err := io.ReadFull(br, hdr[:]); err == nil {
		plen := binary.BigEndian.Uint32(hdr[:])
		if plen > 0 && plen <= 35000 {
			rest := make([]byte, plen)
			if _, err := io.ReadFull(br, rest); err == nil {
				data := append(append([]byte(nil), line...), hdr[:]...)
				data = append(data, rest...)
				a.recordSSHKEX(name, serverID, "server (tes)", data)
			}
		}
	}
	return ms
}

// ---------- manajemen listener jembatan di App ----------

// sshBridgeAssignmentsLocked menghitung port + profil untuk SEMUA server
// SSH payload yang aktif (Enabled + valid). Port = sshBridgePort +
// peringkat ID terurut, jadi stabil terhadap urutan daftar dan tidak
// bentrok antar server. Pemanggil HARUS sudah memegang store.mu (baca);
// buildServers sudah begitu, syncSSHbridge mengunci sendiri di bawah.
func (a *App) sshBridgeAssignmentsLocked() (map[string]int, map[string]sshBridgeProfile) {
	ports := map[string]int{}
	profs := map[string]sshBridgeProfile{}
	ids := []string{}
	for i := range a.store.Servers {
		sv := &a.store.Servers[i]
		if !sv.Enabled || !validServer(sv) {
			continue
		}
		if prof, ok := sshPayloadProfile(sv); ok {
			ids = append(ids, sv.ID)
			profs[sv.ID] = prof
		}
	}
	sort.Strings(ids)
	for rank, id := range ids {
		ports[id] = sshBridgePort + rank
	}
	return ports, profs
}

// syncSSHbridge: pastikan satu listener jalan untuk tiap server SSH
// payload yang aktif, dan matikan listener yang sudah tidak diinginkan.
// HANYA dipanggil saat core dinyalakan (startCore) — di titik itu config
// juga baru di-generate dengan port yang sama, jadi tidak ada geseran
// port di tengah jalan. Jangan dipanggil saat pindah server / urut ulang
// selagi core jalan.
func (a *App) syncSSHbridge() {
	if a.store == nil {
		return
	}
	a.store.mu.RLock()
	ports, profs := a.sshBridgeAssignmentsLocked()
	a.store.mu.RUnlock()
	a.sshBridgeMu.Lock()
	defer a.sshBridgeMu.Unlock()
	if a.sshBridgeLn == nil {
		a.sshBridgeLn = map[int]net.Listener{}
		a.sshBridgeProf = map[int]sshBridgeProfile{}
	}
	// Matikan listener yang port/profilnya sudah tidak diinginkan.
	for port, ln := range a.sshBridgeLn {
		want, ok := profsByPort(ports, profs, port)
		if !ok || want != a.sshBridgeProf[port] {
			_ = ln.Close()
			delete(a.sshBridgeLn, port)
			delete(a.sshBridgeProf, port)
		}
	}
	// Nyalakan listener yang belum ada.
	for id, port := range ports {
		if _, ok := a.sshBridgeLn[port]; ok {
			continue
		}
		prof := profs[id]
		ln, err := net.Listen("tcp", "127.0.0.1:"+itoa(port))
		if err != nil {
			a.logWarnf("jembatan SSH gagal listen 127.0.0.1:%d: %v", port, err)
			continue
		}
		a.sshBridgeLn[port] = ln
		a.sshBridgeProf[port] = prof
		a.logf("jembatan SSH jalan 127.0.0.1:%d -> %s", port, prof.String())
		go func(ln net.Listener, prof sshBridgeProfile) {
			for {
				c, err := ln.Accept()
				if err != nil {
					return // listener ditutup
				}
				go a.handleSSHbridgeConn(c, prof)
			}
		}(ln, prof)
	}
}

// profsByPort: profil untuk satu port dari peta assignment (port -> id dibalik).
func profsByPort(ports map[string]int, profs map[string]sshBridgeProfile, port int) (sshBridgeProfile, bool) {
	for id, p := range ports {
		if p == port {
			prof, ok := profs[id]
			return prof, ok
		}
	}
	return sshBridgeProfile{}, false
}

func (a *App) handleSSHbridgeConn(local net.Conn, prof sshBridgeProfile) {
	// Reconnect otomatis (bukan tombol): dialSSHWithRetry mencoba
	// lagi sendiri sampai 3 kali, termasuk untuk timeout. Setiap
	// percobaan — berhasil maupun gagal — tercatat di ssh.log
	// dengan nama akun, supaya semua akun SSH bisa dibaca.
	up, resp, attempts, err := dialSSHWithRetry(prof, sshBridgeDial)
	name := prof.ServerName
	if name == "" {
		name = prof.SSHHost
	}
	if err != nil {
		a.logWarnf("jembatan SSH dial gagal: %v", err)
		a.recordSSHLog(sshLogEntry{
			Time: time.Now().Format("15:04:05"), ServerID: prof.ServerID, Name: name,
			Proxy: prof.RemoteProxy, Mode: sshModeName(prof), Responses: resp,
			OK: false, Attempts: attempts, Error: err.Error(),
		})
		_ = local.Close()
		return
	}
	a.recordSSHLog(sshLogEntry{
		Time: time.Now().Format("15:04:05"), ServerID: prof.ServerID, Name: name,
		Proxy: prof.RemoteProxy, Mode: sshModeName(prof), Responses: resp,
		OK: true, Attempts: attempts,
	})
	// Relay dua arah; tutup keduanya bila salah satu selesai.
	// Putus di tengah jalan tidak perlu aksi manual: koneksi core
	// berikutnya dial ulang dari nol lewat jembatan ini.
	// Byte dihitung per arah agar ssh.log bisa menjawab apakah server
	// sempat membalas (lihat recordSSHRelayLine): 0 byte dari server
	// berarti handshake SSH tidak pernah jalan, walau TERHUBUNG tercatat.
	start := time.Now()
	done := make(chan struct{}, 2)
	var fromCore, fromServer int64
	var coreBuf, serverBuf []byte
	var cntMu sync.Mutex
	relay := func(dst, src net.Conn, count *int64, buf *[]byte) {
		tr := &tapReader{r: src}
		_, _ = io.Copy(dst, tr)
		cntMu.Lock()
		*count = tr.n
		*buf = tr.buf
		cntMu.Unlock()
		done <- struct{}{}
	}
	go relay(local, up, &fromServer, &serverBuf) // server -> core
	go relay(up, local, &fromCore, &coreBuf)     // core -> server
	<-done
	_ = local.Close()
	_ = up.Close()
	a.recordSSHRelayLine(name, prof.RemoteProxy, sshModeName(prof), time.Since(start), fromCore, fromServer)
	// Diagnostik KEX, sesudah relay selesai (tidak bisa merusak relay):
	// buffer tap berisi banner + KEXINIT tiap arah, sekali per server.
	a.recordSSHKEX(name, prof.ServerID, "server", serverBuf)
	a.recordSSHKEX(name, prof.ServerID, "core", coreBuf)
}

func (a *App) stopSSHbridge() {
	a.sshBridgeMu.Lock()
	defer a.sshBridgeMu.Unlock()
	a.stopSSHbridgeLocked()
}

func (a *App) stopSSHbridgeLocked() {
	for port, ln := range a.sshBridgeLn {
		_ = ln.Close()
		delete(a.sshBridgeLn, port)
	}
	a.sshBridgeLn = nil
	a.sshBridgeProf = nil
}
