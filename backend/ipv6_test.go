package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Blokir IPv6: default ON dan migrasi Ver 13 -> 14 mengaktifkannya.
func TestBlockIPv6DefaultAndMigration(t *testing.T) {
	if !defaultSettings().BlockIPv6 {
		t.Fatal("default BlockIPv6 harus true")
	}
	// simulasi settings lama Ver 13 (belum ada block_ipv6)
	dir := t.TempDir()
	autoDir := filepath.Join(dir, "json", "user otomatis")
	if err := os.MkdirAll(autoDir, 0755); err != nil {
		t.Fatal(err)
	}
	old := `{"ver":13,"hotspot_lock_ip":true,"hotspot_ip":"192.168.56.1"}`
	if err := os.WriteFile(filepath.Join(autoDir, "settings.json"), []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	st := NewStore(autoDir)
	if st.Settings.Ver != 17 {
		t.Fatalf("Ver harus 17, dapat %d", st.Settings.Ver)
	}
	if !st.Settings.BlockIPv6 {
		t.Fatal("BlockIPv6 harus true setelah migrasi dari Ver 13")
	}
	if !st.Settings.HotspotLockIP || st.Settings.HotspotIP != "192.168.56.1" {
		t.Fatal("migrasi tak boleh merusak setting hotspot yang sudah ada")
	}
}

// Pengaturan baru (default) langsung Ver 14 dengan BlockIPv6 aktif.
func TestBlockIPv6Fresh(t *testing.T) {
	a := testApp(t)
	if a.store.Settings.Ver != 17 {
		t.Fatalf("Ver harus 17, dapat %d", a.store.Settings.Ver)
	}
	if !a.store.Settings.BlockIPv6 {
		t.Fatal("BlockIPv6 harus true untuk instalasi baru")
	}
}
