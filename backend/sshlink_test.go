package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Copy SSH harus membawa payload + setingan lain, bukan cuma akun.
// Sebelum perbaikan, buildSSHLink hanya menulis user/password/host/
// port, sehingga import hasil copy jadi SSH polos (tanpa payload,
// tanpa proxy, tanpa mode) dan tidak jalan seperti aslinya.
func TestSSHLinkRoundTrip(t *testing.T) {
	p := map[string]string{
		"server": "akunvpn.nadia-lestari.my.id", "port": "80",
		"username": "taptap", "password": "1234",
		"ssh_payload_on": "1", "ssh_enhanced": "1",
		"ssh_proxy":   "104.17.3.81:80",
		"ssh_payload": "HEAD / HTTP/1.1[crlf]Host: edu.ruangguru.com:80[crlf]Connection: Keep-Alive[crlf]User-Agent: [ua][crlf][crlf]BMOVE / HTTP/1.1[crlf]Host: [host][crlf]Upgrade: websocket[crlf][crlf]",
		"ssh_mode":    "ws",
		"ssh_tls":     "", "ssh_sni": "", "ssh_sni_ver": "", "ssh_insecure": "",
	}
	link := buildLink("ssh", p, "Edukasi")
	sv, err := parseLink(link)
	if err != nil {
		t.Fatalf("import link hasil copy gagal: %v (link=%s)", err, link)
	}
	for _, k := range []string{"server", "port", "username", "password",
		"ssh_payload_on", "ssh_enhanced", "ssh_proxy", "ssh_payload", "ssh_mode"} {
		if sv.Params[k] != p[k] {
			t.Fatalf("param %s hilang/berubah: dapat %q, mau %q (link=%s)", k, sv.Params[k], p[k], link)
		}
	}
	if sv.Name != "Edukasi" {
		t.Fatalf("nama = %q, mau Edukasi", sv.Name)
	}
	// SSH polos dari aplikasi lain tetap bisa diimport, tanpa ssh_*.
	sv2, err := parseLink("ssh://user:pw@h.example:22#Polos")
	if err != nil || sv2.Params["server"] != "h.example" || sv2.Params["username"] != "user" {
		t.Fatalf("SSH polos harus tetap bisa diimport, err=%v sv=%v", err, sv2)
	}
	// Private key ikut bila ada, via ?pk= yang sudah dibaca parseSSH.
	p2 := map[string]string{"server": "h.example", "port": "22", "username": "u", "password": "p",
		"private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----"}
	sv3, err := parseLink(buildLink("ssh", p2, "Kunci"))
	if err != nil || sv3.Params["private_key"] != p2["private_key"] {
		t.Fatalf("private key harus ikut copy, err=%v dapat %q", err, sv3.Params["private_key"])
	}
}

// Celah yang ditemukan 2026-10-27 dari laporan lapangan: server SSH
// lama menyimpan Link lama (tanpa ssh_*) di servers.json, dan
// Bagikan/Export memakai Link tersimpan itu — jadi copy tetap
// kehilangan payload walau buildSSHLink sudah diperbaiki, kecuali
// servernya diedit & disimpan ulang dulu. NewStore harus menyegarkan
// link SSH dari Params saat load, supaya Bagikan langsung benar
// tanpa perlu edit ulang.
func TestSSHLinkRefreshOnLoad(t *testing.T) {
	dir := t.TempDir()
	servers := []Server{{
		ID: "1", Name: "Edukasi", Proto: "ssh", Enabled: true,
		// Link lama, hanya akun — persis yang dihasilkan build lama.
		Link: "ssh://taptap:1234@akunvpn.nadia-lestari.my.id:80#Edukasi",
		Params: map[string]string{
			"server": "akunvpn.nadia-lestari.my.id", "port": "80",
			"username": "taptap", "password": "1234",
			"ssh_payload_on": "1", "ssh_enhanced": "1",
			"ssh_proxy":   "104.17.3.81:80",
			"ssh_payload": "HEAD / HTTP/1.1[crlf]Host: [host][crlf][crlf]",
			"ssh_mode":    "ws",
		},
	}}
	raw, _ := json.Marshal(servers)
	if err := os.WriteFile(filepath.Join(dir, "servers.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	st := NewStore(dir)
	if len(st.Servers) != 1 {
		t.Fatalf("server hilang setelah load, dapat %d", len(st.Servers))
	}
	link := st.Servers[0].Link
	for _, want := range []string{"ssh_payload_on=1", "ssh_proxy=", "ssh_payload=", "ssh_mode=ws"} {
		if !strings.Contains(link, want) {
			t.Fatalf("link setelah load harus membawa %q, dapat %s", want, link)
		}
	}
	// Link segar itu harus bisa diimport kembali dengan payload utuh.
	sv, err := parseLink(link)
	if err != nil || sv.Params["ssh_payload_on"] != "1" || sv.Params["ssh_proxy"] != "104.17.3.81:80" {
		t.Fatalf("link segar harus bisa diimport dengan payload, err=%v params=%v", err, sv.Params)
	}
}
