package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capLogWriter: tepat 200 baris, tulisan berikutnya dibuang diam-diam
// (Write tetap sukses) — proses tetap jalan, file tidak nambah lagi.
func TestCapLogWriterLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.log")
	w := newCapLogWriter(p)
	if w == nil {
		t.Fatal("newCapLogWriter mengembalikan nil")
	}
	for i := 0; i < 250; i++ {
		n, err := w.Write([]byte("baris log\n"))
		if err != nil || n != len("baris log\n") {
			t.Fatalf("tulis ke-%d: n=%d err=%v", i, n, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "\n"); got != 200 {
		t.Fatalf("file harus tepat 200 baris, dapat %d", got)
	}
}

// Satu Write berisi 250 baris langsung dipotong di baris ke-200.
func TestCapLogWriterPartialChunk(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.log")
	w := newCapLogWriter(p)
	if w == nil {
		t.Fatal("newCapLogWriter mengembalikan nil")
	}
	var sb strings.Builder
	for i := 0; i < 250; i++ {
		sb.WriteString("x\n")
	}
	if _, err := w.Write([]byte(sb.String())); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "\n"); got != 200 {
		t.Fatalf("chunk 250 baris harus dipotong jadi 200, dapat %d", got)
	}
}

// File dibuka segar (isi lama dibuang) dan .old sisa rotasi lama dihapus.
func TestCapLogWriterFresh(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.log")
	_ = os.WriteFile(p, []byte(strings.Repeat("lama\n", 500)), 0644)
	_ = os.WriteFile(p+".old", []byte("basi"), 0644)
	w := newCapLogWriter(p)
	if w == nil {
		t.Fatal("newCapLogWriter mengembalikan nil")
	}
	if _, err := w.Write([]byte("baru\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".old"); !os.IsNotExist(err) {
		t.Fatal(".old sisa rotasi lama harus sudah dihapus")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "baru\n" {
		t.Fatalf("file harus segar berisi sesi baru, dapat %q", string(data))
	}
}
