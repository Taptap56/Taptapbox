package main

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSSHRetryable(t *testing.T) {
	if !sshRetryable(errors.New("upgrade websocket ditolak: HTTP/1.1 301"), time.Second) {
		t.Fatal("penolakan cepat harus bisa dicoba lagi")
	}
	// Perbaikan dial 2026-10-27: timeout JUGA diulang bila
	// percobaannya sendiri masih pendek (<12 detik), karena
	// timeout lapangan terbukti intermiten, bukan permanen.
	if !sshRetryable(errors.New("i/o timeout"), time.Second) {
		t.Fatal("timeout pendek harus bisa dicoba lagi")
	}
	if sshRetryable(errors.New("i/o timeout"), 13*time.Second) {
		t.Fatal("percobaan yang sendiri sudah >=12 detik tidak boleh diulang")
	}
	if sshRetryable(nil, time.Second) {
		t.Fatal("tanpa error tidak ada yang diulang")
	}
}

func TestDialSSHWithRetry(t *testing.T) {
	old := sshRetryBackoff
	sshRetryBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { sshRetryBackoff = old }()
	calls := 0
	dialFn := func(prof sshBridgeProfile) (net.Conn, []string, error) {
		calls++
		if calls < 3 {
			return nil, []string{"HTTP/1.1 301 Moved Permanently"}, errors.New("upgrade websocket ditolak")
		}
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, []string{"HTTP/1.1 301 Moved Permanently", "HTTP/1.1 101 Switching Protocols"}, nil
	}
	conn, resp, attempts, err := dialSSHWithRetry(sshBridgeProfile{}, dialFn)
	if err != nil || conn == nil || attempts != 3 {
		t.Fatalf("harus berhasil di percobaan ke-3, attempts=%d err=%v", attempts, err)
	}
	_ = conn.Close()
	if len(resp) != 2 || !strings.Contains(resp[1], "101") {
		t.Fatalf("respons terakhir harus 101, dapat %v", resp)
	}
	// Gagal permanen: berhenti di 3, bukan selamanya.
	calls = 0
	failFn := func(prof sshBridgeProfile) (net.Conn, []string, error) {
		calls++
		return nil, []string{"HTTP/1.1 403 Forbidden"}, errors.New("proxy menolak")
	}
	if _, _, attempts, err := dialSSHWithRetry(sshBridgeProfile{}, failFn); err == nil || attempts != 3 || calls != 3 {
		t.Fatalf("gagal permanen harus berhenti di 3, attempts=%d calls=%d err=%v", attempts, calls, err)
	}
}

func TestSSHDetailPlain(t *testing.T) {
	d := sshDetail(sshLogEntry{OK: true, Attempts: 2, Responses: []string{"HTTP/1.1 301 Moved Permanently", "HTTP/1.1 101 Switching Protocols"}})
	if !strings.Contains(d, "Terhubung") || !strings.Contains(d, "301 lalu 101") || !strings.Contains(d, "2 percobaan") {
		t.Fatalf("kalimat log harus polos dan lengkap: %q", d)
	}
	d2 := sshDetail(sshLogEntry{OK: false, Attempts: 3, Responses: []string{"HTTP/1.1 403 Forbidden"}, Error: "proxy menolak"})
	if !strings.Contains(d2, "Gagal") || !strings.Contains(d2, "403") {
		t.Fatalf("kalimat gagal harus menyebut status: %q", d2)
	}
}

func TestSSHAccountsSummaryAll(t *testing.T) {
	a := &App{store: &Store{Settings: Settings{}}}
	a.store.Servers = []Server{
		{ID: "1", Name: "Teskan", Proto: "ssh", Enabled: true, Params: map[string]string{"server": "h.example", "username": "u", "password": "p", "ssh_payload_on": "1", "ssh_proxy": "1.1.1.1:80", "ssh_payload": "GET /[crlf][crlf]"}},
		{ID: "2", Name: "SSH Polos", Proto: "ssh", Enabled: true, Params: map[string]string{"server": "x.example", "username": "u", "password": "p"}},
		{ID: "3", Name: "VMess", Proto: "vmess", Enabled: true, Params: map[string]string{"server": "y"}},
	}
	a.recordSSHLog(sshLogEntry{Time: "02:00:00", ServerID: "1", Name: "Teskan", Proxy: "1.1.1.1:80", Mode: "WebSocket", Responses: []string{"HTTP/1.1 101 Switching Protocols"}, OK: true, Attempts: 1})
	got := a.sshAccountsSummary()
	if len(got) != 2 {
		t.Fatalf("harus 2 akun SSH (vmess tidak ikut), dapat %d", len(got))
	}
	if got[0]["bridge"] != true || got[1]["bridge"] != false {
		t.Fatalf("jembatan harus beda per akun: %v", got)
	}
	if _, ok := got[0]["last"]; !ok {
		t.Fatal("akun berlog harus membawa entri terakhirnya")
	}
}
