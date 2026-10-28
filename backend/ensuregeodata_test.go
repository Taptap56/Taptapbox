package main

import (
	"log"
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureGeodataSymlink — workdir mihomo harus berisi symlink ke
// data/geodata (bukan salinan 21MB), idempoten, dan salinan reguler
// warisan diganti jadi symlink.
func TestEnsureGeodataSymlink(t *testing.T) {
	dir := t.TempDir()
	a := &App{dataDir: dir, logger: log.New(os.Stderr, "", 0)}
	geoDir := filepath.Join(dir, "geodata")
	if err := os.MkdirAll(geoDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"geoip.dat", "geosite.dat"} {
		if err := os.WriteFile(filepath.Join(geoDir, n), []byte("isi-"+n), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// warisan: satu file masih salinan reguler di workdir
	wd := a.mihomoWorkDir()
	if err := os.WriteFile(filepath.Join(wd, "geoip.dat"), []byte("isi-geoip.dat"), 0644); err != nil {
		t.Fatal(err)
	}
	a.ensureGeodata()
	a.ensureGeodata() // idempoten
	for _, n := range []string{"geoip.dat", "geosite.dat"} {
		dst := filepath.Join(wd, n)
		link, err := os.Readlink(dst)
		if err != nil {
			t.Fatalf("%s bukan symlink: %v", n, err)
		}
		if link != filepath.Join(geoDir, n) {
			t.Fatalf("%s symlink ke %q, mau %q", n, link, filepath.Join(geoDir, n))
		}
		b, err := os.ReadFile(dst) // terbaca mengikuti symlink
		if err != nil || string(b) != "isi-"+n {
			t.Fatalf("%s isi salah: %q %v", n, b, err)
		}
	}
	if !a.geodataReady() {
		t.Fatal("geodataReady harus true dengan symlink")
	}
}
