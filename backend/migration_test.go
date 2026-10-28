package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func migApp(t *testing.T, dir string) *App {
	t.Helper()
	a := &App{dataDir: dir, jsonDir: filepath.Join(dir, "config"), logDir: filepath.Join(dir, "log")}
	a.logger = log.New(io.Discard, "", 0)
	return a
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateLegacyModules(t *testing.T) {
	base := t.TempDir()
	box := filepath.Join(base, "taptapbox")
	clash := filepath.Join(base, "taptapclash")
	old := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)

	writeFile(t, filepath.Join(box, "json", "user otomatis", "settings.json"), `{"web_port":5056}`)
	writeFile(t, filepath.Join(box, "json", "user otomatis", "servers.json"), `[{"name":"box"}]`)
	writeFile(t, filepath.Join(box, "json", "user otomatis", "subscriptions.json"), `[]`)
	writeFile(t, filepath.Join(box, "json", "user otomatis", "builder.json"), `{}`)
	writeFile(t, filepath.Join(box, "json", "user otomatis", "config.override.json"), `{"x":1}`)
	writeFile(t, filepath.Join(box, "json", "user manual", "config.json"), `{"manual":"box"}`)
	writeFile(t, filepath.Join(clash, "json", "user otomatis", "settings.json"), `{"web_port":5556}`)
	writeFile(t, filepath.Join(clash, "json", "user otomatis", "servers.json"), `[{"name":"clash"}]`)
	writeFile(t, filepath.Join(clash, "json", "user otomatis", "subscriptions.json"), `[]`)
	writeFile(t, filepath.Join(clash, "json", "user otomatis", "builder.json"), `{}`)
	writeFile(t, filepath.Join(clash, "json", "user otomatis", "config.override.yaml"), "yaml: 1")
	writeFile(t, filepath.Join(clash, "json", "user manual", "config.yaml"), "manual: clash")
	_ = os.Chtimes(filepath.Join(box, "json", "user otomatis", "settings.json"), old, old)
	_ = os.Chtimes(filepath.Join(clash, "json", "user otomatis", "settings.json"), newer, newer)

	saved := legacyModuleRoots
	legacyModuleRoots = []legacyModule{
		{name: "taptapbox", core: "singbox", root: box},
		{name: "taptapclash", core: "clash", root: clash},
	}
	defer func() { legacyModuleRoots = saved }()

	dir := filepath.Join(base, "data")
	a := migApp(t, dir)
	a.migrateLegacyModules()

	// pemenang = clash (mtime terbaru): servers dari clash
	data, err := os.ReadFile(filepath.Join(dir, "config", "servers.json"))
	if err != nil || string(data) != `[{"name":"clash"}]` {
		t.Fatalf("servers pemenang salah: %v %s", err, data)
	}
	// settings: core=clash, port 5556 -> 5056
	data, _ = os.ReadFile(filepath.Join(dir, "config", "settings.json"))
	var st map[string]any
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	if st["core"] != "clash" {
		t.Fatalf("core=%v, mau clash", st["core"])
	}
	if int(st["web_port"].(float64)) != 5056 {
		t.Fatalf("web_port=%v, mau 5056", st["web_port"])
	}
	// yang kalah terarsip
	if _, err := os.Stat(filepath.Join(dir, "config", "migration-backup", "taptapbox", "settings.json")); err != nil {
		t.Fatalf("backup pihak kalah tidak ada: %v", err)
	}
	// artefak per-core dari modul asal masing-masing
	for _, p := range []string{
		filepath.Join(dir, "config", "clash", "user otomatis", "config.override.yaml"),
		filepath.Join(dir, "config", "clash", "user manual", "config.yaml"),
		filepath.Join(dir, "config", "singbox", "user otomatis", "config.override.json"),
		filepath.Join(dir, "config", "singbox", "user manual", "config.json"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("artefak hilang: %s (%v)", p, err)
		}
	}
}

func TestMigrateLegacyModulesSkip(t *testing.T) {
	base := t.TempDir()
	clash := filepath.Join(base, "taptapclash")
	writeFile(t, filepath.Join(clash, "json", "user otomatis", "settings.json"), `{"web_port":5556}`)
	saved := legacyModuleRoots
	legacyModuleRoots = []legacyModule{{name: "taptapclash", core: "clash", root: clash}}
	defer func() { legacyModuleRoots = saved }()
	dir := filepath.Join(base, "data")
	writeFile(t, filepath.Join(dir, "config", "settings.json"), `{"web_port":5056}`)
	a := migApp(t, dir)
	a.migrateLegacyModules()
	data, _ := os.ReadFile(filepath.Join(dir, "config", "settings.json"))
	if string(data) != `{"web_port":5056}` {
		t.Fatalf("settings gabungan tertimpa: %s", data)
	}
}

func TestMigrateStoreFilesLiftsToRoot(t *testing.T) {
	dir := t.TempDir()
	a := migApp(t, dir)
	writeFile(t, filepath.Join(dir, "config", "singbox", "user otomatis", "servers.json"), `[{"name":"x"}]`)
	a.migrateStoreFiles()
	data, err := os.ReadFile(filepath.Join(dir, "config", "servers.json"))
	if err != nil || string(data) != `[{"name":"x"}]` {
		t.Fatalf("servers tidak terangkat ke akar: %v %s", err, data)
	}
	if _, err := os.Stat(filepath.Join(dir, "config", "singbox", "user otomatis", "servers.json")); !os.IsNotExist(err) {
		t.Fatal("file lama seharusnya sudah pindah (rename)")
	}
}

func TestMigrateRulesetDir(t *testing.T) {
	dir := t.TempDir()
	a := migApp(t, dir)
	writeFile(t, filepath.Join(dir, "config", "singbox", "user otomatis", "_rulesets", "rs-a.srs"), "srs")
	writeFile(t, filepath.Join(dir, "mihomo", "prov-a.dat"), "dat")
	writeFile(t, filepath.Join(dir, "mihomo", "geoip.dat"), "geo")
	a.migrateRulesetDir()
	for _, p := range []string{
		filepath.Join(dir, "config", "singbox", "user otomatis", "rulesets", "rs-a.srs"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("seharusnya sudah pindah: %s (%v)", p, err)
		}
	}
	// TaptapBox: file .dat mihomo TIDAK dipindah ke config/clash lagi
	if _, err := os.Stat(filepath.Join(dir, "config", "clash")); !os.IsNotExist(err) {
		t.Fatalf("folder clash tidak boleh dibuat")
	}
	// geodata core tetap di workdir mihomo
	if _, err := os.Stat(filepath.Join(dir, "mihomo", "geoip.dat")); err != nil {
		t.Fatalf("geoip.dat tidak boleh ikut pindah: %v", err)
	}
	// folder ruleset _rulesets (v1.0.17) sing-box hilang
	if _, err := os.Stat(filepath.Join(dir, "config", "singbox", "user otomatis", "_rulesets")); !os.IsNotExist(err) {
		t.Fatal("folder _rulesets v1.0.17 seharusnya sudah dihapus")
	}
}

func TestMigrateRulesetDirFromOldNames(t *testing.T) {
	dir := t.TempDir()
	a := migApp(t, dir)
	writeFile(t, filepath.Join(dir, "config", "rulesets", "rs-b.srs"), "srs")
	writeFile(t, filepath.Join(dir, "config", "umum", "prov-b.dat"), "dat")
	writeFile(t, filepath.Join(dir, "config", "_rulesets", "rs-c.srs"), "srs")
	writeFile(t, filepath.Join(dir, "config", "_rulesets", "prov-c.dat"), "dat")
	a.migrateRulesetDir()
	for _, p := range []string{
		filepath.Join(dir, "config", "singbox", "user otomatis", "rulesets", "rs-b.srs"),
		filepath.Join(dir, "config", "singbox", "user otomatis", "rulesets", "rs-c.srs"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("seharusnya sudah pindah ke rulesets per-core: %s (%v)", p, err)
		}
	}
	// Folder rulesets (hanya berisi .srs) harus hilang; folder yang masih
	// berisi .dat mihomo dibiarkan (tidak dipindah ke clash).
	if _, err := os.Stat(filepath.Join(dir, "config", "rulesets")); !os.IsNotExist(err) {
		t.Fatalf("folder rulesets seharusnya sudah dihapus")
	}
}

// TaptapBox: migrasi sekali jalan dari data TaptapSlim lama.
func TestMigrateTaptapSlim(t *testing.T) {
	// migApp memakai dir sebagai dataDir; migrateTaptapSlim membaca
	// /data/adb/taptapslim yang absolut, jadi tes ini hanya memeriksa
	// logika lewat fungsi bantu? Lewati bila tidak bisa menulis /data/adb.
	t.Skip("migrasi Slim memakai path absolut /data/adb/taptapslim — diverifikasi manual di HP")
}
