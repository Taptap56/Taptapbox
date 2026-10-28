package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// genbox_test.go — gerbang generator sing-box (port dari tes TaptapBox).
// Semua tes di sini berjalan dengan core aktif = singbox.

func boxTestApp(t *testing.T) *App {
	t.Helper()
	a := testApp(t)
	a.store.Settings.Core = "singbox"
	return a
}

func writeCfgJSON(t *testing.T, name string, cfg map[string]any) string {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := "/tmp/" + name
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %d bytes", name, len(data))
	return path
}

func TestBuildV2rayng(t *testing.T) {
	a := boxTestApp(t)
	cfg := a.buildV2rayng()
	path := writeCfgJSON(t, "cfg-v2rayng.json", cfg)
	// pastikan semua proto jadi outbound
	obs := cfg["outbounds"].([]any)
	types := map[string]bool{}
	for _, o := range obs {
		types[o.(map[string]any)["type"].(string)] = true
	}
	for _, want := range []string{"vmess", "vless", "trojan", "shadowsocks", "hysteria2", "tuic", "socks", "http", "selector"} {
		if !types[want] {
			t.Errorf("outbound %q hilang", want)
		}
	}
	// grup otomatis "♻️ Otomatis" sudah dihapus (v1.4.0): user bikin sendiri
	for _, o := range obs {
		if o.(map[string]any)["tag"] == "♻️ Otomatis" {
			t.Errorf("grup otomatis masih ada")
		}
	}
	// wireguard harus jadi endpoint
	eps, ok := cfg["endpoints"].([]any)
	if !ok || len(eps) == 0 {
		t.Errorf("endpoint wireguard hilang")
	} else if eps[0].(map[string]any)["type"] != "wireguard" {
		t.Errorf("endpoint bukan wireguard")
	}
	_ = path
}

func TestBuildConfigSkipsUncachedRemoteRuleSet(t *testing.T) {
	a := boxTestApp(t)
	a.store.Builder.RuleSets = []RuleSet{
		{Tag: "rs-nocache", Type: "remote", URL: "https://127.0.0.1:1/none.srs"},
	}
	a.store.Builder.Rules = []RouteRule{
		{ID: "rx", RuleSets: []string{"rs-nocache"}, Outbound: "REJECT"},
	}
	cfg, err := a.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(cfg)
	if strings.Contains(string(data), "rs-nocache") {
		t.Fatalf("rs-nocache yg tak tercache harus hilang dari config")
	}
	if strings.Contains(string(data), `"type":"remote"`) {
		t.Fatalf("config tak boleh memuat rule-set remote")
	}
}

func TestBuildConfigUsesCachedRuleSet(t *testing.T) {
	a := boxTestApp(t)
	p := filepath.Join(a.boxRulesetDir(), "rs-cached.srs")
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("fake-srs-data"), 0644); err != nil {
		t.Fatal(err)
	}
	a.store.Builder.RuleSets = []RuleSet{
		{Tag: "rs-cached", Type: "remote", URL: "https://127.0.0.1:1/none.srs"},
	}
	a.store.Builder.Rules = []RouteRule{
		{ID: "rx", RuleSets: []string{"rs-cached"}, Outbound: "DIRECT"},
	}
	cfg, err := a.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	route, _ := cfg["route"].(map[string]any)
	found := false
	for _, rs := range route["rule_set"].([]any) {
		m, _ := rs.(map[string]any)
		if m["tag"] == "rs-cached" {
			found = true
			if m["type"] != "local" || m["path"] != p {
				t.Fatalf("entri salah: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("rs-cached tidak ada di rule_set")
	}
}

func TestDNSHijackToggleGatesSingboxRule(t *testing.T) {
	hasHijackDNS := func(a *App) bool {
		cfg, err := a.buildConfig()
		if err != nil {
			t.Fatal(err)
		}
		route, _ := cfg["route"].(map[string]any)
		rules, _ := route["rules"].([]any)
		for _, r := range rules {
			if m, ok := r.(map[string]any); ok && m["action"] == "hijack-dns" {
				return true
			}
		}
		return false
	}
	a := boxTestApp(t)
	if !a.store.Settings.DNSHijack {
		t.Fatal("default DNSHijack harus true")
	}
	if !hasHijackDNS(a) {
		t.Fatal("rule hijack-dns harus ada saat toggle ON")
	}
	a.store.Settings.DNSHijack = false
	if hasHijackDNS(a) {
		t.Fatal("rule hijack-dns harus hilang saat toggle OFF")
	}
}

func TestQUICBlockNoSingboxRule(t *testing.T) {
	a := boxTestApp(t)
	a.store.Settings.ProxyMode = "tun"
	a.store.Settings.BlockQUIC = true
	cfg, err := a.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	route, _ := cfg["route"].(map[string]any)
	rules, _ := route["rules"].([]any)
	for _, r := range rules {
		m, _ := r.(map[string]any)
		if m["action"] == "reject" {
			if port, ok := m["port"].(int); ok && port == 443 {
				t.Fatal("rule reject 443 sing-box harus sudah dihapus (diganti netfilter)")
			}
			// port bisa float64 dari JSON round-trip; cek juga
			if pf, ok := m["port"].(float64); ok && pf == 443 {
				t.Fatal("rule reject 443 sing-box harus sudah dihapus (diganti netfilter)")
			}
		}
	}
}

func TestRebaseOverrideE2E(t *testing.T) {
	// TaptapBox library: validasi pakai library, tanpa biner sing-box.
	a := boxTestApp(t)

	// Override awal: hasil generate + editan manual user (dns + inbound custom
	// + outbound tag yang sama dengan generate agar valid).
	base, err := a.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	ov := map[string]any{}
	for k, v := range base {
		ov[k] = v
	}
	// Editan manual user: tambah DNS server custom yang valid + ubah log level.
	dns := ov["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	servers = append(servers, map[string]any{
		"tag": "dns-manual", "type": "udp", "server": "9.9.9.9",
	})
	dns["servers"] = servers
	if lg, ok := ov["log"].(map[string]any); ok {
		lg["level"] = "debug" // beda dari hasil generate (warn)
	}
	raw, _ := json.Marshal(ov)
	if err := a.applyOverrideConfigJSON(raw); err != nil {
		t.Fatal(err)
	}
	if !a.boxHasOverride() {
		t.Fatal("override seharusnya aktif")
	}

	// Simulasi edit grup di WebUI: tambah grup baru -> rebase.
	// Grup harus punya anggota agar tidak dilewati generator.
	members := []string{}
	if len(a.store.Servers) > 0 {
		members = append(members, a.store.Servers[0].ID)
	}
	a.store.mu.Lock()
	a.store.Builder.Groups = append(a.store.Builder.Groups, ProxyGroup{
		ID: "g1", Name: "Grup Baru", Type: "selector", Members: members,
	})
	_ = a.store.SaveBuilder()
	a.store.mu.Unlock()
	if err := a.rebaseOverrideJSON(); err != nil {
		t.Fatalf("rebase gagal: %v", err)
	}

	// Verifikasi isi override hasil rebase.
	data, _ := os.ReadFile(a.boxOverridePath())
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	// 1. Grup baru muncul di outbounds.
	found := false
	if obs, ok := got["outbounds"].([]any); ok {
		for _, o := range obs {
			if m, ok := o.(map[string]any); ok && m["tag"] == "Grup Baru" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("grup baru tidak ada di outbounds override setelah rebase")
	}
	// 2. Editan manual tetap utuh.
	dnsg, _ := got["dns"].(map[string]any)
	foundDNS := false
	if s2, ok := dnsg["servers"].([]any); ok {
		for _, s := range s2 {
			if m, ok := s.(map[string]any); ok && m["tag"] == "dns-manual" {
				foundDNS = true
				break
			}
		}
	}
	if !foundDNS {
		t.Error("DNS server manual hilang setelah rebase")
	}
	if lg, ok := got["log"].(map[string]any); ok && lg["level"] != "debug" {
		t.Error("level log manual hilang setelah rebase")
	}
	// 3. Hasil rebase lolos sing-box check (sudah di dalam rebase, validasi
	// ganda langsung di file override).
	if err := a.singboxTest(a.boxOverridePath()); err != nil {
		t.Fatalf("override hasil rebase tidak valid: %v", err)
	}
}

func TestRebaseOverrideInvalid(t *testing.T) {
	// TaptapBox library: validasi pakai library, tanpa biner sing-box.
	a := boxTestApp(t)

	base, err := a.buildConfig()
	if err != nil {
		t.Fatal(err)
	}
	// Buat override dengan dns.detour ke tag yang tidak akan ada di generate.
	ov := map[string]any{}
	for k, v := range base {
		ov[k] = v
	}
	// Tambahkan server DNS custom dengan tipe tidak dikenal — `sing-box
	// check` menolaknya. Ini mewakili editan manual user yang tidak valid
	// (kita pertahankan apa adanya di override awal via tulis langsung,
	// melewati applyOverrideConfigJSON yang bervalidasi).
	dns := ov["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	servers = append(servers, map[string]any{
		"tag": "dns-bad", "type": "tipe-tidak-ada", "server": "9.9.9.9",
	})
	dns["servers"] = servers
	if err := os.MkdirAll(a.boxAutoDir(), 0755); err != nil {
		t.Fatal(err)
	}
	pretty, _ := json.MarshalIndent(ov, "", "  ")
	if err := os.WriteFile(a.boxOverridePath(), pretty, 0644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(a.boxOverridePath())

	// Rebase: generate tidak punya GRUP-FIKTIF-TIDAK-ADA -> hasil invalid
	// -> file lama dipertahankan, error dikembalikan.
	err = a.rebaseOverrideJSON()
	if err == nil {
		t.Fatal("rebase seharusnya gagal karena detour fiktif")
	}
	after, _ := os.ReadFile(a.boxOverridePath())
	if string(before) != string(after) {
		t.Error("file override berubah padahal rebase gagal")
	}
}

func TestRebaseOverrideNoop(t *testing.T) {
	a := boxTestApp(t)
	if a.boxHasOverride() {
		t.Fatal("seharusnya belum ada override")
	}
	if err := a.rebaseOverrideJSON(); err != nil {
		t.Fatalf("rebase tanpa override harus no-op: %v", err)
	}
	_ = filepath.Join // paksa import terpakai bila dioptimalkan
	_ = strings.TrimSpace
}

// v1.0.45: prefetch geosite-ads hanya bila geosite.dat TIDAK ada —
// generator memakai aturan geosite inline dari dat lokal bila ada, jadi
// mengunduhnya tiap Terapkan hanya membuat file yang tidak pernah dipakai.
func TestNeededRemoteRuleSetsAdsFallback(t *testing.T) {
	has := func(list []remoteNeed, tag string) bool {
		for _, rn := range list {
			if rn.tag == tag {
				return true
			}
		}
		return false
	}
	// Tanpa geodata: geosite-ads dibutuhkan (fallback .srs).
	a := boxTestApp(t)
	a.store.Settings.BlockAds = true
	if !has(a.neededRemoteRuleSets(), "geosite-ads") {
		t.Fatal("tanpa geosite.dat, geosite-ads harus dibutuhkan")
	}
	// Dengan geosite.dat: tidak dibutuhkan, jangan diunduh lagi.
	_ = os.MkdirAll(filepath.Join(a.dataDir, "geodata"), 0755)
	_ = os.WriteFile(filepath.Join(a.dataDir, "geodata", "geosite.dat"), []byte("x"), 0644)
	if has(a.neededRemoteRuleSets(), "geosite-ads") {
		t.Fatal("dengan geosite.dat, geosite-ads tidak boleh dibutuhkan")
	}
	// Blokir Iklan mati: tidak dibutuhkan apa pun darinya.
	a.store.Settings.BlockAds = false
	if has(a.neededRemoteRuleSets(), "geosite-ads") {
		t.Fatal("Blokir Iklan mati, geosite-ads tidak boleh dibutuhkan")
	}
}
