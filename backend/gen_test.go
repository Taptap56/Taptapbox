// gen_test.go — test generasi config Mihomo TaptapClash.

package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var sampleLinks = []string{
	`vmess://eyJwcyI6InRlc3Qtdm1lc3MiLCJhZGQiOiIxLjIuMy40IiwicG9ydCI6IjQ0MyIsImlkIjoiMTExMTExMTEtMjIyMi0zMzMzLTQ0NDQtNTU1NTU1NTU1NTU1IiwiYWlkIjoiMCIsInNjeSI6ImF1dG8iLCJuZXQiOiJ3cyIsInR5cGUiOiJub25lIiwiaG9zdCI6ImV4LmNvbSIsInBhdGgiOiIvd3MiLCJ0bHMiOiJ0bHMiLCJzbmkiOiJleC5jb20iLCJmcCI6ImNocm9tZSJ9`,
	`vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?encryption=none&security=reality&sni=ex.com&fp=chrome&pbk=ZME1NDead34YBu8dR7GvcIa-ra52Vv2rZi5Xjz-eQic&sid=abcd1234&type=grpc&serviceName=svc#test-vless`,
	`trojan://rahasia@1.2.3.4:443?sni=ex.com&alpn=h2%2Chttp%2F1.1#test-trojan`,
	`ss://YWVzLTEyOC1nY206cGFzc3dvcmQ=@1.2.3.4:8388#test-ss`,
	`hysteria2://rahasia@1.2.3.4:443?obfs=salamander&obfs-password=<redacted>&sni=ex.com#test-hy2`,
	`tuic://11111111-2222-3333-4444-555555555555:cGFzczEyMw@1.2.3.4:443?congestion_control=bbr&udp_relay_mode=native&sni=ex.com&alpn=h3#test-tuic`,
	`wireguard://F8WxjGRHb%2BLXug5XxpBAErZdN7F9rfGenOXlCDN6G0I%3D@1.2.3.4:51820?publickey=siqBh3WouEv%2BCu8r56iRH0AAKeYnx7YfVV5%2BZ2CbH3E%3D&address=10.0.0.2%2F32&mtu=1280#test-wg`,
	`socks://dXNlcjpwYXNz@1.2.3.4:1080#test-socks`,
	`http://dXNlcjpwYXNz@1.2.3.4:8080#test-http`,
}

func testApp(t *testing.T) *App {
	dir := t.TempDir()
	a := &App{dataDir: dir, jsonDir: dir + "/json", logDir: dir + "/log", webuiDir: dir + "/webui"}
	_ = os.MkdirAll(a.jsonDir, 0755)
	_ = os.MkdirAll(a.logDir, 0755)
	_ = os.MkdirAll(a.webuiDir+"/zashboard", 0755)
	a.logger = log.New(os.Stderr, "", 0)
	a.migrateStoreFiles()
	a.store = NewStore(a.autoDir())
	for i, ln := range sampleLinks {
		sv, err := parseLink(ln)
		if err != nil {
			t.Fatalf("link %d gagal parse: %v", i, err)
		}
		sv.ID = newID()
		a.store.Servers = append(a.store.Servers, *sv)
	}
	return a
}

func writeCfg(t *testing.T, name, yml string) string {
	path := "/tmp/" + name
	if err := os.WriteFile(path, []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %d bytes", name, len(yml))
	return path
}

func TestBuildMihomo(t *testing.T) {
	t.Skip("TaptapBox: mihomo tidak ada, hanya sing-box")
}

func TestBuilderRulesMihomo(t *testing.T) {
	a := testApp(t)
	a.store.Builder.RuleSets = []RuleSet{
		{ID: "rs1", Tag: "geosite-id", Type: "remote",
			URL: "https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geosite-id.dat"},
	}
	a.store.Builder.Rules = []RouteRule{
		{ID: "r1", RuleSets: []string{"geosite-id"}, Outbound: "DIRECT"},
		{ID: "r2", DomainSuffix: []string{"ads.example.com"}, Outbound: "REJECT"},
	}
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := cfg["rules"].([]string)
	joined := strings.Join(rules, "\n")
	if !strings.Contains(joined, "RULE-SET,geosite-id,DIRECT") {
		t.Errorf("rule RULE-SET hilang:\n%s", joined)
	}
	if !strings.Contains(joined, "DOMAIN-SUFFIX,ads.example.com,REJECT") {
		t.Errorf("rule DOMAIN-SUFFIX hilang")
	}
	if !strings.HasSuffix(strings.TrimSpace(joined[strings.LastIndex(joined, "MATCH,"):]), "MATCH,Taptap") {
		t.Errorf("rule terakhir harus MATCH grup utama")
	}
	provs, _ := cfg["rule-providers"].(map[string]any)
	if provs["geosite-id"] == nil {
		t.Errorf("rule-provider geosite-id hilang")
	}
	p := provs["geosite-id"].(map[string]any)
	if p["type"] != "http" || p["behavior"] != "domain" {
		t.Errorf("rule-provider salah: %v", p)
	}
}

func TestVmessNumericFields(t *testing.T) {
	raw := `{"v":"2","ps":"n","add":"1.2.3.4","port":443,"id":"11111111-2222-3333-4444-555555555555","aid":0,"net":"ws","tls":"tls"}`
	sv, err := parseLink("vmess://" + b64enc([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if sv.Params["port"] != "443" || sv.Params["alter_id"] != "0" {
		t.Fatalf("param salah: %v", sv.Params)
	}
	px := serverToMihomoProxy(sv, "t")
	if px["network"] != "ws" || px["tls"] != true {
		t.Fatalf("proxy salah: %v", px)
	}
}

func TestExtraProtocols(t *testing.T) {
	links := []string{
		"hysteria2://pw@1.2.3.4:443,5000-6000/?sni=ex.com&insecure=1#hy2-hop",
		"hysteria://1.2.3.4:443?auth=x&peer=ex.com&upmbps=10&downmbps=50#hy1",
		"anytls://pw@1.2.3.4:443?sni=ex.com#anytls",
		"ssh://root:pw@1.2.3.4:22#ssh",
		"ss://YWVzLTEyOC1nY206cGFzcw%3D%3D@1.2.3.4:8388/?plugin=obfs-local%3Bobfs%3Dhttp#ss-plugin",
	}
	for _, ln := range links {
		sv, err := parseLink(ln)
		if err != nil {
			t.Fatalf("%s: %v", ln, err)
		}
		px := serverToMihomoProxy(sv, "t")
		switch sv.Proto {
		case "hysteria", "anytls":
			if px != nil {
				t.Fatalf("%s: protokol tak didukung harus nil", ln)
			}
		default:
			if px == nil {
				t.Fatalf("%s: proxy nil", ln)
			}
		}
	}
	// hy2 hop ports -> "ports"
	sv, _ := parseLink(links[0])
	px := serverToMihomoProxy(sv, "t")
	if px["ports"] == "" {
		t.Fatalf("hy2 ports hilang: %v", px)
	}
	if px["skip-cert-verify"] != true {
		t.Fatalf("hy2 insecure hilang: %v", px)
	}
	// ss plugin-opts jadi map
	sv, _ = parseLink(links[4])
	px = serverToMihomoProxy(sv, "t")
	po, ok := px["plugin-opts"].(map[string]any)
	if !ok || po["mode"] != "http" {
		t.Fatalf("ss plugin-opts salah: %v", px["plugin-opts"])
	}
}

func TestClashFlowYAML(t *testing.T) {
	y := "proxies:\n  - {name: \"a\", type: vmess, server: 1.2.3.4, port: 443, uuid: 11111111-2222-3333-4444-555555555555, alterId: 0, cipher: auto, tls: true, network: ws, ws-opts: {path: /ws, headers: {Host: ex.com}}}\nproxy-groups:\n  - name: x\n"
	svs, _ := parseSubscriptionServers(y)
	if len(svs) != 1 || svs[0].Params["path"] != "/ws" || svs[0].Params["host"] != "ex.com" {
		t.Fatalf("clash flow gagal: %+v", svs)
	}
	px := serverToMihomoProxy(svs[0], "a")
	if px["network"] != "ws" {
		t.Fatalf("roundtrip clash->mihomo gagal: %v", px)
	}
}

func TestCustomGroups(t *testing.T) {
	a := testApp(t)
	first := a.store.Servers[0].ID
	a.store.Builder.Groups = []ProxyGroup{
		{ID: "g1", Name: "Pilihan", Type: "selector", Members: []string{first, "DIRECT"}},
		{ID: "g2", Name: "Filter", Type: "urltest", Filter: "test-(ss|trojan)"},
		{ID: "g3", Name: "Gabungan", Type: "selector", Members: []string{"g1", "g2", "g9"}},
		{ID: "g4", Name: "Kosong", Type: "selector"},
	}
	a.store.Builder.Rules = []RouteRule{{ID: "r1", DomainSuffix: []string{"a.com"}, Outbound: "g3"}}
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]map[string]any{}
	for _, o := range cfg["proxy-groups"].([]any) {
		m := o.(map[string]any)
		have[m["name"].(string)] = m
	}
	for _, tag := range []string{"Pilihan", "Filter", "Gabungan"} {
		if have[tag] == nil {
			t.Fatalf("grup %q tidak ada", tag)
		}
	}
	if have["Kosong"] != nil {
		t.Fatal("grup kosong harus dilewati")
	}
	if have["Filter"]["type"] != "url-test" {
		t.Fatalf("Filter harus url-test, dapat %v", have["Filter"]["type"])
	}
	if n := len(have["Filter"]["proxies"].([]string)); n != 2 {
		t.Fatalf("filter harus cocok 2 server, dapat %d", n)
	}
	rules := cfg["rules"].([]string)
	last := rules[len(rules)-1]
	// MATCH akhir selalu grup utama (pilihan "rute akhir" dihapus).
	if last != "MATCH,Taptap" {
		t.Fatalf("final salah: %v", last)
	}
}

func TestMainGroupRename(t *testing.T) {
	a := testApp(t)
	a.store.Settings.MainGroupName = "Utamaku"
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]map[string]any{}
	for _, o := range cfg["proxy-groups"].([]any) {
		m := o.(map[string]any)
		have[m["name"].(string)] = m
	}
	sel, ok := have["Utamaku"]
	if !ok {
		t.Fatal("grup utama \"Utamaku\" tidak ada")
	}
	if sel["type"] != "select" {
		t.Fatalf("grup utama harus select, dapat %v", sel["type"])
	}
	if have["Taptap"] != nil {
		t.Fatal("tag lama masih ada")
	}
	rules := cfg["rules"].([]string)
	if rules[len(rules)-1] != "MATCH,Utamaku" {
		t.Fatalf("final salah: %v", rules[len(rules)-1])
	}
}

func TestTproxyMode(t *testing.T) {
	a := testApp(t)
	a.store.Settings.ProxyMode = "tproxy"
	a.store.Settings.TransparentProxy = true
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	if cfg["tproxy-port"] != 15359 {
		t.Errorf("tproxy-port = %v, mau 15359", cfg["tproxy-port"])
	}
	if cfg["tun"] != nil || cfg["redir-port"] != nil {
		t.Errorf("mode tproxy tak boleh memuat tun/redir-port")
	}
}

// external_controller ngikutin WebListenAll (bisa dibuka dari LAN/hotspot).
func TestClashAPIBindLAN(t *testing.T) {
	a := testApp(t)
	a.store.Settings.ClashPort = 19090
	a.store.Settings.WebListenAll = true
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	if cfg["external-controller"] != "0.0.0.0:19090" {
		t.Errorf("harusnya 0.0.0.0:19090, dapat %v", cfg["external-controller"])
	}
	a.store.Settings.WebListenAll = false
	cfg, err = a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	if cfg["external-controller"] != "127.0.0.1:19090" {
		t.Errorf("harusnya 127.0.0.1:19090, dapat %v", cfg["external-controller"])
	}
}

// Toggle DNS hijack mengendalikan dns-hijack TUN mihomo
// (netfilter REDIRECT selalu dikendalikan toggle yang sama).
func TestDNSHijackToggleGatesTun(t *testing.T) {
	a := testApp(t)
	a.store.Settings.ProxyMode = "tun"
	a.store.Settings.TransparentProxy = true
	if !a.store.Settings.DNSHijack {
		t.Fatal("default DNSHijack harus true")
	}
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	tun, _ := cfg["tun"].(map[string]any)
	if tun["dns-hijack"] == nil {
		t.Fatal("dns-hijack harus ada saat toggle ON")
	}
	a.store.Settings.DNSHijack = false
	cfg, err = a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	tun, _ = cfg["tun"].(map[string]any)
	if tun["dns-hijack"] != nil {
		t.Fatal("dns-hijack harus hilang saat toggle OFF")
	}
}

// Blok QUIC murni netfilter: tak ada rule REJECT untuknya di config mihomo.
func TestQUICBlockNoMihomoRule(t *testing.T) {
	a := testApp(t)
	a.store.Settings.ProxyMode = "tun"
	a.store.Settings.BlockQUIC = true
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range cfg["rules"].([]string) {
		if strings.Contains(r, "443") && strings.Contains(r, "REJECT") {
			t.Fatalf("rule REJECT 443 tak boleh ada di config mihomo: %s", r)
		}
	}
}

var _ = filepath.Join
