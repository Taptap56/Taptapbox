package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// buildLink menyusun ulang link dari Params (kebalikan parseLink).
// Dipakai setelah edit server via form ala v2rayNG agar Link tetap sinkron.
func buildLink(proto string, p map[string]string, name string) string {
	switch proto {
	case "vmess":
		return buildVmessLink(p, name)
	case "vless":
		return buildVlessLink(p, name)
	case "trojan":
		return buildTrojanLink(p, name)
	case "ss", "shadowsocks":
		return buildSSLink(p, name)
	case "socks", "http":
		return buildSocksLink(proto, p, name)
	case "hy2":
		return buildHy2Link(p, name)
	case "tuic":
		return buildTuicLink(p, name)
	case "wireguard":
		return buildWireguardLink(p, name)
	case "hysteria":
		return buildHysteria1Link(p, name)
	case "anytls":
		return buildAnyTLSLink(p, name)
	case "ssh":
		return buildSSHLink(p, name)
	default:
		return ""
	}
}

func b64enc(b []byte) string {
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b)
}

func buildVmessLink(p map[string]string, name string) string {
	v := map[string]string{
		"v": "2", "ps": name,
		"add": p["server"], "port": p["port"],
		"id": p["uuid"], "aid": orDefault(p["alter_id"], "0"),
		"scy": orDefault(p["security"], "auto"),
		"net": orDefault(p["net"], "tcp"), "type": p["header_type"],
		"host": p["host"], "path": p["path"],
		"tls": "", "sni": "", "fp": "", "alpn": "",
	}
	if p["tls"] == "1" {
		v["tls"] = "tls"
		v["sni"] = p["sni"]
		v["fp"] = p["fp"]
		v["alpn"] = p["alpn"]
	}
	// bersihkan field kosong agar link rapi
	clean := map[string]string{}
	for k, val := range v {
		if val != "" {
			clean[k] = val
		}
	}
	raw, _ := json.Marshal(clean)
	return "vmess://" + b64enc(raw)
}

func buildVlessLink(p map[string]string, name string) string {
	q := url.Values{}
	q.Set("encryption", orDefault(p["encryption"], "none"))
	if v := p["flow"]; v != "" {
		q.Set("flow", v)
	}
	net := orDefault(p["net"], "tcp")
	q.Set("type", net)
	sec := ""
	if p["tls"] == "1" {
		if p["reality"] == "1" {
			sec = "reality"
			q.Set("pbk", p["pbk"])
			q.Set("sid", p["sid"])
		} else {
			sec = "tls"
		}
		q.Set("sni", p["sni"])
		if v := p["fp"]; v != "" {
			q.Set("fp", v)
		}
		if v := p["alpn"]; v != "" {
			q.Set("alpn", v)
		}
		if truthy(p["insecure"]) {
			q.Set("allowInsecure", "1")
		}
	}
	q.Set("security", sec)
	switch net {
	case "ws", "httpupgrade":
		q.Set("path", p["path"])
		q.Set("host", p["host"])
	case "grpc":
		q.Set("serviceName", orDefault(p["service_name"], strings.Trim(p["path"], "/")))
	case "http", "h2":
		q.Set("path", p["path"])
		q.Set("host", p["host"])
	case "kcp":
		q.Set("headerType", p["header_type"])
	}
	u := &url.URL{
		Scheme:   "vless",
		User:     url.User(p["uuid"]),
		Host:     p["server"] + ":" + p["port"],
		RawQuery: q.Encode(),
		Fragment: name,
	}
	return u.String()
}

func buildTrojanLink(p map[string]string, name string) string {
	q := url.Values{}
	net := orDefault(p["net"], "tcp")
	q.Set("type", net)
	if p["tls"] == "1" {
		q.Set("security", "tls")
		q.Set("sni", p["sni"])
		if v := p["fp"]; v != "" {
			q.Set("fp", v)
		}
		if v := p["alpn"]; v != "" {
			q.Set("alpn", v)
		}
		if truthy(p["insecure"]) {
			q.Set("allowInsecure", "1")
		}
	}
	switch net {
	case "ws", "httpupgrade":
		q.Set("path", p["path"])
		q.Set("host", p["host"])
	case "grpc":
		q.Set("serviceName", orDefault(p["service_name"], strings.Trim(p["path"], "/")))
	}
	u := &url.URL{
		Scheme:   "trojan",
		User:     url.User(p["password"]),
		Host:     p["server"] + ":" + p["port"],
		RawQuery: q.Encode(),
		Fragment: name,
	}
	return u.String()
}

func buildSSLink(p map[string]string, name string) string {
	userinfo := b64enc([]byte(orDefault(p["method"], "aes-256-gcm") + ":" + p["password"]))
	u := &url.URL{
		Scheme:   "ss",
		Host:     fmt.Sprintf("%s@%s:%s", userinfo, p["server"], p["port"]),
		Fragment: name,
	}
	return u.String()
}

func buildSocksLink(proto string, p map[string]string, name string) string {
	var user *url.Userinfo
	if u := p["username"]; u != "" {
		user = url.UserPassword(u, p["password"])
	}
	u := &url.URL{
		Scheme:   proto,
		User:     user,
		Host:     p["server"] + ":" + p["port"],
		Fragment: name,
	}
	return u.String()
}

func hostPort(p map[string]string) string {
	h := p["server"]
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		h = "[" + h + "]"
	}
	return h + ":" + p["port"]
}

func buildHy2Link(p map[string]string, name string) string {
	q := url.Values{}
	if v := p["sni"]; v != "" {
		q.Set("sni", v)
	}
	if truthy(p["insecure"]) {
		q.Set("insecure", "1")
	}
	if v := p["obfs"]; v != "" {
		q.Set("obfs", v)
		q.Set("obfs-password", p["obfs_password"])
	}
	if v := p["server_ports"]; v != "" {
		q.Set("mport", v)
	}
	if v := p["up_mbps"]; v != "" {
		q.Set("upmbps", v)
	}
	if v := p["down_mbps"]; v != "" {
		q.Set("downmbps", v)
	}
	u := &url.URL{Scheme: "hysteria2", User: url.User(p["auth"]), Host: hostPort(p),
		RawQuery: q.Encode(), Fragment: name}
	return u.String()
}

func buildTuicLink(p map[string]string, name string) string {
	q := url.Values{}
	for _, k := range []string{"congestion_control", "udp_relay_mode", "sni", "alpn"} {
		if v := p[k]; v != "" {
			q.Set(k, v)
		}
	}
	if truthy(p["insecure"]) {
		q.Set("allow_insecure", "1")
	}
	u := &url.URL{Scheme: "tuic", User: url.UserPassword(p["uuid"], p["password"]),
		Host: hostPort(p), RawQuery: q.Encode(), Fragment: name}
	return u.String()
}

func buildWireguardLink(p map[string]string, name string) string {
	q := url.Values{}
	q.Set("publickey", p["peer_public_key"])
	for k, qk := range map[string]string{"pre_shared_key": "presharedkey", "local_address": "address",
		"mtu": "mtu", "reserved": "reserved"} {
		if v := p[k]; v != "" {
			q.Set(qk, v)
		}
	}
	u := &url.URL{Scheme: "wireguard", User: url.User(p["private_key"]),
		Host: hostPort(p), RawQuery: q.Encode(), Fragment: name}
	return u.String()
}

func buildHysteria1Link(p map[string]string, name string) string {
	q := url.Values{}
	if v := p["auth"]; v != "" {
		q.Set("auth", v)
	}
	if v := p["sni"]; v != "" {
		q.Set("peer", v)
	}
	if truthy(p["insecure"]) {
		q.Set("insecure", "1")
	}
	for k, qk := range map[string]string{"up_mbps": "upmbps", "down_mbps": "downmbps",
		"alpn": "alpn", "obfs": "obfsParam"} {
		if v := p[k]; v != "" {
			q.Set(qk, v)
		}
	}
	u := &url.URL{Scheme: "hysteria", Host: hostPort(p), RawQuery: q.Encode(), Fragment: name}
	return u.String()
}

func buildAnyTLSLink(p map[string]string, name string) string {
	q := url.Values{}
	if v := p["sni"]; v != "" {
		q.Set("sni", v)
	}
	if truthy(p["insecure"]) {
		q.Set("insecure", "1")
	}
	u := &url.URL{Scheme: "anytls", User: url.User(p["password"]), Host: hostPort(p),
		RawQuery: q.Encode(), Fragment: name}
	return u.String()
}

// buildSSHLink: link SSH membawa SEMUA setingan SSH, bukan cuma
// akunnya. Sebelumnya link hanya berisi user/password/host/port,
// sehingga copy (Bagikan / Export) lalu import kehilangan payload,
// proxy, TLS/SNI, mode, dan private key — server hasil copy jadi
// SSH polos dan tidak jalan seperti aslinya. Sekarang:
//   - private_key ikut sebagai ?pk= (sudah dibaca parseSSH sejak
//     lama, tapi dulu tidak pernah ditulis buildSSHLink);
//   - semua param ssh_* (ssh_payload_on, ssh_enhanced, ssh_payload,
//     ssh_proxy, ssh_tls, ssh_sni, ssh_sni_ver, ssh_insecure,
//     ssh_mode, ssh_raw, dan ssh_* baru di masa depan) ikut sebagai
//     query dengan nama yang sama, jadi parseSSH bisa membacanya
//     kembali apa adanya. Aplikasi lain mengabaikan query yang
//     tidak dikenalnya, jadi link tetap bisa diimport sebagai akun
//     SSH polos di luar TaptapBox, tapi TaptapBox-ke-TaptapBox
//     tidak kehilangan setingan lagi. Nilai kosong tidak ditulis.
func buildSSHLink(p map[string]string, name string) string {
	var user *url.Userinfo
	if p["password"] != "" {
		user = url.UserPassword(p["username"], p["password"])
	} else {
		user = url.User(p["username"])
	}
	q := url.Values{}
	if v := p["private_key"]; v != "" {
		q.Set("pk", v)
	}
	for k, v := range p {
		if strings.HasPrefix(k, "ssh_") && v != "" {
			q.Set(k, v)
		}
	}
	u := &url.URL{Scheme: "ssh", User: user, Host: hostPort(p), RawQuery: q.Encode(), Fragment: name}
	return u.String()
}
