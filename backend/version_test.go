package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMihomoVersion(t *testing.T) {
	out := "Mihomo Meta v1.19.32 linux arm64 with go1.25.0 mihomo/1.19.32\n"
	if v := parseMihomoVersion(out); v != "1.19.32" {
		t.Fatalf("dapat %q, mau 1.19.32", v)
	}
	// Kasus nyata 2026-10-25: biner diganti 1.19.33 tapi WebUI masih
	// 1.19.32 — parser harus mengambil 1.19.33, bukan versi Go di ekor.
	out = "Mihomo Meta v1.19.33 linux arm64 with go1.25.1 mihomo/1.19.33\n"
	if v := parseMihomoVersion(out); v != "1.19.33" {
		t.Fatalf("dapat %q, mau 1.19.33", v)
	}
	if v := parseMihomoVersion(""); v != "" {
		t.Fatalf("string kosong harus \"\", dapat %q", v)
	}
	if v := parseMihomoVersion("bukan versi sama sekali"); v != "" {
		t.Fatalf("sampah harus \"\", dapat %q", v)
	}
}

func TestParseSingboxVersion(t *testing.T) {
	out := "sing-box version 1.14.3\n\nEnvironment: go1.25.0 android/arm64\nTags: with_gvisor,with_quic\n"
	if v := parseSingboxVersion(out); v != "1.14.3" {
		t.Fatalf("dapat %q, mau 1.14.3", v)
	}
	if v := parseSingboxVersion(""); v != "" {
		t.Fatalf("string kosong harus \"\", dapat %q", v)
	}
}

func TestCoreVersionFallback(t *testing.T) {
	dir := t.TempDir()
	a := &App{mihomoBin: filepath.Join(dir, "mihomo"), singboxBin: filepath.Join(dir, "sing-box")}
	_ = os.WriteFile(a.mihomoBin, []byte("biner-palsu"), 0755)
	_ = os.WriteFile(a.singboxBin, []byte("biner-palsu"), 0755)
	// TaptapBox library: mihomo tidak ada -> "", sing-box library -> fallback 1.14.3.
	if v := a.coreVersion("clash"); v != "" {
		t.Fatalf("clash harus \"\" di Slim library, dapat %q", v)
	}
	if v := a.coreVersion("singbox"); v != singboxLibraryVersion {
		t.Fatalf("fallback singbox %q", v)
	}
	a.verMihomo, a.verSingbox = "9.9.9", "8.8.8"
	if v := a.coreVersion("singbox"); v != "8.8.8" {
		t.Fatalf("deteksi singbox %q", v)
	}
	// Library: sing-box tetap terpasang walau tanpa biner di disk.
	b := &App{mihomoBin: filepath.Join(dir, "tak-ada-mihomo"), singboxBin: filepath.Join(dir, "tak-ada-singbox")}
	if v := b.coreVersion("clash"); v != "" {
		t.Fatalf("biner hilang harus \"\", dapat %q", v)
	}
	if v := b.coreVersion("singbox"); v != singboxLibraryVersion {
		t.Fatalf("singbox library harus %s, dapat %q", singboxLibraryVersion, v)
	}
}
