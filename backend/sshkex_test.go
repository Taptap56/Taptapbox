package main

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// buildKEXPacket: paket KEXINIT sintetis (banner + paket biner) untuk tes parser.
func buildKEXPacket(t *testing.T, lists [][]string) []byte {
	t.Helper()
	var payload bytes.Buffer
	payload.WriteByte(20)
	payload.Write(make([]byte, 16)) // cookie dummy, isinya tidak dibaca parser
	for _, l := range lists {
		s := strings.Join(l, ",")
		var lb [4]byte
		binary.BigEndian.PutUint32(lb[:], uint32(len(s)))
		payload.Write(lb[:])
		payload.WriteString(s)
	}
	payload.WriteByte(0) // first_kex_packet_follows = false
	payload.Write(make([]byte, 4))
	padLen := 8
	pktLen := 1 + payload.Len() + padLen
	var pkt bytes.Buffer
	var lb [4]byte
	binary.BigEndian.PutUint32(lb[:], uint32(pktLen))
	pkt.Write(lb[:])
	pkt.WriteByte(byte(padLen))
	pkt.Write(payload.Bytes())
	pkt.Write(make([]byte, padLen))
	out := append([]byte("SSH-2.0-TestServer_1.0\r\n"), pkt.Bytes()...)
	return out
}

func kexLists() [][]string {
	return [][]string{
		{"curve25519-sha256", "ecdh-sha2-nistp256", "diffie-hellman-group14-sha256"},
		{"ssh-ed25519", "rsa-sha2-256"},
		{"aes128-ctr", "aes256-ctr"},
		{"aes128-ctr"},
		{"hmac-sha2-256", "hmac-sha1"},
		{"hmac-sha2-256"},
		{"none"},
		{"none"},
		{""},
		{""},
	}
}

func TestParseSSHHandshake(t *testing.T) {
	data := buildKEXPacket(t, kexLists())
	info, err := parseSSHHandshake(data)
	if err != nil {
		t.Fatalf("parse gagal: %v", err)
	}
	if info.Banner != "SSH-2.0-TestServer_1.0" {
		t.Fatalf("banner = %q", info.Banner)
	}
	if strings.Join(info.KEX, ",") != "curve25519-sha256,ecdh-sha2-nistp256,diffie-hellman-group14-sha256" {
		t.Fatalf("kex = %v", info.KEX)
	}
	if strings.Join(info.HostKey, ",") != "ssh-ed25519,rsa-sha2-256" {
		t.Fatalf("hostkey = %v", info.HostKey)
	}
	if strings.Join(info.CipherC2S, ",") != "aes128-ctr,aes256-ctr" || strings.Join(info.CipherS2C, ",") != "aes128-ctr" {
		t.Fatalf("cipher c2s=%v s2c=%v", info.CipherC2S, info.CipherS2C)
	}
	if strings.Join(info.MACC2S, ",") != "hmac-sha2-256,hmac-sha1" {
		t.Fatalf("mac = %v", info.MACC2S)
	}
}

func TestParseSSHHandshakeErrors(t *testing.T) {
	// Terpotong di tengah paket.
	data := buildKEXPacket(t, kexLists())
	if _, err := parseSSHHandshake(data[:40]); err == nil {
		t.Fatalf("paket terpotong harus gagal")
	}
	// Banner bukan SSH.
	if _, err := parseSSHHandshake([]byte("HTTP/1.1 200 OK\r\n\r\n")); err == nil {
		t.Fatalf("banner non-SSH harus gagal")
	}
	// Paket pertama bukan KEXINIT (type 30, bukan 20).
	bad := buildKEXPacket(t, kexLists())
	// type byte ada di: sesudah banner + 4 len + 1 padlen.
	nl := bytes.IndexByte(bad, '\n')
	bad[nl+1+4+1] = 30
	if _, err := parseSSHHandshake(bad); err == nil {
		t.Fatalf("paket non-KEXINIT harus gagal")
	}
	// Data sesudah KEXINIT tidak mengganggu parse (relay buffer berisi lebih).
	if _, err := parseSSHHandshake(append(data, []byte("buntut sesi lain")...)); err != nil {
		t.Fatalf("data tambahan harus diabaikan: %v", err)
	}
}

func TestTapReaderCaps(t *testing.T) {
	tr := &tapReader{r: strings.NewReader(strings.Repeat("x", 20000))}
	buf := make([]byte, 4096)
	total := 0
	for {
		n, err := tr.Read(buf)
		total += n
		if err != nil {
			break
		}
	}
	if total != 20000 {
		t.Fatalf("total = %d, mau 20000", total)
	}
	if len(tr.buf) != 8192 {
		t.Fatalf("buffer tap = %d, mau 8192", len(tr.buf))
	}
	if tr.n != 20000 {
		t.Fatalf("count = %d, mau 20000", tr.n)
	}
}
