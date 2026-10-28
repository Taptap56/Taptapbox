# Magisk module installer for taptapbox (gaya modern: Magisk 20.4+ / KernelSU / APatch).
# customize.sh di-source oleh installer, jadi kode ditulis langsung di level
# atas (callback lama print_modname/on_install/set_permissions TIDAK dipanggil).

# Modul ini tidak menimpa file /system apa pun -> tidak perlu magic mount.
SKIPMOUNT=true
PROPFILE=false
POSTFSDATA=false
LATESTARTSERVICE=true

ui_print "****************************************"
ui_print "  TaptapBox: sing-box saja + WebUI (full otomatis)"
ui_print "****************************************"

_VER=$(grep -E '^version=' "$MODPATH/module.prop" 2>/dev/null | cut -d= -f2)
[ -z "$_VER" ] && _VER="(versi?)"
ui_print "- Versi  : $_VER"
ui_print "- MODPATH: $MODPATH"

# Binary hanya tersedia untuk arm64: gagal di awal, jangan diam-diam.
# (ARCH diisi oleh installer; bila kosong, layout.sh tetap memeriksa saat boot.)
case "$ARCH" in
  ""|arm64) ;;
  *) abort "! Arsitektur $ARCH tidak didukung (butuh arm64)" ;;
esac

# Logika tata-letak bersama (juga dijalankan tiap boot oleh service.sh,
# karena sebagian root manager hanya mengekstrak file).
. "$MODPATH/layout.sh"
ui_print "- Menata /data/adb/taptapbox/{bin,webui,log,config,geodata} ..."
if ensure_layout "$MODPATH"; then
  ui_print "- Data-dir layout OK"
else
  abort "! Data-dir layout tidak lengkap — lihat pesan di atas"
fi

# Verifikasi akhir: gagal = abort berisik, jangan pernah diam-diam.
# Yang WAJIB hanya backend + WebUI. Core sing-box di TaptapBox TERTANAM
# sebagai library di dalam biner taptapbox — tidak perlu diunduh.
_MISS=""
[ -x /data/adb/taptapbox/bin/taptapbox ] || _MISS="$_MISS taptapbox"
[ -f /data/adb/taptapbox/webui/index.html ] || _MISS="$_MISS webui"
if [ -n "$_MISS" ]; then
  abort "! Verifikasi gagal, hilang:$_MISS"
fi
ui_print "- Verifikasi OK: backend + WebUI"
ui_print "- Core OK: sing-box library 1.14.3 tertanam di taptapbox (tanpa biner sing-box terpisah)"
if [ ! -f /data/adb/taptapbox/webui/zashboard/index.html ] && [ ! -f /data/adb/taptapbox/zashboard/index.html ]; then
  ui_print "! Zashboard belum terpasang — unduh dulu lewat Tentang → Download (setelah reboot; bisa lewat VPN/WiFi)."
fi
if [ ! -f /data/adb/taptapbox/geodata/geoip.dat ] || [ ! -f /data/adb/taptapbox/geodata/geosite.dat ]; then
  ui_print "! Geodata belum terpasang — unduh dulu lewat Tentang → Download."
fi
if [ ! -f /data/adb/taptapbox/bin/scrcpy-server.jar ]; then
  ui_print "! Scrcpy belum terpasang — unduh dulu lewat Tentang → Download bila perlu mirror layar."
fi

# Port WebUI aktual (bisa diubah user di Pengaturan; default 5056).
_PORT=$(grep -o '"web_port": *[0-9]*' "/data/adb/taptapbox/config/settings.json" 2>/dev/null | grep -o '[0-9]*$')
[ -z "$_PORT" ] && _PORT=5056
ui_print ""
ui_print "  WebUI : http://127.0.0.1:$_PORT  (setelah reboot)"
ui_print "  - Dari HP lain (LAN/hotspot) WebUI minta password:"
ui_print "    user 'admin', password lihat Pengaturan WebUI."
ui_print ""

# Izin file: service.sh / uninstall.sh dijalankan lewat 'sh', tapi tetap 0755.
set_perm_recursive "$MODPATH" 0 0 0755 0644
set_perm "$MODPATH/service.sh" 0 0 0755
set_perm "$MODPATH/uninstall.sh" 0 0 0755
# File data-dir ada di luar $MODPATH -> set izin langsung (installer jalan sebagai root).
[ -f /data/adb/taptapbox/bin/taptapbox ] && chmod 755 /data/adb/taptapbox/bin/taptapbox
[ -d /data/adb/taptapbox/webui ] && chmod -R a+rX /data/adb/taptapbox/webui
