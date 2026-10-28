package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sagernet/sing-box"
)

// osSignal0 = sinyal 0 untuk cek proses masih hidup (linux/android).
func osSignal0() os.Signal { return syscall.Signal(0) }

// appVersion diisi saat build: -ldflags "-X main.appVersion=v2.2.0"
var appVersion = "dev"

type App struct {
	dataDir       string
	jsonDir       string
	logDir        string
	webuiDir      string
	binDir        string
	mihomoBin     string
	singboxBin    string
	runningCore   string // core yang sedang berjalan: "clash" | "singbox" | ""
	verMihomo     string // versi mihomo hasil deteksi biner (version.go)
	verSingbox    string // versi sing-box hasil deteksi biner (version.go)
	verMu         sync.RWMutex
	verLastDetect time.Time // deteksi terakhir (untuk throttle status)
	updMu         sync.Mutex
	updCache      *latestCache  // cache rilis terbaru (updates.go, TTL 1 jam)
	selfRelCache  *selfRelCache // cache kartu Rilis terbaru (updates.go, TTL 1 jam, sama seperti Download)
	updDlMu       sync.Mutex    // hanya satu unduhan/pemasangan dalam satu waktu
	effMu         sync.Mutex
	effMixedPort  int // port mixed yang didengar core yang jalan (manualinject.go)
	helperUID     int // UID helper yang pernah berhasil (fetchhelper.go)
	helperUIDMu   sync.Mutex
	sshBridgeMu   sync.Mutex
	sshBridgeLn   map[int]net.Listener
	sshBridgeProf map[int]sshBridgeProfile
	sshLogMu      sync.Mutex
	sshLogEntries []sshLogEntry
	sshLogW       *capLogWriter
	sshKexLogged  map[string]bool // diagnostik KEX sekali per server/sisi (sshkex.go)
	bvMu          sync.Mutex
	bv            *badvpnRelay // relay BadVPN (nil bila nonaktif; lihat badvpn.go)
	store         *Store

	mu        sync.Mutex
	cmd       *exec.Cmd
	done      chan struct{}
	boxInst   interface{ Start() error; Close() error }
	boxCancel context.CancelFunc
	boxDone   chan struct{}
	running   bool
	startedAt time.Time
	logger    *log.Logger

	scrcpy *scrcpySession // sesi mirror layar (view Scrcpy)
}

// logf didefinisikan di logx.go (format "04:20 pm [Info]: pesan").

var idMu sync.Mutex
var idLast int64

// newID: unik walau dipanggil beruntun (import ratusan server sekaligus).
func newID() string {
	idMu.Lock()
	defer idMu.Unlock()
	n := time.Now().UnixNano()
	if n <= idLast {
		n = idLast + 1
	}
	idLast = n
	return fmt.Sprintf("%d", n)
}

// ---------- keamanan WebUI ----------

// lastHit: waktu (unix detik) request WebUI/API terakhir. Dipakai agar tugas
// latar (auto-test) berhenti saat WebUI tidak dibuka (hemat baterai).
var lastHit int64

// defaultWebPass: password bawaan akses WebUI dari jaringan (tanpa username).
const defaultWebPass = "12345"

func isLoopbackAddr(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// hostnameOK: header Host harus IP atau "localhost" (anti DNS-rebinding:
// situs jahat yang di-resolve ke 127.0.0.1 membawa Host berupa nama domain).
func hostnameOK(h string) bool {
	if h == "" {
		return true
	}
	host := h
	if hh, _, err := net.SplitHostPort(h); err == nil {
		host = hh
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return net.ParseIP(host) != nil
}

// guard membungkus seluruh WebUI/API:
//  1. Host harus IP/localhost (anti DNS-rebinding)
//  2. Origin (bila ada) harus sama dengan Host (anti CSRF lintas-situs)
//  3. koneksi dari JARINGAN (bukan 127.0.0.1) wajib login: halaman /login.html
//     (password saja, tanpa username) -> cookie sesi 1 tahun. Basic auth
//     user "admin" tetap diterima untuk kompatibilitas. Dari HP ini sendiri
//     (127.0.0.1) tanpa password.
func (a *App) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostnameOK(r.Host) {
			http.Error(w, "host tidak diizinkan", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || u.Host != r.Host {
				http.Error(w, "origin tidak diizinkan", http.StatusForbidden)
				return
			}
		}
		if loginWhitelist(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !a.authed(r) {
			// API: 401 tanpa WWW-Authenticate (tanpa popup Basic bawaan browser).
			if strings.HasPrefix(r.URL.Path, "/api/") {
				errJSON(w, 401, "butuh login: buka halaman login WebUI dulu")
				return
			}
			// Halaman: arahkan ke halaman login HTML.
			http.Redirect(w, r, "/taptapbox/login.html", http.StatusFound)
			return
		}
		atomic.StoreInt64(&lastHit, time.Now().Unix())
		next.ServeHTTP(w, r)
	})
}

// ---------- lifecycle mihomo ----------

func (a *App) coreAlive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running && a.boxInst != nil {
		return true
	}
	if !a.running || a.cmd == nil || a.cmd.Process == nil {
		return false
	}
	// kirim sinyal 0 untuk cek proses masih hidup
	if err := a.cmd.Process.Signal(osSignal0()); err != nil {
		a.running = false
		return false
	}
	return true
}

func (a *App) startMihomo() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running && a.cmd != nil && a.cmd.Process != nil {
		return fmt.Errorf("mihomo sudah berjalan")
	}
	cfgPath, err := a.writeConfig()
	if err != nil {
		return fmt.Errorf("gagal generate config: %v", err)
	}
	// Header sesi ala xclash: setiap konek log diperbarui dengan blok ini.
	a.logConnectHeader(cfgPath)
	// geodata (geoip.dat/geosite.dat) harus ada di workdir SEBELUM -t,
	// karena rule GEOIP/GEOSITE inline membutuhkannya saat validasi.
	a.ensureGeodata()
	// validasi dulu
	if out, err := a.mihomoTest(cfgPath); err != nil {
		return fmt.Errorf("config tidak valid: %v\n%s", err, string(out))
	}
	// bersihkan tun0 / ip rule basi (mis. crash sebelumnya) agar TUN bisa dibuat ulang
	pm := a.store.Settings.ProxyMode
	if pm == "tun" || pm == "enhance" {
		a.cleanStaleTun()
	}
	// Log mihomo dibatasi 200 baris (capLogWriter, segar tiap start).
	logPath := filepath.Join(a.logDir, "clash.log")
	lf := newCapLogWriter(logPath)
	if lf == nil {
		return fmt.Errorf("gagal buka mihomo.log")
	}
	cmd := exec.Command(a.mihomoBin, "-d", a.mihomoWorkDir(), "-f", cfgPath)
	cmd.Stdout = lf
	cmd.Stderr = lf
	// external-ui boleh di luar home dir mihomo -> daftarkan ke SAFE_PATHS
	cmd.Env = append(os.Environ(), "SAFE_PATHS="+a.webuiDir+string(os.PathListSeparator)+a.dataDir)
	if err := cmd.Start(); err != nil {
		lf.Close()
		return err
	}
	done := make(chan struct{})
	a.cmd = cmd
	a.done = done
	a.running = true
	a.runningCore = "clash"
	a.startedAt = time.Now()
	go func() {
		cmd.Wait()
		lf.Close()
		a.mu.Lock()
		crashed := a.cmd == cmd
		if crashed {
			a.running = false
			a.cmd = nil
			a.runningCore = ""
		}
		a.mu.Unlock()
		close(done)
		if crashed {
			// mati sendiri: bersihkan iptables supaya internet tidak macet
			a.logErrf("mihomo berhenti tak terduga")
			a.clearTransparentProxy()
		}
	}()
	a.logf("mihomo started (pid %d)", cmd.Process.Pid)
	if a.store.Settings.WebListenAll && a.store.Settings.Secret == "" {
		a.logWarnf("PERINGATAN: Clash API terbuka di 0.0.0.0:%d tanpa secret — siapa pun di WiFi/hotspot bisa mengendalikannya. Isi 'Secret Clash API' di Pengaturan bila perlu.",
			a.store.Settings.ClashPort)
	}
	if a.store.Settings.TransparentProxy {
		// beri jeda singkat agar inbound siap
		go func() {
			time.Sleep(2 * time.Second)
			// iptables untuk penangkapan transparan
			if err := a.applyTransparentProxy(); err != nil {
				a.logErrf("iptables gagal: %v", err)
			} else {
				a.logf("transparent proxy aktif (mode %s)", a.store.Settings.ProxyMode)
			}
		}()
	}
	go a.syncSelected()
	return nil
}

// syncSelected memaksa selector grup utama mengikuti server pilihan user
// (cache_file mihomo bisa menyimpan pilihan lama dari zashboard).
func (a *App) syncSelected() {
	a.store.mu.RLock()
	id := a.store.Settings.SelectedID
	mg := a.mainGroupTag()
	a.store.mu.RUnlock()
	if id == "" {
		return
	}
	tag := a.tagOfServer(id)
	if tag == "" {
		return
	}
	for i := 0; i < 15; i++ {
		time.Sleep(time.Second)
		if !a.coreAlive() {
			return
		}
		if err := a.SelectProxy(mg, tag); err == nil {
			return
		}
	}
}

// stopMihomo: SIGTERM dulu agar mihomo membongkar tun0 + ip rule sendiri;
// SIGKILL hanya bila macet (SIGKILL langsung meninggalkan rule rusak).
func (a *App) stopMihomo() {
	a.mu.Lock()
	cmd := a.cmd
	done := a.done
	a.cmd = nil
	a.running = false
	a.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
			a.cleanStaleTun()
		}
	}
	a.clearTransparentProxy()
	a.logf("mihomo stopped")
}

func (a *App) uptimeStr() string {
	if !a.coreAlive() || a.startedAt.IsZero() {
		return "-"
	}
	return time.Since(a.startedAt).Round(time.Second).String()
}

// ---------- HTTP helpers ----------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(v)
}

func errJSON(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	writeJSON(w, map[string]string{"error": msg})
}

// ---------- handlers ----------

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	// Segarkan versi (throttle 1 menit) agar ganti biner di luar
	// daemon ikut tercermin di WebUI tanpa restart backend.
	a.detectCoreVersionsThrottled()
	st := a.cfg()
	cfgFile := a.activeConfigPath()
	coreVer := func(core string) string {
		if v := a.coreVersion(core); v != "" {
			return v
		}
		return "belum terpasang"
	}
	ver := "TaptapBox " + appVersion + " (mihomo " + coreVer("clash") + ")"
	if st.ActiveCore() == "singbox" {
		cfgFile = a.boxActiveConfigPath()
		ver = "TaptapBox " + appVersion + " (sing-box " + coreVer("singbox") + ")"
	}
	writeJSON(w, map[string]any{
		"running":           a.coreAlive(),
		"mode":              "v2rayng",
		"uptime":            a.uptimeStr(),
		"version":           ver,
		"core":              st.ActiveCore(),
		"running_core":      a.runningCore,
		"mihomo_version":    a.coreVersion("clash"),
		"singbox_version":   a.coreVersion("singbox"),
		"mihomo_installed":  a.coreInstalled("clash"),
		"singbox_installed": a.coreInstalled("singbox"),
		"manual":            st.ManualConfig,
		"config_file":       cfgFile,
		"lan_ip":            ftpLocalIPCached(), // IP privat HP ini (untuk buka WebUI dari HP lain)
	})
}

func (a *App) handleConnect(w http.ResponseWriter, r *http.Request) {
	if a.coreAlive() {
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	if err := a.startCore(); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if err := a.verifyRunning(); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// verifyRunning menunggu sebentar lalu memastikan mihomo tetap hidup.
// Jika langsung mati, kembalikan potongan log agar penyebabnya terlihat.
func (a *App) verifyRunning() error {
	time.Sleep(2500 * time.Millisecond)
	if a.coreAlive() {
		return nil
	}
	logName := "clash.log"
	if a.runningCore == "singbox" {
		logName = "sing-box.log"
	}
	data, _ := os.ReadFile(filepath.Join(a.logDir, logName))
	ls := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(ls) > 15 {
		ls = ls[len(ls)-15:]
	}
	return fmt.Errorf("core mati setelah start:\n%s", strings.Join(ls, "\n"))
}

func (a *App) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	a.stopCore()
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleRestart(w http.ResponseWriter, r *http.Request) {
	a.stopCore()
	time.Sleep(time.Second)
	if err := a.startCore(); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if err := a.verifyRunning(); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// Export semua akun: satu link per baris (format share link, siap import ulang).
func (a *App) handleExportServers(w http.ResponseWriter, r *http.Request) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	var b strings.Builder
	for _, sv := range a.store.Servers {
		link := sv.Link
		if link == "" {
			link = buildLink(sv.Proto, sv.Params, sv.Name)
		}
		// SSH: selalu pakai link segar dari Params (lihat NewStore) —
		// link tersimpan lama bisa dibuat sebelum ssh_* ikut di link.
		if sv.Proto == "ssh" {
			if nl := buildLink("ssh", sv.Params, sv.Name); nl != "" {
				link = nl
			}
		}
		if link == "" {
			continue
		}
		b.WriteString(link)
		b.WriteString("\n")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(b.String()))
}

func (a *App) handleServers(w http.ResponseWriter, r *http.Request) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	writeJSON(w, a.store.Servers)
}

func (a *App) handleImportServers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, 400, "body tidak valid")
		return
	}
	added := 0
	list, failed := parseSubscriptionServers(req.Text)
	a.store.mu.Lock()
	for _, sv := range list {
		sv.ID = newID()
		a.store.Servers = append(a.store.Servers, *sv)
		added++
	}
	_ = a.store.SaveServers()
	a.store.mu.Unlock()
	writeJSON(w, map[string]any{"added": added, "failed": failed})
}

func (a *App) handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/servers/")
	var req struct {
		Name    *string           `json:"name"`
		Enabled *bool             `json:"enabled"`
		Params  map[string]string `json:"params"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, 400, "body tidak valid")
		return
	}
	a.store.mu.Lock()
	sv := a.store.findServer(id)
	if sv == nil {
		a.store.mu.Unlock()
		errJSON(w, 404, "server tidak ditemukan")
		return
	}
	if req.Name != nil {
		sv.Name = *req.Name
	}
	if req.Enabled != nil {
		sv.Enabled = *req.Enabled
	}
	if req.Params != nil {
		if sv.Params == nil {
			sv.Params = map[string]string{}
		}
		for k, v := range req.Params {
			if v == "" {
				delete(sv.Params, k)
			} else {
				sv.Params[k] = v
			}
		}
		// sinkronkan Link agar tetap valid
		if nl := buildLink(sv.Proto, sv.Params, sv.Name); nl != "" {
			sv.Link = nl
		}
	}
	_ = a.store.SaveServers()
	a.store.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/servers/")
	a.store.mu.Lock()
	keep := a.store.Servers[:0]
	for _, s := range a.store.Servers {
		if s.ID != id {
			keep = append(keep, s)
		}
	}
	a.store.Servers = keep
	_ = a.store.SaveServers()
	a.store.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

// handleServerDedup: hapus server duplikat (berdasar link).
func (a *App) handleServerDedup(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	seen := map[string]bool{}
	keep := a.store.Servers[:0]
	removed := 0
	for _, s := range a.store.Servers {
		key := s.Link
		if key == "" {
			key = s.Proto + "|" + s.Params["server"] + ":" + s.Params["port"]
		}
		if seen[key] {
			removed++
			continue
		}
		seen[key] = true
		keep = append(keep, s)
	}
	a.store.Servers = keep
	_ = a.store.SaveServers()
	a.store.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "removed": removed})
}

// handleServerClearUnavailable: hapus server yg tesnya gagal (delay -1).
func (a *App) handleServerClearUnavailable(w http.ResponseWriter, r *http.Request) {
	a.store.mu.Lock()
	keep := a.store.Servers[:0]
	removed := 0
	for _, s := range a.store.Servers {
		if s.DelayMs == -1 {
			removed++
			continue
		}
		keep = append(keep, s)
	}
	a.store.Servers = keep
	_ = a.store.SaveServers()
	a.store.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "removed": removed})
}

// handleCreateServer: tambah server manual dari params.
func (a *App) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string            `json:"name"`
		Proto  string            `json:"proto"`
		Params map[string]string `json:"params"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, 400, "body tidak valid")
		return
	}
	if req.Proto == "" || req.Params["server"] == "" {
		errJSON(w, 400, "proto & address wajib diisi")
		return
	}
	sv := &Server{
		ID: newID(), Name: req.Name, Proto: req.Proto,
		Enabled: true, Params: req.Params, DelayMs: -2,
	}
	if sv.Name == "" {
		sv.Name = req.Params["server"] + ":" + req.Params["port"]
	}
	if nl := buildLink(sv.Proto, sv.Params, sv.Name); nl != "" {
		sv.Link = nl
	}
	a.store.mu.Lock()
	a.store.Servers = append(a.store.Servers, *sv)
	_ = a.store.SaveServers()
	a.store.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "id": sv.ID})
}

// handleServerPick: pilih server aktif (selected_id).
func (a *App) handleServerPick(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, 400, "body tidak valid")
		return
	}
	// Mode manual: daftar server tidak tersambung ke config yang sedang jalan.
	if a.cfg().ManualConfig {
		errJSON(w, 400, "mode manual aktif — ganti akun lewat config manual")
		return
	}
	a.store.mu.Lock()
	a.store.Settings.SelectedID = req.ID
	_ = a.store.SaveSettings()
	mg := a.mainGroupTag()
	a.store.mu.Unlock()
	// sedang terhubung: pindah server langsung tanpa restart
	if a.coreAlive() {
		if tag := a.tagOfServer(req.ID); tag != "" {
			if err := a.SelectProxy(mg, tag); err != nil {
				a.logWarnf("ganti server live gagal: %v", err)
			}
		}
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleServerOrder: urutkan server (by=name|delay) atau susun manual (ids=[...]).
func (a *App) handleServerOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		By  string   `json:"by"`
		IDs []string `json:"ids"`
	}
	_ = readJSON(r, &req)
	a.store.mu.Lock()
	if len(req.IDs) > 0 {
		// Susunan manual dari WebUI (drag & drop): id yang dikenal diurutkan
		// sesuai daftar; id yang tidak disebut tetap di belakang urutan semula.
		pos := map[string]int{}
		for i, id := range req.IDs {
			if _, ok := pos[id]; !ok {
				pos[id] = i
			}
		}
		sort.SliceStable(a.store.Servers, func(i, j int) bool {
			pi, oki := pos[a.store.Servers[i].ID]
			pj, okj := pos[a.store.Servers[j].ID]
			if oki && okj {
				return pi < pj
			}
			return oki && !okj
		})
		_ = a.store.SaveServers()
		a.store.mu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	switch req.By {
	case "name":
		sort.SliceStable(a.store.Servers, func(i, j int) bool {
			return strings.ToLower(a.store.Servers[i].Name) < strings.ToLower(a.store.Servers[j].Name)
		})
	case "delay":
		sort.SliceStable(a.store.Servers, func(i, j int) bool {
			di, dj := a.store.Servers[i].DelayMs, a.store.Servers[j].DelayMs
			if di < 0 {
				di = 1 << 30
			}
			if dj < 0 {
				dj = 1 << 30
			}
			return di < dj
		})
	}
	_ = a.store.SaveServers()
	a.store.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

// tagOfServer: tag outbound server (identik dengan hasil buildServers).
func (a *App) tagOfServer(id string) string {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	for _, b := range a.buildServers() {
		if b.ID == id {
			return b.Tag
		}
	}
	return ""
}

// handleTestServers: kind = "tcp" (TCP ping, tanpa mihomo) | "url" (URL test
// via clash API, perlu mihomo hidup) ; clear=true -> reset hasil tes.
func (a *App) handleTestServers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs   []string `json:"ids"`
		Kind  string   `json:"kind"`
		Clear bool     `json:"clear"`
	}
	_ = readJSON(r, &req)
	if req.Clear {
		a.store.mu.Lock()
		for i := range a.store.Servers {
			a.store.Servers[i].DelayMs = -2
			a.store.Servers[i].TestedAt = 0
		}
		_ = a.store.SaveServers()
		a.store.mu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	kind := req.Kind
	if kind == "" {
		kind = "url"
	}
	if kind == "url" && !a.coreAlive() {
		errJSON(w, 400, "URL Test butuh mihomo berjalan — tekan Connect dulu (atau pakai TCP Ping)")
		return
	}
	type target struct {
		ID, Tag, Host, Port, Proto string
	}
	targets := []target{}
	want := map[string]bool{}
	for _, id := range req.IDs {
		want[id] = true
	}
	a.store.mu.RLock()
	tags := map[string]string{}
	for _, b := range a.buildServers() {
		tags[b.ID] = b.Tag
	}
	for _, s := range a.store.Servers {
		if len(want) > 0 && !want[s.ID] {
			continue
		}
		if len(want) == 0 && !s.Enabled {
			continue
		}
		targets = append(targets, target{s.ID, tags[s.ID], s.Params["server"], orDefault(s.Params["port"], "443"), s.Proto})
	}
	a.store.mu.RUnlock()

	type res struct {
		ID    string `json:"id"`
		Delay int    `json:"delay_ms"`
	}
	results := []res{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 6)
	for _, t := range targets {
		wg.Add(1)
		go func(t target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			d := -1
			switch {
			case kind == "tcp" && udpBased(t.Proto):
				d = -3 // protokol UDP: TCP ping tidak bermakna
			case kind == "tcp" && t.Proto == "ssh":
				// SSH payload tidak boleh TCP ping langsung (lihat sshTestPing):
				// host SSH bukan tujuan yang bisa didial langsung.
				d = a.sshTestPing(t.ID, t.Host, t.Port)
			case kind == "tcp":
				d = tcpPing(t.Host, t.Port, 4*time.Second)
			case t.Tag == "":
				d = -3 // server tidak ikut config (tidak valid/tidak didukung)
			default:
				d = a.DelayTest(t.Tag, "")
			}
			a.store.mu.Lock()
			if sv := a.store.findServer(t.ID); sv != nil {
				sv.DelayMs = d
				sv.TestedAt = time.Now().Unix()
			}
			a.store.mu.Unlock()
			mu.Lock()
			results = append(results, res{ID: t.ID, Delay: d})
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	a.store.mu.Lock()
	_ = a.store.SaveServers()
	a.store.mu.Unlock()
	writeJSON(w, results)
}

func (a *App) handleSubs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		a.store.mu.RLock()
		defer a.store.mu.RUnlock()
		writeJSON(w, a.store.Subs)
	case "POST":
		var s Subscription
		if err := readJSON(r, &s); err != nil {
			errJSON(w, 400, "body tidak valid")
			return
		}
		s.URL = strings.TrimSpace(s.URL)
		if !strings.HasPrefix(s.URL, "http") {
			errJSON(w, 400, "URL subscription harus diawali http(s)://")
			return
		}
		if strings.TrimSpace(s.Name) == "" {
			s.Name = s.URL
		}
		s.ID = newID()
		s.Enabled = true
		a.store.mu.Lock()
		a.store.Subs = append(a.store.Subs, s)
		_ = a.store.SaveSubs()
		a.store.mu.Unlock()
		writeJSON(w, s)
	default:
		errJSON(w, 405, "method tidak didukung")
	}
}

func (a *App) handleSubItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/subscriptions/")
	switch r.Method {
	case "DELETE":
		a.store.mu.Lock()
		keep := a.store.Subs[:0]
		for _, s := range a.store.Subs {
			if s.ID != id {
				keep = append(keep, s)
			}
		}
		a.store.Subs = keep
		// hapus juga server dari sub ini
		skeep := a.store.Servers[:0]
		for _, sv := range a.store.Servers {
			if sv.SubID != id {
				skeep = append(skeep, sv)
			}
		}
		a.store.Servers = skeep
		_ = a.store.SaveSubs()
		_ = a.store.SaveServers()
		a.store.mu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
	case "PUT":
		var req struct {
			Name    *string `json:"name"`
			URL     *string `json:"url"`
			Enabled *bool   `json:"enabled"`
		}
		if err := readJSON(r, &req); err != nil {
			errJSON(w, 400, "body tidak valid")
			return
		}
		a.store.mu.Lock()
		found := false
		for i := range a.store.Subs {
			if a.store.Subs[i].ID != id {
				continue
			}
			found = true
			if req.Name != nil {
				a.store.Subs[i].Name = *req.Name
			}
			if req.URL != nil {
				a.store.Subs[i].URL = strings.TrimSpace(*req.URL)
			}
			if req.Enabled != nil {
				a.store.Subs[i].Enabled = *req.Enabled
			}
		}
		if found {
			_ = a.store.SaveSubs()
		}
		a.store.mu.Unlock()
		if !found {
			errJSON(w, 404, "subscription tidak ditemukan")
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		errJSON(w, 405, "method tidak didukung")
	}
}

// httpFetch mengunduh URL dengan timeout (untuk subscription).
func httpFetch(url string, timeout time.Duration) ([]byte, error) {
	return httpFetchUA(url, timeout, "")
}

// handleSubUpdate: POST {"id": "..."} (opsional) — update satu / semua subscription.
// Server lama dipertahankan (ID, status aktif, hasil tes) bila masih ada di
// subscription baru, sehingga server terpilih tidak hilang.
func (a *App) handleSubUpdate(w http.ResponseWriter, r *http.Request) {
	type subRes struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Added int    `json:"added"`
		Error string `json:"error,omitempty"`
	}
	var req struct {
		ID string `json:"id"`
	}
	_ = readJSON(r, &req)
	results := []subRes{}
	a.store.mu.RLock()
	subs := make([]Subscription, len(a.store.Subs))
	copy(subs, a.store.Subs)
	a.store.mu.RUnlock()
	for _, sub := range subs {
		if req.ID != "" && sub.ID != req.ID {
			continue
		}
		if req.ID == "" && !sub.Enabled {
			continue
		}
		sr := subRes{ID: sub.ID, Name: sub.Name}
		body, err := a.fetchSub(sub.URL)
		if err != nil {
			sr.Error = err.Error()
			results = append(results, sr)
			continue
		}
		list, _ := parseSubscriptionServers(string(body))
		if len(list) == 0 {
			// jangan hapus server lama bila isi subscription tidak terbaca
			sr.Error = "tidak ada server valid di subscription"
			results = append(results, sr)
			continue
		}
		a.store.mu.Lock()
		old := map[string]Server{}
		keep := a.store.Servers[:0]
		for _, sv := range a.store.Servers {
			if sv.SubID != sub.ID {
				keep = append(keep, sv)
			} else {
				old[srvKey(&sv)] = sv
			}
		}
		a.store.Servers = keep
		added := 0
		for _, sv := range list {
			if o, ok := old[srvKey(sv)]; ok {
				sv.ID, sv.Enabled, sv.DelayMs, sv.TestedAt = o.ID, o.Enabled, o.DelayMs, o.TestedAt
				delete(old, srvKey(sv))
			} else {
				sv.ID = newID()
			}
			sv.SubID = sub.ID
			a.store.Servers = append(a.store.Servers, *sv)
			added++
		}
		for i := range a.store.Subs {
			if a.store.Subs[i].ID == sub.ID {
				a.store.Subs[i].UpdatedAt = time.Now().Unix()
				a.store.Subs[i].Count = added
			}
		}
		_ = a.store.SaveServers()
		_ = a.store.SaveSubs()
		a.store.mu.Unlock()
		sr.Added = added
		results = append(results, sr)
	}
	// Langganan berubah -> server & anggota grup otomatis bisa berubah:
	// segarkan override bila sedang aktif.
	if rerr := a.rebaseActiveOverride(); rerr != nil {
		a.logWarnf("override rebase gagal: %v", rerr)
	}
	writeJSON(w, results)
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		a.store.mu.RLock()
		defer a.store.mu.RUnlock()
		writeJSON(w, a.store.Settings)
		return
	}
	a.store.mu.Lock()
	// decode di atas salinan settings saat ini: field yang tidak dikirim
	// otomatis pertahankan nilai lama (termasuk Ver dan flag bool)
	old := a.store.Settings
	ns := old
	if err := readJSON(r, &ns); err != nil {
		a.store.mu.Unlock()
		errJSON(w, 400, "body tidak valid")
		return
	}
	// field yang tidak boleh kosong/0 -> pakai nilai lama.
	// (Secret SENGAJA boleh kosong = tanpa password.)
	if ns.TcpPorts == "" {
		ns.TcpPorts = old.TcpPorts
	}
	if ns.WebPort == 0 {
		ns.WebPort = old.WebPort
	}
	if ns.RedirectPort == 0 {
		ns.RedirectPort = old.RedirectPort
	}
	if ns.TproxyPort == 0 {
		ns.TproxyPort = old.TproxyPort
	}
	if ns.MixedPort == 0 {
		ns.MixedPort = old.MixedPort
	}
	if ns.ClashPort == 0 {
		ns.ClashPort = old.ClashPort
	}
	if ns.DnsRemote == "" {
		ns.DnsRemote = old.DnsRemote
	}
	if ns.DnsDirect == "" {
		ns.DnsDirect = old.DnsDirect
	}
	if ns.Mode == "" {
		ns.Mode = old.Mode
	}
	if !validProxyMode(ns.ProxyMode) {
		ns.ProxyMode = old.ProxyMode
	}
	if ns.HealthURL == "" {
		ns.HealthURL = old.HealthURL
	}
	if ns.FtpPort == 0 {
		ns.FtpPort = old.FtpPort
	}
	if ns.FtpPort == 0 {
		ns.FtpPort = ftpDefaultPort
	}
	if ns.FtpRoot == "" {
		ns.FtpRoot = old.FtpRoot
	}
	if ns.FtpRoot == "" {
		ns.FtpRoot = ftpDefaultRoot
	}
	// Nama grup utama: tidak boleh kosong atau menabrak tag khusus.
	if mg := strings.TrimSpace(ns.MainGroupName); mg == "" {
		ns.MainGroupName = old.MainGroupName
	} else {
		switch strings.ToUpper(mg) {
		case "DIRECT", "REJECT", "DNS-OUT":
			a.store.mu.Unlock()
			errJSON(w, 400, "nama grup utama tidak boleh \""+mg+"\"")
			return
		}
		ns.MainGroupName = mg
	}
	// Level log mihomo: hanya nilai yang dikenal.
	switch ns.LogLevel {
	case "trace", "debug", "info", "warn", "error":
	default:
		ns.LogLevel = old.LogLevel
		if ns.LogLevel == "" {
			ns.LogLevel = "warn"
		}
	}
	// Password WebUI (akses dari jaringan). Kosong = pertahankan yang lama;
	// bila WebUI terbuka untuk jaringan, password wajib ada (dibuat otomatis).
	ns.WebPass = strings.TrimSpace(ns.WebPass)
	if ns.WebPass == "" {
		ns.WebPass = old.WebPass
	}
	if ns.WebListenAll && ns.WebPass == "" {
		ns.WebPass = defaultWebPass
	}
	// Folder FTP tidak dibatasi (boleh root /); kosong = default "/".
	ns.FtpRoot = strings.TrimSpace(ns.FtpRoot)
	if ns.FtpRoot == "" {
		ns.FtpRoot = "/"
	}
	// IP kunci hotspot: normalkan, tidak valid -> pakai nilai lama / default.
	if _, ok := hsParseCIDR(ns.HotspotIP); !ok {
		if _, ok2 := hsParseCIDR(old.HotspotIP); ok2 {
			ns.HotspotIP = old.HotspotIP
		} else {
			ns.HotspotIP = hsDefaultIP
		}
	}
	// Mode Enhance khusus mihomo (TUN + proteksi netfilter); di sing-box
	// mode itu tidak ada — petakan ke TUN murni agar generator & netfilter
	// tetap sepakat soal tun0.
	if ns.ActiveCore() == "singbox" && ns.ProxyMode == "enhance" {
		ns.ProxyMode = "tun"
	}
	a.store.Settings = ns
	_ = a.store.SaveSettings()
	a.store.mu.Unlock()

	// Settings berubah (mis. nama grup utama): segarkan override bila aktif.
	rbWarn := ""
	if rerr := a.rebaseActiveOverride(); rerr != nil {
		rbWarn = rerr.Error()
		a.logWarnf("override rebase gagal: %v", rerr)
	}

	if ns.Hotspot != old.Hotspot {
		if err := a.hotspotSet(ns.Hotspot); err != nil {
			errJSON(w, 500, "tersimpan, tapi hotspot bermasalah: "+err.Error())
			return
		}
	} else if ns.Hotspot && (ns.HotspotLockIP != old.HotspotLockIP || ns.HotspotIP != old.HotspotIP) {
		// Kunci IP berubah saat hotspot aktif -> terapkan ulang.
		if err := a.hotspotSet(true); err != nil {
			errJSON(w, 500, "tersimpan, tapi hotspot bermasalah: "+err.Error())
			return
		}
	}

	// FTP server: nyalakan/matikan, atau restart bila port/folder/tulis berubah.
	ftpChanged := ns.FtpEnabled != old.FtpEnabled ||
		(ns.FtpEnabled && (ns.FtpPort != old.FtpPort || ns.FtpRoot != old.FtpRoot || ns.FtpWrite != old.FtpWrite))
	if ftpChanged {
		if err := a.ftpSet(ns.FtpEnabled); err != nil {
			errJSON(w, 500, "tersimpan, tapi FTP bermasalah: "+err.Error())
			return
		}
	}

	// Ganti core saat sedang terhubung: validasi config core tujuan
	// SEBELUM core lama dimatikan. Config tujuan tidak valid -> settings
	// core dikembalikan dan core lama tetap berjalan seperti biasa.
	coreSwitched := false
	ns.Core = "singbox" // TaptapBox: kunci sing-box
	if ns.Core != old.Core && a.coreAlive() {
		var verr error
		if ns.ActiveCore() == "singbox" {
			var p string
			p, verr = a.writeConfigJSON()
			if verr == nil {
				verr = a.singboxTest(p)
			}
		} else {
			var p string
			p, verr = a.writeConfig()
			if verr == nil {
				_, verr = a.mihomoTest(p)
			}
		}
		if verr != nil {
			a.store.mu.Lock()
			a.store.Settings.Core = old.Core
			_ = a.store.SaveSettings()
			a.store.mu.Unlock()
			errJSON(w, 400, "ganti core dibatalkan: config "+ns.ActiveCore()+" tidak valid: "+verr.Error())
			return
		}
		a.stopCore()
		time.Sleep(time.Second)
		if err := a.startCore(); err != nil {
			// jaga-jaga: hidupkan kembali core lama
			a.store.mu.Lock()
			a.store.Settings.Core = old.Core
			_ = a.store.SaveSettings()
			a.store.mu.Unlock()
			_ = a.startCore()
			errJSON(w, 500, "ganti core gagal (kembali ke "+old.ActiveCore()+"): "+err.Error())
			return
		}
		if err := a.verifyRunning(); err != nil {
			errJSON(w, 500, "tersimpan, tapi "+err.Error())
			return
		}
		coreSwitched = true
	}

	// perubahan yang masuk config.json -> perlu restart (hanya bila sedang terhubung;
	// jika tidak, berlaku pada Connect berikutnya — jangan menyalakan VPN diam-diam)
	needRestart := ns.ClashPort != old.ClashPort || ns.RedirectPort != old.RedirectPort ||
		ns.TproxyPort != old.TproxyPort ||
		ns.MixedPort != old.MixedPort || ns.BypassDomains != old.BypassDomains ||
		ns.ProxyMode != old.ProxyMode || ns.TunMTU != old.TunMTU ||
		ns.ExcludePackages != old.ExcludePackages || ns.TransparentProxy != old.TransparentProxy ||
		ns.DnsRemote != old.DnsRemote || ns.DnsDirect != old.DnsDirect ||
		ns.Secret != old.Secret || ns.BlockAds != old.BlockAds ||
		ns.HealthURL != old.HealthURL ||
		ns.MainGroupName != old.MainGroupName || ns.LogLevel != old.LogLevel ||
		ns.WebListenAll != old.WebListenAll ||
		ns.BadvpnEnabled != old.BadvpnEnabled || ns.BadvpnUdpgwPort != old.BadvpnUdpgwPort
	if needRestart && !coreSwitched && a.coreAlive() {
		a.stopCore()
		time.Sleep(time.Second)
		if err := a.startCore(); err != nil {
			errJSON(w, 500, "tersimpan, tapi restart gagal: "+err.Error())
			return
		}
		if err := a.verifyRunning(); err != nil {
			errJSON(w, 500, "tersimpan, tapi "+err.Error())
			return
		}
	} else if !coreSwitched && ns.DNSHijack != old.DNSHijack || ns.DNSUpstream != old.DNSUpstream ||
		ns.TcpPorts != old.TcpPorts || ns.Hotspot != old.Hotspot ||
		ns.BlockIPv6 != old.BlockIPv6 || ns.BlockQUIC != old.BlockQUIC {
		a.reapplyNetfilter()
	}
	// Port/bind WebUI hanya berlaku saat backend (re)start — HTTP listener
	// tidak bisa pindah port tanpa restart proses backend.
	webChanged := ns.WebPort != old.WebPort || ns.WebListenAll != old.WebListenAll
	writeJSON(w, map[string]any{"ok": true, "restart_backend": webChanged, "override_warn": rbWarn})
}

func (a *App) handleBuilder(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		a.store.mu.RLock()
		defer a.store.mu.RUnlock()
		writeJSON(w, a.store.Builder)
		return
	}
	var b Builder
	if err := readJSON(r, &b); err != nil {
		errJSON(w, 400, "body tidak valid")
		return
	}
	a.store.mu.Lock()
	a.store.Builder = b
	err := a.store.SaveBuilder()
	a.store.mu.Unlock()
	if err != nil {
		// JANGAN diam-diam: WebUI harus tahu bila gagal tulis agar tidak
		// menampilkan "Tersimpan" palsu (aturan terlihat ada tapi hilang
		// setelah reboot karena tidak pernah tertulis ke disk).
		errJSON(w, 500, "gagal menyimpan: "+err.Error())
		return
	}
	// Builder berubah (grup/rute): segarkan override bila sedang aktif agar
	// edit langsung berlaku tanpa "kembali ke otomatis" dulu.
	rbWarn := ""
	if rerr := a.rebaseActiveOverride(); rerr != nil {
		rbWarn = rerr.Error()
		a.logWarnf("override rebase gagal: %v", rerr)
	}
	writeJSON(w, map[string]any{"ok": true, "override_warn": rbWarn})
}

// handleConfigCheck: validasi config JSON dengan `mihomo check` tanpa menerapkannya.
func (a *App) handleConfigCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML string `json:"yaml"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, 400, "body tidak valid: "+err.Error())
		return
	}
	if strings.TrimSpace(req.YAML) == "" {
		errJSON(w, 400, "config kosong")
		return
	}
	if a.store.Settings.ActiveCore() == "singbox" {
		tmp := filepath.Join(a.boxAutoDir(), "check.json")
		if err := os.WriteFile(tmp, []byte(req.YAML), 0600); err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		defer os.Remove(tmp)
		if err := a.singboxTest(tmp); err != nil {
			errJSON(w, 400, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	tmp := filepath.Join(a.autoDir(), "check.yaml")
	if err := os.WriteFile(tmp, []byte(req.YAML), 0600); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	defer os.Remove(tmp)
	out, err := a.mihomoTest(tmp)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		errJSON(w, 400, msg)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleBackup: GET = ekspor semua data (server, langganan, pengaturan, builder);
// POST = restore dari hasil ekspor.
func (a *App) handleBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		a.store.mu.RLock()
		defer a.store.mu.RUnlock()
		exp := a.store.Settings
		exp.WebPass = "" // password akses tidak ikut diekspor
		writeJSON(w, map[string]any{
			"format": "taptapbox-backup", "version": 1,
			"servers": a.store.Servers, "subscriptions": a.store.Subs,
			"settings": exp, "builder": a.store.Builder,
		})
		return
	}
	var req struct {
		Servers  []Server       `json:"servers"`
		Subs     []Subscription `json:"subscriptions"`
		Settings *Settings      `json:"settings"`
		Builder  *Builder       `json:"builder"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, 400, "backup tidak valid: "+err.Error())
		return
	}
	if req.Servers == nil && req.Subs == nil && req.Settings == nil && req.Builder == nil {
		errJSON(w, 400, "backup kosong")
		return
	}
	a.store.mu.Lock()
	old := a.store.Settings
	if req.Servers != nil {
		a.store.Servers = req.Servers
	}
	if req.Subs != nil {
		a.store.Subs = req.Subs
	}
	if req.Settings != nil {
		ns := *req.Settings
		ns.Ver = old.Ver
		ns.WebPort = old.WebPort
		ns.WebPass = old.WebPass // password akses tidak ditimpa restore
		if ns.WebListenAll && ns.WebPass == "" {
			ns.WebPass = defaultWebPass
		}
		if ns.LogLevel == "" {
			ns.LogLevel = old.LogLevel
		}
		ns.FtpRoot = orDefault(ns.FtpRoot, old.FtpRoot)
		if !ftpRootAllowed(ns.FtpRoot) {
			ns.FtpRoot = ftpDefaultRoot
		}
		if ns.MixedPort == 0 {
			ns.MixedPort = old.MixedPort
		}
		if ns.ClashPort == 0 {
			ns.ClashPort = old.ClashPort
		}
		if ns.RedirectPort == 0 {
			ns.RedirectPort = old.RedirectPort
		}
		if ns.TproxyPort == 0 {
			ns.TproxyPort = old.TproxyPort
		}
		if ns.TcpPorts == "" {
			ns.TcpPorts = old.TcpPorts
		}
		if ns.DnsRemote == "" {
			ns.DnsRemote = old.DnsRemote
		}
		if ns.DnsDirect == "" {
			ns.DnsDirect = old.DnsDirect
		}
		if !validProxyMode(ns.ProxyMode) {
			ns.ProxyMode = old.ProxyMode
		}
		if ns.Mode == "" {
			ns.Mode = old.Mode
		}
		if ns.HealthURL == "" {
			ns.HealthURL = old.HealthURL
		}
		// Mode Enhance khusus mihomo (TUN + proteksi netfilter); di sing-box
		// mode itu tidak ada — petakan ke TUN murni agar generator & netfilter
		// tetap sepakat soal tun0.
		if ns.ActiveCore() == "singbox" && ns.ProxyMode == "enhance" {
			ns.ProxyMode = "tun"
		}
		a.store.Settings = ns
	}
	if req.Builder != nil {
		a.store.Builder = *req.Builder
	}
	_ = a.store.SaveServers()
	_ = a.store.SaveSubs()
	_ = a.store.SaveSettings()
	_ = a.store.SaveBuilder()
	a.store.mu.Unlock()
	// Restore menimpa builder: segarkan override bila sedang aktif.
	rbWarn := ""
	if rerr := a.rebaseActiveOverride(); rerr != nil {
		rbWarn = rerr.Error()
		a.logWarnf("override rebase gagal: %v", rerr)
	}
	writeJSON(w, map[string]any{"ok": true, "override_warn": rbWarn})
}

func (a *App) handleBuilderPreview(w http.ResponseWriter, r *http.Request) {
	if a.store.Settings.ActiveCore() == "singbox" {
		cfg, err := a.buildConfig()
		if err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"yaml": string(data)})
		return
	}
	yml, err := a.buildConfigYAML()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]string{"yaml": yml})
}

// handleGroupTags: daftar grup custom beserta tag mihomo-nya
// (hanya grup yang punya anggota, sesuai config yang di-generate).
// Dipakai WebUI agar grup bisa dipilih langsung dari halaman utama.
func (a *App) handleGroupTags(w http.ResponseWriter, r *http.Request) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	built := a.buildServers()
	var done map[string]string
	if a.store.Settings.ActiveCore() == "singbox" {
		_, done, _ = a.boxBuildGroups(built)
	} else {
		_, done, _, _ = a.buildGroups(built)
	}
	list := []map[string]string{}
	for _, g := range a.store.Builder.Groups {
		if tag, ok := done[g.ID]; ok {
			list = append(list, map[string]string{
				"id": g.ID, "name": g.Name, "tag": tag, "type": g.Type,
			})
		}
	}
	writeJSON(w, list)
}

// handleConfigRaw: GET = config efektif saat ini.
// Mode otomatis: generate fresh dari data (selalu sinkron) -> user otomatis/config.json.
// Mode manual: baca user manual/config.json apa adanya (penuh manual).
func (a *App) handleConfigRaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	if a.store.Settings.ActiveCore() == "singbox" {
		if !a.store.Settings.ManualConfig {
			if a.boxHasOverride() {
				data, err := os.ReadFile(a.boxOverridePath())
				if err != nil {
					errJSON(w, 500, err.Error())
					return
				}
				writeJSON(w, map[string]any{"yaml": string(data), "override": true})
				return
			}
			cfg, err := a.buildConfig()
			if err != nil {
				errJSON(w, 500, err.Error())
				return
			}
			data, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				errJSON(w, 500, err.Error())
				return
			}
			writeJSON(w, map[string]any{"yaml": string(data), "override": false})
			return
		}
		data, err := os.ReadFile(a.boxActiveConfigPath())
		if err != nil {
			errJSON(w, 404, "config manual belum ada")
			return
		}
		writeJSON(w, map[string]string{"yaml": string(data)})
		return
	}
	if !a.store.Settings.ManualConfig {
		// mode otomatis: bila ada override (hasil edit user), tampilkan itu
		// agar bisa diedit lagi; kalau tidak, tampilkan hasil generate.
		if a.hasOverride() {
			data, err := os.ReadFile(a.overridePath())
			if err != nil {
				errJSON(w, 500, err.Error())
				return
			}
			writeJSON(w, map[string]any{"yaml": string(data), "override": true})
			return
		}
		yml, err := a.buildConfigYAML()
		if err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"yaml": yml, "override": false})
		return
	}
	data, err := os.ReadFile(a.activeConfigPath())
	if err != nil {
		errJSON(w, 404, "config manual belum ada")
		return
	}
	writeJSON(w, map[string]string{"yaml": string(data)})
}

// handleConfigOutbound: GET /api/config/outbound?tag=xxx — baca satu proxy
// dari config.yaml aktif sebagai params ala v2rayNG (untuk form edit).
func (a *App) handleConfigOutbound(w http.ResponseWriter, r *http.Request) {
	tag := r.URL.Query().Get("tag")
	if tag == "" {
		errJSON(w, 400, "tag kosong")
		return
	}
	var cfg map[string]any
	if !a.store.Settings.ManualConfig {
		var err error
		cfg, err = a.buildMihomoMap()
		if err != nil {
			errJSON(w, 500, err.Error())
			return
		}
	} else {
		data, err := os.ReadFile(a.activeConfigPath())
		if err != nil {
			errJSON(w, 404, "config manual belum ada")
			return
		}
		var err2 error
		cfg, err2 = yamlToMap(string(data))
		if err2 != nil {
			errJSON(w, 500, "config manual rusak: "+err2.Error())
			return
		}
	}
	proxies, _ := cfg["proxies"].([]any)
	for _, o := range proxies {
		px, ok := o.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := px["name"].(string); t == tag {
			proto, p := mihomoProxyToParams(px)
			writeJSON(w, map[string]any{"tag": tag, "proto": proto, "params": p})
			return
		}
	}
	errJSON(w, 404, "proxy tidak ditemukan: "+tag)
}

// handleConfigManual: PUT {"enabled":bool,"config":{...}} — terapkan config mentah.
func (a *App) handleConfigManual(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	var req struct {
		Enabled bool   `json:"enabled"`
		YAML    string `json:"yaml"`
		JSON    string `json:"json"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, 400, "body tidak valid")
		return
	}
	a.store.mu.Lock()
	if req.Enabled {
		if strings.TrimSpace(req.YAML) == "" && strings.TrimSpace(req.JSON) == "" {
			a.store.mu.Unlock()
			errJSON(w, 400, "config kosong")
			return
		}
		var err error
		if a.store.Settings.ActiveCore() == "singbox" {
			txt := req.JSON
			if txt == "" {
				txt = req.YAML
			}
			err = a.applyRawConfigJSON(json.RawMessage(txt))
		} else {
			err = a.applyRawConfig(req.YAML)
		}
		if err != nil {
			a.store.mu.Unlock()
			errJSON(w, 400, err.Error())
			return
		}
		a.store.Settings.ManualConfig = true
	} else {
		a.store.Settings.ManualConfig = false
		// kembali ke otomatis murni: hapus juga override bila ada
		if a.store.Settings.ActiveCore() == "singbox" {
			_ = os.Remove(a.boxOverridePath())
		} else {
			_ = os.Remove(a.overridePath())
		}
	}
	_ = a.store.SaveSettings()
	manual := a.store.Settings.ManualConfig
	a.store.mu.Unlock()
	// restart agar perubahan berlaku — hanya bila sedang terhubung
	if a.coreAlive() {
		a.stopCore()
		time.Sleep(time.Second)
		if err := a.startCore(); err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		if err := a.verifyRunning(); err != nil {
			errJSON(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "manual": manual})
}

// handleConfigOverride: PUT {"yaml":"..."} — simpan config override untuk mode
// otomatis (tetap otomatis, tapi isi config dari hasil edit user). yaml kosong
// = hapus override (kembali ke config bawaan hasil generate). Selalu divalidasi
// dengan mihomo -t sebelum disimpan.
func (a *App) handleConfigOverride(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	var req struct {
		YAML string `json:"yaml"`
		JSON string `json:"json"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, 400, "body tidak valid")
		return
	}
	a.store.mu.Lock()
	if a.store.Settings.ManualConfig {
		a.store.mu.Unlock()
		errJSON(w, 400, "mode manual aktif — pakai \"Terapkan sebagai manual\"")
		return
	}
	singbox := a.store.Settings.ActiveCore() == "singbox"
	var err error
	if singbox {
		txt := req.JSON
		if txt == "" {
			txt = req.YAML
		}
		err = a.applyOverrideConfigJSON(json.RawMessage(txt))
	} else {
		err = a.applyOverrideConfig(req.YAML)
	}
	if err != nil {
		a.store.mu.Unlock()
		errJSON(w, 400, err.Error())
		return
	}
	over := a.hasOverride()
	if singbox {
		over = a.boxHasOverride()
	}
	a.store.mu.Unlock()
	// restart agar perubahan berlaku — hanya bila sedang terhubung
	if a.coreAlive() {
		a.stopCore()
		time.Sleep(time.Second)
		if err := a.startCore(); err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		if err := a.verifyRunning(); err != nil {
			errJSON(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "override": over})
}

func (a *App) handleApply(w http.ResponseWriter, r *http.Request) {
	// Unduh dulu rule-set remote yg belum ada di cache (bisa lama bila
	// jaringan ke host-nya bermasalah); yg gagal di-skip dgn peringatan
	// agar mihomo tetap bisa start tanpa crash-loop.
	warns := a.prefetchRuleSets()
	a.stopCore()
	time.Sleep(time.Second)
	if err := a.startCore(); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if err := a.verifyRunning(); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "warnings": warns})
}

func (a *App) handleSelect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Group string `json:"group"`
		Name  string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, 400, "body tidak valid")
		return
	}
	if err := a.SelectProxy(req.Group, req.Name); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	typ := r.URL.Query().Get("type")
	if typ == "" {
		typ = "mihomo"
	}
	lines := 200
	if n := r.URL.Query().Get("lines"); n != "" {
		fmt.Sscanf(n, "%d", &lines)
	}
	name := "clash.log"
	if typ == "backend" {
		name = "backend.log"
	}
	if typ == "singbox" {
		name = "sing-box.log"
	}
	if typ == "ssh" {
		name = "ssh.log"
	}
	data, _ := os.ReadFile(filepath.Join(a.logDir, name))
	ls := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(ls) > lines {
		ls = ls[len(ls)-lines:]
	}
	// "logs" (teks) dipakai WebUI; "lines" dipertahankan untuk kompatibilitas
	writeJSON(w, map[string]any{"lines": ls, "logs": strings.Join(ls, "\n")})
}

// reverse proxy ke clash API (agar WebUI tak kena CORS & butuh secret)
func (a *App) handleClashProxy(w http.ResponseWriter, r *http.Request) {
	target, _ := url.Parse(a.clashBase())
	proxy := httputil.NewSingleHostReverseProxy(target)
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/clash")
	r.Host = target.Host
	r.Header.Del("Authorization") // kredensial WebUI bukan untuk clash API
	if sec := a.cfg().Secret; sec != "" {
		r.Header.Set("Authorization", "Bearer "+sec)
	}
	proxy.ServeHTTP(w, r)
}

// ---------- auto test loop ----------

func (a *App) autoTestLoop() {
	t := time.NewTicker(3 * time.Minute)
	defer t.Stop()
	for range t.C {
		if !a.cfg().AutoTest || !a.coreAlive() {
			continue
		}
		// hemat baterai: hasil tes hanya dilihat lewat WebUI -> lewati bila
		// WebUI tidak dibuka dalam 10 menit terakhir.
		if time.Now().Unix()-atomic.LoadInt64(&lastHit) > 600 {
			continue
		}
		// bangun peta tag SEKALI (dulu tagOfServer per server = O(n^2))
		a.store.mu.RLock()
		tagByID := map[string]string{}
		for _, b := range a.buildServers() {
			tagByID[b.ID] = b.Tag
		}
		targets := []string{}
		for _, s := range a.store.Servers {
			if s.Enabled {
				targets = append(targets, s.ID)
			}
		}
		a.store.mu.RUnlock()
		var wg sync.WaitGroup
		sem := make(chan struct{}, 3)
		for _, id := range targets {
			tag := tagByID[id]
			if tag == "" {
				continue
			}
			wg.Add(1)
			go func(id, tag string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				d := a.DelayTest(tag, "")
				a.store.mu.Lock()
				if sv := a.store.findServer(id); sv != nil {
					sv.DelayMs = d
					sv.TestedAt = time.Now().Unix()
				}
				a.store.mu.Unlock()
			}(id, tag)
		}
		wg.Wait()
		a.store.mu.Lock()
		_ = a.store.SaveServers()
		a.store.mu.Unlock()
	}
}

// ---------- main ----------

func main() {
	// Subcommand helper fetch (v1.0.33): HARUS paling awal, sebelum flag
	// daemon diparse — anak proses ini hanya mengambil satu URL lalu keluar.
	if len(os.Args) >= 2 && os.Args[1] == "fetch" {
		os.Exit(runFetchHelper(os.Args[2:]))
	}
	datadir := flag.String("datadir", "/data/adb/taptapbox", "direktori data")
	webui := flag.String("webui", "", "direktori webui (default <datadir>/webui)")
	flag.Parse()

	a := &App{dataDir: *datadir}
	a.jsonDir = filepath.Join(*datadir, "config")
	a.logDir = filepath.Join(*datadir, "log")
	a.binDir = filepath.Join(*datadir, "bin")
	a.webuiDir = *webui
	if a.webuiDir == "" {
		a.webuiDir = filepath.Join(*datadir, "webui")
	}
	a.mihomoBin = filepath.Join(a.binDir, "mihomo")
	a.singboxBin = filepath.Join(a.binDir, "sing-box")
	for _, d := range []string{a.jsonDir, a.logDir, a.binDir, a.webuiDir} {
		_ = os.MkdirAll(d, 0755)
	}
	// Folder sesi sendiri (rapi): siapkan + pindahkan websess.key lama dari
	// akar data (sekali jalan; sesi lama tetap valid karena kunci dipindah).
	_ = os.MkdirAll(filepath.Join(*datadir, webSessionDir), 0700)
	_ = a.sessionFile()
	// Log backend dibatasi 200 baris (capLogWriter, segar tiap start;
	// setelah 200 baris tulisan dibuang, backend tetap jalan).
	lf := newCapLogWriter(filepath.Join(a.logDir, "backend.log"))
	if lf == nil {
		fmt.Println("gagal buka backend.log")
		os.Exit(1)
	}
	// hanya ke file (service.sh sudah mengarahkan stdout ke file yang sama -> hindari baris ganda)
	// tanpa flag/prefix: timestamp & level ditulis oleh loglv (logx.go) ala xclash
	a.logger = log.New(lf, "", 0)
	a.migrateLegacyModules()      // impor data modul lama (sekali saja, bila config/ kosong)
	a.migrateTaptapSlim()         // impor data TaptapSlim -> TaptapBox (sekali saja, bila config/ kosong)
	a.migrateStoreFiles()         // kumpulkan file data warisan ke akar config/ (sekali saja)
	a.migrateRulesetDir()         // sebar cache rule-set ke folder per-core (sekali saja)
	a.store = NewStore(a.jsonDir) // data (servers/settings/builder/subs) tinggal di config/
	a.ensureConfigDirs()          // siapkan folder user otomatis/ & user manual/ per core
	a.detectCoreVersions()        // baca versi kedua core dari binernya (untuk UI)
	// WebUI terbuka untuk jaringan -> wajib ada password (dibuat acak sekali, bisa diubah di Pengaturan)
	a.store.mu.Lock()
	if a.store.Settings.WebListenAll && a.store.Settings.WebPass == "" {
		a.store.Settings.WebPass = defaultWebPass
		_ = a.store.SaveSettings()
		a.logf("password WebUI untuk akses jaringan dibuat otomatis (lihat Pengaturan WebUI)")
	}
	a.store.mu.Unlock()
	a.logf("start datadir=%s webui=%s", a.dataDir, a.webuiDir)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/updates", a.handleUpdates)
	mux.HandleFunc("/api/release/latest", a.handleSelfRelease)
	mux.HandleFunc("/api/updates/versions", a.handleUpdateVersions)
	mux.HandleFunc("/api/updates/download", a.handleUpdateDownload)
	mux.HandleFunc("/api/hotspot", a.handleHotspotStatus)
	mux.HandleFunc("/api/login", a.handleLogin)
	mux.HandleFunc("/api/ftp", a.handleFtp)
	mux.HandleFunc("/api/packages", a.handlePackages)
	mux.HandleFunc("/api/animasi", a.handleAnimasi)
	mux.HandleFunc("/api/animasi/file", a.handleAnimasiFile)
	mux.HandleFunc("/api/connect", a.handleConnect)
	mux.HandleFunc("/api/disconnect", a.handleDisconnect)
	mux.HandleFunc("/api/restart", a.handleRestart)
	mux.HandleFunc("/api/apply", a.handleApply)
	mux.HandleFunc("/api/servers", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			a.handleServers(w, r)
		case "POST":
			a.handleCreateServer(w, r)
		case "DELETE":
			a.handleDeleteServer(w, r)
		case "PUT":
			a.handleUpdateServer(w, r)
		default:
			errJSON(w, 405, "method tidak didukung")
		}
	})
	mux.HandleFunc("/api/servers/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			a.handleUpdateServer(w, r)
		case "DELETE":
			a.handleDeleteServer(w, r)
		default:
			errJSON(w, 405, "method tidak didukung")
		}
	})
	mux.HandleFunc("/api/servers/import", a.handleImportServers)
	mux.HandleFunc("/api/servers/export", a.handleExportServers)
	mux.HandleFunc("/api/servers/dedup", a.handleServerDedup)
	mux.HandleFunc("/api/servers/clear-unavailable", a.handleServerClearUnavailable)
	mux.HandleFunc("/api/servers/order", a.handleServerOrder)
	mux.HandleFunc("/api/servers/pick", a.handleServerPick)
	mux.HandleFunc("/api/servers/test", a.handleTestServers)
	mux.HandleFunc("/api/subscriptions", a.handleSubs)
	mux.HandleFunc("/api/subscriptions/", a.handleSubItem)
	mux.HandleFunc("/api/subscriptions/update", a.handleSubUpdate)
	mux.HandleFunc("/api/settings", a.handleSettings)
	mux.HandleFunc("/api/builder", a.handleBuilder)
	mux.HandleFunc("/api/ruleset-files", a.handleRulesetFiles)
	mux.HandleFunc("/api/builder/preview", a.handleBuilderPreview)
	mux.HandleFunc("/api/groups/tags", a.handleGroupTags)
	mux.HandleFunc("/api/config/raw", a.handleConfigRaw)
	mux.HandleFunc("/api/config/manual", a.handleConfigManual)
	mux.HandleFunc("/api/config/override", a.handleConfigOverride)
	mux.HandleFunc("/api/config/outbound", a.handleConfigOutbound)
	mux.HandleFunc("/api/config/check", a.handleConfigCheck)
	mux.HandleFunc("/api/backup", a.handleBackup)
	mux.HandleFunc("/api/select", a.handleSelect)
	mux.HandleFunc("/api/logs", a.handleLogs)
	mux.HandleFunc("/api/ssh-log", a.handleSSHLog)
	mux.HandleFunc("/api/scrcpy/status", a.handleScrcpyStatus)
	mux.HandleFunc("/api/scrcpy/config", a.handleScrcpyConfig)
	mux.HandleFunc("/api/scrcpy/start", a.handleScrcpyStart)
	mux.HandleFunc("/api/scrcpy/stop", a.handleScrcpyStop)
	mux.HandleFunc("/api/scrcpy/paste", a.handleScrcpyPaste)
	mux.HandleFunc("/api/scrcpy/ws", a.handleScrcpyWS)
	mux.HandleFunc("/api/clash/", a.handleClashProxy)

	// WebUI utama di /taptapbox/ (scope PWA sendiri, tak seri dengan modul lain
	// di 127.0.0.1) + fallback SPA. API tetap di /api/*.
	mux.HandleFunc("/taptapbox/", func(w http.ResponseWriter, r *http.Request) {
		// WebUI TIDAK BOLEH di-cache browser (2026-10-27): penyebab
		// fitur yang dihapus masih "nyangkut" adalah Chrome memakai
		// salinan lama app.js/style.css/index.html/sw.js — backend
		// dulu tidak mengirim Cache-Control, sw.js dan app.js?v=
		// tidak pernah berubah di semua build. no-store di sini +
		// token per-build di build.sh (marker versi SW & ?v= app.js)
		// + SW network-only (tanpa cache sama sekali) membuat WebUI
		// selalu segar tiap flash/reboot tanpa hapus data situs
		// manual lagi, dan tidak ada lagi yang tertinggal sesudah
		// uninstall. Hanya WebUI TaptapBox yang no-store;
		// zashboard di "/" tidak disentuh.
		w.Header().Set("Cache-Control", "no-store")
		rel := strings.TrimPrefix(r.URL.Path, "/taptapbox")
		if rel == "" || rel == "/" {
			http.ServeFile(w, r, filepath.Join(a.webuiDir, "index.html"))
			return
		}
		fp := filepath.Join(a.webuiDir, filepath.Clean("/"+rel))
		if st, err := os.Stat(fp); err == nil && !st.IsDir() {
			http.ServeFile(w, r, fp)
			return
		}
		http.ServeFile(w, r, filepath.Join(a.webuiDir, "index.html"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		// zashboard: SPA sendiri, fallback ke index.html miliknya.
		// zashboardDir() dihitung per request agar unduhan langsung
		// berlaku tanpa restart daemon.
		if strings.HasPrefix(r.URL.Path, "/zashboard/") || r.URL.Path == "/zashboard" {
			rel := strings.TrimPrefix(r.URL.Path, "/zashboard")
			fp := filepath.Join(a.zashboardDir(), filepath.Clean("/"+rel))
			if st, err := os.Stat(fp); err == nil && !st.IsDir() {
				http.ServeFile(w, r, fp)
				return
			}
			http.ServeFile(w, r, filepath.Join(a.zashboardDir(), "index.html"))
			return
		}
		// path lama -> redirect ke /taptapbox/
		http.Redirect(w, r, "/taptapbox/", http.StatusFound)
	})

	go a.autoTestLoop()
	// DNS stub selalu aktif (hanya dipakai rule iptables); jika mode diganti
	// dari TUN ke iptables saat berjalan, listener sudah siap.
	a.startDNSStub()
	if a.store.Settings.Hotspot {
		go func() {
			if err := a.hotspotSet(true); err != nil {
				a.logErrf("hotspot: %v", err)
			}
		}()
	}
	if a.store.Settings.FtpEnabled {
		go func() {
			if err := a.ftpSet(true); err != nil {
				a.logErrf("ftp: %v", err)
			}
		}()
	}

	if a.store.Settings.AutoStart {
		go func() {
			time.Sleep(3 * time.Second)
			if err := a.startCore(); err != nil {
				a.logErrf("autostart gagal: %v", err)
			}
		}()
	}

	// Salinan helper untuk updater (v1.0.35): disiapkan di awal agar
	// percobaan helper pertama tidak menunggu salinan; gagal bukan fatal
	// (spawn nanti mencoba lagi secara malas, lalu rantai jatuh ke direct).
	if err := ensureHelperBinary(true); err != nil {
		a.logWarnf("helper: salinan tidak bisa disiapkan: %v", err)
	}

	host := "127.0.0.1"
	if a.store.Settings.WebListenAll {
		host = "0.0.0.0" // bisa dibuka dari HP lain di LAN (WiFi/hotspot yang sama)
	}
	addr := host + ":" + itoa(a.store.Settings.WebPort)
	a.logf("WebUI: http://%s", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           a.guard(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	if err := srv.ListenAndServe(); err != nil {
		a.logErrf("http gagal: %v", err)
	}
}

// ---------- start/stop core sing-box (port dari TaptapBox) ----------

func (a *App) startSingbox() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running && a.boxInst != nil {
		return fmt.Errorf("sing-box sudah berjalan")
	}
	cfgPath, err := a.writeConfigJSON()
	if err != nil {
		return fmt.Errorf("gagal generate config: %v", err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	opts, ctx, err := parseSingboxConfig(data)
	if err != nil {
		return fmt.Errorf("config tidak valid: %v", err)
	}
	// bersihkan tun0 / ip rule basi (mis. crash sebelumnya) agar TUN bisa dibuat ulang
	if a.store.Settings.ProxyMode == "tun" {
		a.cleanStaleTun()
	}
	// Log sing-box dibatasi 200 baris (capLogWriter, segar tiap start),
	// lewat PlatformWriter library (View Log -> sing-box.log tetap jalan).
	logPath := filepath.Join(a.logDir, "sing-box.log")
	lf := newCapLogWriter(logPath)
	if lf == nil {
		return fmt.Errorf("gagal buka sing-box.log")
	}
	ctx, cancel := context.WithCancel(ctx)
	inst, err := box.New(box.Options{Context: ctx, Options: opts, PlatformLogWriter: &singboxLogWriter{w: lf}})
	if err != nil {
		cancel()
		lf.Close()
		return fmt.Errorf("config tidak valid: %v", err)
	}
	if err := inst.Start(); err != nil {
		_ = inst.Close()
		cancel()
		lf.Close()
		return err
	}
	done := make(chan struct{})
	a.boxInst = inst
	a.boxCancel = cancel
	a.boxDone = done
	a.running = true
	a.runningCore = "singbox"
	a.startedAt = time.Now()
	go func() {
		// Close() dipanggil stopSingbox; goroutine ini hanya menunggu
		// done ditutup stopSingbox, lalu menutup log.
		<-done
		lf.Close()
	}()
	a.logf("sing-box library started (in-process, v%s)", singboxLibraryVersion)
	if a.store.Settings.WebListenAll && a.store.Settings.Secret == "" {
		a.logf("PERINGATAN: Clash API terbuka di 0.0.0.0:%d tanpa secret — siapa pun di WiFi/hotspot bisa mengendalikannya. Isi 'Secret Clash API' di Pengaturan bila perlu.",
			a.store.Settings.ClashPort)
	}
	if a.store.Settings.TransparentProxy {
		// beri jeda singkat agar inbound siap
		go func() {
			time.Sleep(2 * time.Second)
			if err := a.applyTransparentProxy(); err != nil {
				a.logf("iptables gagal: %v", err)
			} else {
				a.logf("transparent proxy aktif (mode %s)", a.store.Settings.ProxyMode)
			}
		}()
	}
	go a.syncSelected()
	return nil
}

func (a *App) stopSingbox() {
	a.mu.Lock()
	inst := a.boxInst
	cancel := a.boxCancel
	done := a.boxDone
	a.boxInst = nil
	a.boxCancel = nil
	a.boxDone = nil
	a.cmd = nil
	a.running = false
	a.mu.Unlock()
	if inst != nil {
		_ = inst.Close()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			close(done)
		}
	} else if done != nil {
		close(done)
	}
	a.clearTransparentProxy()
	a.logf("sing-box stopped")
}

// ---------- CoreManager: dispatch menurut core ----------

// startCore menyalakan core sesuai Settings.ActiveCore().
func (a *App) startCore() error {
	// Modul slim: biner core mungkin belum diunduh — gagalkan dengan
	// pesan yang mengarahkan ke kotak Download, bukan error exec mentah.
	active := a.store.Settings.ActiveCore()
	if !a.coreInstalled(active) {
		name := "mihomo"
		if active == "singbox" {
			name = "sing-box"
		}
		return fmt.Errorf("core %s belum terpasang — unduh dulu lewat Tentang → Download", name)
	}
	var err error
	// Jembatan SSH harus sudah jalan SEBELUM core dial ke 127.0.0.1:15411+
	// (config server SSH payload menunjuk ke sana).
	a.syncSSHbridge()
	// Relay BadVPN harus sudah jalan SEBELUM config di-generate, karena
	// config memakai port SOCKS/UDP-nya (bind acak, bukan port tetap).
	a.startBadvpn()
	if active == "singbox" {
		err = a.startSingbox()
	} else {
		err = a.startMihomo()
	}
	if err != nil && !a.coreAlive() {
		// Core gagal nyala: jangan sisakan relay yatim. Bila core masih
		// hidup (start ganda), relay dibiarkan — core itu memakainya.
		a.stopBadvpn()
	}
	if err == nil {
		// Core baru jalan (mungkin binernya baru diganti): segarkan
		// versi terdeteksi agar Tentang/Pengaturan langsung benar.
		a.detectCoreVersions()
	}
	return err
}

// stopCore menghentikan core yang SEDANG berjalan (runningCore), bukan
// mengikuti setting yang mungkin baru saja diganti user.
func (a *App) stopCore() {
	a.stopSSHbridge()
	switch a.runningCore {
	case "singbox":
		a.stopSingbox()
	case "clash":
		a.stopMihomo()
	default:
		if a.store.Settings.ActiveCore() == "singbox" {
			a.stopSingbox()
		} else {
			a.stopMihomo()
		}
	}
	// Relay dimatikan sesudah core (stream udpgw-nya lewat core).
	a.stopBadvpn()
}

// mihomoAlive: nama lama dari coreAlive (dipertahankan untuk kompat).
func (a *App) mihomoAlive() bool { return a.coreAlive() }
