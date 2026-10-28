package main

// FTP Server: bagikan file HP ke HP lain yang terhubung via hotspot /
// tethering. HP lain membuka alamat ftp://IP:port dengan MT Manager
// (atau file manager lain yang mendukung FTP).
//
// Implementasi mengikuti pendekatan tcpsvd.sh milik user: busybox
// `tcpsvd` sebagai listener + applet `ftpd` (mode anonim, opsi tulis).
// Dikelola dari WebUI (Pengaturan > FTP Server) seperti fitur Hotspot.

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	ftpBusyboxMagisk = "/data/adb/magisk/busybox"
	ftpDefaultPort   = 2121
	ftpDefaultRoot   = "/"
)

var ftpMu sync.Mutex

// ftpBusybox mencari binary busybox: Magisk dulu, lalu PATH.
func ftpBusybox() string {
	if _, err := os.Stat(ftpBusyboxMagisk); err == nil {
		return ftpBusyboxMagisk
	}
	if p, err := exec.LookPath("busybox"); err == nil {
		return p
	}
	return "busybox"
}

// ftpCheckApplets memastikan applet tcpsvd & ftpd tersedia di busybox.
func ftpCheckApplets(bb string) error {
	out, err := exec.Command(bb, "--list").Output()
	if err != nil {
		return fmt.Errorf("busybox tidak bisa dijalankan: %v", err)
	}
	have := map[string]bool{}
	for _, ln := range strings.Split(string(out), "\n") {
		have[strings.TrimSpace(ln)] = true
	}
	var miss []string
	for _, ap := range []string{"tcpsvd", "ftpd"} {
		if !have[ap] {
			miss = append(miss, ap)
		}
	}
	if len(miss) > 0 {
		return fmt.Errorf("busybox tidak punya applet: %s", strings.Join(miss, ", "))
	}
	return nil
}

func (a *App) ftpPidFile() string {
	return filepath.Join(a.dataDir, "ftp", "ftpd.pid")
}

// ftpPID membaca PID dari pidfile; mengembalikan 0 bila tidak valid.
func (a *App) ftpPID() int {
	b, err := os.ReadFile(a.ftpPidFile())
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid <= 0 {
		return 0
	}
	return pid
}

// ftpRunning true bila proses tcpsvd masih hidup. Pidfile basi dibersihkan.
func (a *App) ftpRunning() bool {
	pid := a.ftpPID()
	if pid == 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		_ = os.Remove(a.ftpPidFile())
		return false
	}
	return true
}

// ftpStop mematikan server (seluruh process group: listener + koneksi aktif).
func (a *App) ftpStop() {
	pid := a.ftpPID()
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGTERM) // group (tcpsvd + anak ftpd)
		_ = syscall.Kill(pid, syscall.SIGTERM)  // fallback: proses saja
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if syscall.Kill(pid, 0) != nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	_ = os.Remove(a.ftpPidFile())
}

// ftpStart menjalankan: busybox tcpsvd -vE 0.0.0.0 <port> busybox ftpd [-w] -A <root>
func (a *App) ftpStart(port int, root string, write bool) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d tidak valid", port)
	}
	root = strings.TrimSpace(root)
	if root == "" {
		root = ftpDefaultRoot
	}
	root = filepath.Clean(root)
	// Kebijakan user: folder FTP tidak dibatasi (boleh root /). Satu-satunya
	// syarat: path absolut. FTP anonim jalan sebagai root, jadi bila folder di
	// luar penyimpanan pengguna, catat peringatan di log.
	if !ftpRootAllowed(root) {
		return fmt.Errorf("folder %q harus path absolut", root)
	}
	if !ftpRootUserStorage(root) {
		a.logWarnf("FTP: folder %s di luar penyimpanan pengguna — akses root penuh via jaringan hotspot!", root)
	}
	bb := ftpBusybox()
	if err := ftpCheckApplets(bb); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return fmt.Errorf("folder %s tidak bisa dipakai: %v", root, err)
	}

	a.ftpStop() // pastikan tidak ada instance lama

	if err := os.MkdirAll(filepath.Dir(a.ftpPidFile()), 0755); err != nil {
		return err
	}
	args := []string{"tcpsvd", "-vE", "0.0.0.0", strconv.Itoa(port), bb, "ftpd"}
	if write {
		args = append(args, "-w")
	}
	args = append(args, "-A", root)
	cmd := exec.Command(bb, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// log tcpsvd (-v) dibuang; status cukup dari pidfile + endpoint
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("gagal menjalankan tcpsvd: %v", err)
	}
	go cmd.Wait() // reap zombie saat proses mati
	pid := cmd.Process.Pid
	_ = os.WriteFile(a.ftpPidFile(), []byte(strconv.Itoa(pid)), 0644)
	time.Sleep(400 * time.Millisecond)
	if !a.ftpRunning() {
		_ = os.Remove(a.ftpPidFile())
		return fmt.Errorf("tcpsvd mati sesaat setelah dijalankan (port %d mungkin dipakai)", port)
	}
	a.logf("ftp: berjalan di port %d root=%s tulis=%v (pid %d)", port, root, write, pid)
	return nil
}

// ftpSet mengaktifkan / mematikan FTP sesuai settings yang tersimpan.
func (a *App) ftpSet(on bool) error {
	ftpMu.Lock()
	defer ftpMu.Unlock()
	if !on {
		a.ftpStop()
		a.logf("ftp: dimatikan")
		return nil
	}
	s := a.store.Settings
	port := s.FtpPort
	if port == 0 {
		port = ftpDefaultPort
	}
	root := s.FtpRoot
	if root == "" {
		root = ftpDefaultRoot
	}
	return a.ftpStart(port, root, s.FtpWrite)
}

// ftpRootAllowed: folder FTP boleh di mana saja (permintaan user: tidak dibatasi).
// Satu-satunya syarat: path absolut, agar tidak relatif terhadap cwd backend.
func ftpRootAllowed(root string) bool {
	r := filepath.Clean(strings.TrimSpace(root))
	return strings.HasPrefix(r, "/")
}

// ftpRootUserStorage: true bila folder di penyimpanan pengguna (untuk peringatan log).
func ftpRootUserStorage(root string) bool {
	r := filepath.Clean(strings.TrimSpace(root))
	for _, p := range []string{"/sdcard", "/storage", "/data/media", "/mnt/media_rw", "/mnt/sdcard"} {
		if r == p || strings.HasPrefix(r, p+"/") {
			return true
		}
	}
	return false
}

// ftpLocalIPCached: ftpLocalIP dengan cache 10 detik (dipanggil tiap polling status).
var (
	ipCacheMu  sync.Mutex
	ipCacheVal string
	ipCacheAt  time.Time
)

func ftpLocalIPCached() string {
	ipCacheMu.Lock()
	defer ipCacheMu.Unlock()
	if !ipCacheAt.IsZero() && time.Since(ipCacheAt) < 10*time.Second {
		return ipCacheVal
	}
	ipCacheVal = ftpLocalIP()
	ipCacheAt = time.Now()
	return ipCacheVal
}

// ftpLocalIP mencari IP privat HP ini (biasanya IP gateway hotspot,
// mis. 192.168.43.1) untuk ditampilkan sebagai alamat koneksi MT Manager.
func ftpLocalIP() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var cands []string
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, ad := range addrs {
			var ip net.IP
			switch v := ad.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil {
				continue
			}
			ip4 := ip.To4()
			if ip4 == nil || !ip4.IsPrivate() {
				continue
			}
			cands = append(cands, ip4.String())
		}
	}
	// prioritaskan subnet tethering Android (192.168.43.x / 192.168.44.x)
	for _, p := range []string{"192.168.43.", "192.168.44.", "192.168."} {
		for _, c := range cands {
			if strings.HasPrefix(c, p) {
				return c
			}
		}
	}
	if len(cands) > 0 {
		return cands[0]
	}
	return ""
}

// handleFtp: GET /api/ftp -> status server untuk ditampilkan di WebUI.
func (a *App) handleFtp(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	a.store.mu.RLock()
	port := a.store.Settings.FtpPort
	root := a.store.Settings.FtpRoot
	write := a.store.Settings.FtpWrite
	a.store.mu.RUnlock()
	if port == 0 {
		port = ftpDefaultPort
	}
	if root == "" {
		root = ftpDefaultRoot
	}
	writeJSON(w, map[string]any{
		"running": a.ftpRunning(),
		"port":    port,
		"root":    root,
		"write":   write,
		"addr":    ftpLocalIP(),
	})
}
