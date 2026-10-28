package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var hyPortsRe = regexp.MustCompile(`:(\d+)((?:[,-]\d+)+)([/?#]|$)`)

func truthy(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "1" || s == "true"
}

// b64dec mendekode base64 standar maupun URL-safe, dengan/tanpa padding.
func b64dec(s string) ([]byte, error) {
	s = strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(s)
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

func parseLink(link string) (*Server, error) {
	link = strings.TrimSpace(link)
	if link == "" {
		return nil, fmt.Errorf("link kosong")
	}
	scheme := ""
	if i := strings.Index(link, "://"); i > 0 {
		scheme = strings.ToLower(link[:i])
	}
	switch scheme {
	case "vmess":
		return parseVmess(link)
	case "vless":
		return parseVless(link)
	case "trojan":
		return parseTrojan(link)
	case "ss", "shadowsocks":
		return parseSS(link)
	case "hysteria2", "hy2":
		return parseHy2(link)
	case "tuic":
		return parseTuic(link)
	case "hysteria", "hy":
		return parseHysteria1(link)
	case "anytls":
		return parseAnyTLS(link)
	case "ssh":
		return parseSSH(link)
	case "wireguard", "wg":
		return parseWireguard(link)
	case "socks", "socks5":
		return parseSocks(link, "socks")
	case "http", "https":
		// hanya proxy http polos (bukan URL web) — heuristik: ada @host:port
		return parseSocks(link, "http")
	default:
		return nil, fmt.Errorf("skema tidak dikenal: %s", scheme)
	}
}

func remarkOf(u *url.URL) string {
	if r, err := url.PathUnescape(u.Fragment); err == nil && r != "" {
		return r
	}
	return u.Fragment
}

func parseVmess(link string) (*Server, error) {
	body := link[len("vmess://"):]
	if i := strings.Index(body, "#"); i >= 0 {
		body = body[:i]
	}
	raw, err := b64dec(body)
	if err != nil {
		return nil, fmt.Errorf("vmess base64 rusak: %v", err)
	}
	// nilai bisa string ATAU angka (port/aid sering berupa angka)
	var jm map[string]interface{}
	if err := json.Unmarshal(raw, &jm); err != nil {
		return nil, fmt.Errorf("vmess json rusak: %v", err)
	}
	v := map[string]string{}
	for k, x := range jm {
		switch t := x.(type) {
		case string:
			v[k] = t
		case float64:
			v[k] = strconv.FormatFloat(t, 'f', -1, 64)
		case bool:
			if t {
				v[k] = "true"
			}
		}
	}
	p := map[string]string{
		"server": v["add"], "port": v["port"],
		"uuid": v["id"], "alter_id": v["aid"], "security": v["scy"],
		"net": v["net"], "header_type": v["type"], "host": v["host"], "path": v["path"],
		"service_name": v["path"],
	}
	if p["security"] == "" {
		p["security"] = "auto"
	}
	if p["alter_id"] == "" {
		p["alter_id"] = "0"
	}
	if v["tls"] == "tls" {
		p["tls"] = "1"
		p["sni"] = v["sni"]
		if p["sni"] == "" {
			p["sni"] = v["host"]
		}
		if p["sni"] == "" {
			p["sni"] = v["add"]
		}
		p["fp"] = v["fp"]
		p["alpn"] = v["alpn"]
		if truthy(v["allowInsecure"]) || truthy(v["insecure"]) {
			p["insecure"] = "1"
		}
	}
	name := v["ps"]
	if name == "" {
		name = v["add"] + ":" + v["port"]
	}
	return &Server{Name: name, Proto: "vmess", Link: link, Enabled: true, Params: p, DelayMs: -2}, nil
}

func parseVless(link string) (*Server, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	p := map[string]string{
		"server": u.Hostname(), "port": u.Port(),
		"uuid": u.User.Username(), "encryption": q.Get("encryption"),
		"flow": q.Get("flow"), "net": q.Get("type"),
		"path": q.Get("path"), "host": q.Get("host"),
		"header_type": q.Get("headerType"), "service_name": q.Get("serviceName"),
		"alpn": q.Get("alpn"), "fp": q.Get("fp"),
		"pbk": q.Get("pbk"), "sid": q.Get("sid"),
		"packet_encoding": q.Get("packetEncoding"),
	}
	if p["encryption"] == "" {
		p["encryption"] = "none"
	}
	if q.Get("security") == "tls" {
		p["tls"] = "1"
	} else if q.Get("security") == "reality" {
		p["tls"] = "1"
		p["reality"] = "1"
	}
	if p["tls"] == "1" {
		p["sni"] = q.Get("sni")
		if p["sni"] == "" {
			p["sni"] = u.Hostname()
		}
		if truthy(q.Get("allowInsecure")) || truthy(q.Get("insecure")) {
			p["insecure"] = "1"
		}
	}
	name := remarkOf(u)
	if name == "" {
		name = u.Hostname() + ":" + u.Port()
	}
	return &Server{Name: name, Proto: "vless", Link: link, Enabled: true, Params: p, DelayMs: -2}, nil
}

func parseTrojan(link string) (*Server, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	pw, _ := url.PathUnescape(u.User.Username())
	p := map[string]string{
		"server": u.Hostname(), "port": u.Port(),
		"password": pw, "tls": "1",
		"sni": q.Get("sni"), "alpn": q.Get("alpn"), "fp": q.Get("fp"),
		"net": q.Get("type"), "path": q.Get("path"), "host": q.Get("host"),
		"header_type": q.Get("headerType"), "service_name": q.Get("serviceName"),
		"pbk": q.Get("pbk"), "sid": q.Get("sid"),
	}
	if p["sni"] == "" {
		p["sni"] = u.Hostname()
	}
	if q.Get("security") == "none" {
		p["tls"] = ""
	}
	if truthy(q.Get("allowInsecure")) || truthy(q.Get("insecure")) {
		p["insecure"] = "1"
	}
	name := remarkOf(u)
	if name == "" {
		name = u.Hostname() + ":" + u.Port()
	}
	return &Server{Name: name, Proto: "trojan", Link: link, Enabled: true, Params: p, DelayMs: -2}, nil
}

func parseSS(link string) (*Server, error) {
	rest := link[strings.Index(link, "://")+3:]
	// pisahkan fragment
	frag := ""
	if i := strings.Index(rest, "#"); i >= 0 {
		frag = rest[i+1:]
		rest = rest[:i]
	}
	name, _ := url.PathUnescape(frag)
	query := ""
	if i := strings.Index(rest, "?"); i >= 0 {
		query = rest[i+1:]
		rest = rest[:i]
	}
	rest = strings.TrimSuffix(rest, "/")
	var method, password, host, port string
	if strings.Contains(rest, "@") {
		// ss://base64(method:password)@host:port  atau  ss://method:password@host:port
		parts := strings.SplitN(rest, "@", 2)
		up := parts[0]
		if un, err := url.PathUnescape(up); err == nil {
			up = un
		}
		if !strings.Contains(up, ":") {
			dec, err := b64dec(up)
			if err != nil {
				return nil, fmt.Errorf("ss userinfo rusak")
			}
			up = string(dec)
		} else if dec, err := b64dec(up); err == nil && strings.Contains(string(dec), ":") {
			up = string(dec)
		}
		upParts := strings.SplitN(up, ":", 2)
		if len(upParts) != 2 {
			return nil, fmt.Errorf("ss method:password rusak")
		}
		method, password = upParts[0], upParts[1]
		hp := parts[1]
		if i := strings.LastIndex(hp, ":"); i >= 0 {
			host, port = hp[:i], hp[i+1:]
		} else {
			return nil, fmt.Errorf("ss host:port rusak")
		}
		// plugin di query?
		if i := strings.Index(port, "?"); i >= 0 {
			port = port[:i]
		}
		if i := strings.Index(port, "/"); i >= 0 {
			port = port[:i]
		}
	} else {
		// ss://base64(method:password@host:port)
		dec, err := b64dec(rest)
		if err != nil {
			return nil, fmt.Errorf("ss base64 rusak")
		}
		full := string(dec)
		at := strings.LastIndex(full, "@")
		if at < 0 {
			return nil, fmt.Errorf("ss format rusak")
		}
		up := full[:at]
		hp := full[at+1:]
		upParts := strings.SplitN(up, ":", 2)
		if len(upParts) != 2 {
			return nil, fmt.Errorf("ss method:password rusak")
		}
		method, password = upParts[0], upParts[1]
		if i := strings.LastIndex(hp, ":"); i >= 0 {
			host, port = hp[:i], hp[i+1:]
		}
	}
	host = strings.Trim(host, "[]")
	if name == "" {
		name = host + ":" + port
	}
	p := map[string]string{"server": host, "port": port, "method": method, "password": password}
	if query != "" {
		if qv, err := url.ParseQuery(query); err == nil {
			if pl := qv.Get("plugin"); pl != "" {
				seg := strings.SplitN(pl, ";", 2)
				p["plugin"] = seg[0]
				if len(seg) == 2 {
					p["plugin_opts"] = seg[1]
				}
			}
		}
	}
	return &Server{Name: name, Proto: "ss", Link: link, Enabled: true, Params: p, DelayMs: -2}, nil
}

func parseHy2(link string) (*Server, error) {
	ports := ""
	if m := hyPortsRe.FindStringSubmatch(link); m != nil {
		ports = m[1] + m[2]
		link = strings.Replace(link, ":"+ports, ":"+m[1], 1)
	}
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	auth, _ := url.PathUnescape(u.User.Username())
	p := map[string]string{
		"server": u.Hostname(), "port": u.Port(),
		"auth": auth, "tls": "1",
		"obfs": q.Get("obfs"), "obfs_password": q.Get("obfs-password"),
		"sni": q.Get("sni"), "insecure": q.Get("insecure"),
		"up_mbps": q.Get("upmbps"), "down_mbps": q.Get("downmbps"),
	}
	if ports == "" {
		ports = q.Get("mport")
	}
	if ports != "" {
		p["server_ports"] = ports
	}
	if p["sni"] == "" {
		p["sni"] = u.Hostname()
	}
	name := remarkOf(u)
	if name == "" {
		name = u.Hostname() + ":" + u.Port()
	}
	return &Server{Name: name, Proto: "hy2", Link: link, Enabled: true, Params: p, DelayMs: -2}, nil
}

func parseTuic(link string) (*Server, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	uuid := u.User.Username()
	pw, _ := u.User.Password()
	_ = uuid
	p := map[string]string{
		"server": u.Hostname(), "port": u.Port(),
		"uuid": u.User.Username(), "password": pw, "tls": "1",
		"congestion_control": q.Get("congestion_control"),
		"udp_relay_mode":     q.Get("udp_relay_mode"),
		"sni":                q.Get("sni"), "alpn": q.Get("alpn"),
	}
	if p["sni"] == "" {
		p["sni"] = u.Hostname()
	}
	if p["alpn"] == "" {
		p["alpn"] = "h3"
	}
	if truthy(q.Get("allow_insecure")) || truthy(q.Get("allowInsecure")) || truthy(q.Get("insecure")) {
		p["insecure"] = "1"
	}
	name := remarkOf(u)
	if name == "" {
		name = u.Hostname() + ":" + u.Port()
	}
	return &Server{Name: name, Proto: "tuic", Link: link, Enabled: true, Params: p, DelayMs: -2}, nil
}

func parseWireguard(link string) (*Server, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	pk, _ := url.PathUnescape(u.User.Username())
	p := map[string]string{
		"server": u.Hostname(), "port": u.Port(),
		"private_key": pk, "peer_public_key": q.Get("publickey"),
		"pre_shared_key": q.Get("presharedkey"),
		"local_address":  q.Get("address"),
		"mtu":            q.Get("mtu"),
		"reserved":       q.Get("reserved"),
	}
	if p["port"] == "" {
		p["port"] = "51820"
	}
	name := remarkOf(u)
	if name == "" {
		name = u.Hostname() + ":" + p["port"]
	}
	return &Server{Name: name, Proto: "wireguard", Link: link, Enabled: true, Params: p, DelayMs: -2}, nil
}

func parseSocks(link, proto string) (*Server, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	user := u.User.Username()
	pw, _ := u.User.Password()
	if pw == "" && user != "" {
		// gaya v2rayN: socks://base64(user:pass)@host:port
		if dec, err := b64dec(user); err == nil && strings.Contains(string(dec), ":") {
			up := strings.SplitN(string(dec), ":", 2)
			user, pw = up[0], up[1]
		}
	}
	p := map[string]string{
		"server": u.Hostname(), "port": u.Port(),
		"username": user, "password": pw,
	}
	if p["port"] == "" {
		p["port"] = "1080"
		if proto == "http" {
			p["port"] = "8080"
		}
	}
	if proto == "http" && strings.HasPrefix(strings.ToLower(link), "https://") {
		p["tls"] = "1"
		p["sni"] = u.Hostname()
	}
	name := remarkOf(u)
	if name == "" {
		name = u.Hostname() + ":" + p["port"]
	}
	return &Server{Name: name, Proto: proto, Link: link, Enabled: true, Params: p, DelayMs: -2}, nil
}

// parseSubscriptionText memecah teks subscription menjadi daftar link.
func parseSubscriptionText(text string) []string {
	text = strings.TrimSpace(text)
	if dec, err := b64dec(text); err == nil {
		if looksLikeLinks(string(dec)) {
			text = string(dec)
		}
	}
	var out []string
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(strings.TrimSuffix(ln, "\r"))
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		if strings.Contains(ln, "://") {
			out = append(out, ln)
		}
	}
	return out
}

func looksLikeLinks(s string) bool {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.Contains(ln, "://") {
			return true
		}
	}
	return false
}

// hysteria://host:port?auth=..&peer=sni&insecure=1&upmbps=10&downmbps=50&alpn=h3&obfsParam=..
func parseHysteria1(link string) (*Server, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	p := map[string]string{
		"server": u.Hostname(), "port": u.Port(), "tls": "1",
		"auth": q.Get("auth"), "sni": orDefault(q.Get("peer"), q.Get("sni")),
		"alpn": q.Get("alpn"), "obfs": q.Get("obfsParam"),
		"up_mbps": q.Get("upmbps"), "down_mbps": q.Get("downmbps"),
	}
	if truthy(q.Get("insecure")) {
		p["insecure"] = "1"
	}
	if p["sni"] == "" {
		p["sni"] = u.Hostname()
	}
	name := remarkOf(u)
	if name == "" {
		name = u.Hostname() + ":" + u.Port()
	}
	return &Server{Name: name, Proto: "hysteria", Link: link, Enabled: true, Params: p, DelayMs: -2}, nil
}

// anytls://password@host:port?sni=..&insecure=1#name
func parseAnyTLS(link string) (*Server, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	pw := u.User.Username()
	if p2, ok := u.User.Password(); ok {
		pw = pw + ":" + p2
	}
	p := map[string]string{
		"server": u.Hostname(), "port": u.Port(), "password": pw, "tls": "1",
		"sni": q.Get("sni"), "alpn": q.Get("alpn"), "fp": q.Get("fp"),
	}
	if p["sni"] == "" {
		p["sni"] = u.Hostname()
	}
	if truthy(q.Get("insecure")) || truthy(q.Get("allowInsecure")) {
		p["insecure"] = "1"
	}
	name := remarkOf(u)
	if name == "" {
		name = u.Hostname() + ":" + u.Port()
	}
	return &Server{Name: name, Proto: "anytls", Link: link, Enabled: true, Params: p, DelayMs: -2}, nil
}

// ssh://user:password@host:port#name
// Query ssh_* (ditulis buildSSHLink) dibaca kembali apa adanya,
// supaya copy SSH tidak kehilangan payload/proxy/TLS/mode.
// Query lain yang tidak dikenal diabaikan, jadi link SSH polos
// dari aplikasi lain tetap bisa diimport seperti biasa.
func parseSSH(link string) (*Server, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	pw, _ := u.User.Password()
	p := map[string]string{
		"server": u.Hostname(), "port": u.Port(),
		"username": u.User.Username(), "password": pw,
		"private_key": u.Query().Get("pk"),
	}
	for k, vs := range u.Query() {
		if strings.HasPrefix(k, "ssh_") && len(vs) > 0 && vs[0] != "" {
			p[k] = vs[0]
		}
	}
	if p["port"] == "" {
		p["port"] = "22"
	}
	name := remarkOf(u)
	if name == "" {
		name = u.Hostname() + ":" + p["port"]
	}
	return &Server{Name: name, Proto: "ssh", Link: link, Enabled: true, Params: p, DelayMs: -2}, nil
}

// parseSubscriptionServers: isi subscription -> daftar server.
// Mendukung kumpulan link (plain/base64) dan Clash YAML.
func parseSubscriptionServers(body string) ([]*Server, int) {
	t := strings.TrimSpace(body)
	if strings.HasPrefix(t, "proxies:") || strings.Contains(t, "\nproxies:") {
		list := clashYAMLToServers(t)
		return list, 0
	}
	var out []*Server
	failed := 0
	for _, ln := range parseSubscriptionText(t) {
		sv, err := parseLink(ln)
		if err != nil {
			failed++
			continue
		}
		out = append(out, sv)
	}
	return out, failed
}
