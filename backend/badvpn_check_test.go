// badvpn_check_test.go — validasi config hasil generate dengan biner
// sing-box ASLI (bukan hanya pengecekan nama field oleh tes sendiri).
// Dijalankan hanya bila env SINGBOX_CHECK_BIN menunjuk ke biner;
// tanpa env itu tes ini di-skip agar suite tetap jalan di mana saja.
// Pelajaran 2026-10-27: field "destination_ip_cidr" lolos tes unit
// (tes hanya memeriksa nama field yang ditulis sendiri) tapi ditolak
// sing-box asli — gerbang ini mencegah terulangnya kesalahan itu.

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

func TestBadvpnSingboxCheck(t *testing.T) {
	bin := os.Getenv("SINGBOX_CHECK_BIN")
	if bin == "" {
		t.Skip("SINGBOX_CHECK_BIN tidak diset")
	}
	run := func(name string, badvpn bool) {
		t.Run(name, func(t *testing.T) {
			a := testApp(t)
			sshID := bvSSHTestServer(a)
			a.store.Settings.SelectedID = sshID
			a.store.Settings.Core = "singbox"
			a.store.Settings.BadvpnEnabled = badvpn
			if badvpn {
				setBadvpnRelay(a, sshID)
			}
			cfg := a.buildV2rayng()
			data, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			f, err := os.CreateTemp("", "bv-check-*.json")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(f.Name())
			if _, err := f.Write(data); err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
			out, err := exec.Command(bin, "check", "-c", f.Name()).CombinedOutput()
			if err != nil {
				t.Fatalf("sing-box check gagal: %v\n%s", err, string(out))
			}
			t.Logf("sing-box check OK (%s)", name)
		})
	}
	run("tanpa-badvpn", false)
	run("dengan-badvpn", true)
}
