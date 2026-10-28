package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
)

// hsParseCIDR: "192.168.56.1" -> "192.168.56.1/24", bentuk CIDR dipakai apa adanya.
func TestHsParseCIDR(t *testing.T) {
	ok := map[string]string{
		"192.168.56.1":    "192.168.56.1/24",
		" 192.168.56.1 ":  "192.168.56.1/24",
		"192.168.56.1/24": "192.168.56.1/24",
		"10.0.0.1/8":      "10.0.0.1/8",
	}
	for in, want := range ok {
		got, valid := hsParseCIDR(in)
		if !valid || got != want {
			t.Errorf("hsParseCIDR(%q) = %q,%v; mau %q,true", in, got, valid, want)
		}
	}
	for _, in := range []string{"", "abc", "192.168.56.1/33", "2001:db8::1", "192.168.56.256", "56.1"} {
		if _, valid := hsParseCIDR(in); valid {
			t.Errorf("hsParseCIDR(%q) seharusnya tidak valid", in)
		}
	}
}

// Default + migrasi: kunci IP hotspot aktif dengan 192.168.56.1.
func TestHotspotIPDefaults(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if !s.Settings.HotspotLockIP {
		t.Error("HotspotLockIP harus true (default)")
	}
	if s.Settings.HotspotIP != "192.168.56.1" {
		t.Errorf("HotspotIP harus 192.168.56.1, dapat %q", s.Settings.HotspotIP)
	}
	if c, ok := hsParseCIDR(s.Settings.HotspotIP); !ok || c != "192.168.56.1/24" {
		t.Errorf("HotspotIP default harus lolos hsParseCIDR, dapat %q,%v", c, ok)
	}
}

// handleServerOrder dengan ids: susunan manual dari drag & drop WebUI.
func TestServerOrderManual(t *testing.T) {
	a := testApp(t)
	a.store.Servers = []Server{
		{ID: "a", Name: "A"},
		{ID: "b", Name: "B"},
		{ID: "c", Name: "C"},
	}
	call := func(body string) {
		r := httptest.NewRequest("POST", "/api/servers/order", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1"
		w := httptest.NewRecorder()
		a.handleServerOrder(w, r)
		if w.Code != 200 {
			t.Fatalf("order harus 200, dapat %d", w.Code)
		}
	}
	order := func() string {
		var ids []string
		for _, s := range a.store.Servers {
			ids = append(ids, s.ID)
		}
		return strings.Join(ids, ",")
	}
	call(`{"ids":["c","a","b"]}`)
	if got := order(); got != "c,a,b" {
		t.Fatalf("urutan harus c,a,b; dapat %s", got)
	}
	// id tak dikenal diabaikan; id yang tak disebut tetap di belakang
	call(`{"ids":["b","x"]}`)
	if got := order(); got != "b,c,a" {
		t.Fatalf("urutan harus b,c,a; dapat %s", got)
	}
	// mode lama by=name tetap jalan
	call(`{"by":"name"}`)
	if got := order(); got != "a,b,c" {
		t.Fatalf("urutan by=name harus a,b,c; dapat %s", got)
	}
}

// /api/hotspot: status kunci IP untuk indikator Pengaturan.
func TestHotspotStatusEndpoint(t *testing.T) {
	a := testApp(t)
	a.store.Settings.Hotspot = true
	a.store.Settings.HotspotLockIP = true
	a.store.Settings.HotspotIP = "192.168.56.1"
	r := httptest.NewRequest("GET", "/api/hotspot", nil)
	r.RemoteAddr = "127.0.0.1:1"
	w := httptest.NewRecorder()
	a.handleHotspotStatus(w, r)
	if w.Code != 200 {
		t.Fatalf("status harus 200, dapat %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["active"] != true || got["lock_ip"] != true {
		t.Errorf("active/lock_ip harus true: %v", got)
	}
	if got["ip"] != "192.168.56.1/24" {
		t.Errorf("ip harus 192.168.56.1/24, dapat %v", got["ip"])
	}
	if _, ok := got["ap_ifaces"].([]any); !ok {
		t.Errorf("ap_ifaces harus array: %v", got)
	}
}

// v1.0.35: peringatan "tidak ada interface AP" hanya sekali per kondisi
// hilang (bukan tiap tick) agar tidak memenuhi batas 200 baris log sesi.
func TestHsNoAPWarnOnce(t *testing.T) {
	oldApplied, oldCidr, oldWarned := hsIPApplied, hsIPCidr, hsNoAPWarned
	defer func() { hsIPApplied, hsIPCidr, hsNoAPWarned = oldApplied, oldCidr, oldWarned }()
	hsIPApplied, hsIPCidr, hsNoAPWarned = nil, "", false
	var buf bytes.Buffer
	a := &App{store: &Store{Settings: Settings{HotspotLockIP: true, HotspotIP: "192.168.56.1"}}, logger: log.New(&buf, "", 0)}
	hsMu.Lock()
	a.hsEnsureLockIP()
	first := buf.String()
	buf.Reset()
	a.hsEnsureLockIP()
	second := buf.String()
	hsMu.Unlock()
	if !strings.Contains(first, "tidak ada interface AP") {
		t.Fatalf("peringatan pertama harus ada: %q", first)
	}
	if strings.Contains(second, "tidak ada interface AP") {
		t.Fatalf("peringatan kedua tidak boleh ditulis: %q", second)
	}
}
