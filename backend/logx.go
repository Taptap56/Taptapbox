// logx.go — log backend ala xclash: "04:20 pm [Info]: pesan".
// Setiap konek menulis blok header sesi agar log rapi & mudah dibaca
// (lihat halaman Log di WebUI).

package main

import (
	"fmt"
	"strings"
	"time"
)

// logf: Info (kompatibel dengan semua pemanggil lama).
func (a *App) logf(format string, v ...any) { a.loglv("Info", format, v...) }

// logWarnf / logErrf: peringatan & error.
func (a *App) logWarnf(format string, v ...any) { a.loglv("Warning", format, v...) }
func (a *App) logErrf(format string, v ...any)  { a.loglv("Error", format, v...) }

// logDebugf: hanya bila level log pengaturan = debug.
func (a *App) logDebugf(format string, v ...any) {
	if a.store != nil && a.store.Settings.LogLevel == "debug" {
		a.loglv("Debug", format, v...)
	}
}

// loglv: satu baris "03:04 pm [Level]: pesan".
// Timestamp & prefix ditulis di sini — logger std diset tanpa flag/prefix
// (lihat main()).
func (a *App) loglv(level, format string, v ...any) {
	if a.logger == nil {
		return // tes memakai App kosong tanpa logger
	}
	msg := strings.TrimSpace(fmt.Sprintf(format, v...))
	if msg == "" {
		return
	}
	a.logger.Printf("%s [%s]: %s\n", time.Now().Format("03:04 pm"), level, msg)
}

// logConnectHeader: blok header sesi ala xclash, ditulis setiap konek
// (Connect/Terapkan/auto-start). Dipanggil setelah config ditulis.
func (a *App) logConnectHeader(cfgPath string) {
	st := a.store.Settings
	now := time.Now()
	tz := now.Location().String()
	if tz == "Local" || tz == "" {
		tz = "Asia/Jakarta"
	}
	cfg := strings.TrimPrefix(cfgPath, a.dataDir+"/")
	ov := ""
	if a.hasOverride() {
		ov = " (override)"
	}
	a.logger.Print("----------------------------------------\n")
	a.logger.Printf("%s %s\n", tz, now.Format("Mon Jan 02 15:04:05 MST 2006"))
	a.logger.Printf("version=%s (arm64-v8a)\n", appVersion)
	a.logger.Printf("mode=%s, config=%s%s\n", st.ProxyMode, cfg, ov)
	a.logger.Print("----------------------------------------\n")
}
