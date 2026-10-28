package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestExpandSSHPayload(t *testing.T) {
	got := expandSSHPayload("GET / [protocol][crlf]Host: [host][crlf]User-Agent: [ua][crlf]Upgrade: websocket[crlf][crlf]", "d123.cloudfront.net")
	if !strings.HasPrefix(got, "GET / HTTP/1.1\r\nHost: d123.cloudfront.net\r\n") {
		t.Fatalf("prefix salah: %q", got)
	}
	if !strings.Contains(got, "User-Agent: Mozilla/5.0") {
		t.Fatalf("UA tidak tersubstitusi: %q", got)
	}
	if !strings.HasSuffix(got, "\r\n\r\n") {
		t.Fatalf("harus diakhiri baris kosong: %q", got)
	}
	// Template tempelan dengan \n asli juga jadi CRLF.
	got2 := expandSSHPayload("GET / HTTP/1.1\nHost: [host]\n\n", "h.example")
	if got2 != "GET / HTTP/1.1\r\nHost: h.example\r\n\r\n" {
		t.Fatalf("normalisasi \\n gagal: %q", got2)
	}
}

func TestSSHPayloadProfile(t *testing.T) {
	sv := &Server{Proto: "ssh", Params: map[string]string{
		"server": "d123.cloudfront.net", "ssh_payload_on": "1",
		"ssh_proxy": "1.2.3.4:443", "ssh_payload": "GET / HTTP/1.1[crlf]Host: [host][crlf][crlf]",
		"ssh_tls": "1", "ssh_insecure": "1",
	}}
	prof, ok := sshPayloadProfile(sv)
	if !ok || prof.RemoteProxy != "1.2.3.4:443" || !prof.UseTLS || !prof.Insecure || prof.SNI != "d123.cloudfront.net" {
		t.Fatalf("profil salah: %+v ok=%v", prof, ok)
	}
	// Payload mati -> tidak aktif; proxy kosong -> tidak aktif.
	sv.Params["ssh_payload_on"] = ""
	if _, ok := sshPayloadProfile(sv); ok {
		t.Fatal("payload off harus tidak aktif")
	}
	sv.Params["ssh_payload_on"] = "1"
	sv.Params["ssh_proxy"] = ""
	if _, ok := sshPayloadProfile(sv); ok {
		t.Fatal("proxy kosong harus tidak aktif")
	}
}

func TestSSHOutboundRewrite(t *testing.T) {
	sv := &Server{Name: "SSHCF", Proto: "ssh", Params: map[string]string{
		"server": "d123.cloudfront.net", "port": "443", "username": "u1", "password": "p1",
		"ssh_payload_on": "1", "ssh_proxy": "1.2.3.4:443", "ssh_payload": "GET /[crlf][crlf]",
	}}
	ob, _ := serverToOutbound(sv, "SSHCF")
	if ob["server"] != "127.0.0.1" || ob["server_port"] != sshBridgePort {
		t.Fatalf("sing-box harus ke jembatan, dapat %v:%v", ob["server"], ob["server_port"])
	}
	if _, ok := ob["tls"]; ok {
		t.Fatal("sing-box SSH jembatan tidak boleh membawa tls core")
	}
	px := serverToMihomoProxy(sv, "SSHCF")
	if px["server"] != "127.0.0.1" || px["port"] != sshBridgePort {
		t.Fatalf("mihomo harus ke jembatan, dapat %v:%v", px["server"], px["port"])
	}
	if _, ok := px["tls"]; ok {
		t.Fatal("mihomo SSH jembatan tidak boleh membawa tls core")
	}
	// SSH tanpa payload tetap direct seperti dulu.
	sv2 := &Server{Name: "SSH", Proto: "ssh", Params: map[string]string{
		"server": "10.0.0.1", "port": "22", "username": "root", "password": "p",
	}}
	ob2, _ := serverToOutbound(sv2, "SSH")
	if ob2["server"] != "10.0.0.1" || ob2["server_port"] != 22 {
		t.Fatalf("SSH polos harus direct, dapat %v:%v", ob2["server"], ob2["server_port"])
	}
}

// wsTestServer: server WS tiruan — baca header HTTP, jawab 101, lalu
// echo frame WS (unmask klien, kirim balik tanpa mask).
func wsTestServer(t *testing.T) net.Listener {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				var req bytes.Buffer
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					req.WriteString(line)
					if strings.TrimRight(line, "\r\n") == "" {
						break
					}
				}
				if !strings.Contains(req.String(), "Upgrade: websocket") {
					return
				}
				_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
				// Frame dari klien mungkin sudah sebagian terbaca br;
				// di tes ini payload kecil dan belum terbaca, jadi baca dari c
				// lewat br agar tidak hilang bila sudah buffered.
				for {
					op, payload, err := readSSHWSFrame(br)
					if err != nil {
						return
					}
					if op == 0x8 {
						return
					}
					// Kirim balik sebagai frame server (tanpa mask).
					var hdr [10]byte
					hdr[0] = 0x82
					n := len(payload)
					var hlen int
					switch {
					case n < 126:
						hdr[1] = byte(n)
						hlen = 2
					case n < 65536:
						hdr[1] = 126
						hdr[2] = byte(n >> 8)
						hdr[3] = byte(n)
						hlen = 4
					default:
						hdr[1] = 127
						hlen = 10
					}
					if _, err := c.Write(hdr[:hlen]); err != nil {
						return
					}
					if _, err := c.Write(payload); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln
}

func TestSSHbridgeDialWS(t *testing.T) {
	ln := wsTestServer(t)
	defer ln.Close()
	prof := sshBridgeProfile{
		SSHHost: "d123.cloudfront.net", RemoteProxy: ln.Addr().String(),
		Payload: "GET / HTTP/1.1[crlf]Host: [host][crlf]Connection: Upgrade[crlf]Upgrade: websocket[crlf][crlf]",
	}
	conn, _, err := sshBridgeDial(prof)
	if err != nil {
		t.Fatalf("dial jembatan WS gagal: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	msg := []byte("SSH-2.0-test\r\n")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("tulis WS gagal: %v", err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], msg) {
		t.Fatalf("echo WS gagal: n=%d err=%v got=%q", n, err, buf[:n])
	}
}

func TestSSHbridgeDialRaw(t *testing.T) {
	// Server pipa mentah: jawab 200 CONNECT, lalu echo mentah.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimRight(line, "\r\n") == "" {
						break
					}
				}
				_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
				_, _ = io.Copy(c, br)
			}(c)
		}
	}()
	prof := sshBridgeProfile{
		SSHHost: "h.example", RemoteProxy: ln.Addr().String(),
		Payload: "CONNECT [host]:22 HTTP/1.1[crlf][crlf]",
	}
	conn, _, err := sshBridgeDial(prof)
	if err != nil {
		t.Fatalf("dial pipa mentah gagal: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	msg := []byte("SSH-2.0-raw\r\n")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("tulis mentah gagal: %v", err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], msg) {
		t.Fatalf("echo mentah gagal: n=%d err=%v got=%q", n, err, buf[:n])
	}
}

func sshTestServer(t *testing.T, id, proxy string) Server {
	t.Helper()
	return Server{ID: id, Name: "SSH-" + id, Proto: "ssh", Enabled: true, Params: map[string]string{
		"server": "h" + id + ".example", "port": "443", "username": "u", "password": "p",
		"ssh_payload_on": "1", "ssh_proxy": proxy, "ssh_payload": "GET /[crlf][crlf]",
	}}
}

func TestSSHbridgeAssignmentsDistinct(t *testing.T) {
	t.Skip("TaptapBox: jalur mihomo tidak ada, hanya sing-box")
	a := &App{store: &Store{Settings: Settings{}}}
	a.store.Servers = []Server{
		sshTestServer(t, "200", "2.2.2.2:443"),
		sshTestServer(t, "100", "1.1.1.1:443"),
		{ID: "300", Name: "SSH-mati", Proto: "ssh", Enabled: false, Params: map[string]string{"server": "x", "password": "p", "ssh_payload_on": "1", "ssh_proxy": "3.3.3.3:443", "ssh_payload": "GET /[crlf][crlf]"}},
		{ID: "400", Name: "VMess", Proto: "vmess", Enabled: true, Params: map[string]string{"server": "x", "uuid": "u"}},
	}
	ports, profs := a.sshBridgeAssignmentsLocked()
	if len(ports) != 2 {
		t.Fatalf("harus 2 assignment, dapat %v", ports)
	}
	// ID terurut: 100 -> 15411, 200 -> 15412 (bukan urutan daftar).
	if ports["100"] != sshBridgePort || ports["200"] != sshBridgePort+1 {
		t.Fatalf("port tidak terurut-ID: %v", ports)
	}
	if profs["100"].RemoteProxy != "1.1.1.1:443" || profs["200"].RemoteProxy != "2.2.2.2:443" {
		t.Fatalf("profil tertukar: %v", profs)
	}
	// buildServers mihomo (default core=clash) harus memakai port itu.
	built := a.buildServers()
	got := map[string]int{}
	for _, b := range built {
		if b.Ob["name"] != nil {
			// tag = nama unik, bukan ID; petakan lewat server/port saja
			got[b.Tag] = b.Ob["port"].(int)
		}
	}
	if len(got) != 3 { // 2 SSH + 1 vmess
		t.Fatalf("built harus 3, dapat %v", got)
	}
	found := map[int]bool{}
	for _, p := range got {
		found[p] = true
	}
	if !found[sshBridgePort] || !found[sshBridgePort+1] {
		t.Fatalf("port jembatan tidak ada di built: %v", got)
	}
	// Core sing-box: field-nya server_port, bukan port.
	a.store.Settings.Core = "singbox"
	built = a.buildServers()
	found = map[int]bool{}
	for _, b := range built {
		if p, ok := b.Ob["server_port"].(int); ok {
			found[p] = true
		}
	}
	if !found[sshBridgePort] || !found[sshBridgePort+1] {
		t.Fatalf("sing-box port jembatan hilang: %v", built)
	}
}

func TestSyncSSHbridgeListeners(t *testing.T) {
	a := &App{store: &Store{Settings: Settings{}}}
	a.store.Servers = []Server{
		sshTestServer(t, "100", "1.1.1.1:443"),
		sshTestServer(t, "200", "2.2.2.2:443"),
	}
	a.syncSSHbridge()
	if len(a.sshBridgeLn) != 2 {
		a.stopSSHbridge()
		t.Fatalf("harus 2 listener, dapat %d", len(a.sshBridgeLn))
	}
	if _, ok := a.sshBridgeLn[sshBridgePort]; !ok {
		t.Fatal("listener 15411 hilang")
	}
	a.stopSSHbridge()
	if len(a.sshBridgeLn) != 0 {
		t.Fatalf("stop harus mengosongkan listener, dapat %d", len(a.sshBridgeLn))
	}
}

func TestAddWSHandshakeHeaders(t *testing.T) {
	base := "GET / HTTP/1.1\r\nHost: h.example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"
	got := addWSHandshakeHeaders(base)
	if !strings.Contains(got, "Sec-WebSocket-Key: ") || !strings.Contains(got, "Sec-WebSocket-Version: 13") {
		t.Fatalf("Key/Version tidak ditambah: %q", got)
	}
	if !strings.HasSuffix(got, "\r\n\r\n") {
		t.Fatalf("harus diakhiri baris kosong: %q", got)
	}
	// Sudah ada Key -> tidak diduplikasi.
	withKey := "GET / HTTP/1.1\r\nHost: h.example\r\nUpgrade: websocket\r\nSec-WebSocket-Key: abc==\r\n\r\n"
	if addWSHandshakeHeaders(withKey) != withKey {
		t.Fatal("Key yang sudah ada harus dibiarkan")
	}
	// Payload non-WS -> tidak ditambah.
	plain := "CONNECT h.example:22 HTTP/1.1\r\n\r\n"
	if addWSHandshakeHeaders(plain) != plain {
		t.Fatal("payload non-WS tidak boleh ditambah Key")
	}
}

func TestSSHbridgeDialRawWS(t *testing.T) {
	// Server 101 + echo mentah (bukan frame WS): meniru proxy injector.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimRight(line, "\r\n") == "" {
						break
					}
				}
				_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
				_, _ = io.Copy(c, br)
			}(c)
		}
	}()
	prof := sshBridgeProfile{
		SSHHost: "h.example", RemoteProxy: ln.Addr().String(), Raw: true,
		Payload: "GET / HTTP/1.1[crlf]Host: [host][crlf]Connection: Upgrade[crlf]Upgrade: websocket[crlf][crlf]",
	}
	conn, _, err := sshBridgeDial(prof)
	if err != nil {
		t.Fatalf("dial raw-WS gagal: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	msg := []byte("SSH-2.0-raw\r\n")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("tulis raw gagal: %v", err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], msg) {
		t.Fatalf("echo raw-WS gagal: n=%d err=%v got=%q", n, err, buf[:n])
	}
}

func TestSplitSSHPayloadRequests(t *testing.T) {
	if got := splitSSHPayloadRequests("GET / HTTP/1.1\r\nHost: h\r\n\r\n"); len(got) != 1 {
		t.Fatalf("satu request harus 1 blok, dapat %d: %q", len(got), got)
	}
	got := splitSSHPayloadRequests("HEAD / HTTP/1.1\r\nHost: depan\r\n\r\nBMOVE / HTTP/1.1\r\nHost: h\r\n\r\n")
	if len(got) != 2 || !strings.HasPrefix(got[0], "HEAD ") || !strings.HasPrefix(got[1], "BMOVE ") {
		t.Fatalf("dua request harus 2 blok, dapat %q", got)
	}
}

// TestSSHbridgeDialMultiResponse meniru log HTTP Custom 2026-10-27
// 02:02:29: request depan HEAD dibalas 301, request kedua BMOVE
// upgrade WS dibalas 101, lalu frame WS. Kode lama gagal di 301 di
// kedua mode; kode baru harus lolos sampai 101.
func TestSSHbridgeDialMultiResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				readBlock := func() (string, bool) {
					var sb strings.Builder
					for {
						line, err := br.ReadString('\n')
						if err != nil {
							return "", false
						}
						sb.WriteString(line)
						if strings.TrimRight(line, "\r\n") == "" {
							return sb.String(), true
						}
					}
				}
				first, ok := readBlock()
				if !ok || !strings.HasPrefix(first, "HEAD ") {
					return
				}
				_, _ = io.WriteString(c, "HTTP/1.1 301 Moved Permanently\r\nLocation: https://x/\r\n\r\n")
				second, ok := readBlock()
				if !ok || !strings.HasPrefix(second, "BMOVE ") || !strings.Contains(second, "Sec-WebSocket-Key: ") {
					return
				}
				_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
				for {
					op, payload, err := readSSHWSFrame(br)
					if err != nil || op == 0x8 {
						return
					}
					hdr := []byte{0x82, byte(len(payload))}
					if _, err := c.Write(hdr); err != nil {
						return
					}
					if _, err := c.Write(payload); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	prof := sshBridgeProfile{
		SSHHost: "akun.example:80", RemoteProxy: ln.Addr().String(),
		Payload: "HEAD / HTTP/1.1[crlf]Host: edu.ruangguru.com:80[crlf]Connection: Keep-Alive[crlf]User-Agent: [ua][crlf][crlf]BMOVE / HTTP/1.1[crlf]Host: [host][crlf]Upgrade: websocket[crlf][crlf]",
	}
	conn, _, err := sshBridgeDial(prof)
	if err != nil {
		t.Fatalf("dial multi-respons gagal: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	msg := []byte("SSH-2.0-multi\r\n")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("tulis WS multi gagal: %v", err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], msg) {
		t.Fatalf("echo WS multi gagal: n=%d err=%v got=%q", n, err, buf[:n])
	}
}

// rawBannerServer: proxy tiruan — baca satu request, jawab 101, lalu
// kirim banner SSH mentah (mode Pipa mentah, tanpa frame WS).
func rawBannerServer(t *testing.T, banner string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimRight(line, "\r\n") == "" {
						break
					}
				}
				_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\n\r\n")
				if banner != "" {
					_, _ = io.WriteString(c, banner)
				}
				// Tahan sebentar agar tes sempat membaca, lalu tutup.
				time.Sleep(200 * time.Millisecond)
			}(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

func TestSSHTestPingBanner(t *testing.T) {
	proxy := rawBannerServer(t, "SSH-2.0-fake\r\n")
	sv := Server{ID: "teskan", Name: "Teskan", Proto: "ssh", Enabled: true, Params: map[string]string{
		"server": "ssh.example", "port": "80", "username": "u", "password": "p",
		"ssh_payload_on": "1", "ssh_proxy": proxy,
		"ssh_payload": "GET / HTTP/1.1[crlf]Host: [host][crlf]Upgrade: websocket[crlf][crlf]",
		"ssh_mode":    "raw",
	}}
	a := &App{store: &Store{Servers: []Server{sv}}}
	if ms := a.sshTestPing("teskan", "ssh.example", "80"); ms <= 0 {
		t.Fatalf("banner SSH harus lolos dengan ms>0, dapat %d", ms)
	}
	// Tanpa banner: proxy terhubung (101) tapi server diam -> Timeout (-1).
	proxy2 := rawBannerServer(t, "")
	sv.Params["ssh_proxy"] = proxy2
	a2 := &App{store: &Store{Servers: []Server{sv}}}
	if ms := a2.sshTestPing("teskan", "ssh.example", "80"); ms != -1 {
		t.Fatalf("tanpa banner harus -1, dapat %d", ms)
	}
}

func TestDetectSSHFraming(t *testing.T) {
	cases := []struct {
		in   []byte
		want string
	}{
		{[]byte("SSH-"), "raw"},
		{[]byte("SSH-2"), "raw"},
		{[]byte{0x82, 0x05, 'a', 'b'}, "ws"},
		{[]byte{0x81, 0x10, 'a', 'b'}, "ws"},
		{[]byte{0x82, 0x85, 'a', 'b'}, ""}, // termask -> bukan frame server normal
		{[]byte("HT"), ""},
		{[]byte{}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := detectSSHFraming(c.in); got != c.want {
			t.Fatalf("detectSSHFraming(%q) = %q, mau %q", c.in, got, c.want)
		}
	}
}

// framingServer: proxy tiruan — jawab 101, lalu kirim data pertama
// mentah (raw) atau sebagai satu frame WS (ws), lalu tahan.
func framingServer(t *testing.T, mode string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimRight(line, "\r\n") == "" {
						break
					}
				}
				_, _ = io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\n\r\n")
				banner := "SSH-2.0-fake\r\n"
				if mode == "ws" {
					_, _ = c.Write([]byte{0x82, byte(len(banner))})
					_, _ = io.WriteString(c, banner)
				} else {
					_, _ = io.WriteString(c, banner)
				}
				time.Sleep(500 * time.Millisecond)
			}(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

func TestSSHBridgeDialFramingDetection(t *testing.T) {
	// Server kirim banner mentah, padahal dropdown WS: deteksi harus
	// menang, dial mengembalikan koneksi mentah (bukan frame WS).
	proxy := framingServer(t, "raw")
	prof := sshBridgeProfile{RemoteProxy: proxy, SSHHost: "ssh.example",
		Payload: "GET / HTTP/1.1[crlf]Host: [host][crlf]Upgrade: websocket[crlf][crlf]", Raw: false}
	conn, _, err := sshBridgeDial(prof)
	if err != nil {
		t.Fatalf("dial raw-detected gagal: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "SSH-") {
		t.Fatalf("harus baca banner mentah, dapat %q err=%v", line, err)
	}
	// Server kirim frame WS, padahal dropdown Pipa mentah: deteksi
	// harus menang, dial mengembalikan koneksi frame WS.
	proxy2 := framingServer(t, "ws")
	prof2 := sshBridgeProfile{RemoteProxy: proxy2, SSHHost: "ssh.example",
		Payload: "GET / HTTP/1.1[crlf]Host: [host][crlf]Upgrade: websocket[crlf][crlf]", Raw: true}
	conn2, _, err := sshBridgeDial(prof2)
	if err != nil {
		t.Fatalf("dial ws-detected gagal: %v", err)
	}
	defer conn2.Close()
	_ = conn2.SetReadDeadline(time.Now().Add(5 * time.Second))
	line2, err := bufio.NewReader(conn2).ReadString('\n')
	if err != nil || !strings.HasPrefix(line2, "SSH-") {
		t.Fatalf("harus baca banner dari frame WS, dapat %q err=%v", line2, err)
	}
}
