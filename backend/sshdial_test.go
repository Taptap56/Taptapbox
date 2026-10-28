package main

import (
	"errors"
	"net"
	"testing"
	"time"
)

// Perbaikan dial jembatan 2026-10-27: timeout dial/payload
// dipersingkat dari 15 detik ke 5 detik agar satu percobaan gagal
// tidak memakan anggaran connect_timeout sing-box (20 detik),
// dan timeout ikut di-retry sampai 3 kali (dulu tidak).
func TestSSHShortDialTimeouts(t *testing.T) {
	if sshDialTimeout != 5*time.Second {
		t.Fatalf("sshDialTimeout = %v, mau 5s", sshDialTimeout)
	}
	if sshPayloadDeadline != 5*time.Second {
		t.Fatalf("sshPayloadDeadline = %v, mau 5s", sshPayloadDeadline)
	}
}

func TestDialSSHWithRetryTimeout(t *testing.T) {
	old := sshRetryBackoff
	sshRetryBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { sshRetryBackoff = old }()
	calls := 0
	dialFn := func(prof sshBridgeProfile) (net.Conn, []string, error) {
		calls++
		if calls < 3 {
			return nil, nil, errors.New("read tcp 127.0.0.1:1->127.0.0.2:40343: i/o timeout")
		}
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, []string{"HTTP/1.1 101 Switching Protocols"}, nil
	}
	conn, _, attempts, err := dialSSHWithRetry(sshBridgeProfile{}, dialFn)
	if err != nil || conn == nil || attempts != 3 || calls != 3 {
		t.Fatalf("timeout intermiten harus berhasil di percobaan ke-3, attempts=%d calls=%d err=%v", attempts, calls, err)
	}
	_ = conn.Close()
}
