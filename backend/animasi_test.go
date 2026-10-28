package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAnimasi(t *testing.T, a *App) {
	t.Helper()
	dir := filepath.Join(a.webuiDir, "animasi")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"1. jaringan saraf ai.html": "<html>neural</html>",
		"2. bumi ai.html":           "<html>earth</html>",
		"10.html":                   "<html>sepuluh</html>",
		"catatan.txt":               "abaikan",
		"nohtml":                    "abaikan",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "3"), 0755); err != nil {
		t.Fatal(err)
	}
}

// Nomor diekstrak dari nama file (nama bebas), urut menaik; non-html & folder diabaikan.
func TestAnimasiFiles(t *testing.T) {
	a := testApp(t)
	writeAnimasi(t, a)
	got := a.animasiFiles()
	want := []int{1, 2, 10}
	if len(got) != len(want) {
		t.Fatalf("dapat %v, mau %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dapat %v, mau %v", got, want)
		}
	}
}

// /api/animasi/file?n=2 menyajikan file bernomor 2 apa pun namanya.
func TestHandleAnimasiFile(t *testing.T) {
	a := testApp(t)
	writeAnimasi(t, a)

	get := func(q string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/animasi/file?"+q, nil)
		w := httptest.NewRecorder()
		a.handleAnimasiFile(w, r)
		return w
	}
	w := get("n=2")
	if w.Code != 200 {
		t.Fatalf("n=2 harus 200, dapat %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type harus html: %q", ct)
	}
	if !strings.Contains(w.Body.String(), "earth") {
		t.Fatalf("isi salah: %q", w.Body.String())
	}
	if w := get("n=99"); w.Code != 404 {
		t.Fatalf("n=99 harus 404, dapat %d", w.Code)
	}
	if w := get("n=abc"); w.Code != 400 {
		t.Fatalf("n=abc harus 400, dapat %d", w.Code)
	}
	if w := get("n=../x"); w.Code != 400 {
		t.Fatalf("path traversal harus 400, dapat %d", w.Code)
	}
}

// /api/animasi mengembalikan daftar nomor.
func TestHandleAnimasi(t *testing.T) {
	a := testApp(t)
	writeAnimasi(t, a)
	r := httptest.NewRequest("GET", "/api/animasi", nil)
	w := httptest.NewRecorder()
	a.handleAnimasi(w, r)
	if w.Code != 200 {
		t.Fatalf("harus 200, dapat %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "[1,2,10]") {
		t.Fatalf("daftar nomor salah: %s", w.Body.String())
	}
}

// Migrasi settings lama (ver 8) -> AnimasiBg default 0, Ver 9.
func TestAnimasiBgMigrasi(t *testing.T) {
	dir := t.TempDir()
	old := `{"ver":8,"mode":"v2rayng","ftp_port":2121}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if s.Settings.Ver != 17 {
		t.Fatalf("Ver harus 17, dapat %d", s.Settings.Ver)
	}
	if s.Settings.AnimasiBg != 0 {
		t.Fatalf("AnimasiBg harus 0 (mati), dapat %d", s.Settings.AnimasiBg)
	}
	if s.Settings.AnimasiAlpha != 55 || s.Settings.AnimasiBlur != 8 {
		t.Fatalf("default alpha/blur harus 55/8, dapat %d/%d",
			s.Settings.AnimasiAlpha, s.Settings.AnimasiBlur)
	}
	if s.Settings.FtpPort != 2121 {
		t.Fatalf("FtpPort harus tetap 2121, dapat %d", s.Settings.FtpPort)
	}
}

// PUT /api/settings {animasi_bg} menyimpan tanpa mengubah field lain.
func TestHandleSettingsAnimasiBg(t *testing.T) {
	a := testApp(t)
	put := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body))
		w := httptest.NewRecorder()
		a.handleSettings(w, r)
		return w
	}
	if w := put(`{"animasi_bg":2}`); w.Code != 200 {
		t.Fatalf("code %d: %s", w.Code, w.Body.String())
	}
	if a.store.Settings.AnimasiBg != 2 {
		t.Fatalf("AnimasiBg harus 2, dapat %d", a.store.Settings.AnimasiBg)
	}
	if a.store.Settings.FtpPort != 2121 || a.store.Settings.Mode != "v2rayng" {
		t.Fatalf("field lain ikut berubah: ftp_port=%d mode=%q",
			a.store.Settings.FtpPort, a.store.Settings.Mode)
	}
	if w := put(`{"animasi_bg":0}`); w.Code != 200 {
		t.Fatalf("code %d", w.Code)
	}
	if a.store.Settings.AnimasiBg != 0 {
		t.Fatalf("AnimasiBg harus kembali 0")
	}
}

// PUT /api/settings {animasi_alpha, animasi_blur} tersimpan (0 valid untuk blur).
func TestHandleSettingsAnimasiCfg(t *testing.T) {
	a := testApp(t)
	r := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"animasi_alpha":30,"animasi_blur":0}`))
	w := httptest.NewRecorder()
	a.handleSettings(w, r)
	if w.Code != 200 {
		t.Fatalf("code %d: %s", w.Code, w.Body.String())
	}
	if a.store.Settings.AnimasiAlpha != 30 || a.store.Settings.AnimasiBlur != 0 {
		t.Fatalf("dapat alpha=%d blur=%d", a.store.Settings.AnimasiAlpha, a.store.Settings.AnimasiBlur)
	}
}

// List kosong harus [] bukan null — WebUI mengasumsikan array (filter/map/length).
// Simulasi install baru: servers.json & subscriptions.json belum ada (nil saat load).
func TestEmptyListsNotNull(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if s.Servers == nil || s.Subs == nil {
		t.Fatalf("NewStore harus normalisasi nil jadi slice kosong")
	}
	a := &App{store: s}
	r := httptest.NewRequest("GET", "/api/servers", nil)
	w := httptest.NewRecorder()
	a.handleServers(w, r)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("/api/servers harus [], dapat %s", w.Body.String())
	}
	r = httptest.NewRequest("GET", "/api/subscriptions", nil)
	w = httptest.NewRecorder()
	a.handleSubs(w, r)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("/api/subscriptions harus [], dapat %s", w.Body.String())
	}
}
