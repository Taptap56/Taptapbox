package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// logOnce: catat satu kali per kunci (buildServers dipanggil sering; jangan banjiri log).
var logOnceSeen sync.Map

func (a *App) logOnce(key, format string, v ...any) {
	if _, loaded := logOnceSeen.LoadOrStore(key, true); loaded {
		return
	}
	a.logf(format, v...)
}

// cfg: salinan Settings yang aman dibaca dari goroutine mana pun (RLock sesaat).
// JANGAN dipanggil saat pemanggil sudah memegang store.mu.
func (a *App) cfg() Settings {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	return a.store.Settings
}

// mainGroupTag: tag grup utama. Namanya bisa diubah user (Pengaturan/Grup);
// default "Taptap". Selalu non-kosong dan tidak menabrak tag khusus.
func (a *App) mainGroupTag() string {
	if t := strings.TrimSpace(a.store.Settings.MainGroupName); t != "" {
		return t
	}
	return "Taptap"
}

// reservedTags: tag yang tidak boleh dipakai oleh server / grup custom.
func (a *App) reservedTags() map[string]bool {
	return map[string]bool{
		"DIRECT": true, "REJECT": true, "dns-out": true,
		a.mainGroupTag(): true,
	}
}

// healthURL: URL tes koneksi (Pengaturan) atau default.
func (a *App) healthURL() string {
	if u := strings.TrimSpace(a.store.Settings.HealthURL); strings.HasPrefix(u, "http") {
		return u
	}
	return healthDefault
}

// builtServer = server aktif yang valid beserta outbound/endpoint-nya.
// Ob = outbound/proxy menurut dialek core aktif; Ep khusus sing-box
// (endpoint, mis. WireGuard) — di mode mihomo selalu nil.
type builtServer struct {
	ID  string
	Tag string
	Ob  map[string]any
	Ep  map[string]any
}

// buildServers menghitung tag + outbound untuk server aktif yang valid.
// Server tidak valid dilewati agar satu akun rusak tidak menggagalkan
// seluruh config. Pemanggil harus memegang store.mu (read).
func (a *App) buildServers() []builtServer {
	used := a.reservedTags()
	for _, t := range a.groupTags(a.store.Builder.Groups) {
		used[t] = true
	}
	// Port jembatan per server SSH payload (dihitung sekali, dipakai
	// semua outbound di bawah + listener di syncSSHbridge).
	bridgePorts, _ := a.sshBridgeAssignmentsLocked()
	applyBridgePort := func(ob map[string]any, sv *Server) {
		port, ok := bridgePorts[sv.ID]
		if !ok || ob == nil {
			return
		}
		ob["server"] = "127.0.0.1"
		if _, ok := ob["server_port"]; ok {
			ob["server_port"] = port
		}
		if _, ok := ob["port"]; ok {
			ob["port"] = port
		}
	}
	out := []builtServer{}
	for i := range a.store.Servers {
		sv := &a.store.Servers[i]
		if !sv.Enabled {
			continue
		}
		if !validServer(sv) {
			a.logOnce(sv.ID+"|invalid", "server %q dilewati: parameter tidak lengkap", sv.Name)
			continue
		}
		tag := uniqueTag(sv.Name, used)
		if a.store.Settings.ActiveCore() == "singbox" {
			ob, ep := serverToOutbound(sv, tag)
			if ob == nil && ep == nil {
				a.logOnce(sv.ID+"|proto", "server %q dilewati: protokol %q tidak didukung", sv.Name, sv.Proto)
				continue
			}
			applyBridgePort(ob, sv)
			used[tag] = true
			out = append(out, builtServer{ID: sv.ID, Tag: tag, Ob: ob, Ep: ep})
			continue
		}
		if mihomoUnsupported(sv.Proto) {
			a.logOnce(sv.ID+"|proto", "server %q dilewati: protokol %q tidak didukung core mihomo", sv.Name, sv.Proto)
			continue
		}
		ob := serverToMihomoProxy(sv, tag)
		if ob == nil {
			a.logOnce(sv.ID+"|proto", "server %q dilewati: protokol %q tidak didukung", sv.Name, sv.Proto)
			continue
		}
		applyBridgePort(ob, sv)
		used[tag] = true
		out = append(out, builtServer{ID: sv.ID, Tag: tag, Ob: ob})
	}
	return out
}

func setDNSAddr(o map[string]any, hostport string, defPort int) {
	host := hostport
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		host = h
		if n, e := strconv.Atoi(p); e == nil && n > 0 && n != defPort {
			o["server_port"] = n
		}
	}
	o["server"] = strings.Trim(host, "[]")
}

// packageUIDs: nama paket Android -> UID (dari /data/system/packages.list).
func packageUIDs(pkgs []string) []int {
	f, err := os.Open("/data/system/packages.list")
	if err != nil {
		return nil
	}
	defer f.Close()
	want := map[string]bool{}
	for _, p := range pkgs {
		want[strings.ToLower(p)] = true
	}
	var out []int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 2 || !want[strings.ToLower(fs[0])] {
			continue
		}
		if n, err := strconv.Atoi(fs[1]); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func ipRun(args ...string) error { return exec.Command("ip", args...).Run() }

// cleanStaleTun membuang sisa tun0 / ip rule mihomo (table 2022, pref
// 9000-9010) yang tertinggal bila proses sebelumnya mati paksa.
// validProxyMode: daftar mode proxy yang sah (lihat WebUI Pengaturan).
func validProxyMode(m string) bool {
	switch m {
	case "tun", "tproxy", "iptables", "mixed", "enhance":
		return true
	}
	return false
}

func (a *App) cleanStaleTun() {
	_ = ipRun("link", "del", "tun0")
	for _, fam := range []string{"-4", "-6"} {
		for i := 0; i < 8; i++ {
			if ipRun(fam, "rule", "del", "table", "2022") != nil {
				break
			}
		}
		_ = ipRun(fam, "route", "flush", "table", "2022")
		for pref := 9000; pref <= 9010; pref++ {
			for i := 0; i < 3; i++ {
				if ipRun(fam, "rule", "del", "pref", strconv.Itoa(pref)) != nil {
					break
				}
			}
		}
	}
}

// maxLogLines: batas tiap file log (backend.log, clash.log,
// sing-box.log, scrcpy-server.log) — permintaan user 2026-10-24:
// backend.log sempat 917 KB, jangan sampai 1 MB lagi. Halaman Log
// WebUI memang menampilkan 200 baris terakhir, jadi file mentok di
// angka yang sama: cukup untuk diagnosis sesi berjalan, tak pernah
// membesar lagi.
// capLogWriter: penulis log berbatas baris (pengganti rotWriter 1 MB).
//   - File dimulai SEGAR tiap dibuka (isi lama dibuang) agar log selalu
//     mencerminkan sesi berjalan, bukan akumulasi lintas reboot.
//   - Setelah 200 baris, tulisan berikutnya DIBUANG diam-diam (Write
//     tetap mengembalikan sukses) — proses tetap jalan, file tidak
//     pernah nambah baris lagi. Hapus file manual untuk me-reset.
//   - File .old sisa rotasi 1 MB lama dihapus saat dibuka.
const maxLogLines = 200

type capLogWriter struct {
	mu    sync.Mutex
	f     *os.File
	lines int
}

func newCapLogWriter(path string) *capLogWriter {
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	_ = os.Remove(path + ".old") // sisa rotasi lama, sudah tak dipakai
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return nil
	}
	return &capLogWriter{f: f}
}

func (w *capLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil || w.lines >= maxLogLines {
		return len(p), nil // penuh: dibuang, proses tetap jalan
	}
	n := bytes.Count(p, []byte{'\n'})
	if w.lines+n <= maxLogLines {
		w.lines += n
		return w.f.Write(p)
	}
	// Tulis parsial sampai baris ke-200, sisanya dibuang.
	need := maxLogLines - w.lines
	end := 0
	for i := 0; i < len(p) && need > 0; i++ {
		if p[i] == '\n' {
			need--
			if need == 0 {
				end = i + 1
			}
		}
	}
	if end > 0 {
		_, _ = w.f.Write(p[:end])
	}
	w.lines = maxLogLines
	return len(p), nil
}

func (w *capLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// tcpPing mengukur waktu connect TCP (ms), -1 bila gagal.
func tcpPing(host, port string, timeout time.Duration) int {
	start := time.Now()
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), timeout)
	if err != nil {
		return -1
	}
	_ = c.Close()
	ms := int(time.Since(start).Milliseconds())
	if ms < 1 {
		ms = 1
	}
	return ms
}

// udpBased: protokol berbasis UDP (TCP ping tidak bermakna).
func udpBased(proto string) bool {
	switch proto {
	case "hy2", "hysteria", "tuic", "wireguard":
		return true
	}
	return false
}

// httpFetchUA mengunduh URL (opsional lewat proxy HTTP) dengan UA v2rayNG
// agar penyedia subscription mengirim format link standar.
func httpFetchUA(rawurl string, timeout time.Duration, proxy string) ([]byte, error) {
	tr := &http.Transport{}
	if proxy != "" {
		if pu, err := url.Parse(proxy); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	c := &http.Client{Timeout: timeout, Transport: tr}
	req, err := http.NewRequest("GET", rawurl, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "v2rayNG/1.9.0")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// fetchSub: langsung dulu; bila gagal dan mihomo hidup, ulangi lewat proxy lokal.
func (a *App) fetchSub(u string) ([]byte, error) {
	body, err := httpFetchUA(u, 30*time.Second, "")
	if err == nil {
		return body, nil
	}
	if a.mihomoAlive() {
		a.store.mu.RLock()
		port := a.store.Settings.MixedPort
		a.store.mu.RUnlock()
		if b2, err2 := httpFetchUA(u, 40*time.Second, "http://127.0.0.1:"+itoa(port)); err2 == nil {
			return b2, nil
		}
	}
	return nil, err
}

func srvKey(sv *Server) string {
	return sv.Proto + "|" + sv.Params["server"] + "|" + sv.Params["port"] + "|" + sv.Name
}

func fpOf(m map[string]string) string {
	return orDefault(m["client-fingerprint"], m["fingerprint"])
}

// normalizePorts: "5000-6000,7000" -> "5000:6000,7000:7000"
func normalizePorts(s string) string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		t = strings.ReplaceAll(t, "-", ":")
		if !strings.Contains(t, ":") {
			t = t + ":" + t
		}
		out = append(out, t)
	}
	return strings.Join(out, ",")
}
