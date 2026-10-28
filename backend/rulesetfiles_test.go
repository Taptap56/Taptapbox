package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Daftar file rulesets: file copy manual tampil, entri builder terdaftar.
func TestRulesetFilesList(t *testing.T) {
	a := testApp(t)
	dir := a.boxRulesetDir()
	if err := os.WriteFile(filepath.Join(dir, "geoip-id.srs"), []byte("0123456789"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bebas.mrs"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	a.store.Builder.RuleSets = []RuleSet{{ID: "r1", Tag: "geoip-id", Type: "remote", Format: "binary", URL: "https://x/geoip-id.srs"}}
	a.store.mu.Lock()
	_ = a.store.SaveBuilder()
	a.store.mu.Unlock()

	req := httptest.NewRequest("GET", "/api/ruleset-files?core=singbox", nil)
	rec := httptest.NewRecorder()
	a.handleRulesetFiles(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Core  string        `json:"core"`
		Dir   string        `json:"dir"`
		Files []rulesetFile `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Core != "singbox" || len(resp.Files) != 2 {
		t.Fatalf("list salah: %+v", resp)
	}
	for _, f := range resp.Files {
		if f.Name == "geoip-id.srs" && !f.Registered {
			t.Fatalf("geoip-id.srs harus terdaftar")
		}
		if f.Name == "bebas.mrs" && f.Registered {
			t.Fatalf("bebas.mrs harus belum terdaftar")
		}
		if f.Name == "geoip-id.srs" && f.RelPath != "config/singbox/user otomatis/rulesets/geoip-id.srs" {
			// relpath dihitung thd dataDir (tmpdir testApp) — cukup pastikan
			// berakhiran nama file di folder rulesets.
			if !strings.HasSuffix(f.RelPath, "rulesets/geoip-id.srs") {
				t.Fatalf("relpath salah: %s", f.RelPath)
			}
		}
	}
}

// Hapus per nama: file hilang + entri builder yang memakainya ikut cabut.
// Path traversal (../) ditolak.
func TestRulesetFilesDelete(t *testing.T) {
	a := testApp(t)
	dir := a.boxRulesetDir()
	p := filepath.Join(dir, "hapus.srs")
	if err := os.WriteFile(p, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	a.store.Builder.RuleSets = []RuleSet{{ID: "r1", Tag: "hapus", Type: "remote", Format: "binary", URL: "https://x/hapus.srs"}}
	a.store.mu.Lock()
	_ = a.store.SaveBuilder()
	a.store.mu.Unlock()

	body, _ := json.Marshal(map[string]any{"core": "singbox", "name": "hapus.srs"})
	req := httptest.NewRequest("DELETE", "/api/ruleset-files", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	a.handleRulesetFiles(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("file harus sudah terhapus")
	}
	if len(a.store.Builder.RuleSets) != 0 {
		t.Fatalf("entri builder harus ikut tercabut: %+v", a.store.Builder.RuleSets)
	}

	// Traversal ditolak, tidak ada file di luar folder yang tersentuh.
	luar := filepath.Join(a.dataDir, "jangan-hapus.txt")
	if err := os.WriteFile(luar, []byte("penting"), 0644); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(map[string]any{"core": "singbox", "name": "../jangan-hapus.txt"})
	req = httptest.NewRequest("DELETE", "/api/ruleset-files", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	a.handleRulesetFiles(rec, req)
	if rec.Code != 400 {
		t.Fatalf("traversal harus 400, dapat %d", rec.Code)
	}
	if _, err := os.Stat(luar); err != nil {
		t.Fatalf("file luar folder tidak boleh tersentuh")
	}
}

// Hapus per tag: file turunan tag (tag.srs) hilang walau nama file
// tidak disebut eksplisit — ini jalur tombol hapus entri rule-set.
func TestRulesetFilesDeleteByTag(t *testing.T) {
	a := testApp(t)
	dir := a.clashRulesetDir()
	p := filepath.Join(dir, "lama.dat")
	if err := os.WriteFile(p, []byte("cache"), 0644); err != nil {
		t.Fatal(err)
	}
	a.store.Builder.RuleSets = []RuleSet{{ID: "r1", Tag: "lama", Type: "remote", Format: "binary", URL: "https://x/lama.mrs"}}
	a.store.mu.Lock()
	_ = a.store.SaveBuilder()
	a.store.mu.Unlock()

	body, _ := json.Marshal(map[string]any{"core": "clash", "tag": "lama"})
	req := httptest.NewRequest("DELETE", "/api/ruleset-files", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	a.handleRulesetFiles(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("file tag.dat harus sudah terhapus")
	}
	if len(a.store.Builder.RuleSets) != 0 {
		t.Fatalf("entri builder harus ikut tercabut")
	}
}
