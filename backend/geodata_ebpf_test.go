package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Aturan GEOIP/GEOSITE muncul bila geodata ada & toggle aktif.
func TestGeodataRules(t *testing.T) {
	a := sampleApp()
	// siapkan geodata palsu di workdir (cukup ada; isi tidak dibaca saat generate)
	wd := a.mihomoWorkDir()
	for _, n := range []string{"geoip.dat", "geosite.dat"} {
		if err := os.WriteFile(filepath.Join(wd, n), []byte("fake"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if !a.geodataReady() {
		t.Fatal("geodataReady harus true")
	}
	st := a.store.Settings
	st.TransparentProxy = true
	st.ProxyMode = "tun"
	st.BypassPrivate = true
	st.BlockAds = true
	a.store.Settings = st
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	if cfg["geodata-mode"] != true {
		t.Fatal("geodata-mode harus true")
	}
	rules, ok := cfg["rules"].([]string)
	if !ok {
		t.Fatal("rules harus []string")
	}
	has := func(s string) bool {
		for _, r := range rules {
			if r == s {
				return true
			}
		}
		return false
	}
	if !has("GEOIP,private,DIRECT") {
		t.Fatalf("rules harus ada GEOIP,private,DIRECT: %v", rules)
	}
	if !has("GEOSITE,category-ads-all,REJECT") {
		t.Fatalf("rules harus ada GEOSITE,category-ads-all,REJECT: %v", rules)
	}
}

// Tanpa geodata: tidak ada rule GEOIP/GEOSITE, fallback rule-provider untuk ads.
func TestGeodataMissingFallback(t *testing.T) {
	a := sampleApp()
	// pastikan workdir kosong dari geodata
	wd := a.mihomoWorkDir()
	os.Remove(filepath.Join(wd, "geoip.dat"))
	os.Remove(filepath.Join(wd, "geosite.dat"))
	if a.geodataReady() {
		t.Fatal("geodataReady harus false")
	}
	st := a.store.Settings
	st.TransparentProxy = true
	st.ProxyMode = "tun"
	st.BypassPrivate = true
	st.BlockAds = true
	a.store.Settings = st
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg["geodata-mode"]; ok {
		t.Fatal("geodata-mode tidak boleh ada tanpa file")
	}
	rules := cfg["rules"].([]string)
	for _, r := range rules {
		if r == "GEOIP,private,DIRECT" || r == "GEOSITE,category-ads-all,REJECT" {
			t.Fatalf("rule %q tidak boleh ada tanpa geodata", r)
		}
	}
	has := false
	for _, r := range rules {
		if r == "RULE-SET,geosite-ads,REJECT" {
			has = true
		}
	}
	if !has {
		t.Fatal("harus fallback ke RULE-SET,geosite-ads,REJECT")
	}
}
