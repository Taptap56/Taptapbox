package main

import (
	"strings"
	"testing"
	"time"
)

func testDNSQuery(name string, qtype uint16) []byte {
	q := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, l := range strings.Split(name, ".") {
		q = append(q, byte(len(l)))
		q = append(q, l...)
	}
	return append(q, 0, byte(qtype>>8), byte(qtype), 0, 1)
}

func TestDNSQuestionKeyCaseInsensitive(t *testing.T) {
	k1, qt, qend, ok := dnsQuestion(testDNSQuery("Example.COM", 1))
	if !ok || qt != 1 {
		t.Fatalf("query A harus terbaca: ok=%v qt=%d", ok, qt)
	}
	k2, _, _, _ := dnsQuestion(testDNSQuery("example.com", 1))
	if k1 != k2 {
		t.Fatal("kunci cache harus tak peduli huruf besar/kecil")
	}
	if qend != len(testDNSQuery("example.com", 1)) {
		t.Fatalf("qend salah: %d", qend)
	}
	if _, _, _, ok := dnsQuestion([]byte{1, 2, 3}); ok {
		t.Fatal("paket pendek bukan query valid")
	}
}

func TestDNSEmptyReplyAAAA(t *testing.T) {
	q := testDNSQuery("example.com", 28)
	_, qt, qend, ok := dnsQuestion(q)
	if !ok || qt != 28 {
		t.Fatal("AAAA harus terdeteksi")
	}
	r := dnsEmptyReply(q, qend)
	if r[0] != 0x12 || r[1] != 0x34 {
		t.Fatal("ID harus sama dengan query")
	}
	if r[2]&0x80 == 0 || r[3]&0x0F != 0 || r[6] != 0 || r[7] != 0 {
		t.Fatal("harus respons NOERROR tanpa jawaban")
	}
}

func TestDNSRespTTL(t *testing.T) {
	q := testDNSQuery("example.com", 1)
	r := append([]byte{}, q...)
	r[2], r[3], r[7] = 0x81, 0x80, 1
	r = append(r, 0xC0, 0x0C, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 1, 2, 3, 4)
	ttl, ok := dnsRespTTL(r)
	if !ok || ttl != 60*time.Second {
		t.Fatalf("TTL harus 60s, dapat %v ok=%v", ttl, ok)
	}
	nodata := append([]byte{}, q...)
	nodata[2], nodata[3] = 0x81, 0x80
	if _, ok := dnsRespTTL(nodata); ok {
		t.Fatal("jawaban kosong tidak boleh di-cache")
	}
}
