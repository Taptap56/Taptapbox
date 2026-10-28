package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// genbox.go — generator config sing-box (port dari TaptapBox config.go).
// Menghasilkan config JSON sing-box dari Store yang sama dengan
// generator mihomo (mihomo.go). Dipakai saat core aktif = singbox.

type remoteNeed struct{ tag, url string }

func (a *App) buildConfig() (map[string]any, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	return a.buildV2rayng(), nil
}

func (a *App) buildV2rayng() map[string]any {
	st := a.store
	built := a.buildServers()
	gOut, gTag, gOrder := a.boxBuildGroups(built)
	tags := []string{}
	obMap := map[string]string{} // serverID -> tag
	for _, b := range built {
		tags = append(tags, b.Tag)
		obMap[b.ID] = b.Tag
	}

	outbounds := []any{}
	// Grup utama (selector): nama bisa diubah user, default "Taptap".
	// Grup otomatis "♻️ Otomatis" dihapus di v1.4.0 — user membuat grup
	// sendiri bila butuh urltest.
	mg := a.mainGroupTag()
	selMembers := []string{}
	selMembers = append(selMembers, tags...)
	selMembers = append(selMembers, gOrder...)
	selMembers = append(selMembers, "DIRECT")
	def := "DIRECT"
	if len(tags) > 0 {
		// belum ada pilihan / server terpilih sudah dihapus: pakai server pertama,
		// jangan diam-diam keluar lewat DIRECT (tanpa proxy)
		def = tags[0]
	}
	if sel := st.findServer(st.Settings.SelectedID); sel != nil {
		if t, ok := obMap[sel.ID]; ok {
			def = t
		}
	}
	outbounds = append(outbounds, map[string]any{
		"type": "selector", "tag": mg,
		"outbounds": selMembers, "default": def,
	})
	endpoints := []any{}
	for _, b := range built {
		if b.Ob != nil {
			outbounds = append(outbounds, b.Ob)
		}
		if b.Ep != nil {
			endpoints = append(endpoints, b.Ep)
		}
	}
	outbounds = append(outbounds, gOut...)
	outbounds = append(outbounds, map[string]any{"type": "direct", "tag": "DIRECT"})
	// BadVPN: outbound SOCKS lokal untuk UDP + tag SSH terpilih.
	// (Aturan-aturannya disisipkan di bawah; tanpa relay/tag, tidak ada
	// aturan yang di-emit — config persis seperti sebelum BadVPN.)
	var bvSocks, bvUdpgw int
	var bvSSH string
	var bvOk bool
	// Gerbang ganda: relay jalan DAN toggle nyala DAN bukan manual.
	// (Relay yatim dari startCore lama tidak boleh membocorkan aturan
	// bila user sudah mematikan toggle tanpa restart.)
	if sp, up, ok := a.badvpnCfg(); ok && st.Settings.BadvpnEnabled && !st.Settings.ManualConfig {
		if tag, ok2 := a.badvpnSelectedSSH(built); ok2 {
			bvSocks, bvUdpgw, bvSSH, bvOk = sp, up, tag, true
			outbounds = append(outbounds, map[string]any{"type": "socks", "tag": badvpnTag, "server": "127.0.0.1", "server_port": bvSocks, "version": "5"})
		}
	}

	bld := a.store.Builder
	rules := []any{}
	if !bld.NoSniff {
		rules = append(rules, sniffRule())
	}
	if !bld.NoHijackDNS && st.Settings.DNSHijack {
		rules = append(rules, map[string]any{"protocol": "dns", "action": "hijack-dns"})
	}
	// Blok QUIC kini di level netfilter (applyQUICBlock, semua mode non-tproxy),
	// bukan lagi route rule — agar user config manual pun kebagian.
	// BadVPN: stream TCP ke udpgw server HARUS ke SSH, bukan DIRECT —
	// 127.0.0.1 adalah IP privat, jadi aturan ini mendahului aturan
	// privat->DIRECT di bawah. Hanya port udpgw persis yang dicocokkan.
	if bvOk {
		rules = append(rules, map[string]any{"network": []string{"tcp"}, "ip_cidr": []string{"127.0.0.1/32"}, "port": []int{bvUdpgw}, "action": "route", "outbound": bvSSH})
	}
	if !bld.NoPrivateDirect {
		rules = append(rules, map[string]any{"ip_is_private": true, "action": "route", "outbound": "DIRECT"})
	}
	// domain bypass: langsung (DIRECT), tidak lewat proxy.
	if bypass := parseDomainList(a.store.Settings.BypassDomains); len(bypass) > 0 {
		rules = append(rules, map[string]any{
			"domain_suffix": bypass, "action": "route", "outbound": "DIRECT",
		})
	}
	// rule-set + rules dari Builder
	ruleSets := []any{}
	have := map[string]bool{}
	// Rule-set remote TIDAK PERNAH di-emit sebagai "remote": unduhan awal yg gagal
	// itu FATAL bagi sing-box (crash-loop). Strateginya: cache-or-skip.
	// - ada cache -> "local" (tanpa unduhan saat start, tak bisa fatal)
	// - belum ada -> dilewati; aturan yg membutuhkannya di-skip otomatis;
	//   pengunduhan hanya dicoba saat apply eksplisit (prefetchRuleSets).
	var needRemote []remoteNeed
	// Blokir iklan: pakai geosite.dat lokal bila ada (tanpa unduhan, seperti
	// TaptapClash); fallback ke rule-set .srs remote bila dat tidak ada.
	geositeDat := a.geodataFile("geosite.dat")
	adsRule := map[string]any{"rule_set": []string{"geosite-ads"}, "action": "reject"}
	if a.store.Settings.BlockAds {
		if geositeDat != "" {
			adsRule = map[string]any{"geosite": "category-ads-all", "action": "reject"}
		} else {
			needRemote = append(needRemote, remoteNeed{"geosite-ads", "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ads-all.srs"})
		}
	}
	for i := range a.store.Builder.RuleSets {
		rs := a.store.Builder.RuleSets[i]
		if rs.Tag == "" || have[rs.Tag] {
			continue
		}
		if rs.Type == "remote" {
			if rs.URL == "" {
				continue
			}
			needRemote = append(needRemote, remoteNeed{rs.Tag, rs.URL})
			continue
		}
		e := map[string]any{"tag": rs.Tag, "type": rs.Type, "format": orDefault(rs.Format, "binary")}
		path := rs.Path
		if path == "" {
			continue
		}
		if strings.HasSuffix(strings.ToLower(path), ".yaml") || strings.HasSuffix(strings.ToLower(path), ".yml") {
			conv, err := a.convertClashRuleYAML(rs.Tag, path)
			if err != nil {
				a.logf("rule-set %q: %v", rs.Tag, err)
				continue
			}
			path = conv
			e["format"] = "source" // hasil konversi berupa JSON source
		} else if strings.HasSuffix(strings.ToLower(path), ".json") {
			e["format"] = "source"
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(a.dataDir, path)
		}
		e["path"] = path
		have[rs.Tag] = true
		ruleSets = append(ruleSets, e)
	}
	_ = a.boxRulesetDir() // pastikan folder ada walau belum ada yg terunduh
	for _, rn := range needRemote {
		if have[rn.tag] {
			continue
		}
		if e, ok := a.cachedRuleSet(rn.tag); ok {
			have[rn.tag] = true
			ruleSets = append(ruleSets, e)
		} else {
			a.logf("rule-set %q dilewati: belum terunduh (coba unduh saat Terapkan)", rn.tag)
		}
	}
	if a.store.Settings.BlockAds && have["geosite-ads"] {
		rules = append(rules, adsRule)
	}
	for _, r := range a.store.Builder.Rules {
		if r.Disabled {
			continue
		}
		// skip aturan tanpa filter apa pun: akan match SEMUA trafik (catch-all)
		if len(r.RuleSets) == 0 && len(r.DomainSuffix) == 0 && len(r.IPCIDR) == 0 &&
			len(r.GeoIP) == 0 && len(r.GeoSite) == 0 &&
			len(r.Port) == 0 && len(r.Protocol) == 0 && len(r.PackageNames) == 0 {
			a.logf("builder: rule %q tanpa filter di-skip (catch-all)", r.ID)
			continue
		}
		ob := r.Outbound
		if ob == "Proxy" || ob == "" {
			ob = mg
		}
		if t, ok := gTag[ob]; ok {
			ob = t // grup custom
		} else if ob != "DIRECT" && ob != "REJECT" && ob != mg {
			ob = mg
		}
		// domain / IP / rule-set dalam satu rule dianggap ATAU (bukan DAN):
		// dipecah jadi beberapa rule sing-box.
		groups := []map[string]any{}
		if len(r.RuleSets) > 0 {
			okSets := []string{}
			for _, t := range r.RuleSets {
				if have[t] {
					okSets = append(okSets, t)
				}
			}
			if len(okSets) > 0 {
				groups = append(groups, map[string]any{"rule_set": okSets})
			}
		}
		if len(r.DomainSuffix) > 0 {
			groups = append(groups, map[string]any{"domain_suffix": r.DomainSuffix})
		}
		if len(r.IPCIDR) > 0 {
			groups = append(groups, map[string]any{"ip_cidr": r.IPCIDR})
		}
		// geoip/geosite memakai database lokal (geodata/) bila ada; bila tidak
		// ada dat-nya, grup di-skip agar tidak error.
		if len(r.GeoIP) > 0 && a.geodataFile("geoip.dat") != "" {
			groups = append(groups, map[string]any{"geoip": r.GeoIP})
		}
		if len(r.GeoSite) > 0 && a.geodataFile("geosite.dat") != "" {
			groups = append(groups, map[string]any{"geosite": r.GeoSite})
		}
		if len(groups) == 0 {
			if len(r.Port) == 0 && len(r.Protocol) == 0 && len(r.PackageNames) == 0 {
				a.logf("builder: rule %q rule-set tidak ditemukan, di-skip", r.ID)
				continue
			}
			groups = append(groups, map[string]any{})
		}
		for _, g := range groups {
			if len(r.Port) > 0 {
				g["port"] = r.Port
			}
			if len(r.Protocol) > 0 {
				g["protocol"] = r.Protocol
			}
			// package_name di-AND dengan tiap grup (seperti port/protokol):
			// "aplikasi X DAN (domain/IP/rule-set Y)" -> outbound.
			if len(r.PackageNames) > 0 {
				g["package_name"] = r.PackageNames
			}
			rules = append(rules, withAction(g, ob))
		}
	}
	// BadVPN: sisa UDP (sesudah aturan user) ke relay lokal. DNS UDP
	// (port 53) dikecualikan agar perilakunya persis seperti hari ini
	// (ditangani DNS core / hijack, bukan dilempar ke udpgw).
	if bvOk {
		rules = append(rules, map[string]any{"network": []string{"udp"}, "port": []int{53}, "action": "route", "outbound": mg})
		rules = append(rules, map[string]any{"network": []string{"udp"}, "ip_is_private": false, "action": "route", "outbound": badvpnTag})
	}
	// Rute akhir SELALU grup utama (satu-satunya final, seperti MATCH di
	// TaptapClash). Aturan custom tanpa outbound eksplisit juga ke grup utama.
	final := mg
	routeCfg := map[string]any{
		"rules":                   rules,
		"rule_set":                ruleSets,
		"final":                   final,
		"auto_detect_interface":   true,
		"default_domain_resolver": map[string]any{"server": "dns-local"},
	}
	// database geoip/geosite lokal (format v2ray, dari module/geodata) —
	// sing-box pakai untuk rule "geoip"/"geosite" tanpa perlu unduhan.
	if p := a.geodataFile("geoip.dat"); p != "" {
		routeCfg["geoip"] = map[string]any{"path": p}
	}
	if p := a.geodataFile("geosite.dat"); p != "" {
		routeCfg["geosite"] = map[string]any{"path": p}
	}
	cfg := map[string]any{
		"log":          map[string]any{"level": a.logLevel()},
		"dns":          a.commonDNS(mg, ""),
		"inbounds":     a.commonInbounds(),
		"outbounds":    outbounds,
		"route":        routeCfg,
		"experimental": a.commonExperimental(),
	}
	if len(endpoints) > 0 {
		cfg["endpoints"] = endpoints
	}
	return cfg
}

// cachedRuleSet: rule-set yg sudah terunduh -> entri "local" (sing-box tak
// mengunduh apa pun saat start, jadi tak ada kegagalan FATAL).
func (a *App) cachedRuleSet(tag string) (map[string]any, bool) {
	p := filepath.Join(a.boxRulesetDir(), tag+".srs")
	if st, err := os.Stat(p); err == nil && st.Size() > 0 {
		return map[string]any{"tag": tag, "type": "local", "format": "binary", "path": p}, true
	}
	return nil, false
}

func (a *App) commonDNS(detourProxy, detourDirect string) map[string]any {
	st := a.store.Settings
	servers := []any{
		dnsServerObject("dns-remote", st.DnsRemote, detourProxy),
		dnsServerObject("dns-direct", st.DnsDirect, detourDirect),
		dnsServerObject("dns-local", st.DnsDirect, ""),
	}
	final := "dns-local"
	rules := []any{}
	// Mode TUN & TProxy: semua DNS aplikasi lewat sing-box.
	// Default via remote (terproteksi tunnel), domain bypass via direct.
	if (st.ProxyMode == "tun" || st.ProxyMode == "tproxy") && st.TransparentProxy {
		final = "dns-remote"
		if bypass := parseDomainList(st.BypassDomains); len(bypass) > 0 {
			rules = append(rules, map[string]any{
				"domain_suffix": bypass, "server": "dns-direct",
			})
		}
	}
	return map[string]any{
		"servers":  servers,
		"rules":    rules,
		"final":    final,
		"strategy": "prefer_ipv4",
	}
}

// ---------- bagian umum ----------
func (a *App) commonExperimental() map[string]any {
	// Clash API dibuka ke LAN/hotspot bila WebUI juga dibuka ke LAN,
	// supaya dashboard (zashboard) bisa diakses dari HP/PC lain.
	clashHost := "127.0.0.1"
	if a.store.Settings.WebListenAll {
		clashHost = "0.0.0.0"
	}
	return map[string]any{
		"clash_api": map[string]any{
			"external_controller": clashHost + ":" + itoa(a.store.Settings.ClashPort),
			"external_ui":         a.zashboardDir(),
			"secret":              a.store.Settings.Secret,
		},
		"cache_file": map[string]any{
			"enabled": true,
			"path":    filepath.Join(a.boxAutoDir(), "cache.db"),
		},
	}
}

func (a *App) commonInbounds() []any {
	st := a.store.Settings
	inb := []any{
		map[string]any{
			"type": "mixed", "tag": "mixed-in",
			"listen": "127.0.0.1", "listen_port": st.MixedPort,
		},
	}
	if !st.TransparentProxy {
		return inb
	}
	// Mode TUN (ala TaptapBox): tangkap SEMUA trafik (TCP+UDP) via tun0.
	// Mode TProxy: tangkap TCP+UDP via iptables TPROXY (tanpa tun0).
	// Mode iptables: REDIRECT klasik untuk TCP 80,443.
	if st.ProxyMode == "tun" {
		mtu := st.TunMTU
		if mtu <= 0 {
			mtu = 9000
		}
		tun := map[string]any{
			"type": "tun", "tag": "tun-in",
			"interface_name":           "tun0",
			"address":                  "172.19.0.1/30",
			"mtu":                      mtu,
			"auto_route":               true,
			"strict_route":             true,
			"stack":                    "system",
			"endpoint_independent_nat": true,
		}
		if pkgs := parseDomainList(st.ExcludePackages); len(pkgs) > 0 {
			// binary root (non-APK) tidak punya PackageManager: resolve ke UID
			// lewat packages.list; fallback ke exclude_package bila gagal.
			if uids := packageUIDs(pkgs); len(uids) > 0 {
				tun["exclude_uid"] = uids
			} else {
				tun["exclude_package"] = pkgs
			}
		}
		inb = append(inb, tun)
	} else if st.ProxyMode == "tproxy" {
		port := st.TproxyPort
		if port <= 0 {
			port = 15359
		}
		inb = append(inb, map[string]any{
			"type": "tproxy", "tag": "tproxy-in",
			"listen": "::", "listen_port": port,
		})
	} else {
		inb = append(inb, map[string]any{
			"type": "redirect", "tag": "redirect-in",
			"listen": "127.0.0.1", "listen_port": st.RedirectPort,
		})
	}
	return inb
}

func (a *App) convertClashRuleYAML(tag, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.dataDir, path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	ds, d, dk, ipc := []string{}, []string{}, []string{}, []string{}
	for _, ln := range strings.Split(string(body), "\n") {
		ln = strings.TrimSpace(ln)
		ln = strings.TrimPrefix(ln, "- ")
		ln = strings.TrimPrefix(ln, "-")
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") || !strings.Contains(ln, ",") {
			continue
		}
		kv := strings.SplitN(ln, ",", 2)
		typ := strings.ToUpper(strings.TrimSpace(kv[0]))
		val := strings.TrimSpace(kv[1])
		switch typ {
		case "DOMAIN-SUFFIX":
			ds = append(ds, val)
		case "DOMAIN":
			d = append(d, val)
		case "DOMAIN-KEYWORD":
			dk = append(dk, val)
		case "IP-CIDR", "IP-CIDR6":
			ipc = append(ipc, val)
		}
	}
	rule := map[string]any{}
	if len(ds) > 0 {
		rule["domain_suffix"] = ds
	}
	if len(d) > 0 {
		rule["domain"] = d
	}
	if len(dk) > 0 {
		rule["domain_keyword"] = dk
	}
	if len(ipc) > 0 {
		rule["ip_cidr"] = ipc
	}
	src := map[string]any{"version": 3, "rules": []any{rule}}
	out := filepath.Join(a.boxRulesetDir(), tag+".json")
	data, _ := json.MarshalIndent(src, "", "  ")
	if err := os.WriteFile(out, data, 0644); err != nil {
		return "", err
	}
	return out, nil
}

// geodataFile: path absolut ke file geodata (geoip.dat/geosite.dat format v2ray)
// di /data/adb/taptapbox/geodata/. "" bila tidak ada.
func (a *App) geodataFile(name string) string {
	p := filepath.Join(a.dataDir, "geodata", name)
	if st, err := os.Stat(p); err == nil && st.Size() > 0 {
		return p
	}
	return ""
}

// logLevel: level log sing-box dari Pengaturan (default warn).
func (a *App) logLevel() string {
	switch lv := a.store.Settings.LogLevel; lv {
	case "trace", "debug", "info", "warn", "error":
		return lv
	}
	return "warn"
}

// neededRemoteRuleSets: semua rule-set remote yg dibutuhkan config saat ini.
// geosite-ads hanya dibutuhkan bila geosite.dat TIDAK ada: generator memakai
// aturan geosite inline dari dat lokal bila ada. v1.0.45: prefetch lama
// mengunduhnya tanpa syarat, jadi file itu selalu muncul tiap Terapkan
// walau tidak pernah dipakai (laporan user 12:15).
func (a *App) neededRemoteRuleSets() []remoteNeed {
	var out []remoteNeed
	if a.store.Settings.BlockAds && a.geodataFile("geosite.dat") == "" {
		out = append(out, remoteNeed{"geosite-ads", "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ads-all.srs"})
	}
	for _, rs := range a.store.Builder.RuleSets {
		if rs.Type == "remote" && rs.Tag != "" && rs.URL != "" {
			dup := false
			for _, o := range out {
				if o.tag == rs.Tag {
					dup = true
					break
				}
			}
			if !dup {
				out = append(out, remoteNeed{rs.Tag, rs.URL})
			}
		}
	}
	return out
}

// ---------- mode v2rayng ----------
// routeAction membuat aksi rule: REJECT -> action reject (pengganti outbound
// block yang dihapus di sing-box 1.13), lainnya -> route ke outbound.
func routeAction(ob string) map[string]any {
	if ob == "REJECT" {
		return map[string]any{"action": "reject"}
	}
	return map[string]any{"action": "route", "outbound": ob}
}

// boxRulesetDir / clashRulesetDir: cache rule-set PER CORE di folder
// user otomatis masing-masing (keputusan user 2026-10-24: tiap core
// mandiri seperti mode manual yg rule-setnya menyertai config-nya) —
// .srs/.json sing-box di singbox/user otomatis/rulesets, .dat provider
// mihomo di clash/user otomatis/rulesets (path absolut di config;
// lolos cek SAFE_PATHS karena di bawah dataDir). Selalu dipastikan ada
// agar user bisa menaruh file manual bila unduhan gagal. Cache lama
// (folder bersama config/_rulesets dkk.) dipindah sekali jalan oleh
// migrateRulesetDir.
func (a *App) boxRulesetDir() string {
	p := filepath.Join(a.boxAutoDir(), "rulesets")
	_ = os.MkdirAll(p, 0755)
	return p
}

func (a *App) clashRulesetDir() string {
	p := filepath.Join(a.autoDir(), "rulesets")
	_ = os.MkdirAll(p, 0755)
	return p
}

// singboxTest: validasi config dengan library sing-box (box.New + Close,
// sama seperti sing-box check) — tanpa biner sing-box terpisah.
func (a *App) singboxTest(path string) error {
	return a.checkSingboxConfigFile(path)
}

func sniffRule() map[string]any {
	// 300ms = default resmi sing-box. 1s membuat koneksi yang klien-nya tidak kirim data
	// duluan (mis. SMTP/IMAP/SSH/protokol server-first) tertahan 1 detik sebelum jalan.
	return map[string]any{"action": "sniff", "timeout": "300ms"}
}

func withAction(m map[string]any, ob string) map[string]any {
	for k, v := range routeAction(ob) {
		m[k] = v
	}
	return m
}

// rebaseOverride: segarkan bagian-bagian config yang digenerate dari builder
// (outbounds, endpoints, route.rules, route.final) di dalam file override,
// tanpa menyentuh editan manual user di bagian lain (dns, inbounds,
// experimental, log, dsb). route.rule_set digabung: rule-set hasil generate
// menimpa tag yang sama, rule-set manual user dipertahankan.
//
// Dipanggil setelah builder/settings/langganan berubah selagi override aktif,
// agar edit grup (dan rute/server) langsung berlaku tanpa harus "kembali ke
// otomatis" dulu. Hasil selalu divalidasi `sing-box check`; file lama tidak
// diubah bila rebase gagal. No-op bila tidak ada override.
func (a *App) rebaseOverrideJSON() error {
	if !a.boxHasOverride() {
		return nil
	}
	data, err := os.ReadFile(a.boxOverridePath())
	if err != nil {
		return err
	}
	var ov map[string]any
	if err := json.Unmarshal(data, &ov); err != nil {
		return fmt.Errorf("override tidak bisa diurai: %v", err)
	}
	base, err := a.buildConfig()
	if err != nil {
		return err
	}
	// Bagian yang 100% milik builder: timpa penuh dari hasil generate terbaru.
	for _, k := range []string{"outbounds", "endpoints"} {
		if v, ok := base[k]; ok {
			ov[k] = v
		} else {
			delete(ov, k)
		}
	}
	// route.rules & route.final mengikuti builder; rule_set digabung per tag.
	baseRoute, _ := base["route"].(map[string]any)
	ovRoute, ok := ov["route"].(map[string]any)
	if !ok || ovRoute == nil {
		ovRoute = map[string]any{}
		ov["route"] = ovRoute
	}
	if baseRoute != nil {
		if v, ok := baseRoute["rules"]; ok {
			ovRoute["rules"] = v
		}
		if v, ok := baseRoute["final"]; ok {
			ovRoute["final"] = v
		}
		// Gabung rule_set: tag hasil generate menimpa, tag manual user tetap.
		if bRS, ok := baseRoute["rule_set"].([]any); ok && len(bRS) > 0 {
			merged := map[string]any{}
			order := []string{}
			if oRS, ok := ovRoute["rule_set"].([]any); ok {
				for _, item := range oRS {
					if m, ok := item.(map[string]any); ok {
						if tag, ok := m["tag"].(string); ok && tag != "" {
							if _, seen := merged[tag]; !seen {
								order = append(order, tag)
							}
							merged[tag] = item
						}
					}
				}
			}
			for _, item := range bRS {
				if m, ok := item.(map[string]any); ok {
					if tag, ok := m["tag"].(string); ok && tag != "" {
						if _, seen := merged[tag]; !seen {
							order = append(order, tag)
						}
						merged[tag] = item
					}
				}
			}
			out := make([]any, 0, len(order))
			for _, tag := range order {
				out = append(out, merged[tag])
			}
			ovRoute["rule_set"] = out
		}
	}
	pretty, err := json.MarshalIndent(ov, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(a.boxAutoDir(), "config.override.rebase.json")
	if err := os.WriteFile(tmp, pretty, 0600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := a.singboxTest(tmp); err != nil {
		return fmt.Errorf("hasil rebase tidak valid: %v", err)
	}
	if err := os.WriteFile(a.boxOverridePath(), pretty, 0644); err != nil {
		return err
	}
	a.logf("override: direbase dari builder terbaru")
	return nil
}

// overridePath: file config override untuk mode otomatis. Bila ada, isinya
// dipakai sebagai config aktif (setelah divalidasi) menggantikan hasil
// generate — tanpa pindah ke mode manual penuh.
func (a *App) boxOverridePath() string { return filepath.Join(a.boxAutoDir(), "config.override.json") }

// hasOverride: true bila mode otomatis sedang memakai config override.
func (a *App) boxHasOverride() bool {
	if a.store.Settings.ManualConfig {
		return false
	}
	st, err := os.Stat(a.boxOverridePath())
	return err == nil && st.Size() > 0
}

// serverToOutbound mengubah Server menjadi outbound sing-box (map JSON).
// WireGuard dikembalikan sebagai endpoint (ob=nil, ep=terisi) karena
// outbound wireguard sudah deprecated di sing-box 1.11+.
func serverToOutbound(sv *Server, tag string) (ob map[string]any, ep map[string]any) {
	p := sv.Params
	port := atoi(p["port"], defaultPort(sv.Proto))
	ob = map[string]any{
		"tag":         tag,
		"server":      p["server"],
		"server_port": port,
	}
	// SSH payload: core bicara SSH polos ke jembatan lokal, BUKAN ke
	// server jauh. TLS/transport core dimatikan — TLS dan WS dipegang
	// jembatan (sshbridge.go), bukan core.
	_, isBridge := sshPayloadProfile(sv)
	if isBridge {
		ob["server"] = "127.0.0.1"
		ob["server_port"] = sshBridgePort
	} else {
		if tls := tlsObject(p); tls != nil {
			ob["tls"] = tls
		}
		if tr := transportObject(p); tr != nil {
			ob["transport"] = tr
		}
	}
	switch sv.Proto {
	case "vmess":
		ob["type"] = "vmess"
		ob["uuid"] = p["uuid"]
		ob["security"] = orDefault(p["security"], "auto")
		ob["alter_id"] = atoi(p["alter_id"], 0)
		if pe := p["packet_encoding"]; pe != "" {
			ob["packet_encoding"] = pe
		} else {
			ob["packet_encoding"] = "xudp"
		}
	case "vless":
		ob["type"] = "vless"
		ob["uuid"] = p["uuid"]
		if f := p["flow"]; f != "" {
			ob["flow"] = f
		}
		if pe := p["packet_encoding"]; pe != "" {
			ob["packet_encoding"] = pe
		} else {
			ob["packet_encoding"] = "xudp"
		}
	case "trojan":
		ob["type"] = "trojan"
		ob["password"] = p["password"]
	case "ss":
		ob["type"] = "shadowsocks"
		ob["method"] = p["method"]
		ob["password"] = p["password"]
		if pl := strings.ToLower(p["plugin"]); pl != "" {
			if pl == "simple-obfs" {
				pl = "obfs-local"
			}
			if pl == "obfs-local" || pl == "v2ray-plugin" {
				ob["plugin"] = pl
				if po := p["plugin_opts"]; po != "" {
					ob["plugin_opts"] = po
				}
			}
		}
	case "hy2":
		ob["type"] = "hysteria2"
		if up := atoi(p["up_mbps"], 0); up > 0 {
			ob["up_mbps"] = up
		}
		if down := atoi(p["down_mbps"], 0); down > 0 {
			ob["down_mbps"] = down
		}
		ob["password"] = p["auth"]
		if o := p["obfs"]; o != "" {
			ob["obfs"] = map[string]any{"type": o, "password": p["obfs_password"]}
		}
		if sp := normalizePorts(p["server_ports"]); sp != "" {
			ob["server_ports"] = strings.Split(sp, ",")
		}
	case "hysteria":
		ob["type"] = "hysteria"
		ob["up_mbps"] = atoi(p["up_mbps"], 20)
		ob["down_mbps"] = atoi(p["down_mbps"], 100)
		if au := p["auth"]; au != "" {
			ob["auth_str"] = au
		}
		if o := p["obfs"]; o != "" {
			ob["obfs"] = o
		}
	case "anytls":
		ob["type"] = "anytls"
		ob["password"] = p["password"]
	case "ssh":
		ob["type"] = "ssh"
		ob["user"] = orDefault(p["username"], "root")
		if pw := p["password"]; pw != "" {
			ob["password"] = pw
		}
		if pk := p["private_key"]; pk != "" {
			ob["private_key"] = pk
		}
		// Perbaikan timeout intermiten sing-box (2026-10-27, Teskan):
		// core sing-box kadang Timeout kadang konek, mihomo mudah konek.
		// Dial jembatan (proxy + payload + deteksi framing) bisa makan
		// beberapa detik, sementara timeout default core pendek, jadi
		// core menutup koneksi di tengah dial (pola log: 21 byte banner
		// dari core, 0 byte dari server). connect_timeout 20s memberi
		// core waktu menunggu dial jembatan selesai. Ini BUKAN
		// kex_algorithm/cipher/mac — negosiasi KEX sudah terbukti
		// sukses dengan irisan yang ada (Dropbear 2019.78 vs sing-box).
		ob["connect_timeout"] = "20s"
		// client_version tetap, bukan acak sing-box: versi acak adalah
		// variabel yang tidak perlu di tiap dial, dan OpenSSH_7.8 ini
		// adalah versi sesi sukses 2m20s (Dropbear 2019.78, log KEX
		// 2026-10-27 03:43). Server lain umumnya menerima string versi
		// apa pun, jadi mengunci satu versi yang terbukti tidak merusak
		// SSH polos maupun lewat jembatan.
		ob["client_version"] = "SSH-2.0-OpenSSH_7.8"
	case "tuic":
		ob["type"] = "tuic"
		ob["uuid"] = p["uuid"]
		ob["password"] = p["password"]
		if cc := p["congestion_control"]; cc != "" {
			ob["congestion_control"] = cc
		}
		if urm := p["udp_relay_mode"]; urm != "" {
			ob["udp_relay_mode"] = urm
		}
	case "wireguard":
		return nil, wireguardEndpoint(sv, tag)
	case "socks":
		ob["type"] = "socks"
		ob["version"] = "5"
		if u := p["username"]; u != "" {
			ob["username"] = u
			ob["password"] = p["password"]
		}
	case "http":
		ob["type"] = "http"
		if u := p["username"]; u != "" {
			ob["username"] = u
			ob["password"] = p["password"]
		}
	default:
		return nil, nil
	}
	return ob, nil
}

// splitEarlyData memisahkan "?ed=2048" dari path websocket (early data).
// tlsObject membangun objek tls sing-box dari params.
func tlsObject(p map[string]string) map[string]any {
	if p["tls"] != "1" {
		return nil
	}
	t := map[string]any{"enabled": true}
	if sni := p["sni"]; sni != "" {
		t["server_name"] = sni
	}
	if p["insecure"] == "1" || strings.EqualFold(p["insecure"], "true") {
		t["insecure"] = true
	}
	if alpn := p["alpn"]; alpn != "" {
		t["alpn"] = strings.Split(alpn, ",")
	}
	if fp := p["fp"]; fp != "" {
		t["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	if p["reality"] == "1" {
		// reality WAJIB uTLS; tanpa fp sing-box menolak config
		if _, ok := t["utls"]; !ok {
			t["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
		}
		t["reality"] = map[string]any{
			"enabled":    true,
			"public_key": p["pbk"],
			"short_id":   p["sid"],
		}
	}
	return t
}

// transportObject membangun objek transport (ws/grpc/http/httpupgrade).
func transportObject(p map[string]string) map[string]any {
	switch strings.ToLower(p["net"]) {
	case "ws", "websocket":
		ws := map[string]any{"type": "ws"}
		if path := p["path"]; path != "" {
			path, ed := splitEarlyData(path)
			ws["path"] = path
			if ed > 0 {
				ws["max_early_data"] = ed
				ws["early_data_header_name"] = "Sec-WebSocket-Protocol"
			}
		}
		if host := p["host"]; host != "" {
			ws["headers"] = map[string]string{"Host": host}
		}
		return ws
	case "grpc", "gun":
		g := map[string]any{"type": "grpc"}
		if sn := p["service_name"]; sn != "" {
			g["service_name"] = sn
		} else if path := p["path"]; path != "" {
			g["service_name"] = strings.Trim(path, "/")
		}
		return g
	case "http", "h2":
		h := map[string]any{"type": "http"}
		if host := p["host"]; host != "" {
			h["host"] = strings.Split(host, ",")
		}
		if path := p["path"]; path != "" {
			h["path"] = path
		}
		if m := p["method"]; m != "" {
			h["method"] = m
		}
		return h
	case "quic":
		return map[string]any{"type": "quic"}
	case "httpupgrade":
		h := map[string]any{"type": "httpupgrade"}
		if host := p["host"]; host != "" {
			h["host"] = host
		}
		if path := p["path"]; path != "" {
			h["path"] = path
		}
		return h
	}
	// tcp dengan header http obfuscation (vmess type=http)
	if strings.ToLower(p["header_type"]) == "http" {
		h := map[string]any{"type": "http"}
		if host := p["host"]; host != "" {
			h["host"] = strings.Split(host, ",")
		}
		if path := p["path"]; path != "" {
			h["path"] = path
		}
		return h
	}
	return nil
}

// wireguardEndpoint membangun endpoint wireguard sing-box 1.11+.
// Field ob sengaja dikosongkan: tag tetap dipakai di selector/route seperti outbound biasa.
func wireguardEndpoint(sv *Server, tag string) map[string]any {
	p := sv.Params
	addrs := []string{}
	for _, a := range strings.Split(p["local_address"], ",") {
		a = strings.TrimSpace(a)
		if a != "" {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		addrs = []string{"172.16.0.2/32"}
	}
	peer := map[string]any{
		"address":     p["server"],
		"port":        atoi(p["port"], 51820),
		"public_key":  p["peer_public_key"],
		"allowed_ips": []string{"0.0.0.0/0"},
	}
	if psk := p["pre_shared_key"]; psk != "" {
		peer["pre_shared_key"] = psk
	}
	if r := strings.TrimSpace(p["reserved"]); r != "" {
		res := []int{}
		for _, x := range strings.Split(r, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
				res = append(res, n)
			}
		}
		if len(res) == 3 {
			peer["reserved"] = res
		}
	}
	ep := map[string]any{
		"type":        "wireguard",
		"tag":         tag,
		"address":     addrs,
		"private_key": p["private_key"],
		"peers":       []any{peer},
	}
	if mtu := atoi(p["mtu"], 0); mtu > 0 {
		ep["mtu"] = mtu
	}
	return ep
}

// buildGroups membuat outbound grup custom. Mengembalikan outbound, peta
// ID grup -> tag (hanya grup yang punya anggota) dan urutan tag.
// Grup hanya boleh mengacu grup yang ada di atasnya (mencegah siklus).
// Pemanggil harus memegang store.mu (read).
func (a *App) boxBuildGroups(built []builtServer) ([]any, map[string]string, []string) {
	groups := a.store.Builder.Groups
	tags := a.groupTags(groups)
	srvTag := map[string]string{}
	for _, b := range built {
		srvTag[b.ID] = b.Tag
	}
	done := map[string]string{}
	out := []any{}
	order := []string{}
	for i, g := range groups {
		members := []string{}
		seen := map[string]bool{}
		add := func(t string) {
			if t != "" && !seen[t] {
				seen[t] = true
				members = append(members, t)
			}
		}
		for _, m := range g.Members {
			if m == "DIRECT" {
				add("DIRECT")
			} else if t, ok := srvTag[m]; ok {
				add(t)
			} else if t, ok := done[m]; ok {
				add(t)
			}
		}
		if len(g.SubIDs) > 0 || strings.TrimSpace(g.Filter) != "" {
			match := compileFilter(g.Filter)
			for k := range a.store.Servers {
				sv := &a.store.Servers[k]
				t, ok := srvTag[sv.ID]
				if !ok {
					continue
				}
				if len(g.SubIDs) > 0 {
					key := sv.SubID
					if key == "" {
						key = "manual"
					}
					if !containsStr(g.SubIDs, key) {
						continue
					}
				}
				if match != nil && !match(sv.Name) {
					continue
				}
				add(t)
			}
		}
		if g.Direct {
			add("DIRECT")
		}
		if len(members) == 0 {
			a.logf("grup %q dilewati: tidak ada anggota", g.Name)
			continue
		}
		tag := tags[i]
		ob := map[string]any{"tag": tag, "outbounds": members}
		if g.Type == "urltest" {
			ob["type"] = "urltest"
			ob["url"] = a.healthURL()
			ob["interval"] = "10m"
			ob["tolerance"] = 50
		} else {
			ob["type"] = "selector"
			ob["default"] = members[0]
		}
		out = append(out, ob)
		done[g.ID] = tag
		order = append(order, tag)
	}
	return out, done, order
}

// outboundToParams mengubah outbound sing-box (dari config.json) kembali
// menjadi Params ala v2rayNG — kebalikan serverToOutbound.
// Dipakai Builder agar config.json bisa dibaca jadi form edit v2rayNG.
func outboundToParams(ob map[string]any) (proto string, p map[string]string) {
	p = map[string]string{}
	typ, _ := ob["type"].(string)
	proto = typ
	str := func(k string) string {
		if v, ok := ob[k].(string); ok {
			return v
		}
		return ""
	}
	if s := str("server"); s != "" {
		p["server"] = s
	}
	if v, ok := ob["server_port"].(float64); ok {
		p["port"] = fmt.Sprintf("%.0f", v)
	}
	if v := str("uuid"); v != "" {
		p["uuid"] = v
	}
	if v := str("password"); v != "" {
		p["password"] = v
	}
	if v := str("security"); v != "" {
		p["security"] = v
	}
	if v, ok := ob["alter_id"].(float64); ok {
		p["alter_id"] = fmt.Sprintf("%.0f", v)
	}
	if v := str("flow"); v != "" {
		p["flow"] = v
	}
	if v := str("method"); v != "" {
		p["method"] = v
	}
	if v := str("username"); v != "" {
		p["username"] = v
	}
	// TLS
	if t, ok := ob["tls"].(map[string]any); ok {
		if en, _ := t["enabled"].(bool); en {
			p["tls"] = "1"
			if s, ok := t["server_name"].(string); ok {
				p["sni"] = s
			}
			if iv, _ := t["insecure"].(bool); iv {
				p["insecure"] = "1"
			}
			if alpn, ok := t["alpn"].([]any); ok {
				ss := []string{}
				for _, a := range alpn {
					if s, ok := a.(string); ok {
						ss = append(ss, s)
					}
				}
				p["alpn"] = strings.Join(ss, ",")
			}
			if u, ok := t["utls"].(map[string]any); ok {
				if fp, ok := u["fingerprint"].(string); ok {
					p["fp"] = fp
				}
			}
			if r, ok := t["reality"].(map[string]any); ok {
				if en, _ := r["enabled"].(bool); en {
					p["reality"] = "1"
					if s, ok := r["public_key"].(string); ok {
						p["pbk"] = s
					}
					if s, ok := r["short_id"].(string); ok {
						p["sid"] = s
					}
				}
			}
		}
	}
	// transport
	if tr, ok := ob["transport"].(map[string]any); ok {
		tt, _ := tr["type"].(string)
		p["net"] = tt
		if s, ok := tr["path"].(string); ok {
			p["path"] = s
		}
		if s, ok := tr["service_name"].(string); ok {
			p["service_name"] = s
		}
		switch tt {
		case "ws":
			if h, ok := tr["headers"].(map[string]any); ok {
				if s, ok := h["Host"].(string); ok {
					p["host"] = s
				}
			}
		case "http":
			if hs, ok := tr["host"].([]any); ok {
				ss := []string{}
				for _, hv := range hs {
					if s, ok := hv.(string); ok {
						ss = append(ss, s)
					}
				}
				p["host"] = strings.Join(ss, ",")
			}
			if m, ok := tr["method"].(string); ok {
				p["method"] = m
			}
		case "httpupgrade":
			if s, ok := tr["host"].(string); ok {
				p["host"] = s
			}
		}
	}
	return proto, p
}

func dnsServerObject(tag, addr, detour string) map[string]any {
	addr = strings.TrimSpace(addr)
	o := map[string]any{"tag": tag}
	if detour != "" {
		o["detour"] = detour
	}
	lower := strings.ToLower(addr)
	hostOf := func(rest string) string { return strings.Split(rest, "/")[0] }
	switch {
	case strings.HasPrefix(lower, "https://"):
		o["type"] = "https"
		hn := hostOf(addr[len("https://"):])
		if u, err := url.Parse(addr); err == nil && u.Hostname() != "" {
			hn = u.Hostname()
			if n, e := strconv.Atoi(u.Port()); e == nil && n > 0 && n != 443 {
				o["server_port"] = n
			}
			if u.Path != "" && u.Path != "/" && u.Path != "/dns-query" {
				o["path"] = u.Path
			}
		}
		o["server"] = hn
		o["tls"] = map[string]any{"enabled": true, "server_name": hn}
	case strings.HasPrefix(lower, "tls://"):
		o["type"] = "tls"
		setDNSAddr(o, hostOf(addr[len("tls://"):]), 853)
		o["tls"] = map[string]any{"enabled": true, "server_name": o["server"]}
	case strings.HasPrefix(lower, "tcp://"):
		o["type"] = "tcp"
		setDNSAddr(o, hostOf(addr[len("tcp://"):]), 53)
	case strings.HasPrefix(lower, "quic://"):
		o["type"] = "quic"
		setDNSAddr(o, hostOf(addr[len("quic://"):]), 853)
	default:
		o["type"] = "udp"
		setDNSAddr(o, hostOf(strings.TrimPrefix(addr, "udp://")), 53)
	}
	return o
}

// applyOverrideConfigJSON: padanan sing-box dari applyOverrideConfig (YAML).
func (a *App) applyOverrideConfigJSON(raw json.RawMessage) error {
	path := a.boxOverridePath()
	if len(bytes.TrimSpace(raw)) == 0 {
		_ = os.Remove(path)
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("JSON tidak valid: %v", err)
	}
	pretty, _ := json.MarshalIndent(v, "", "  ")
	_ = os.MkdirAll(a.boxAutoDir(), 0755)
	tmp := filepath.Join(a.boxAutoDir(), "config.override.check.json")
	if err := os.WriteFile(tmp, pretty, 0600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := a.singboxTest(tmp); err != nil {
		return err
	}
	return os.WriteFile(path, pretty, 0644)
}

// ---------- jalur runtime sing-box (port dari TaptapBox) ----------

func (a *App) boxActiveConfigPath() string {
	if a.store.Settings.ManualConfig {
		return filepath.Join(a.boxManualDir(), "config.json")
	}
	return filepath.Join(a.boxAutoDir(), "config.json")
}

func (a *App) writeConfigJSON() (string, error) {
	path := a.boxActiveConfigPath()
	// Mode manual: pakai config.json user manual apa adanya, KECUALI mixed
	// inbound disisipkan ke salinan runtime bila belum ada (manualinject.go).
	if a.store.Settings.ManualConfig {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("mode manual aktif tapi %s tidak ada", path)
		}
		feed, port := a.manualSingboxFeed(path)
		a.setEffMixedPort(port)
		if err := a.checkSingboxConfigFile(feed); err != nil {
			return "", fmt.Errorf("config manual tidak valid: %v", err)
		}
		return feed, nil
	}
	// Mode otomatis + override: pakai hasil edit user (sudah divalidasi saat
	// disimpan, validasi ulang di sini sebagai pengaman).
	if a.boxHasOverride() {
		ov := a.boxOverridePath()
		if err := a.singboxTest(ov); err != nil {
			return "", fmt.Errorf("config override tidak valid: %v", err)
		}
		data, err := os.ReadFile(ov)
		if err != nil {
			return "", err
		}
		// Pastikan mixed inbound ada di salinan aktif (file override tak disentuh).
		data, port := a.ensureMixedSingbox(data)
		a.setEffMixedPort(port)
		if err := os.WriteFile(path, data, 0644); err != nil {
			return "", err
		}
		return path, nil
	}
	cfg, err := a.buildConfig()
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	a.setEffMixedPort(a.mixedSetting())
	return path, nil
}

func (a *App) applyRawConfigJSON(raw json.RawMessage) error {
	_ = os.MkdirAll(a.boxManualDir(), 0755)
	path := filepath.Join(a.boxManualDir(), "config.json")
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("JSON tidak valid: %v", err)
	}
	pretty, _ := json.MarshalIndent(v, "", "  ")
	if err := os.WriteFile(path, pretty, 0644); err != nil {
		return err
	}
	if err := a.checkSingboxConfigFile(path); err != nil {
		return fmt.Errorf("config tidak valid: %v", err)
	}
	return nil
}

func (a *App) prefetchRuleSetsSingbox() []string {
	warns := []string{}
	for _, rn := range a.neededRemoteRuleSets() {
		p := filepath.Join(a.boxRulesetDir(), rn.tag+".srs")
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			continue
		}
		// fetchSub: coba langsung dulu, bila gagal (mis. DNS Android tak dikenal
		// resolver Go) ulangi lewat proxy lokal sing-box yg sedang berjalan.
		body, err := a.fetchSub(rn.url)
		if err != nil {
			msg := fmt.Sprintf("rule-set %q gagal diunduh (%v) — aturan terkait dilewati", rn.tag, err)
			warns = append(warns, msg)
			a.logf("prefetch %s", msg)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, body, 0644); err != nil {
			a.logf("prefetch %s: gagal simpan cache: %v", rn.tag, err)
			continue
		}
		a.logf("prefetch %s OK (%d byte)", rn.tag, len(body))
	}
	return warns
}
