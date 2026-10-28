package main

import "testing"

// Perbaikan timeout intermiten sing-box (2026-10-27): outbound SSH
// sing-box harus membawa connect_timeout panjang (dial jembatan bisa
// makan beberapa detik) dan client_version tetap (bukan acak).
func TestSingboxSSHOutboundTimeoutAndVersion(t *testing.T) {
	sv := &Server{
		Proto: "ssh",
		Params: map[string]string{
			"server": "example.example", "username": "taptap", "password": "pw",
		},
	}
	ob, _ := serverToOutbound(sv, "Teskan")
	if ob == nil {
		t.Fatalf("outbound SSH hilang")
	}
	if ob["type"] != "ssh" {
		t.Fatalf("type = %v, mau ssh", ob["type"])
	}
	if ob["connect_timeout"] != "20s" {
		t.Fatalf("connect_timeout = %v, mau 20s", ob["connect_timeout"])
	}
	if ob["client_version"] != "SSH-2.0-OpenSSH_7.8" {
		t.Fatalf("client_version = %v, mau SSH-2.0-OpenSSH_7.8", ob["client_version"])
	}
	// KEX/cipher/mac sengaja TIDAK diset: negosiasi KEX sudah terbukti
	// sukses dengan irisan default (Dropbear 2019.78), jadi field itu
	// tidak boleh muncul sebagai tebakan.
	if _, ok := ob["kex_algorithm"]; ok {
		t.Fatalf("kex_algorithm tidak boleh diset tanpa bukti")
	}
	if _, ok := ob["cipher"]; ok {
		t.Fatalf("cipher tidak boleh diset tanpa bukti")
	}
	if _, ok := ob["mac"]; ok {
		t.Fatalf("mac tidak boleh diset tanpa bukti")
	}
}
