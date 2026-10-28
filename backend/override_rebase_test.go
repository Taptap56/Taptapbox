package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSingleMatchLine: baris "MATCH," hanya boleh satu di seluruh config —
// di paling akhir, menunjuk grup utama. Aturan tanpa kondisi domain/IP/
// rule-set (mis. hanya port atau aplikasi) tidak boleh memancarkan MATCH
// di tengah daftar (dulu menelan semua rute di bawahnya).
func TestSingleMatchLine(t *testing.T) {
	a := testApp(t)
	a.store.Builder.Rules = []RouteRule{
		{ID: "r1", DomainSuffix: []string{"a.com"}, Outbound: "Proxy"},
		{ID: "r2", Port: []int{443}, Outbound: "Proxy"},                           // hanya port
		{ID: "r3", PackageNames: []string{"com.example.app"}, Outbound: "DIRECT"}, // hanya aplikasi
		{ID: "r4", Protocol: []string{"udp"}, Outbound: "Proxy"},                  // hanya protokol
	}
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	rules := cfg["rules"].([]string)
	n := 0
	for _, r := range rules {
		if strings.HasPrefix(r, "MATCH,") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("harus tepat 1 baris MATCH, dapat %d: %v", n, rules)
	}
	if last := rules[len(rules)-1]; last != "MATCH,Taptap" {
		t.Fatalf("MATCH akhir salah: %v", last)
	}
	// baris DST-PORT/PROCESS-NAME/NETWORK tetap ada sebagai pencocok
	joined := strings.Join(rules, "\n")
	for _, want := range []string{"DST-PORT,443,Taptap", "PROCESS-NAME,com.example.app,DIRECT", "NETWORK,UDP,Taptap"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("baris %q hilang", want)
		}
	}
}

// TestUnifiedDelayDefault: unified-delay: true selalu ada di config bawaan.
func TestUnifiedDelayDefault(t *testing.T) {
	a := testApp(t)
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	if cfg["unified-delay"] != true {
		t.Fatalf("unified-delay harus true, dapat %v", cfg["unified-delay"])
	}
	yml, err := a.buildConfigYAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(yml, "unified-delay: true") {
		t.Fatal("YAML tidak memuat 'unified-delay: true'")
	}
}

// TestRebaseOverride: edit grup selagi override aktif menyegarkan bagian
// proxies/proxy-groups/rules di file override tanpa menghapus editan manual.
func TestRebaseOverride(t *testing.T) {
	a := testApp(t)
	// stub mihomo -t: selalu valid (biner asli tidak ada di mesin test)
	stub := filepath.Join(t.TempDir(), "mihomo-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	a.mihomoBin = stub

	// override awal: hasil generate + editan manual user (mixed-port)
	// + bagian proxy-groups dihapus user (harus dimunculkan lagi oleh rebase)
	yml, err := a.buildConfigYAML()
	if err != nil {
		t.Fatal(err)
	}
	ov, err := yamlToMap(yml)
	if err != nil {
		t.Fatal(err)
	}
	ov["mixed-port"] = 7899 // editan manual
	delete(ov, "proxy-groups")
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(ov); err != nil {
		t.Fatal(err)
	}
	enc.Close()
	if err := os.WriteFile(a.overridePath(), []byte(sb.String()), 0644); err != nil {
		t.Fatal(err)
	}
	if !a.hasOverride() {
		t.Fatal("hasOverride harus true")
	}

	// user menambah grup baru
	first := a.store.Servers[0].ID
	a.store.Builder.Groups = []ProxyGroup{
		{ID: "gx", Name: "Baru", Type: "selector", Members: []string{first}},
	}
	if err := a.rebaseOverride(); err != nil {
		t.Fatalf("rebase gagal: %v", err)
	}
	data, err := os.ReadFile(a.overridePath())
	if err != nil {
		t.Fatal(err)
	}
	after, err := yamlToMap(string(data))
	if err != nil {
		t.Fatal(err)
	}
	// editan manual tetap ada
	if after["mixed-port"] != 7899 {
		t.Fatalf("editan manual hilang: mixed-port=%v", after["mixed-port"])
	}
	// grup baru masuk
	found := false
	for _, g := range after["proxy-groups"].([]any) {
		if g.(map[string]any)["name"] == "Baru" {
			found = true
		}
	}
	if !found {
		t.Fatalf("grup baru tidak ada di override: %v", after["proxy-groups"])
	}
	// rebase idempoten
	if err := a.rebaseOverride(); err != nil {
		t.Fatalf("rebase kedua gagal: %v", err)
	}
	// tanpa override = no-op
	_ = os.Remove(a.overridePath())
	if err := a.rebaseOverride(); err != nil {
		t.Fatalf("rebase tanpa override harus no-op: %v", err)
	}
}

// TestRebaseOverrideInboundSync: ganti mode proxy saat override aktif harus
// menyinkronkan inbound (tproxy-port/redir-port/tun/dns.listen) agar internet
// tidak mati (regresi: override dari mode TUN dipakai di mode tproxy tanpa
// tproxy-port + sisa "tun:" basi).
func TestRebaseOverrideInboundSync(t *testing.T) {
	t.Skip("TaptapBox: jalur mihomo tidak ada, hanya sing-box")
	a := testApp(t)
	stub := filepath.Join(t.TempDir(), "mihomo-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	a.mihomoBin = stub

	// override dibuat dari config mode TUN (ada "tun:", tanpa tproxy-port)
	yml, err := a.buildConfigYAML()
	if err != nil {
		t.Fatal(err)
	}
	ov, _ := yamlToMap(yml)
	if _, ok := ov["tun"]; !ok {
		t.Fatal("base TUN harus punya kunci tun")
	}
	ov["mixed-port"] = 7899 // editan manual user
	writeYAML(t, a.overridePath(), ov)

	// ganti ke tproxy -> rebase
	a.store.Settings.ProxyMode = "tproxy"
	if err := a.rebaseOverride(); err != nil {
		t.Fatalf("rebase: %v", err)
	}
	after := readYAML(t, a.overridePath())
	if after["tproxy-port"] != 15359 {
		t.Fatalf("tproxy-port harus 15359, dapat %v", after["tproxy-port"])
	}
	if _, ok := after["tun"]; ok {
		t.Fatal("kunci 'tun' basi harus dihapus saat pindah ke tproxy")
	}
	if after["dns"].(map[string]any)["listen"] != "127.0.0.1:1053" {
		t.Fatalf("dns.listen salah: %v", after["dns"])
	}
	if after["mixed-port"] != 7899 {
		t.Fatal("editan manual mixed-port harus dipertahankan")
	}

	// kembali ke tun -> rebase
	a.store.Settings.ProxyMode = "tun"
	if err := a.rebaseOverride(); err != nil {
		t.Fatalf("rebase: %v", err)
	}
	after = readYAML(t, a.overridePath())
	if _, ok := after["tun"]; !ok {
		t.Fatal("kunci 'tun' harus kembali saat mode tun")
	}
	if _, ok := after["tproxy-port"]; ok {
		t.Fatal("tproxy-port basi harus dihapus saat mode tun")
	}
	if _, ok := after["dns"].(map[string]any)["listen"]; ok {
		t.Fatal("dns.listen basi harus dihapus saat mode tun")
	}
}

// TestRebaseOverridePreservesText: regresi keluhan user — rebase TIDAK boleh
// mengacak urutan kunci jadi alfabetis, membuang komentar, atau memindah
// section manual (mis. listeners eBPF). Hanya section kelolaan builder
// (proxies/proxy-groups/rules + inbound mode + dns.listen) yang diganti.
func TestRebaseOverridePreservesText(t *testing.T) {
	a := testApp(t)
	stub := filepath.Join(t.TempDir(), "mihomo-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	a.mihomoBin = stub

	// override tulisan tangan user: urutan tidak alfabetis, ada komentar,
	// listeners eBPF di tengah, mixed-port editan manual.
	ovr := `# config saya — jangan diacak
mixed-port: 7899
geodata-mode: true
ipv6: false
listeners:
  - name: ebpf-in
    type: ebpf
    port: 15361
    local:
      enabled: true
      dns-mode: hijack
# komentar di atas log-level
log-level: warning
proxies:
  - name: LAMA
    type: socks5
    server: 127.0.0.1
    port: 1080
rules:
  - MATCH,LAMA
`
	if err := os.WriteFile(a.overridePath(), []byte(ovr), 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.rebaseOverride(); err != nil {
		t.Fatalf("rebase gagal: %v", err)
	}
	data, err := os.ReadFile(a.overridePath())
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	// komentar & editan manual utuh
	for _, want := range []string{"# config saya — jangan diacak", "mixed-port: 7899", "# komentar di atas log-level"} {
		if !strings.Contains(out, want) {
			t.Fatalf("teks user hilang: %q\n--- hasil ---\n%s", want, out)
		}
	}
	// blok listeners utuh persis & tetap SEBELUM log-level (urutan user)
	lis := "listeners:\n  - name: ebpf-in\n    type: ebpf\n    port: 15361\n    local:\n      enabled: true\n      dns-mode: hijack\n"
	if !strings.Contains(out, lis) {
		t.Fatalf("blok listeners berubah:\n%s", out)
	}
	if strings.Index(out, "listeners:") > strings.Index(out, "log-level:") {
		t.Fatalf("urutan berubah: listeners pindah sesudah log-level:\n%s", out)
	}
	// section kelolaan diperbarui dari builder (proxy LAMA hilang, MATCH ke grup utama)
	if strings.Contains(out, "LAMA") {
		t.Fatalf("section kelolaan tidak diperbarui:\n%s", out)
	}
	parsed, err := yamlToMap(out)
	if err != nil {
		t.Fatalf("hasil bukan YAML sah: %v", err)
	}
	rules, _ := parsed["rules"].([]any)
	if len(rules) == 0 || rules[len(rules)-1] != "MATCH,Taptap" {
		t.Fatalf("rules baru tidak ada / MATCH akhir salah: %v", rules)
	}
	// hasil tetap YAML sah
	if _, err := yamlToMap(out); err != nil {
		t.Fatalf("hasil rebase bukan YAML sah: %v", err)
	}
	// idempoten: rebase kedua tidak mengubah apa pun
	if err := a.rebaseOverride(); err != nil {
		t.Fatalf("rebase kedua gagal: %v", err)
	}
	data2, _ := os.ReadFile(a.overridePath())
	if string(data2) != out {
		t.Fatalf("rebase tidak idempoten:\n--- 1 ---\n%s\n--- 2 ---\n%s", out, data2)
	}
}

func writeYAML(t *testing.T, path string, m map[string]any) {
	t.Helper()
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		t.Fatal(err)
	}
	enc.Close()
	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		t.Fatal(err)
	}
}

func readYAML(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := yamlToMap(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
