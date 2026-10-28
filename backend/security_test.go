package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// hostnameOK: hanya IP / localhost (anti DNS-rebinding).
func TestHostnameOK(t *testing.T) {
	ok := []string{"127.0.0.1:5056", "192.168.43.1:5056", "localhost:5056", "[::1]:5056", "10.0.0.2", ""}
	bad := []string{"evil.example.com:5056", "evil.example.com", "rebind.attacker.net"}
	for _, h := range ok {
		if !hostnameOK(h) {
			t.Errorf("host %q seharusnya diizinkan", h)
		}
	}
	for _, h := range bad {
		if hostnameOK(h) {
			t.Errorf("host %q seharusnya ditolak", h)
		}
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	if !isLoopbackAddr("127.0.0.1:41234") || !isLoopbackAddr("[::1]:41234") {
		t.Error("127.0.0.1 / ::1 harus dianggap loopback")
	}
	if isLoopbackAddr("192.168.43.20:41234") {
		t.Error("IP LAN bukan loopback")
	}
}

// Folder FTP tidak dibatasi: semua path absolut diizinkan, relatif ditolak.
func TestFtpRootAllowed(t *testing.T) {
	for _, r := range []string{"/", "/sdcard", "/sdcard/Download", "/sdcard/../data", "/storage/emulated/0", "/data/media/0", "/data", "/data/data/com.x", "/system", "/sdcardx"} {
		if !ftpRootAllowed(r) {
			t.Errorf("%q seharusnya diizinkan", r)
		}
	}
	for _, r := range []string{"", "sdcard", "data/media", "a/b"} {
		if ftpRootAllowed(r) {
			t.Errorf("%q seharusnya ditolak", r)
		}
	}
}

// guard: loopback bebas; jaringan wajib Basic admin:<WebPass>; Origin asing ditolak.
func TestGuard(t *testing.T) {
	a := testApp(t)
	a.store.Settings.WebPass = "rahasia123"
	h := a.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	do := func(remote, host, origin, user, pass string) int {
		r := httptest.NewRequest("GET", "/api/status", nil)
		r.RemoteAddr = remote
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if user != "" {
			r.SetBasicAuth(user, pass)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := do("127.0.0.1:1", "127.0.0.1:5056", "", "", ""); c != 200 {
		t.Errorf("loopback tanpa login harus 200, dapat %d", c)
	}
	if c := do("192.168.43.20:1", "192.168.43.1:5056", "", "", ""); c != 401 {
		t.Errorf("jaringan tanpa login harus 401, dapat %d", c)
	}
	if c := do("192.168.43.20:1", "192.168.43.1:5056", "", "admin", "salah"); c != 401 {
		t.Errorf("password salah harus 401, dapat %d", c)
	}
	if c := do("192.168.43.20:1", "192.168.43.1:5056", "", "admin", "rahasia123"); c != 200 {
		t.Errorf("login benar harus 200, dapat %d", c)
	}
	if c := do("127.0.0.1:1", "evil.example.com:5056", "", "", ""); c != 403 {
		t.Errorf("Host domain harus 403, dapat %d", c)
	}
	if c := do("127.0.0.1:1", "127.0.0.1:5056", "http://evil.example.com", "", ""); c != 403 {
		t.Errorf("Origin asing harus 403, dapat %d", c)
	}
	if c := do("127.0.0.1:1", "127.0.0.1:5056", "http://127.0.0.1:5056", "", ""); c != 200 {
		t.Errorf("Origin sama harus 200, dapat %d", c)
	}
}

// Server terpilih hilang / belum dipilih -> grup select utama berisi proxy
// (mihomo memilih proxy pertama; tak pernah jatuh ke DIRECT).
func TestSelectorDefaultsToFirstServer(t *testing.T) {
	t.Skip("TaptapBox: jalur mihomo tidak ada, hanya sing-box")
	a := testApp(t)
	a.store.Settings.SelectedID = "id-yang-tidak-ada"
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := cfg["proxy-groups"].([]any)
	if len(groups) == 0 {
		t.Fatal("proxy-groups kosong")
	}
	sel, _ := groups[0].(map[string]any)
	proxies, _ := sel["proxies"].([]string)
	if len(proxies) == 0 {
		t.Fatal("grup utama kosong")
	}
	if proxies[0] == "DIRECT" {
		t.Fatalf("proxy pertama grup utama tidak boleh DIRECT")
	}
}

// Level log default warn -> warning mihomo; nilai tak dikenal jatuh ke warning.
func TestLogLevel(t *testing.T) {
	a := testApp(t)
	if got := a.mihomoLogLevel(); got != "warning" {
		t.Fatalf("default level harus warning, dapat %q", got)
	}
	a.store.Settings.LogLevel = "bukan-level"
	if got := a.mihomoLogLevel(); got != "warning" {
		t.Fatalf("level tak dikenal harus warning, dapat %q", got)
	}
	a.store.Settings.LogLevel = "info"
	if got := a.mihomoLogLevel(); got != "info" {
		t.Fatalf("level info harus dipertahankan, dapat %q", got)
	}
}

// fwmark tproxy harus di bit tinggi: bit 0-15 dipakai Android untuk netId,
// mark 0x1 dulu bertabrakan dengan jaringan ber-ID ganjil.
func TestTproxyMarkHighBit(t *testing.T) {
	v, err := strconv.ParseUint(tpMarkVal, 0, 32)
	if err != nil {
		t.Fatal(err)
	}
	if v&0xffff != 0 {
		t.Fatalf("tpMarkVal %s menyentuh bit netId Android (0-15)", tpMarkVal)
	}
	if tpMark != tpMarkVal+"/"+tpMarkVal {
		t.Fatalf("tpMark harus nilai/mask, dapat %s", tpMark)
	}
}

// Daftar bypass valid CIDR dan interface hotspot tidak boleh memuat uplink wlan0.
func TestTproxyBypassLists(t *testing.T) {
	for _, c := range tpIntranet {
		if _, _, err := net.ParseCIDR(c); err != nil {
			t.Errorf("CIDR %q tidak valid: %v", c, err)
		}
	}
	for _, ifc := range tpAPIfaces {
		if ifc == "wlan0" || ifc == "wlan+" {
			t.Errorf("interface %q akan menangkap balasan uplink WiFi", ifc)
		}
	}
}

// Login HTML: password saja -> cookie sesi 1 tahun; sesi valid -> langsung masuk.
func TestLoginFlow(t *testing.T) {
	a := testApp(t)
	a.store.Settings.WebPass = "12345"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", a.handleLogin)
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/taptapbox/login.html", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := a.guard(mux)
	lan, host := "192.168.43.20:1", "192.168.43.1:5056"
	do := func(method, path, body, cookie string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		r := httptest.NewRequest(method, path, rd)
		r.RemoteAddr = lan
		r.Host = host
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// Halaman tanpa login -> 302 ke /login.html (tanpa popup Basic).
	w := do("GET", "/", "", "")
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/taptapbox/login.html") {
		t.Fatalf("GET / tanpa login harus 302 /taptapbox/login.html, dapat %d %q", w.Code, w.Header().Get("Location"))
	}
	// API tanpa login -> 401, tanpa header WWW-Authenticate.
	w = do("GET", "/api/status", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("API tanpa login harus 401, dapat %d", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("tidak boleh ada popup Basic bawaan browser")
	}
	// Halaman login + ikon boleh diakses tanpa login.
	mux.HandleFunc("/taptapbox/icons/icon-192.png", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	for _, p := range []string{"/taptapbox/login.html", "/taptapbox/icons/icon-192.png"} {
		if w := do("GET", p, "", ""); w.Code != http.StatusOK {
			t.Fatalf("GET %s harus 200, dapat %d", p, w.Code)
		}
	}
	// Password salah -> 401.
	w = do("POST", "/api/login", `{"password":"salah"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("password salah harus 401, dapat %d", w.Code)
	}
	// Password benar -> 200 + cookie sesi HttpOnly.
	w = do("POST", "/api/login", `{"password":"12345"}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("password benar harus 200, dapat %d", w.Code)
	}
	ck := w.Header().Get("Set-Cookie")
	if !strings.Contains(ck, "ttb_sess=") || !strings.Contains(ck, "HttpOnly") {
		t.Fatalf("cookie sesi tidak diset: %q", ck)
	}
	cookie := strings.Split(ck, ";")[0]
	// Dengan cookie sesi: API 200, halaman 200 (langsung masuk).
	if w := do("GET", "/api/status", "", cookie); w.Code != http.StatusOK {
		t.Fatalf("API dg sesi harus 200, dapat %d", w.Code)
	}
	if w := do("GET", "/", "", cookie); w.Code != http.StatusOK {
		t.Fatalf("GET / dg sesi harus 200, dapat %d", w.Code)
	}
	// Cookie palsu ditolak.
	if w := do("GET", "/api/status", "", "ttb_sess=palsu"); w.Code != http.StatusUnauthorized {
		t.Fatalf("sesi palsu harus 401, dapat %d", w.Code)
	}
	// Basic auth lama (admin:pass) tetap diterima.
	r := httptest.NewRequest("GET", "/api/status", nil)
	r.RemoteAddr = lan
	r.Host = host
	r.SetBasicAuth("admin", "12345")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r)
	if w2.Code != http.StatusOK {
		t.Fatalf("Basic auth harus 200, dapat %d", w2.Code)
	}
	// Sesi tetap valid untuk App kedua dg dataDir sama (secret persisten).
	a2 := &App{dataDir: a.dataDir}
	if !a2.validSessionToken(strings.TrimPrefix(cookie, "ttb_sess=")) {
		t.Fatal("token sesi harus tetap valid setelah restart (secret persisten)")
	}
}
