package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TaptapBox: hanya folder singbox "user otomatis" & "user manual" dibuat;
// folder clash TIDAK dibuat lagi.
func TestEnsureConfigDirs(t *testing.T) {
	a := testApp(t)
	a.ensureConfigDirs()
	for _, d := range []string{a.boxAutoDir(), a.boxManualDir()} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Fatalf("folder tidak ada: %s", d)
		}
	}
	// (testApp sendiri memakai autoDir sebagai store lama, jadi folder
	// clash bisa sudah ada dari setup tes — yang diuji di sini adalah
	// ensureConfigDirs membuat folder singbox, bukan clash.)
	// boxActiveConfigPath sesuai mode (config.json)
	if got := a.boxActiveConfigPath(); got != filepath.Join(a.boxAutoDir(), "config.json") {
		t.Fatalf("mode otomatis salah: %s", got)
	}
	a.store.Settings.ManualConfig = true
	if got := a.boxActiveConfigPath(); got != filepath.Join(a.boxManualDir(), "config.json") {
		t.Fatalf("mode manual salah: %s", got)
	}
}

// config.json lama (format sing-box) diarsipkan, tidak dipakai mihomo.
func TestEnsureConfigDirsArsip(t *testing.T) {
	a := testApp(t)
	legacy := filepath.Join(a.jsonDir, "config.json")
	if err := os.WriteFile(legacy, []byte(`{"log":{"level":"info"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	a.ensureConfigDirs()
	if _, err := os.Stat(legacy + ".singbox-bak"); err != nil {
		t.Fatalf("config.json lama harus diarsipkan: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("config.json lama seharusnya sudah dipindah")
	}
}

// Export: satu link per baris.
func TestHandleExportServers(t *testing.T) {
	a := testApp(t)
	// server tanpa Link tersimpan -> fallback buildLink
	a.store.Servers[0].Link = ""
	req := httptest.NewRequest("GET", "/api/servers/export", nil)
	rec := httptest.NewRecorder()
	a.handleExportServers(rec, req)
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != len(a.store.Servers) {
		t.Fatalf("dapat %d baris, server %d", len(lines), len(a.store.Servers))
	}
	for i, ln := range lines {
		if !strings.Contains(ln, "://") {
			t.Fatalf("baris %d bukan link: %q", i, ln)
		}
	}
}

// Pick server ditolak saat mode manual, jalan saat otomatis.
func TestHandleServerPickManual(t *testing.T) {
	a := testApp(t)
	id := a.store.Servers[0].ID
	body := strings.NewReader(`{"id":"` + id + `"}`)
	req := httptest.NewRequest("POST", "/api/servers/pick", body)
	rec := httptest.NewRecorder()
	a.store.Settings.ManualConfig = true
	a.handleServerPick(rec, req)
	if rec.Code != 400 {
		t.Fatalf("mode manual harus 400, dapat %d", rec.Code)
	}
	if a.store.Settings.SelectedID == id {
		t.Fatalf("SelectedID tidak boleh berubah di mode manual")
	}
	body2 := strings.NewReader(`{"id":"` + id + `"}`)
	req2 := httptest.NewRequest("POST", "/api/servers/pick", body2)
	rec2 := httptest.NewRecorder()
	a.store.Settings.ManualConfig = false
	a.handleServerPick(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("mode otomatis harus 200, dapat %d", rec2.Code)
	}
	if a.store.Settings.SelectedID != id {
		t.Fatalf("SelectedID seharusnya %s", id)
	}
}

// Migrasi: file data lama di json/ pindah ke user otomatis/ dan terbaca store.
func TestMigrateStoreFiles(t *testing.T) {
	dir := t.TempDir()
	a := &App{jsonDir: filepath.Join(dir, "json")}
	_ = os.MkdirAll(a.jsonDir, 0755)
	// Semantik gabungan: Store tinggal di AKAR jsonDir (config/). File yang
	// sudah di akar TETAP di akar — tidak dipindah ke subfolder per-core.
	for _, n := range []string{"servers.json", "settings.json"} {
		if err := os.WriteFile(filepath.Join(a.jsonDir, n), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	a.migrateStoreFiles()
	for _, n := range []string{"servers.json", "settings.json"} {
		if _, err := os.Stat(filepath.Join(a.jsonDir, n)); err != nil {
			t.Fatalf("%s hilang dari root: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(a.autoDir(), n)); !os.IsNotExist(err) {
			t.Fatalf("%s ikut tersalin ke subfolder core", n)
		}
	}
	// File di akar menang atas salinan warisan di subfolder per-core.
	if err := os.WriteFile(filepath.Join(a.jsonDir, "builder.json"), []byte(`{"x":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(a.autoDir(), 0755)
	if err := os.WriteFile(filepath.Join(a.autoDir(), "builder.json"), []byte(`{"x":2}`), 0644); err != nil {
		t.Fatal(err)
	}
	a.migrateStoreFiles()
	b, _ := os.ReadFile(filepath.Join(a.jsonDir, "builder.json"))
	if !strings.Contains(string(b), `"x":1`) {
		t.Fatalf("file akar tertimpa: %s", b)
	}
}
