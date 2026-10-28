// outbound.go — helper konversi Server/params untuk TaptapClash (core mihomo).
// Konversi utama ada di mihomo.go (serverToMihomoProxy).

package main

import (
	"strconv"
	"strings"
)

func atoi(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return def
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func defaultPort(proto string) int {
	switch proto {
	case "ssh":
		return 22
	case "socks":
		return 1080
	case "wireguard":
		return 51820
	}
	return 443
}

// validServer: parameter minimum agar proxy valid. Server tidak valid
// dilewati saat build config (bukan menggagalkan seluruh config).
func validServer(sv *Server) bool {
	p := sv.Params
	if strings.TrimSpace(p["server"]) == "" {
		return false
	}
	switch sv.Proto {
	case "vmess", "vless":
		return p["uuid"] != ""
	case "tuic":
		return p["uuid"] != ""
	case "trojan":
		return p["password"] != ""
	case "ss":
		return p["method"] != ""
	case "wireguard":
		return p["private_key"] != "" && p["peer_public_key"] != ""
	case "ssh":
		return p["password"] != "" || p["private_key"] != ""
	case "hy2":
		return p["auth"] != ""
	}
	return true
}
