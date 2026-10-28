package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Migrasi port WebUI (TaptapBox): 5050 -> 5056 (hanya default lama;
// langkah 5056 -> 5556 milik TaptapClash tidak berlaku di modul gabungan),
// dan WebListenAll default true agar WebUI bisa dibuka dari HP lain.
func TestWebPortMigrasi(t *testing.T) {
	dir := t.TempDir()
	old := `{"ver":10,"mode":"v2rayng","web_port":5050}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if s.Settings.Ver != 17 {
		t.Fatalf("Ver harus 17, dapat %d", s.Settings.Ver)
	}
	if s.Settings.WebPort != 5056 {
		t.Fatalf("WebPort harus 5056 (migrasi dari 5050), dapat %d", s.Settings.WebPort)
	}
	if !s.Settings.WebListenAll {
		t.Fatal("WebListenAll harus true secara default")
	}
}

// Port & pilihan listen user sendiri (settings ver 11) tidak boleh disentuh.
func TestWebPortCustomTidakBerubah(t *testing.T) {
	dir := t.TempDir()
	old := `{"ver":11,"mode":"v2rayng","web_port":8080,"web_listen_all":false}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if s.Settings.WebPort != 8080 {
		t.Fatalf("WebPort custom harus tetap 8080, dapat %d", s.Settings.WebPort)
	}
	if s.Settings.WebListenAll {
		t.Fatal("WebListenAll=false pilihan user harus dipertahankan")
	}
}
