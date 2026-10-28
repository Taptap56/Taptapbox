// mihomo_gen_test.go — generate config YAML sample untuk validasi
// dengan `mihomo -t`. Bukan unit test biasa: butuh argumen -dump.
package main

import (
	"flag"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var dumpPath = flag.String("dump", "", "tulis config YAML sample ke path ini")

func sampleApp() *App {
	dir, _ := os.MkdirTemp("", "ttc-test")
	a := &App{dataDir: dir}
	a.jsonDir = filepath.Join(dir, "json")
	a.logDir = filepath.Join(dir, "log")
	a.binDir = filepath.Join(dir, "bin")
	a.webuiDir = filepath.Join(dir, "webui")
	_ = os.MkdirAll(a.jsonDir, 0755)
	_ = os.MkdirAll(a.logDir, 0755)
	a.logger = log.New(os.Stderr, "", 0)
	a.store = NewStore(a.autoDir())
	// sample servers: semua protokol yang didukung
	a.store.Servers = []Server{
		{ID: "1", Name: "vless-reality", Proto: "vless", Enabled: true, Params: map[string]string{
			"server": "sg.example.com", "port": "443", "uuid": "11111111-2222-4333-8444-555555555555",
			"tls": "1", "sni": "sg.example.com", "fp": "chrome", "reality": "1",
			"pbk": "X4Y5p9VBVV8eTQ5RkISEoafMczkGpPuVfY16pv11mXs", "sid": "abcd1234", "flow": "xtls-rprx-vision", "net": "tcp",
		}},
		{ID: "2", Name: "vmess-ws", Proto: "vmess", Enabled: true, Params: map[string]string{
			"server": "us.example.com", "port": "443", "uuid": "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
			"tls": "1", "sni": "us.example.com", "net": "ws", "path": "/ws?ed=2048", "host": "us.example.com",
			"security": "auto", "alter_id": "0",
		}},
		{ID: "3", Name: "trojan-grpc", Proto: "trojan", Enabled: true, Params: map[string]string{
			"server": "jp.example.com", "port": "443", "password": "secretpw",
			"tls": "1", "sni": "jp.example.com", "net": "grpc", "service_name": "TunService", "alpn": "h2",
		}},
		{ID: "4", Name: "ss-obfs", Proto: "ss", Enabled: true, Params: map[string]string{
			"server": "ss.example.com", "port": "8388", "method": "aes-256-gcm", "password": "sspass",
			"plugin": "obfs-local", "plugin_opts": "obfs=tls;obfs-host=www.bing.com",
		}},
		{ID: "5", Name: "hy2", Proto: "hy2", Enabled: true, Params: map[string]string{
			"server": "hy.example.com", "port": "443", "auth": "hy2pass",
			"up_mbps": "20", "down_mbps": "100", "obfs": "salamander", "obfs_password": "obfspw",
			"sni": "hy.example.com", "insecure": "1",
		}},
		{ID: "6", Name: "tuic", Proto: "tuic", Enabled: true, Params: map[string]string{
			"server": "tuic.example.com", "port": "443", "uuid": "ffffffff-1111-4222-8333-444444444444",
			"password": "tuicpw", "congestion_control": "bbr", "udp_relay_mode": "native",
			"sni": "tuic.example.com", "alpn": "h3",
		}},
		{ID: "7", Name: "wg", Proto: "wireguard", Enabled: true, Params: map[string]string{
			"server": "wg.example.com", "port": "51820", "private_key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			"peer_public_key": "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
			"local_address":   "10.0.0.2/32", "mtu": "1280",
		}},
		{ID: "8", Name: "socks5", Proto: "socks", Enabled: true, Params: map[string]string{
			"server": "socks.example.com", "port": "1080", "username": "u1", "password": "p1",
		}},
		{ID: "9", Name: "ssh", Proto: "ssh", Enabled: true, Params: map[string]string{
			"server": "ssh.example.com", "port": "22", "username": "root", "password": "sshpass",
		}},
		{ID: "10", Name: "hysteria1-unsupported", Proto: "hysteria", Enabled: true, Params: map[string]string{
			"server": "h1.example.com", "port": "443", "auth": "x",
		}},
	}
	a.store.Settings.TransparentProxy = true
	a.store.Settings.ProxyMode = "tun"
	a.store.Settings.BypassDomains = "googlevideo.com, .netflix.com"
	a.store.Settings.BlockAds = true
	a.store.Builder.Groups = []ProxyGroup{
		{ID: "g1", Name: "SG Group", Type: "select", Members: []string{"1"}},
		{ID: "g2", Name: "Auto", Type: "urltest", Members: []string{"2", "3"}},
	}
	a.store.Builder.Rules = []RouteRule{
		{ID: "r1", DomainSuffix: []string{"youtube.com"}, Outbound: "g1"},
		{ID: "r2", IPCIDR: []string{"1.2.3.0/24"}, Port: []int{80, 443}, Outbound: "DIRECT"},
	}
	a.store.Builder.RuleSets = []RuleSet{
		{Tag: "geosite-ads", Type: "remote", URL: "https://example.com/ads.dat"},
	}
	return a
}

func TestDumpSampleConfig(t *testing.T) {
	if *dumpPath == "" {
		t.Skip("pakai -args -dump=<path> untuk dump")
	}
	a := sampleApp()
	yml, err := a.buildConfigYAML()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(*dumpPath, []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("config ditulis ke %s (%d bytes)", *dumpPath, len(yml))
}

func TestGeoNativeRe(t *testing.T) {
	cases := map[string][]string{
		"geoip-id":   {"geoip", "id"},
		"geoip-cn":   {"geoip", "cn"},
		"geosite-cn": {"geosite", "cn"},
		"GEOIP-ID":   {"geoip", "id"},
	}
	for tag, want := range cases {
		m := geoNativeRe.FindStringSubmatch(tag)
		if m == nil {
			t.Errorf("%s: tidak match", tag)
			continue
		}
		if !strings.EqualFold(m[1], want[0]) || !strings.EqualFold(m[2], want[1]) {
			t.Errorf("%s: dapat %v, mau %v", tag, m[1:3], want)
		}
	}
	for _, tag := range []string{"geoip-id.srs", "custom-set", "geosite", "geoip"} {
		if geoNativeRe.MatchString(tag) {
			t.Errorf("%s: seharusnya tidak match", tag)
		}
	}
}

// TestGeoRuleEmission — regresi bug lapangan 2026-10-23: rule-set tag
// "geoip-id" (URL .srs warisan sing-box) diterjemahkan jadi
// "GEOIP,geoip,DIRECT" (kode negara = kata "geoip") sehingga mihomo -t
// gagal "country code geoip not found". Yang benar: GEOIP,id,DIRECT.
// Sekalian: rule gaya sing-box dengan field GeoIP/GeoSite tidak boleh
// dibuang diam-diam di mode clash.
func TestGeoRuleEmission(t *testing.T) {
	a := sampleApp()
	wd := a.mihomoWorkDir()
	for _, n := range []string{"geoip.dat", "geosite.dat"} {
		if err := os.WriteFile(filepath.Join(wd, n), []byte("dummy"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	a.store.Builder.RuleSets = []RuleSet{
		{Tag: "geoip-id", URL: "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-id.srs"},
	}
	a.store.Builder.Rules = []RouteRule{
		{ID: "g1", RuleSets: []string{"geoip-id"}, Outbound: "DIRECT"},
		{ID: "g2", GeoIP: []string{"id"}, Outbound: "DIRECT"},
		{ID: "g3", GeoSite: []string{"youtube"}, Outbound: "Proxy"},
	}
	yml, err := a.buildConfigYAML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(yml, "GEOIP,geoip") || strings.Contains(yml, "GEOSITE,geosite") {
		t.Fatalf("kode geo salah (kata geoip/geosite ikut ter-emit):\n%s", yml)
	}
	if n := strings.Count(yml, "GEOIP,id,DIRECT"); n != 2 {
		t.Fatalf("GEOIP,id,DIRECT harus muncul 2x (tag + field), dapat %d:\n%s", n, yml)
	}
	if !strings.Contains(yml, "GEOSITE,youtube,") {
		t.Fatalf("GEOSITE,youtube tidak ter-emit:\n%s", yml)
	}
}

// TestMihomoGeoIDE2E — kasus persis HP user divalidasi biner mihomo asli
// + geodata bawaan asli. Gate: MIHOMO_BIN (amd64) & aset geodata repo.
func TestMihomoGeoIDE2E(t *testing.T) {
	bin := os.Getenv("MIHOMO_BIN")
	if bin == "" {
		t.Skip("MIHOMO_BIN kosong")
	}
	geoDir := filepath.Join("..", "..", "assets", "geodata")
	for _, n := range []string{"geoip.dat", "geosite.dat"} {
		if _, err := os.Stat(filepath.Join(geoDir, n)); err != nil {
			t.Skipf("geodata aset tidak ada: %v", err)
		}
	}
	a := sampleApp()
	wd := a.mihomoWorkDir()
	for _, n := range []string{"geoip.dat", "geosite.dat"} {
		b, err := os.ReadFile(filepath.Join(geoDir, n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wd, n), b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	a.store.Builder.RuleSets = []RuleSet{
		{Tag: "geoip-id", URL: "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-id.srs"},
	}
	a.store.Builder.Rules = []RouteRule{
		{ID: "g1", RuleSets: []string{"geoip-id"}, Outbound: "DIRECT"},
	}
	yml, err := a.buildConfigYAML()
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(a.autoDir(), "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-t", "-d", wd, "-f", cfg)
	// samakan dengan startMihomo produksi: SAFE_PATHS = webuiDir + dataDir
	cmd.Env = append(os.Environ(), "SAFE_PATHS="+a.webuiDir+string(os.PathListSeparator)+a.dataDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mihomo -t gagal: %v\n%s", err, out)
	}
}
