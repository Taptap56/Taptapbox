// obparse.go — mihomoProxyToParams: kebalikan serverToMihomoProxy.
// Mengubah satu proxy Mihomo (dari config.yaml aktif) kembali menjadi
// Params ala v2rayNG untuk form edit server.

package main

import (
	"fmt"
	"strings"
)

// mihomoProxyToParams mengubah proxy mihomo (map YAML) menjadi
// (proto, params ala v2rayNG).
func mihomoProxyToParams(px map[string]any) (proto string, p map[string]string) {
	p = map[string]string{}
	typ, _ := px["type"].(string)
	str := func(k string) string {
		if v, ok := px[k].(string); ok {
			return v
		}
		return ""
	}
	num := func(k string) string {
		switch v := px[k].(type) {
		case int:
			return fmt.Sprintf("%d", v)
		case int64:
			return fmt.Sprintf("%d", v)
		case float64:
			return fmt.Sprintf("%.0f", v)
		case string:
			return v
		}
		return ""
	}
	boolStr := func(k string) string {
		if v, ok := px[k].(bool); ok && v {
			return "1"
		}
		return ""
	}
	if s := str("server"); s != "" {
		p["server"] = s
	}
	if v := num("port"); v != "" {
		p["port"] = v
	}
	// TLS umum
	if _, ok := px["tls"].(bool); ok {
		if b, _ := px["tls"].(bool); b {
			p["tls"] = "1"
		}
	}
	if s := str("servername"); s != "" {
		p["sni"] = s
	} else if s := str("sni"); s != "" {
		p["sni"] = s
	}
	if v := boolStr("skip-cert-verify"); v != "" {
		p["insecure"] = v
	}
	if alpn, ok := px["alpn"].([]any); ok {
		ss := []string{}
		for _, x := range alpn {
			if s, ok := x.(string); ok {
				ss = append(ss, s)
			}
		}
		if len(ss) > 0 {
			p["alpn"] = strings.Join(ss, ",")
		}
	}
	if s := str("fingerprint"); s != "" {
		p["fp"] = s
	}
	if ro, ok := px["reality-opts"].(map[string]any); ok {
		p["reality"] = "1"
		if s, ok := ro["public-key"].(string); ok {
			p["pbk"] = s
		}
		if s, ok := ro["short-id"].(string); ok {
			p["sid"] = s
		}
	}
	// transport
	switch str("network") {
	case "ws":
		p["net"] = "ws"
		if wo, ok := px["ws-opts"].(map[string]any); ok {
			if s, ok := wo["path"].(string); ok {
				p["path"] = s
			}
			if h, ok := wo["headers"].(map[string]any); ok {
				if s, ok := h["Host"].(string); ok {
					p["host"] = s
				}
			}
			if v, ok := wo["v2ray-http-upgrade"].(bool); ok && v {
				p["net"] = "httpupgrade"
			}
		}
	case "grpc":
		p["net"] = "grpc"
		if go_, ok := px["grpc-opts"].(map[string]any); ok {
			if s, ok := go_["grpc-service-name"].(string); ok {
				p["service_name"] = s
			}
		}
	case "h2":
		p["net"] = "h2"
		if ho, ok := px["h2-opts"].(map[string]any); ok {
			if s, ok := ho["path"].(string); ok {
				p["path"] = s
			}
		}
	case "http":
		p["net"] = "tcp"
		p["header_type"] = "http"
	}
	// per tipe
	switch typ {
	case "vmess":
		proto = "vmess"
		p["uuid"] = str("uuid")
		p["security"] = str("cipher")
		p["alter_id"] = num("alterId")
	case "vless":
		proto = "vless"
		p["uuid"] = str("uuid")
		p["flow"] = str("flow")
	case "trojan":
		proto = "trojan"
		p["password"] = str("password")
	case "ss", "shadowsocks":
		proto = "ss"
		p["method"] = str("cipher")
		p["password"] = str("password")
		p["plugin"] = str("plugin")
	case "hysteria2":
		proto = "hy2"
		p["auth"] = str("password")
		p["up_mbps"] = num("up")
		p["down_mbps"] = num("down")
		p["obfs"] = str("obfs")
		p["obfs_password"] = str("obfs-password")
	case "tuic":
		proto = "tuic"
		p["uuid"] = str("uuid")
		p["password"] = str("password")
		p["congestion_control"] = str("congestion-controller")
		p["udp_relay_mode"] = str("udp-relay-mode")
	case "wireguard":
		proto = "wireguard"
		p["private_key"] = str("private-key")
		p["peer_public_key"] = str("public-key")
		p["pre_shared_key"] = str("pre-shared-key")
		p["local_address"] = str("ip")
		p["mtu"] = num("mtu")
	case "ssh":
		proto = "ssh"
		p["username"] = str("username")
		p["password"] = str("password")
		p["private_key"] = str("private-key")
	case "socks5":
		proto = "socks"
		p["username"] = str("username")
		p["password"] = str("password")
	case "http":
		proto = "http"
		p["username"] = str("username")
		p["password"] = str("password")
	default:
		proto = typ
	}
	return proto, p
}
