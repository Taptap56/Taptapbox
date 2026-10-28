package main

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// version.go — deteksi versi core dari biner sungguhan.
//
// Zashboard tahu versi mihomo dari API core yang sedang jalan; modul ini
// membacanya langsung dari kedua biner (flag versi). Dulu hanya sekali
// saat daemon start — akibatnya ganti biner (mis. mihomo 1.19.33) tidak
// tercermin di WebUI sampai backend di-restart, walau zashboard sudah
// menampilkan versi baru. Sejak v1.0.26 deteksi diulang tiap core start
// dan (di-throttle 1 menit) tiap status, jadi ganti biner + Terapkan
// langsung memperbarui Tentang/Pengaturan tanpa restart backend.
// Gagal deteksi -> nilai lama dipertahankan; bila belum pernah ada,
// pemanggil memakai fallback konstanta paket (versi core rilis ini).

var reSemver = regexp.MustCompile(`\d+\.\d+\.\d+`)

func parseMihomoVersion(out string) string {
	// Format: "Mihomo Meta v1.19.32 linux arm64 with go1.25.0 ..."
	// Ambil semver pertama SETELAH kata "Meta" agar versi Go di ekor
	// kalimat tidak ikut terambil.
	if i := strings.Index(out, "Meta"); i >= 0 {
		if m := reSemver.FindString(out[i:]); m != "" {
			return m
		}
	}
	return reSemver.FindString(out)
}

func parseSingboxVersion(out string) string {
	// Format baris pertama: "sing-box version 1.14.3"
	return reSemver.FindString(out)
}

// detectCoreVersions: baca ulang versi kedua biner (dipaksa, tanpa
// throttle). Hanya menimpa bila parse berhasil — probe gagal / output
// tak terurai tidak boleh menghapus versi bagus yang sudah ada.
func (a *App) detectCoreVersions() {
	a.verMu.Lock()
	defer a.verMu.Unlock()
	oldM, oldS := a.verMihomo, a.verSingbox
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if out, err := exec.CommandContext(ctx, a.mihomoBin, "-v").CombinedOutput(); err == nil {
		if v := parseMihomoVersion(string(out)); v != "" {
			a.verMihomo = v
		}
	}
	cancel()
	// TaptapBox library: sing-box tertanam, bukan biner — versi = konstanta library.
	a.verSingbox = singboxLibraryVersion
	a.verLastDetect = time.Now()
	// Catat hanya bila ada yang berubah (status dipanggil sering;
	// log tiap menit akan menghabiskan jatah 200 baris backend.log).
	if a.verMihomo != oldM || a.verSingbox != oldS {
		a.logf("versi core terdeteksi: mihomo=%s sing-box=%s", a.verMihomo, a.verSingbox)
	}
}

// detectCoreVersionsThrottled: deteksi ulang maksimal sekali per menit —
// dipakai jalur status agar ganti biner di luar daemon (Upgrade core
// dari zashboard, copy manual) ikut tercermin tanpa restart backend,
// tanpa membebani tiap panggilan status dengan dua exec.
func (a *App) detectCoreVersionsThrottled() {
	a.verMu.RLock()
	last := a.verLastDetect
	a.verMu.RUnlock()
	if !last.IsZero() && time.Since(last) < time.Minute {
		return
	}
	a.detectCoreVersions()
}

// coreInstalled: biner core ada di data/bin (modul slim v1.0.32 tidak
// membundel core — core diunduh lewat Tentang setelah instalasi).
func (a *App) coreInstalled(core string) bool {
	if core == "singbox" {
		// Library tertanam: selalu terpasang, tidak ada biner sing-box.
		return true
	}
	// Mihomo tidak ada di TaptapBox (hanya sing-box library).
	return false
}

// coreVersion: versi core untuk ditampilkan (hasil deteksi biner; bila
// belum pernah terdeteksi, fallback ke versi yang dibundel rilis ini).
// Biner TAK ADA -> "" (bukan fallback), agar WebUI tak mengklaim versi
// yang tidak terpasang.
func (a *App) coreVersion(core string) string {
	if !a.coreInstalled(core) {
		return ""
	}
	a.verMu.RLock()
	defer a.verMu.RUnlock()
	if core == "singbox" {
		if a.verSingbox != "" {
			return a.verSingbox
		}
		return singboxLibraryVersion
	}
	if a.verMihomo != "" {
		return a.verMihomo
	}
	return "1.19.32"
}
