package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// sessionFile: kunci lama di akar data pindah ke session/ (isi sama,
// file lama hilang, folder 0700); sesi lama tetap valid karena kunci
// yang sama dipakai, bukan kunci baru.
func TestSessionFileMigratesLegacyKey(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "websess.key")
	key := bytes.Repeat([]byte{0xab}, 32)
	if err := os.WriteFile(old, key, 0600); err != nil {
		t.Fatal(err)
	}
	a := &App{dataDir: dir}
	got := a.sessionFile()
	want := filepath.Join(dir, "session", "websess.key")
	if got != want {
		t.Fatalf("path = %q, mau %q", got, want)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("file lama masih ada (err=%v)", err)
	}
	b, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, key) {
		t.Fatal("isi kunci berubah saat migrasi")
	}
	if st, err := os.Stat(filepath.Join(dir, "session")); err != nil || st.Mode().Perm() != 0700 {
		t.Fatalf("folder session perm = %v err = %v", st.Mode(), err)
	}
	if st, err := os.Stat(want); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("file kunci perm = %v err = %v", st.Mode(), err)
	}
	// Idempotent: panggilan kedua tidak mengubah apa-apa.
	if got2 := a.sessionFile(); got2 != want {
		t.Fatalf("panggilan kedua path = %q", got2)
	}
	// sessSecret memakai kunci hasil migrasi (bukan kunci baru).
	if k := a.sessSecret(); !bytes.Equal(k, key) {
		t.Fatal("sessSecret tidak memakai kunci hasil migrasi")
	}
}

// Instalasi segar: tidak ada file lama -> kunci baru langsung di session/.
func TestSessionFileFreshInstall(t *testing.T) {
	dir := t.TempDir()
	a := &App{dataDir: dir}
	k := a.sessSecret()
	if len(k) != 32 {
		t.Fatalf("panjang kunci = %d", len(k))
	}
	if _, err := os.Stat(filepath.Join(dir, "websess.key")); !os.IsNotExist(err) {
		t.Fatal("file lama tercipta di akar data")
	}
	if _, err := os.Stat(filepath.Join(dir, "session", "websess.key")); err != nil {
		t.Fatalf("kunci baru tidak di session/: %v", err)
	}
	if k2 := a.sessSecret(); !bytes.Equal(k, k2) {
		t.Fatal("sessSecret tidak stabil (cache)")
	}
}

// File lama rusak (bukan 32 byte) -> dibuang, kunci baru dibuat di session/.
func TestSessionFileCorruptLegacyRemoved(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "websess.key")
	if err := os.WriteFile(old, []byte("rusak"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &App{dataDir: dir}
	k := a.sessSecret()
	if len(k) != 32 {
		t.Fatalf("panjang kunci = %d", len(k))
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("file lama rusak tidak dibuang")
	}
}
