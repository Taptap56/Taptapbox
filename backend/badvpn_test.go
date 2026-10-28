// badvpn_test.go — header UDP SOCKS5 + generasi config BadVPN.

package main

import (
	"net"
	"strings"
	"testing"
)

func TestSocksUDPParseBuild(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		port int
	}{
		{"203.0.113.7", 27015},
		{"2001:db8::5", 443},
	} {
		pkt := buildSocksUDP(net.ParseIP(tc.ip), tc.port, []byte("data"))
		s, err := parseSocksUDP(pkt)
		if err != nil {
			t.Fatal(err)
		}
		if s.ip.String() != tc.ip || s.port != tc.port || string(s.payload) != "data" || s.frag != 0 {
			t.Fatalf("round-trip salah untuk %s: %+v", tc.ip, s)
		}
	}
	// Domain: parse mengembalikan domain, bukan IP.
	pkt := []byte{0, 0, 0, 0x03, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0x1f, 0x90}
	s, err := parseSocksUDP(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if s.domain != "example" || s.port != 8080 {
		t.Fatalf("domain salah: %+v", s)
	}
	// Fragmentasi ditolak di level parse? Tidak — parse lolos, tapi
	// handleUDPPacket membuangnya (lihat TestBadvpnFragmentDropped).
	if _, err := parseSocksUDP([]byte{0, 0, 1, 0x01, 1, 2, 3, 4, 0, 53}); err != nil {
		t.Fatal(err)
	}
}

func TestBadvpnFragmentDropped(t *testing.T) {
	a := &App{store: &Store{Settings: Settings{}}}
	r := &badvpnRelay{a: a, flows: map[string]uint16{}, rev: map[uint16]bvFlow{}, nextID: 1}
	pkt := buildSocksUDP(net.ParseIP("1.2.3.4"), 1234, []byte("x"))
	pkt[2] = 1 // FRAG != 0
	src, _ := net.ResolveUDPAddr("udp", "127.0.0.1:40000")
	r.handleUDPPacket(pkt, src)
	if len(r.flows) != 0 {
		t.Fatal("paket terfragmentasi harus dibuang, bukan dipetakan")
	}
}

// bvSSHTestServer: server SSH valid untuk test generasi config.
func bvSSHTestServer(a *App) string {
	sv := Server{
		ID: "ssh-1", Name: "Edukasi", Proto: "ssh", Enabled: true,
		Params: map[string]string{"server": "10.9.8.7", "port": "22", "username": "u", "password": "p"},
	}
	a.store.Servers = append(a.store.Servers, sv)
	return sv.ID
}

func setBadvpnRelay(a *App, sshID string) {
	a.bvMu.Lock()
	a.bv = &badvpnRelay{socksPort: 15501, udpPort: 15502, udpgwPort: 7300, sshID: sshID}
	a.bvMu.Unlock()
}

func findBoxOutbound(cfg map[string]any, tag string) map[string]any {
	obs, _ := cfg["outbounds"].([]any)
	for _, o := range obs {
		if m, ok := o.(map[string]any); ok && m["tag"] == tag {
			return m
		}
	}
	return nil
}

func boxRules(cfg map[string]any) []any {
	route, _ := cfg["route"].(map[string]any)
	rules, _ := route["rules"].([]any)
	return rules
}

func ruleHas(rule map[string]any, key string, val any) bool {
	got, ok := rule[key]
	if !ok {
		return false
	}
	switch v := val.(type) {
	case string:
		s, ok := got.(string)
		return ok && s == v
	case []string:
		ss, ok := got.([]string)
		if !ok || len(ss) != len(v) {
			return false
		}
		for i := range v {
			if ss[i] != v[i] {
				return false
			}
		}
		return true
	case []int:
		// JSON-ish: bisa []int atau []any berisi int.
		switch g := got.(type) {
		case []int:
			if len(g) != len(v) {
				return false
			}
			for i := range v {
				if g[i] != v[i] {
					return false
				}
			}
			return true
		}
		return false
	}
	return false
}

func TestBadvpnSingboxGen(t *testing.T) {
	a := testApp(t)
	sshID := bvSSHTestServer(a)
	a.store.Settings.SelectedID = sshID
	a.store.Settings.Core = "singbox"
	a.store.Settings.BadvpnEnabled = true
	setBadvpnRelay(a, sshID)
	cfg := a.buildV2rayng()
	if ob := findBoxOutbound(cfg, badvpnTag); ob == nil {
		t.Fatal("outbound BadVPN tidak ada di config sing-box")
	} else {
		if ob["type"] != "socks" || ob["server_port"] != 15501 {
			t.Fatalf("outbound BadVPN salah: %v", ob)
		}
	}
	rules := boxRules(cfg)
	var tcpIdx, privIdx, udp53Idx, udpIdx = -1, -1, -1, -1
	for i, rr := range rules {
		m, ok := rr.(map[string]any)
		if !ok {
			continue
		}
		if ruleHas(m, "ip_cidr", []string{"127.0.0.1/32"}) && ruleHas(m, "port", []int{7300}) {
			tcpIdx = i
			if m["outbound"] == badvpnTag {
				t.Fatal("rule TCP udpgw tidak boleh ke relay (harus ke SSH)")
			}
		}
		if m["ip_is_private"] == true {
			privIdx = i
		}
		if ruleHas(m, "network", []string{"udp"}) && ruleHas(m, "port", []int{53}) {
			udp53Idx = i
		}
		if ruleHas(m, "network", []string{"udp"}) && m["ip_is_private"] == false && m["outbound"] == badvpnTag {
			udpIdx = i
		}
	}
	if tcpIdx < 0 || privIdx < 0 || udp53Idx < 0 || udpIdx < 0 {
		t.Fatalf("rule BadVPN tidak lengkap (tcp=%d priv=%d udp53=%d udp=%d)", tcpIdx, privIdx, udp53Idx, udpIdx)
	}
	if !(tcpIdx < privIdx) {
		t.Fatal("rule TCP udpgw harus SEBELUM aturan privat->DIRECT")
	}
	if !(udp53Idx < udpIdx) {
		t.Fatal("pengecualian UDP 53 harus sebelum rule UDP->relay")
	}
}

func TestBadvpnMihomoGen(t *testing.T) {
	a := testApp(t)
	sshID := bvSSHTestServer(a)
	a.store.Settings.SelectedID = sshID
	a.store.Settings.Core = "clash"
	a.store.Settings.BadvpnEnabled = true
	setBadvpnRelay(a, sshID)
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	proxies, _ := cfg["proxies"].([]any)
	found := false
	for _, p := range proxies {
		if m, ok := p.(map[string]any); ok && m["name"] == badvpnTag {
			found = true
			if m["type"] != "socks5" || m["udp"] != true {
				t.Fatalf("proxy BadVPN salah: %v", m)
			}
		}
	}
	if !found {
		t.Fatal("proxy BadVPN tidak ada di config mihomo")
	}
	rules, _ := cfg["rules"].([]string)
	if len(rules) == 0 || !strings.HasPrefix(rules[0], "DST-PORT,7300,") {
		t.Fatalf("rule pertama mihomo harus DST-PORT udpgw, dapat: %v", rules[:1])
	}
	udpIdx, matchIdx := -1, -1
	for i, r := range rules {
		if r == "NETWORK,UDP,"+badvpnTag {
			udpIdx = i
		}
		if strings.HasPrefix(r, "MATCH,") {
			matchIdx = i
		}
	}
	if udpIdx < 0 || matchIdx < 0 || !(udpIdx < matchIdx) {
		t.Fatalf("NETWORK,UDP harus sebelum MATCH (udp=%d match=%d)", udpIdx, matchIdx)
	}
	// Grup utama TIDAK boleh berisi proxy BadVPN (tidak bisa dipilih user).
	groups, _ := cfg["proxy-groups"].([]any)
	for _, g := range groups {
		if m, ok := g.(map[string]any); ok {
			if ps, ok := m["proxies"].([]string); ok {
				for _, p := range ps {
					if p == badvpnTag {
						t.Fatal("proxy BadVPN tidak boleh masuk grup utama")
					}
				}
			}
		}
	}
}

func TestBadvpnGenOffMatrix(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		relay   bool
		sshSel  bool
	}{
		{"toggle mati", false, true, true},
		{"relay tidak jalan", true, false, true},
		{"server terpilih bukan SSH", true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			sshID := bvSSHTestServer(a)
			if tc.sshSel {
				a.store.Settings.SelectedID = sshID
			} else {
				// Pilih server vmess pertama dari testApp.
				a.store.Settings.SelectedID = a.store.Servers[0].ID
			}
			a.store.Settings.BadvpnEnabled = tc.enabled
			if tc.relay {
				setBadvpnRelay(a, sshID)
			}
			a.store.Settings.Core = "singbox"
			cfg := a.buildV2rayng()
			if ob := findBoxOutbound(cfg, badvpnTag); ob != nil {
				t.Fatal("outbound BadVPN harus TIDAK ada")
			}
			for _, rr := range boxRules(cfg) {
				if m, ok := rr.(map[string]any); ok && m["outbound"] == badvpnTag {
					t.Fatal("rule ke BadVPN harus TIDAK ada")
				}
			}
			a.store.Settings.Core = "clash"
			mcfg, err := a.buildMihomoMap()
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range mcfg["rules"].([]string) {
				if strings.Contains(r, badvpnTag) {
					t.Fatalf("rule mihomo ke BadVPN harus TIDAK ada: %s", r)
				}
			}
		})
	}
}
