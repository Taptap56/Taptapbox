package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Migrasi settings lama (ver 7, tanpa field FTP) -> default FTP terisi, Ver 8.
func TestFtpSettingsMigrasi(t *testing.T) {
	dir := t.TempDir()
	old := `{"ver":7,"mode":"v2rayng","hotspot":false}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if s.Settings.Ver != 17 {
		t.Fatalf("Ver harus 17, dapat %d", s.Settings.Ver)
	}
	if s.Settings.FtpPort != 2121 {
		t.Fatalf("FtpPort harus 2121, dapat %d", s.Settings.FtpPort)
	}
	if s.Settings.FtpRoot != "/" {
		t.Fatalf("FtpRoot harus /, dapat %q", s.Settings.FtpRoot)
	}
	if !s.Settings.FtpWrite {
		t.Fatalf("FtpWrite harus true (default)")
	}
	if s.Settings.FtpEnabled {
		t.Fatalf("FtpEnabled harus false (default)")
	}
}

// Port tidak valid ditolak sebelum menyentuh busybox.
func TestFtpStartPortInvalid(t *testing.T) {
	a := testApp(t)
	for _, p := range []int{0, -1, 70000} {
		if err := a.ftpStart(p, t.TempDir(), true); err == nil {
			t.Fatalf("port %d seharusnya ditolak", p)
		}
	}
}

// Pidfile basi (PID tidak ada) -> ftpRunning false + pidfile dibersihkan.
func TestFtpRunningStalePid(t *testing.T) {
	a := testApp(t)
	pf := a.ftpPidFile()
	if err := os.MkdirAll(filepath.Dir(pf), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pf, []byte("2147483000"), 0644); err != nil {
		t.Fatal(err)
	}
	if a.ftpRunning() {
		t.Fatalf("ftpRunning harus false untuk PID basi")
	}
	if _, err := os.Stat(pf); !os.IsNotExist(err) {
		t.Fatalf("pidfile basi seharusnya dihapus")
	}
}

// GET /api/ftp mengembalikan status JSON.
func TestHandleFtpStatus(t *testing.T) {
	a := testApp(t)
	r := httptest.NewRequest("GET", "/api/ftp", nil)
	w := httptest.NewRecorder()
	a.handleFtp(w, r)
	if w.Code != 200 {
		t.Fatalf("status code %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["running"] != false {
		t.Fatalf("running harus false, dapat %v", got["running"])
	}
	if got["port"] != float64(2121) {
		t.Fatalf("port harus 2121, dapat %v", got["port"])
	}
	if got["root"] != "/" {
		t.Fatalf("root harus /, dapat %v", got["root"])
	}
	if _, ok := got["addr"].(string); !ok {
		t.Fatalf("addr harus string")
	}
}

// ftpLocalIP tidak boleh panic; hasilnya IP valid atau string kosong.
func TestFtpLocalIP(t *testing.T) {
	ip := ftpLocalIP()
	if ip == "" {
		return // VM tanpa IP privat: wajar
	}
	parts := 0
	for _, c := range ip {
		if c == '.' {
			parts++
		}
	}
	if parts != 3 {
		t.Fatalf("bukan IPv4: %q", ip)
	}
}
