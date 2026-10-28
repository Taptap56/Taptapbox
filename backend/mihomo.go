// mihomo.go — generasi config Mihomo (YAML) untuk TaptapClash.
//
// Backend tetap memakai model data lama (Server/params ala v2rayNG, grup,
// aturan builder, pengaturan). File ini mengubah semuanya menjadi config
// Mihomo: proxies, proxy-groups, rules, rule-providers, DNS, TUN.

package main

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// geoNativeRe: tag rule-set "geoip-<kode>" / "geosite-<kode>" (mis. geoip-id,
// geosite-cn) dipetakan ke rule native GEOIP/GEOSITE mihomo memakai geodata
// lokal — tanpa rule-provider http.
var geoNativeRe = regexp.MustCompile(`^(?i)(geoip|geosite)[-_]([a-z0-9-]+)$`)

const mihomoCoreVersion = "1.19.32"

const healthDefault = "https://www.gstatic.com/generate_204"

// mihomoWorkDir: direktori kerja mihomo (cache, rule-provider, dll).
func (a *App) mihomoWorkDir() string {
	d := filepath.Join(a.dataDir, "mihomo")
	_ = os.MkdirAll(d, 0755)
	return d
}

// ensureGeodata: salin geoip.dat/geosite.dat dari data/geodata ke workdir
// mihomo bila belum ada (mihomo membaca geodata dari workdir/config dir).
// ensureGeodata: mihomo hanya mau membaca geoip.dat/geosite.dat dari
// folder rumahnya (workdir). Agar tidak dobel ~21MB, file di workdir
// dibuat SYMLINK ke salinan kerja di data/geodata (mihomo membuka file
// mengikuti symlink); salinan reguler warisan diganti sekali jalan.
// Fallback: salin beneran bila symlink gagal.
func (a *App) ensureGeodata() {
	wd := a.mihomoWorkDir()
	for _, n := range []string{"geoip.dat", "geosite.dat"} {
		dst := filepath.Join(wd, n)
		src := filepath.Join(a.dataDir, "geodata", n)
		if _, err := os.Stat(src); err != nil {
			a.logWarnf("geodata: %s tidak ada di %s", n, src)
			continue
		}
		if link, err := os.Readlink(dst); err == nil && link == src {
			continue // sudah symlink ke sumber yang benar
		}
		_ = os.Remove(dst)
		if err := os.Symlink(src, dst); err == nil {
			a.logf("geodata: %s -> symlink ke data/geodata", n)
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			a.logWarnf("geodata: %s tidak ada di %s", n, src)
			continue
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			a.logWarnf("geodata: gagal salin %s: %v", n, err)
			continue
		}
		a.logf("geodata: %s disalin ke workdir", n)
	}
}

// geodataReady: true bila kedua file geodata ada di workdir mihomo.
func (a *App) geodataReady() bool {
	wd := a.mihomoWorkDir()
	for _, n := range []string{"geoip.dat", "geosite.dat"} {
		if _, err := os.Stat(filepath.Join(wd, n)); err != nil {
			return false
		}
	}
	return true
}

// mihomoTest memvalidasi config: mihomo -t -f <path>.
// external-ui boleh di luar home dir -> SAFE_PATHS wajib di-set juga
// saat test (sama seperti saat run).
func (a *App) mihomoTest(path string) ([]byte, error) {
	cmd := exec.Command(a.mihomoBin, "-t", "-d", a.mihomoWorkDir(), "-f", path)
	cmd.Env = append(os.Environ(), "SAFE_PATHS="+a.webuiDir+string(os.PathListSeparator)+a.dataDir)
	return cmd.CombinedOutput()
}

// yamlToMap: parse teks YAML menjadi map (untuk baca config manual).
func yamlToMap(yml string) (map[string]any, error) {
	var m map[string]any
	dec := yaml.NewDecoder(strings.NewReader(yml))
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// ---------- Server -> proxy mihomo ----------

// mihomoUnsupported: protokol yang tidak didukung core mihomo.
// Server berprotokol ini dilewati saat generate config (dengan peringatan),
// bukan menggagalkan seluruh config.
func mihomoUnsupported(proto string) bool {
	switch proto {
	case "hysteria", "anytls":
		return true
	}
	return false
}

// serverToMihomoProxy mengubah Server menjadi proxy Mihomo (map YAML).
// Mengembalikan nil bila protokol tidak didukung mihomo.
func serverToMihomoProxy(sv *Server, name string) map[string]any {
	p := sv.Params
	port := atoi(p["port"], defaultPort(sv.Proto))
	px := map[string]any{
		"name":   name,
		"server": p["server"],
		"port":   port,
		"udp":    true,
	}
	// SSH payload: sama seperti sing-box — core ke jembatan lokal, tanpa
	// TLS/transport core (dipegang jembatan). Lihat sshbridge.go.
	_, isBridge := sshPayloadProfile(sv)
	if isBridge {
		px["server"] = "127.0.0.1"
		px["port"] = sshBridgePort
	} else {
		applyMihomoTLS(p, px, sv.Proto)
		applyMihomoTransport(p, px)
	}
	switch sv.Proto {
	case "vmess":
		px["type"] = "vmess"
		px["uuid"] = p["uuid"]
		px["alterId"] = atoi(p["alter_id"], 0)
		px["cipher"] = orDefault(p["security"], "auto")
	case "vless":
		px["type"] = "vless"
		px["uuid"] = p["uuid"]
		if f := p["flow"]; f != "" {
			px["flow"] = f
		}
	case "trojan":
		px["type"] = "trojan"
		px["password"] = p["password"]
	case "ss":
		px["type"] = "ss"
		px["cipher"] = p["method"]
		px["password"] = p["password"]
		if pl := strings.ToLower(p["plugin"]); pl != "" {
			if pl == "simple-obfs" || pl == "obfs-local" {
				pl = "obfs"
			}
			if pl == "obfs" || pl == "v2ray-plugin" {
				px["plugin"] = pl
				if po := p["plugin_opts"]; po != "" {
					px["plugin-opts"] = pluginOptsMap(po)
				}
			}
		}
	case "hy2":
		px["type"] = "hysteria2"
		px["password"] = p["auth"]
		if up := atoi(p["up_mbps"], 0); up > 0 {
			px["up"] = strconv.Itoa(up)
		}
		if down := atoi(p["down_mbps"], 0); down > 0 {
			px["down"] = strconv.Itoa(down)
		}
		if o := p["obfs"]; o != "" {
			px["obfs"] = o
			if op := p["obfs_password"]; op != "" {
				px["obfs-password"] = op
			}
		}
		if sp := normalizePorts(p["server_ports"]); sp != "" {
			px["ports"] = sp
		}
	case "tuic":
		px["type"] = "tuic"
		px["uuid"] = p["uuid"]
		px["password"] = p["password"]
		if cc := strings.ToLower(p["congestion_control"]); cc != "" {
			px["congestion-controller"] = cc
		}
		if urm := strings.ToLower(p["udp_relay_mode"]); urm != "" {
			px["udp-relay-mode"] = urm
		}
	case "wireguard":
		px["type"] = "wireguard"
		px["private-key"] = p["private_key"]
		px["public-key"] = p["peer_public_key"]
		if psk := p["pre_shared_key"]; psk != "" {
			px["pre-shared-key"] = psk
		}
		if la := strings.TrimSpace(p["local_address"]); la != "" {
			// ambil alamat pertama; mihomo pakai "ip" (tanpa prefix bila perlu)
			ip := strings.TrimSpace(strings.Split(la, ",")[0])
			px["ip"] = strings.Split(ip, "/")[0]
		}
		if mtu := atoi(p["mtu"], 0); mtu > 0 {
			px["mtu"] = mtu
		}
	case "ssh":
		px["type"] = "ssh"
		px["username"] = orDefault(p["username"], "root")
		if pw := p["password"]; pw != "" {
			px["password"] = pw
		}
		if pk := p["private_key"]; pk != "" {
			px["private-key"] = pk
		}
	case "socks":
		px["type"] = "socks5"
		if u := p["username"]; u != "" {
			px["username"] = u
			px["password"] = p["password"]
		}
	case "http":
		px["type"] = "http"
		if u := p["username"]; u != "" {
			px["username"] = u
			px["password"] = p["password"]
		}
	default:
		return nil
	}
	return px
}

// applyMihomoTLS: tls/servername/alpn/fingerprint/reality dari params.
func applyMihomoTLS(p map[string]string, px map[string]any, proto string) {
	if p["tls"] != "1" {
		return
	}
	px["tls"] = true
	sni := p["sni"]
	if sni == "" {
		sni = p["server"]
	}
	if sni != "" {
		// trojan memakai "sni", tipe lain memakai "servername"
		if proto == "trojan" {
			px["sni"] = sni
		} else {
			px["servername"] = sni
		}
	}
	if p["insecure"] == "1" || strings.EqualFold(p["insecure"], "true") {
		px["skip-cert-verify"] = true
	}
	if alpn := p["alpn"]; alpn != "" {
		px["alpn"] = strings.Split(alpn, ",")
	}
	fp := p["fp"]
	if p["reality"] == "1" {
		// reality WAJIB fingerprint; tanpa fp mihomo menolak config
		if fp == "" {
			fp = "chrome"
		}
		px["reality-opts"] = map[string]any{
			"public-key": p["pbk"],
			"short-id":   p["sid"],
		}
	}
	if fp != "" {
		px["fingerprint"] = fp
	}
}

// applyMihomoTransport: network ws/grpc/h2/http/httpupgrade dari params.
func applyMihomoTransport(p map[string]string, px map[string]any) {
	switch strings.ToLower(p["net"]) {
	case "ws", "websocket":
		px["network"] = "ws"
		ws := map[string]any{}
		if path := p["path"]; path != "" {
			path, ed := splitEarlyData(path)
			ws["path"] = path
			if ed > 0 {
				ws["max-early-data"] = ed
				ws["early-data-header-name"] = "Sec-WebSocket-Protocol"
			}
		}
		if host := p["host"]; host != "" {
			ws["headers"] = map[string]string{"Host": host}
		}
		px["ws-opts"] = ws
	case "grpc", "gun":
		px["network"] = "grpc"
		sn := p["service_name"]
		if sn == "" {
			sn = strings.Trim(p["path"], "/")
		}
		px["grpc-opts"] = map[string]any{"grpc-service-name": sn}
	case "http", "h2":
		px["network"] = "h2"
		h2 := map[string]any{}
		if host := p["host"]; host != "" {
			h2["host"] = strings.Split(host, ",")
		}
		if path := p["path"]; path != "" {
			h2["path"] = path
		}
		px["h2-opts"] = h2
	case "httpupgrade":
		px["network"] = "ws"
		ws := map[string]any{"v2ray-http-upgrade": true}
		if path := p["path"]; path != "" {
			ws["path"] = path
		}
		if host := p["host"]; host != "" {
			ws["headers"] = map[string]string{"Host": host}
		}
		px["ws-opts"] = ws
	default:
		// tcp dengan header http obfuscation (vmess type=http)
		if strings.ToLower(p["header_type"]) == "http" {
			px["network"] = "http"
			ho := map[string]any{}
			if m := p["method"]; m != "" {
				ho["method"] = m
			}
			if path := p["path"]; path != "" {
				ho["path"] = []string{path}
			}
			if host := p["host"]; host != "" {
				hdrs := map[string][]string{}
				hdrs["Host"] = strings.Split(host, ",")
				ho["headers"] = hdrs
			}
			px["http-opts"] = ho
		}
	}
}

// pluginOptsMap: "obfs=tls;obfs-host=x" (gaya v2rayNG/simple-obfs) -> map
// untuk plugin-opts mihomo ("mode", "host", ...).
func pluginOptsMap(s string) map[string]any {
	m := map[string]any{}
	for _, kv := range strings.Split(s, ";") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			continue
		}
		// terjemahkan nama kunci simple-obfs -> mihomo
		switch strings.ToLower(k) {
		case "obfs":
			k = "mode"
		case "obfs-host":
			k = "host"
		case "obfs-path", "obfs-uri":
			k = "path"
		}
		m[k] = v
	}
	return m
}

// splitEarlyData memisahkan "?ed=2048" dari path websocket (early data).
func splitEarlyData(path string) (string, int) {
	i := strings.Index(path, "?")
	if i < 0 {
		return path, 0
	}
	q, err := url.ParseQuery(path[i+1:])
	if err != nil || q.Get("ed") == "" {
		return path, 0
	}
	ed, _ := strconv.Atoi(q.Get("ed"))
	q.Del("ed")
	base := path[:i]
	if rest := q.Encode(); rest != "" {
		base += "?" + rest
	}
	return base, ed
}

// ---------- config utama ----------

// mihomoLogLevel: level log Pengaturan -> level log mihomo.
func (a *App) mihomoLogLevel() string {
	switch lv := a.store.Settings.LogLevel; lv {
	case "debug":
		return "debug"
	case "info":
		return "info"
	case "error", "silent":
		return "error"
	case "warn", "warning":
		return "warning"
	}
	return "warning"
}

// buildMihomoMap membangun config Mihomo sebagai map (siap di-marshal ke YAML).
func (a *App) buildMihomoMap() (map[string]any, error) {
	st := a.store.Settings
	bld := a.store.Builder

	mg := a.mainGroupTag()
	built := a.buildServers()

	// ---- proxies ----
	proxies := []any{}
	for _, b := range built {
		if b.Ob != nil {
			proxies = append(proxies, b.Ob)
		}
	}
	if len(proxies) == 0 {
		a.logWarnf("peringatan: tidak ada server valid untuk config mihomo")
	}

	// ---- proxy-groups ----
	objs, gTag, _, extraProxies := a.buildGroups(built)
	// proxy sintetik emulasi relay (dialer-proxy) ikut didaftarkan sebagai proxies
	for _, xp := range extraProxies {
		proxies = append(proxies, xp)
	}
	// BadVPN: proxy SOCKS5 lokal untuk UDP (tidak masuk grup utama —
	// hanya dirujuk aturan NETWORK,UDP di bawah). Untuk mihomo, Tag
	// builtServer sama dengan nama proxy (serverToMihomoProxy memakai
	// tag sebagai name), jadi bvSSH bisa dipakai langsung sebagai nama.
	var bvSocks, bvUdpgw int
	var bvSSH string
	var bvOk bool
	// Gerbang ganda: relay jalan DAN toggle nyala DAN bukan manual.
	if sp, up, ok := a.badvpnCfg(); ok && st.BadvpnEnabled && !st.ManualConfig {
		if tag, ok2 := a.badvpnSelectedSSH(built); ok2 {
			bvSocks, bvUdpgw, bvSSH, bvOk = sp, up, tag, true
			proxies = append(proxies, map[string]any{"name": badvpnTag, "type": "socks5", "server": "127.0.0.1", "port": bvSocks, "udp": true})
		}
	}
	groups := []any{}
	mainMembers := []string{}
	for _, b := range built {
		if b.Ob != nil {
			if n, ok := b.Ob["name"].(string); ok {
				mainMembers = append(mainMembers, n)
			}
		}
	}
	if len(mainMembers) == 0 {
		// mihomo menolak proxy-group kosong: tanpa server, grup utama
		// berisi DIRECT agar config tetap valid (trafik langsung).
		mainMembers = []string{"DIRECT"}
	}
	groups = append(groups, map[string]any{
		"name":    mg,
		"type":    "select",
		"proxies": mainMembers,
	})
	for _, o := range objs {
		groups = append(groups, o)
	}

	// ---- rules ----
	rules := []string{}
	// BadVPN: stream TCP ke udpgw server HARUS ke SSH. Ditaruh paling
	// atas karena 127.0.0.1 akan ditangkap aturan privat->DIRECT bila
	// aturan ini di bawahnya. Catatan: DST-PORT juga menangkap UDP
	// port udpgw persis (game di UDP port itu akan ke SSH, bukan ke
	// relay) — praktis tidak terjadi, port game bukan port udpgw.
	if bvOk {
		rules = append(rules, "DST-PORT,"+strconv.Itoa(bvUdpgw)+","+bvSSH)
	}
	// geodata (geoip.dat/geosite.dat yang di-bundle): aktif bila file ada.
	geoOK := a.geodataReady()
	// bypass IP private/lokal via GEOIP (aman & standar: LAN tidak perlu proxy)
	if st.BypassPrivate && geoOK {
		rules = append(rules, "GEOIP,private,DIRECT")
	}
	// blokir iklan: GEOSITE dari geosite.dat bundle (offline); bila geodata
	// belum ada, fallback ke rule-provider http (download).
	if st.BlockAds {
		if geoOK {
			rules = append(rules, "GEOSITE,category-ads-all,REJECT")
		} else {
			rules = append(rules, "RULE-SET,geosite-ads,REJECT")
		}
	}
	// blokir QUIC (UDP 443) tidak bisa diungkapkan dalam satu baris rule mihomo;
	// QUIC tetap lewat MATCH -> keluar via proxy (tidak bocor), jadi aturan
	// khusus tidak diperlukan di sini.
	// domain bypass -> DIRECT (didahulukan)
	for _, d := range parseDomainList(st.BypassDomains) {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if strings.HasPrefix(d, ".") {
			d = d[1:]
		}
		rules = append(rules, "DOMAIN-SUFFIX,"+d+",DIRECT")
	}
	// aturan custom builder
	for _, r := range bld.Rules {
		if r.Disabled {
			continue
		}
		if len(r.RuleSets) == 0 && len(r.DomainSuffix) == 0 && len(r.IPCIDR) == 0 &&
			len(r.Port) == 0 && len(r.Protocol) == 0 && len(r.PackageNames) == 0 &&
			len(r.GeoIP) == 0 && len(r.GeoSite) == 0 {
			a.logf("builder: rule %q tanpa filter di-skip (catch-all)", r.ID)
			continue
		}
		ob := r.Outbound
		if ob == "Proxy" || ob == "" {
			ob = mg
		}
		if t, ok := gTag[ob]; ok {
			ob = t
		} else if ob != "DIRECT" && ob != "REJECT" && ob != mg {
			ob = mg
		}
		// domain / IP / rule-set dalam satu rule dianggap ATAU: dipecah per baris.
		emit := []string{}
		for _, rs := range r.RuleSets {
			// geoip-<kode>/geosite-<kode>: pakai rule native GEOIP/GEOSITE
			// dengan geoip.dat/geosite.dat lokal (offline) — jangan buat
			// rule-provider http, karena URL .srs (sing-box) tidak bisa
			// dibaca mihomo ("file must have a 'payload' field").
			if m := geoNativeRe.FindStringSubmatch(rs); m != nil && geoOK {
				kind := "GEOIP"
				if strings.HasPrefix(strings.ToLower(rs), "geosite") {
					kind = "GEOSITE"
				}
				emit = append(emit, kind+","+strings.ToLower(m[2])+","+ob)
				continue
			}
			emit = append(emit, "RULE-SET,"+rs+","+ob)
		}
		for _, g := range r.GeoIP {
			code := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(g)), "geoip:")
			if code != "" {
				emit = append(emit, "GEOIP,"+code+","+ob)
			}
		}
		for _, g := range r.GeoSite {
			name := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(g)), "geosite:")
			if name != "" {
				emit = append(emit, "GEOSITE,"+name+","+ob)
			}
		}
		for _, d := range r.DomainSuffix {
			emit = append(emit, "DOMAIN-SUFFIX,"+d+","+ob)
		}
		for _, c := range r.IPCIDR {
			if strings.Contains(c, ":") {
				emit = append(emit, "IP-CIDR6,"+c+","+ob)
			} else {
				emit = append(emit, "IP-CIDR,"+c+","+ob)
			}
		}
		// Aturan tanpa kondisi domain/IP/rule-set (mis. hanya port/protokol/
		// aplikasi) TIDAK boleh memancarkan baris "MATCH," di tengah daftar —
		// baris MATCH hanya satu, di paling akhir (= grup utama). Baris
		// DST-PORT/NETWORK/PROCESS-NAME di bawah sudah cukup sebagai pencocok.
		// port / protocol / package di-AND: ditempel ke tiap baris via koma tidak bisa;
		// mihomo memakai satu baris per kombinasi — sederhanakan: bila ada filter
		// tambahan, buat baris khusus untuk tiap jenis filter.
		extra := []string{}
		if len(r.Port) > 0 {
			ps := make([]string, 0, len(r.Port))
			for _, pn := range r.Port {
				ps = append(ps, strconv.Itoa(pn))
			}
			// mihomo: multi-port pakai "/" (koma = pemisah field rule)
			extra = append(extra, "DST-PORT,"+strings.Join(ps, "/")+","+ob)
		}
		for _, proto := range r.Protocol {
			switch strings.ToLower(proto) {
			case "tcp", "udp":
				extra = append(extra, "NETWORK,"+strings.ToUpper(proto)+","+ob)
			}
		}
		for _, pkg := range r.PackageNames {
			extra = append(extra, "PROCESS-NAME,"+pkg+","+ob)
		}
		rules = append(rules, emit...)
		rules = append(rules, extra...)
	}
	// IP privat/lokal -> DIRECT (anti bocor LAN)
	rules = append(rules,
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
		"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
		"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR6,::1/128,DIRECT,no-resolve",
		"IP-CIDR6,fc00::/7,DIRECT,no-resolve",
		"IP-CIDR6,fe80::/10,DIRECT,no-resolve",
	)
	// BadVPN: sisa UDP (sesudah aturan user & privat->DIRECT) ke relay.
	if bvOk {
		rules = append(rules, "NETWORK,UDP,"+badvpnTag)
	}
	// MATCH hanya satu baris di paling akhir: selalu grup utama.
	// (Pilihan "rute akhir" dihapus — satu match, satu grup utama.)
	rules = append(rules, "MATCH,"+mg)

	// ---- rule-providers ----
	providers := map[string]any{}
	providerNames := map[string]bool{}
	// geosite-ads via http hanya bila geodata bundle tidak ada (fallback).
	if st.BlockAds && !geoOK {
		providers["geosite-ads"] = map[string]any{
			"type":     "http",
			"behavior": "domain",
			"url":      "https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/geosite-ads.dat",
			"path":     filepath.Join(a.clashRulesetDir(), "geosite-ads.dat"),
			"interval": 86400,
		}
		providerNames["geosite-ads"] = true
	}
	for _, rs := range bld.RuleSets {
		if providerNames[rs.Tag] {
			continue
		}
		// Entri local (file copy manual di folder rulesets, didaftarkan
		// dari WebUI): provider file — tanpa unduhan. Format mengikuti
		// ekstensi; .srs/.json/.dat bukan format provider file mihomo.
		if rs.Type == "local" && rs.Path != "" {
			p := rs.Path
			if !filepath.IsAbs(p) {
				p = filepath.Join(a.dataDir, p)
			}
			var format string
			switch strings.ToLower(filepath.Ext(p)) {
			case ".mrs":
				format = "mrs"
			case ".yaml", ".yml":
				format = "yaml"
			case ".txt", ".list":
				format = "text"
			default:
				a.logf("rule-set %q: format file %q tidak didukung provider mihomo — dilewati", rs.Tag, filepath.Ext(p))
				continue
			}
			behavior := rs.Behavior
			if behavior != "ipcidr" && behavior != "domain" {
				behavior = "domain"
			}
			providers[rs.Tag] = map[string]any{
				"type":     "file",
				"behavior": behavior,
				"path":     p,
				"format":   format,
			}
			providerNames[rs.Tag] = true
			continue
		}
		if rs.URL == "" {
			continue
		}
		// geoip-*/geosite-*: sudah jadi rule native GEOIP/GEOSITE (lihat
		// atas) — jangan buat rule-provider http.
		if geoNativeRe.MatchString(rs.Tag) && geoOK {
			continue
		}
		// mihomo rule-provider http: mendukung format geosite/dat & yaml klasik.
		p := map[string]any{
			"type":     "http",
			"behavior": "domain",
			"url":      rs.URL,
			"path":     filepath.Join(a.clashRulesetDir(), rs.Tag+".dat"),
			"interval": 86400,
		}
		// format provider mengikuti ekstensi URL: .mrs = format biner mihomo,
		// .yaml/.txt = format klasik. Tanpa field ini file .mrs gagal dibaca.
		switch lu := strings.ToLower(rs.URL); {
		case strings.HasSuffix(lu, ".mrs"):
			p["format"] = "mrs"
		case strings.HasSuffix(lu, ".yaml"), strings.HasSuffix(lu, ".yml"):
			p["format"] = "yaml"
		case strings.HasSuffix(lu, ".txt"), strings.HasSuffix(lu, ".list"):
			p["format"] = "text"
		}
		providers[rs.Tag] = p
		providerNames[rs.Tag] = true
	}

	cfg := map[string]any{
		"mixed-port":          orDefaultPort(st.MixedPort, 7890),
		"ipv6":                !st.BlockIPv6,
		"mode":                "rule",
		"log-level":           a.mihomoLogLevel(),
		"external-controller": clashListen(st) + ":" + itoa(st.ClashPort),
		"external-ui":         a.zashboardDir(),
		"secret":              st.Secret,
		"profile": map[string]any{
			"store-selected": true,
		},
		"proxies":      proxies,
		"proxy-groups": groups,
		"rules":        rules,
	}
	// Koneksi lebih cepat: coba semua IP hasil resolve bersamaan (CDN seperti
	// googlevideo punya banyak IP) & hitung delay tanpa handshake ganda.
	cfg["tcp-concurrent"] = true
	cfg["unified-delay"] = true
	// Sniffer: baca domain dari TLS/HTTP/QUIC. Tanpa ini, trafik yang hanya
	// berupa IP (tproxy/redirect, Private DNS, DoH) tak bisa dicocokkan dengan
	// rule domain/GEOSITE dan server proxy menerima IP mentah (CDN tidak optimal).
	// Mengikuti toggle "Traffic Sniffing" di Aturan (Builder.NoSniff).
	if !bld.NoSniff {
		cfg["sniffer"] = map[string]any{
			"enable":               true,
			"force-dns-mapping":    true,
			"parse-pure-ip":        true,
			"override-destination": true,
			"sniff": map[string]any{
				"TLS":  map[string]any{"ports": []string{"443", "8443"}},
				"HTTP": map[string]any{"ports": []string{"80", "8080-8880"}},
				"QUIC": map[string]any{"ports": []string{"443", "8443"}},
			},
		}
	}
	// geodata bundle: GEOIP/GEOSITE inline (format v2ray .dat).
	if geoOK {
		cfg["geodata-mode"] = true
	}

	if len(providers) > 0 {
		cfg["rule-providers"] = providers
	}

	// ---- DNS ----
	dnsRemote := orDefault(strings.TrimSpace(st.DnsRemote), "https://1.1.1.1/dns-query")
	dnsDirect := orDefault(strings.TrimSpace(st.DnsDirect), "https://223.5.5.5/dns-query")
	dnsCfg := map[string]any{
		"enable":        true,
		"ipv6":          !st.BlockIPv6,
		"enhanced-mode": "fake-ip",
		"fake-ip-range": "198.18.0.1/16",
		"fake-ip-filter": []string{
			"*.lan",
			"+.local",
			"time.*.com",
			"ntp.*.com",
			"+.pool.ntp.org",
			"+.stun.*.*",
			"+.stun.*.*.*",
		},
		// nameserver dilewatkan lewat routing rules -> keluar via proxy
		// (padanan detour sing-box). proxy-server-nameserver dipakai untuk
		// resolve hostname nameserver itu sendiri (langsung).
		"respect-rules":           true,
		"nameserver":              []string{dnsRemote},
		"proxy-server-nameserver": []string{dnsDirect},
		"direct-nameserver":       []string{dnsDirect},
	}
	// Mode non-TUN (tproxy/redirect/mixed): DNS aplikasi di-REDIRECT ke DNS mihomo
	// (lihat applyDNSHijack). Di mode TUN, tun.dns-hijack yang menangani.
	if dnsViaMihomo(st) {
		lis := "127.0.0.1"
		if st.Hotspot {
			lis = "0.0.0.0" // klien hotspot di-REDIRECT lewat PREROUTING
		}
		dnsCfg["listen"] = lis + ":" + itoa(mihomoDNSPort)
	}
	if bypass := parseDomainList(st.BypassDomains); len(bypass) > 0 {
		policyKeys := []string{}
		for _, d := range bypass {
			d = strings.ToLower(strings.TrimSpace(d))
			if strings.HasPrefix(d, ".") {
				d = d[1:]
			}
			policyKeys = append(policyKeys, "+."+d)
		}
		dnsCfg["nameserver-policy"] = map[string]any{
			strings.Join(policyKeys, ","): []string{dnsDirect},
		}
	}
	cfg["dns"] = dnsCfg

	// ---- inbound transparan ----
	if st.TransparentProxy {
		// Mode proxy (lihat WebUI Pengaturan):
		//   tun      = TUN saja
		//   tproxy   = TPROXY TCP+UDP
		//   iptables = REDIRECT TCP (label UI: Redirect)
		//   mixed    = TCP via REDIRECT + UDP via TPROXY
		//   enhance  = TUN + proteksi dipaksa penuh
		// Catatan: mode ebpf/hybrid (bpf2socks) dihapus 2026-10-16; eBPF
		// native mihomo (PR #3175) hanya untuk gateway/LAN, tidak menangkap
		// trafik HP sendiri — jadi tidak dipakai. Lihat KEPUTUSAN-EBPF.md.
		tunMode := st.ProxyMode == "tun" || st.ProxyMode == "enhance"
		if tunMode {
			mtu := st.TunMTU
			if mtu <= 0 {
				mtu = 9000
			}
			cfg["tun"] = map[string]any{
				"enable":     true,
				"device":     "tun0",
				"stack":      "gvisor",
				"mtu":        mtu,
				"auto-route": true,
				// deteksi interface otomatis agar auto-route tidak menelan
				// interface hotspot / tethering
				"auto-detect-interface": true,
			}
			// DNS hijack di level TUN mengikuti toggle yang sama dengan
			// netfilter REDIRECT (berlaku juga untuk config manual).
			// Mode enhance memaksanya aktif.
			if st.DNSHijack || st.ProxyMode == "enhance" {
				cfg["tun"].(map[string]any)["dns-hijack"] = []string{"any:53"}
			}
			if strings.TrimSpace(st.ExcludePackages) != "" {
				a.logWarnf("peringatan: ExcludePackages tidak didukung core mihomo, diabaikan")
			}
		}
		if st.ProxyMode == "tproxy" || st.ProxyMode == "mixed" {
			port := st.TproxyPort
			if port <= 0 {
				port = 15359
			}
			cfg["tproxy-port"] = port
		}
		if st.ProxyMode == "iptables" || st.ProxyMode == "mixed" {
			cfg["redir-port"] = orDefaultPort(st.RedirectPort, 7891)
		}
	}

	return cfg, nil
}

// buildConfigYAML: config Mihomo final sebagai teks YAML.
func (a *App) buildConfigYAML() (string, error) {
	cfg, err := a.buildMihomoMap()
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return "", err
	}
	_ = enc.Close()
	return sb.String(), nil
}

// clashListen: alamat listen Clash API (LAN bila WebListenAll).
func clashListen(st Settings) string {
	if st.WebListenAll {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

// orDefaultPort: port bila <=0 pakai default.
func orDefaultPort(p, def int) int {
	if p <= 0 {
		return def
	}
	return p
}
