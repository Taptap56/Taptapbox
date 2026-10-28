# layout.sh — shared taptapbox data-dir layout.
#
# Sourced by BOTH:
#   - customize.sh (install time) — if the root manager actually runs it
#   - service.sh    (every boot)  — the reliable path; e.g. KernelSU-Next's
#     "overlayfsx metainstall" only extracts files and never calls on_install.
#
# Idempotent: safe to run any number of times. Never touches user data
# (servers/settings/logs). The WebUI is ALWAYS replaced by the module's copy
# (even when the version is unchanged). Sejak v1.0.41 TANPA bak: WebUI lama
# hanya dipindah sementara — animasi custom + zashboard disalin kembali
# (bila paket baru tak membawanya), lalu SEMUA webui.bak-* dihapus.
# Custom animations (files absent from the new package) are restored after
# the swap so user-made animations are never lost. Zashboard is restored
# the same way when the new package lacks it (slim): the dashboard then
# survives updates inside webui/ without digging into webui.bak manually.
# (Downloads in data/zashboard still win via the backend's zashboardDir.)
# Binaries shipped in the module dir are always treated as newest.
# Sejak modul slim (v1.0.32): core/geodata/scrcpy TIDAK dibundel — diunduh
# lewat Tentang setelah instalasi. Layout TIDAK PERNAH menghapus biner di
# data/bin (hasil download bertahan tiap flash); core yang belum terpasang
# bukan kegagalan layout, hanya syarat wajib taptapbox + webui.

# ensure_layout <moduledir>
# Returns 0 when taptapbox + webui/ are in place under /data/adb/taptapbox/,
# 1 otherwise (caller decides how fatal that is). Core yang belum terpasang
# (modul slim) tidak membuat return 1.
ensure_layout() {
  _LL_MODDIR="$1"
  _LL_DATA=/data/adb/taptapbox

  _ll_say() {
    if command -v ui_print >/dev/null 2>&1; then
      ui_print "$1"
    else
      echo "$1"
    fi
  }

  # _ll_drop_baks: hapus SEMUA webui.bak-* (v1.0.41: tanpa bak).
  # Isi berguna (animasi custom + zashboard) disalin dulu bila belum ada
  # di webui baru dan belum ada unduhan data/zashboard — jadi backup lama
  # dari boot sebelumnya pun aman dibersihkan.
  _ll_drop_baks() {
    for _bk in "$_LL_DATA"/webui.bak-*; do
      [ -d "$_bk" ] || continue
      if [ -d "$_bk/animasi" ]; then
        mkdir -p "$_LL_DATA/webui/animasi" 2>/dev/null
        for _af in "$_bk"/animasi/*; do
          [ -f "$_af" ] || continue
          _ab=$(basename "$_af")
          [ -f "$_LL_DATA/webui/animasi/$_ab" ] || cp -f "$_af" "$_LL_DATA/webui/animasi/$_ab" 2>/dev/null
        done
      fi
      if [ -f "$_bk/zashboard/index.html" ] && [ ! -f "$_LL_DATA/webui/zashboard/index.html" ] && [ ! -f "$_LL_DATA/zashboard/index.html" ]; then
        cp -r "$_bk/zashboard" "$_LL_DATA/webui/zashboard" 2>/dev/null
      fi
      rm -rf "$_bk" 2>/dev/null
    done
  }

  # (Salinan data warisan "json/" dari era TaptapClash<-TaptapBox sudah
  # dihapus: migrasi data modul lama kini ditangani backend ke config/.)

  _ARCH=$(getprop ro.product.cpu.abi 2>/dev/null)
  [ -z "$_ARCH" ] && _ARCH=$(uname -m 2>/dev/null)
  case "$_ARCH" in
    arm64-v8a|aarch64) _LL_BA=arm64 ;;
    armeabi-v7a|armeabi|armv7l|armv7|arm) _LL_BA=armv7 ;;
    *) _LL_BA="" ;;
  esac

  mkdir -p "$_LL_DATA/bin" "$_LL_DATA/log" 2>/dev/null || return 1

  # bpf2socks sisa versi lama (eBPF dihapus): buang binernya bila masih ada.
  rm -f "$_LL_DATA/bin/bpf2socks" 2>/dev/null

  # Sisa backup unduhan era .prev/.bak (v1.0.27-40): mulai v1.0.41
  # unduhan tanpa backup, jadi bersihkan yang masih tertinggal.
  rm -f "$_LL_DATA/bin/"*.prev 2>/dev/null
  rm -f "$_LL_DATA/geodata/"*.bak 2>/dev/null
  rm -rf "$_LL_DATA/zashboard.bak" 2>/dev/null

  # --- binaries: the copy shipped in the module dir is always the newest ---
  # (scrcpy-server.jar ikut dipindah agar backend menemukannya di data/bin;
  #  jar bukan syarat layout sukses — tanpanya hanya fitur Scrcpy yang mati.)
  if [ -n "$_LL_BA" ]; then
    for _b in taptapbox; do
      if [ -f "$_LL_MODDIR/bin/$_LL_BA/$_b" ]; then
        if mv -f "$_LL_MODDIR/bin/$_LL_BA/$_b" "$_LL_DATA/bin/$_b" 2>/dev/null; then
          _ll_say "- layout: $_b -> $_LL_DATA/bin/"
        fi
      fi
      [ -f "$_LL_DATA/bin/$_b" ] && chmod 755 "$_LL_DATA/bin/$_b" 2>/dev/null
    done
    if [ -f "$_LL_MODDIR/bin/$_LL_BA/scrcpy-server.jar" ]; then
      mv -f "$_LL_MODDIR/bin/$_LL_BA/scrcpy-server.jar" "$_LL_DATA/bin/scrcpy-server.jar" 2>/dev/null && _ll_say "- layout: scrcpy-server.jar -> $_LL_DATA/bin/"
    fi
    # Versi jar HARUS ikut pindah (dan ikut ke-reset saat flash): argumen
    # versi server harus persis sama dengan jar yang terpasang, bila tidak
    # server menolak koneksi. File ini juga diperbarui tiap Download scrcpy
    # dari WebUI (kotak Download di Tentang).
    if [ -f "$_LL_MODDIR/bin/$_LL_BA/scrcpy-server.version" ]; then
      mv -f "$_LL_MODDIR/bin/$_LL_BA/scrcpy-server.version" "$_LL_DATA/bin/scrcpy-server.version" 2>/dev/null
    fi
    # Folder modul langsung bersih: hapus direktori bin yang sudah kosong
    # (rmdir hanya menghapus bila kosong — file yang gagal pindah atau
    # sisa lain tetap tinggal sebagai petunjuk + sumber percobaan ulang
    # boot berikutnya; gate rm -rf di bawah tetap jadi pengaman).
    rmdir "$_LL_MODDIR/bin/$_LL_BA" "$_LL_MODDIR/bin" 2>/dev/null
    if [ -f "$_LL_DATA/bin/taptapbox" ]; then
      rm -rf "$_LL_MODDIR/bin"
    fi
  else
    _ll_say "! arsitektur tidak dikenal: $_ARCH (butuh arm64 atau v7a)"
  fi

  # --- WebUI: SELALU diganti dengan WebUI baru dari modul (backend+WebUI harus sinkron),
  # walau versinya sama. WebUI lama hanya transit ke webui.bak-<ver>-<timestamp>
  # (v1.0.41: tanpa bak — isi berguna disalin kembali, lalu semua bak dihapus).
  # Animasi buatan user (file yang tak ada di paket baru) dikembalikan lagi.
  if [ -d "$_LL_MODDIR/webui" ]; then
    _LL_NEWVER=$(grep -E '^version=' "$_LL_MODDIR/module.prop" 2>/dev/null | cut -d= -f2)
    _LL_OLDVER=$(cat "$_LL_DATA/webui/VERSION" 2>/dev/null)
    [ -n "$_LL_OLDVER" ] || _LL_OLDVER="tanpaver"
    _LL_STAMP=$(date +%Y%m%d-%H%M%S 2>/dev/null)
    [ -n "$_LL_STAMP" ] || _LL_STAMP="$$"
    _LL_BAK="$_LL_DATA/webui.bak-$_LL_OLDVER-$_LL_STAMP"
    if [ -d "$_LL_DATA/webui" ]; then
      # Transit saja: isi berguna disalin kembali sesudah swap, lalu bak dihapus.
      mv -f "$_LL_DATA/webui" "$_LL_BAK" 2>/dev/null
    else
      rm -rf "$_LL_DATA/webui" # drop broken/empty leftover so mv can't nest
    fi
    if mv -f "$_LL_MODDIR/webui" "$_LL_DATA/webui" 2>/dev/null; then
      _ll_say "- layout: WebUI ${_LL_NEWVER:-?} -> $_LL_DATA/webui/"
      # animasi custom user: kembalikan file yang tak ada di paket baru
      if [ -d "$_LL_BAK/animasi" ]; then
        mkdir -p "$_LL_DATA/webui/animasi" 2>/dev/null
        for _af in "$_LL_BAK"/animasi/*; do
          [ -f "$_af" ] || continue
          _ab=$(basename "$_af")
          if [ ! -f "$_LL_DATA/webui/animasi/$_ab" ]; then
            cp -f "$_af" "$_LL_DATA/webui/animasi/$_ab" 2>/dev/null
            _ll_say "- layout: animasi custom dikembalikan: $_ab"
          fi
        done
      fi
      # Zashboard: pola yang sama — paket baru tanpa zashboard (slim)
      # tidak boleh menghilangkan dashboard lama; kembalikan dari backup
      # (salinan; backup dihapus sesudah restore). Paket FULL yang
      # membawa zashboard baru selalu menang (syarat index.html baru).
      if [ -n "$_LL_BAK" ] && [ -f "$_LL_BAK/zashboard/index.html" ] && [ ! -f "$_LL_DATA/webui/zashboard/index.html" ]; then
        cp -r "$_LL_BAK/zashboard" "$_LL_DATA/webui/zashboard" 2>/dev/null
        _ll_say "- layout: zashboard lama dikembalikan"
      fi
      # Tanpa bak: semua backup (baru + sisa boot lama) dihapus sekarang.
      _ll_drop_baks
      _ll_say "- layout: webui.bak-* dibersihkan (tanpa bak)"
    fi
  fi
  # Modul webui sudah habis dikonsumsi boot sebelumnya tapi bak masih ada
  # (sisa v1.0.40): bersihkan juga dengan pemeriksaan restore yang sama.
  if [ ! -d "$_LL_MODDIR/webui" ] && [ -d "$_LL_DATA/webui" ]; then
    _ll_drop_baks
  fi
  if [ -f "$_LL_DATA/webui/index.html" ]; then
    rm -rf "$_LL_MODDIR/webui"
    chmod -R a+rX "$_LL_DATA/webui" 2>/dev/null
  fi
  # Folder animasi: backend + galeri WebUI membaca dari sini.
  # Dibuat bila belum ada (mis. source lama tanpa animasi / user menghapus).
  mkdir -p "$_LL_DATA/webui/animasi" 2>/dev/null

  _LL_MISSING=""
  # Syarat wajib hanya daemon + webui; core boleh belum terpasang
  # (modul slim — diunduh lewat Tentang). Jangan pernah jadikan core
  # yang belum ada sebagai kegagalan layout.
  [ -f "$_LL_DATA/bin/taptapbox" ] || _LL_MISSING="$_LL_MISSING taptapbox"
  [ -f "$_LL_DATA/webui/index.html" ] || _LL_MISSING="$_LL_MISSING webui"
  # geodata (geoip.dat/geosite.dat): folder module cuma kendaraan kirim.
  # Pindahkan ke data bila data belum punya ATAU bawaan module lebih baru
  # (update modul), lalu SELALU kosongkan sisi module — dulu syarat
  # "data belum punya" membuat sisa geodata module menumpuk dobel
  # permanen setiap habis update.
  if [ -d "$_LL_MODDIR/geodata" ]; then
    mkdir -p "$_LL_DATA/geodata" 2>/dev/null
    for _g in geoip.dat geosite.dat; do
      _src="$_LL_MODDIR/geodata/$_g"; _dst="$_LL_DATA/geodata/$_g"
      [ -f "$_src" ] || continue
      if [ ! -f "$_dst" ] || [ "$_src" -nt "$_dst" ]; then
        mv -f "$_src" "$_dst" 2>/dev/null || cp -f "$_src" "$_dst" 2>/dev/null
      fi
      rm -f "$_src" 2>/dev/null
    done
    rmdir "$_LL_MODDIR/geodata" 2>/dev/null
  fi
  if [ -n "$_LL_MISSING" ]; then
    _ll_say "! layout incomplete, missing:$_LL_MISSING"
    return 1
  fi
  return 0
}
