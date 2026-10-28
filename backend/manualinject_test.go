package main

import (
	"strings"
	"testing"
)

func TestMihomoMixedPort(t *testing.T) {
	if p, ok := mihomoMixedPort([]byte("mixed-port: 10809\nproxies: []\n")); !ok || p != 10809 {
		t.Fatalf("mixed-port terbaca = %d, %v", p, ok)
	}
	if _, ok := mihomoMixedPort([]byte("proxies: []\n")); ok {
		t.Fatalf("tanpa mixed-port harus false")
	}
	if _, ok := mihomoMixedPort([]byte("mixed-port: 0\n")); ok {
		t.Fatalf("mixed-port 0 harus false")
	}
}

func TestInjectMixedPortYAML(t *testing.T) {
	out, err := injectMixedPortYAML([]byte("mode: rule\nproxies: []\n"), 10809)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := mihomoMixedPort(out); !ok || p != 10809 {
		t.Fatalf("hasil injeksi terbaca = %d, %v", p, ok)
	}
	// Kunci lain utuh.
	if string(out) == "" || !strings.Contains(string(out), "mode: rule") {
		t.Fatalf("kunci lain hilang: %s", out)
	}
	// Nilai yang sudah ada tak ditimpa oleh ensure.
	data := []byte("mixed-port: 9999\n")
	if p, ok := mihomoMixedPort(data); !ok || p != 9999 {
		t.Fatalf("port user harus dihormati, dapat %d, %v", p, ok)
	}
}

func TestSingboxMixedPort(t *testing.T) {
	with := []byte(`{"inbounds": [{"type": "mixed", "listen_port": 10809}]}`)
	if p, ok := singboxMixedPort(with); !ok || p != 10809 {
		t.Fatalf("mixed inbound terbaca = %d, %v", p, ok)
	}
	without := []byte(`{"inbounds": [{"type": "tun", "tag": "tun-in"}]}`)
	if _, ok := singboxMixedPort(without); ok {
		t.Fatalf("tanpa mixed harus false")
	}
}

func TestInjectMixedInboundJSON(t *testing.T) {
	out, err := injectMixedInboundJSON([]byte(`{"inbounds": [{"type": "tun", "tag": "tun-in"}], "outbounds": []}`), 10809)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := singboxMixedPort(out); !ok || p != 10809 {
		t.Fatalf("hasil injeksi terbaca = %d, %v", p, ok)
	}
	if !strings.Contains(string(out), "tun-in") {
		t.Fatalf("inbound lama hilang: %s", out)
	}
}
