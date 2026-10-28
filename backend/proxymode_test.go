package main

import "testing"

// helper: App minimal dengan ProxyMode tertentu untuk uji buildMihomoMap.
func modeTestApp(mode string) *App {
	a := sampleApp()
	st := a.store.Settings
	st.TransparentProxy = true
	st.ProxyMode = mode
	st.DNSHijack = false
	st.TproxyPort = 15359
	st.RedirectPort = 7891
	a.store.Settings = st
	return a
}

func TestProxyModeMixedConfig(t *testing.T) {
	a := modeTestApp("mixed")
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	if cfg["tproxy-port"] != 15359 {
		t.Fatalf("mixed harus punya tproxy-port, dapat %v", cfg["tproxy-port"])
	}
	if cfg["redir-port"] != 7891 {
		t.Fatalf("mixed harus punya redir-port, dapat %v", cfg["redir-port"])
	}
	if _, ok := cfg["tun"]; ok {
		t.Fatal("mixed tidak boleh mengaktifkan tun")
	}
}

func TestProxyModeEnhanceForcesDNSHijack(t *testing.T) {
	a := modeTestApp("enhance") // DNSHijack=false di helper
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	tun, ok := cfg["tun"].(map[string]any)
	if !ok {
		t.Fatal("enhance harus mengaktifkan tun")
	}
	hj, ok := tun["dns-hijack"].([]string)
	if !ok || len(hj) == 0 {
		t.Fatal("enhance harus memaksa dns-hijack di TUN walau toggle mati")
	}
	if !hardenFull(a.store.Settings) {
		t.Fatal("hardenFull harus true untuk mode enhance")
	}
	if hardenFull(Settings{ProxyMode: "tun"}) {
		t.Fatal("hardenFull harus false untuk mode tun")
	}
}

func TestValidProxyMode(t *testing.T) {
	for _, m := range []string{"tun", "tproxy", "iptables", "mixed", "enhance"} {
		if !validProxyMode(m) {
			t.Fatalf("mode %q harus valid", m)
		}
	}
	for _, m := range []string{"", "TUN", "socks", "hybrid", "ebpf"} {
		if validProxyMode(m) {
			t.Fatalf("mode %q harus ditolak", m)
		}
	}
}

// Mode non-TUN: DNS aplikasi harus lewat DNS fake-ip mihomo + sniffer aktif.
func TestNonTunDNSViaMihomoAndSniffer(t *testing.T) {
	t.Skip("TaptapBox: jalur mihomo tidak ada, hanya sing-box")
	for _, mode := range []string{"tproxy", "iptables", "mixed"} {
		a := modeTestApp(mode)
		cfg, err := a.buildMihomoMap()
		if err != nil {
			t.Fatal(err)
		}
		dns := cfg["dns"].(map[string]any)
		if dns["listen"] != "127.0.0.1:1053" {
			t.Fatalf("%s: dns.listen harus 127.0.0.1:1053, dapat %v", mode, dns["listen"])
		}
		if _, ok := cfg["sniffer"]; !ok {
			t.Fatalf("%s: sniffer harus aktif", mode)
		}
		if cfg["tcp-concurrent"] != true {
			t.Fatalf("%s: tcp-concurrent harus true", mode)
		}
		a.store.Settings.Hotspot = true
		cfg, _ = a.buildMihomoMap()
		if cfg["dns"].(map[string]any)["listen"] != "0.0.0.0:1053" {
			t.Fatalf("%s: hotspot harus listen 0.0.0.0", mode)
		}
	}
	// TUN: tidak perlu dns.listen
	a := modeTestApp("tun")
	cfg, _ := a.buildMihomoMap()
	if _, ok := cfg["dns"].(map[string]any)["listen"]; ok {
		t.Fatal("tun tidak boleh punya dns.listen")
	}
	// toggle Traffic Sniffing mati -> tanpa sniffer
	a.store.Builder.NoSniff = true
	cfg, _ = a.buildMihomoMap()
	if _, ok := cfg["sniffer"]; ok {
		t.Fatal("NoSniff=true harus menghilangkan sniffer")
	}
}
