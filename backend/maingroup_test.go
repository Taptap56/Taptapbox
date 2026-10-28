package main

import "testing"

// Nama bawaan grup utama adalah "Taptap" (2026-10-27, permintaan
// user): instalasi segar / pengaturan kosong harus memakai nama ini,
// bukan "🚀 Proxy" lagi. Nama yang sudah tersimpan user tidak boleh
// diubah oleh bawaan ini — mainGroupTag hanya fallback bila kosong.
func TestMainGroupDefaultName(t *testing.T) {
	a := testApp(t)
	if got := a.mainGroupTag(); got != "Taptap" {
		t.Fatalf("bawaan grup utama = %q, mau \"Taptap\"", got)
	}
	a.store.Settings.MainGroupName = ""
	if got := a.mainGroupTag(); got != "Taptap" {
		t.Fatalf("grup utama kosong harus fallback ke \"Taptap\", dapat %q", got)
	}
	a.store.Settings.MainGroupName = "Utamaku"
	if got := a.mainGroupTag(); got != "Utamaku" {
		t.Fatalf("nama tersimpan harus dipakai apa adanya, dapat %q", got)
	}
}
