#!/bin/sh
# build.sh — build backend Go (arm64 + armv7/v7a) & paket modul Magisk TaptapBox.
# CATATAN: android/arm (32-bit) TIDAK bisa di-link internal oleh Go —
# butuh compiler NDK (armv7a-linux-androideabi21-clang). Cari otomatis
# lewat V7A_CC, lalu ANDROID_NDK_LATEST_HOME / ANDROID_NDK_HOME
# (GitHub runner sudah punya NDK). Tanpa NDK, build v7a dilewati
# dengan peringatan dan hanya zip arm64 yang dibuat — workflow GitHub
# wajib memverifikasi kedua zip ada, jadi di sana NDK hilang = gagal.
#
# Aset eksternal (tidak ikut repo source) ditaruh di ../assets/:
#   (library) sing-box 1.14.3 tertanam di biner taptapbox — tidak ada aset sing-box lagi
#   assets/geodata/        — geoip.dat + geosite.dat (MetaCubeX; SATU set bersama)
#   assets/zashboard/      — isi zashboard (dashboard mihomo)
#   assets/scrcpy-server.jar — server scrcpy 5.0.1 resmi (Genymobile) untuk
#     view Scrcpy; layout.sh memindahkannya ke data/bin saat install.
# Semua bisa dioverride lewat env: GO, ASSETS, MIHOMO_BIN, SINGBOX_BIN,
# GEODATA_DIR, ZASHBOARD_DIR.
#
# TaptapBox SLIM: geodata/zashboard/scrcpy TIDAK dibundel (diunduh lewat Tentang → Download) agar file kecil; sing-box adalah library di dalam biner taptapbox, bukan biner terpisah.
# FULL=0 = SLIM (bawaan TaptapBox). FULL=1 hanya bila geodata+zashboard+scrcpy memang mau dibundel.
#
# Hasil: dist/taptapbox-$VER-arm64.zip + dist/taptapbox-$VER-v7a.zip (siap flash)
#      + dist/taptapbox-$VER-source.zip (slim: tanpa zashboard & geodata)
set -e
cd "$(dirname "$0")"
# Versi diambil dari module.prop (satu sumber kebenaran, tak bisa beda).
VER=$(grep -E '^version=' module/module.prop | cut -d= -f2)
[ -n "$VER" ] || { echo "version= tidak ada di module/module.prop"; exit 1; }
GO="${GO:-go}"
# Perbaikan untuk build GitHub: "go" dari PATH (setup-go) harus diterima.
# Logika lama ([ -x "go" ]) hanya lolos bila GO berupa path absolut, karena
# -x menguji file ./go, bukan hasil pencarian PATH — jadi build selalu gagal
# bila go hanya tersedia lewat PATH. Fallback lokal dipertahankan.
if ! command -v "$GO" >/dev/null 2>&1; then
  if [ -x "$HOME/workspace/build/go-tool-127/go/bin/go" ]; then
    GO="$HOME/workspace/build/go-tool-127/go/bin/go"
  else
    echo "Go toolchain tidak ada: $GO"; exit 1
  fi
fi
ASSETS="${ASSETS:-../assets}"
MIHOMO_BIN="${MIHOMO_BIN:-$ASSETS/mihomo}"
SINGBOX_BIN="${SINGBOX_BIN:-$ASSETS/sing-box}"
GEODATA_DIR="${GEODATA_DIR:-$ASSETS/geodata}"
ZASHBOARD_DIR="${ZASHBOARD_DIR:-$ASSETS/zashboard}"
SCRCPY_JAR="${SCRCPY_JAR:-$ASSETS/scrcpy-server.jar}"
FULL="${FULL:-0}"
if [ "$FULL" = "1" ]; then
  for f in "$GEODATA_DIR/geoip.dat" "$GEODATA_DIR/geosite.dat" "$SCRCPY_JAR" "$ZASHBOARD_DIR/index.html"; do
    [ -f "$f" ] || { echo "aset hilang: $f (lihat header skrip ini)"; exit 1; }
  done
fi

echo "== backend: android/arm64 == (FULL=$FULL)"
rm -rf module/bin/arm64 && mkdir -p module/bin/arm64
(cd backend && CGO_ENABLED=0 GOOS=android GOARCH=arm64 "$GO" build -trimpath -tags "with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api" -ldflags "-s -w -X main.appVersion=$VER" -o ../module/bin/arm64/taptapbox .)
if [ "$FULL" = "1" ]; then
  cp -f "$SCRCPY_JAR" module/bin/arm64/scrcpy-server.jar
  # Versi jar bawaan (HARUS sama dengan scrcpyVersion di backend/scrcpy.go):
  # dibaca backend sebagai argumen versi server + "Versi saat ini" scrcpy
  # di kotak Download; layout.sh memindahkannya ke data/bin tiap boot.
  printf '%s\n' "5.0.1" > module/bin/arm64/scrcpy-server.version
fi
chmod 755 module/bin/arm64/*

echo "== backend: android/arm (v7a) =="
rm -rf module/bin/armv7 && mkdir -p module/bin/armv7
V7A_CC="${V7A_CC:-}"
if [ -z "$V7A_CC" ]; then
  for _NDK in "$ANDROID_NDK_LATEST_HOME" "$ANDROID_NDK_HOME"; do
    if [ -n "$_NDK" ] && [ -x "$_NDK/toolchains/llvm/prebuilt/linux-x86_64/bin/armv7a-linux-androideabi21-clang" ]; then
      V7A_CC="$_NDK/toolchains/llvm/prebuilt/linux-x86_64/bin/armv7a-linux-androideabi21-clang"
      break
    fi
  done
fi
if [ -n "$V7A_CC" ]; then
  (cd backend && CGO_ENABLED=1 CC="$V7A_CC" GOOS=android GOARCH=arm GOARM=7 "$GO" build -trimpath -tags "with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api" -ldflags "-s -w -X main.appVersion=$VER" -o ../module/bin/armv7/taptapbox .)
  echo "v7a OK: $V7A_CC"
  if [ "$FULL" = "1" ]; then
    cp -f "$SCRCPY_JAR" module/bin/armv7/scrcpy-server.jar
    printf '%s\n' "5.0.1" > module/bin/armv7/scrcpy-server.version
  fi
  chmod 755 module/bin/armv7/*
else
  echo "WARNING: compiler NDK v7a tidak ditemukan (V7A_CC / ANDROID_NDK_* kosong) — zip v7a dilewati, hanya arm64 yang dibuat"
  rm -rf module/bin/armv7
fi

echo "== webui =="
rm -rf module/webui
cp -r webui module/webui
# Token anti-cache per-build (2026-10-27, diubah 2026-10-27: SW kini
# network-only, jadi token di sw.js bukan lagi nama cache melainkan
# marker versi SW_VERSION): fitur yang dihapus kadang "nyangkut"
# karena sw.js dan app.js?v= tidak pernah berubah di semua build,
# jadi Chrome mengira tidak ada update. Token = hash isi file WebUI
# (berubah tiap ada tambah/hapus/ubah fitur, sama bila isi sama) —
# disisipkan HANYA ke salinan paket (module/webui), bukan ke source,
# supaya build dari source zip di GitHub pun menghasilkan tokennya
# sendiri. Bersama header no-store di backend, WebUI selalu segar
# tiap flash tanpa hapus data situs, dan SW baru tiap build memicu
# activate yang menghapus sisa cache taptapbox-* dari SW lama.
if command -v sha256sum >/dev/null 2>&1; then
  WEBUI_TOKEN=$(cat webui/app.js webui/style.css webui/index.html webui/sw.js | sha256sum | cut -c1-12)
  sed -i "s/taptapbox-v1/taptapbox-$WEBUI_TOKEN/" module/webui/sw.js
  sed -i "s|app.js?v=1.0.0|app.js?v=$WEBUI_TOKEN|" module/webui/index.html
  echo "webui token: $WEBUI_TOKEN"
else
  echo "sha256sum tidak ada — token WebUI dilewati (no-store backend tetap berlaku)"
fi
if [ "$FULL" = "1" ]; then
  echo "== webui: +zashboard bawaan (hanya FULL) =="
  mkdir -p module/webui/zashboard
  cp -r "$ZASHBOARD_DIR"/* module/webui/zashboard/
else
  echo "== slim: tanpa zashboard bawaan (unduh lewat Tentang) =="
fi
echo "$VER" > module/webui/VERSION

# Minify WebUI (JS/CSS/HTML inline) agar modul lebih ringan; perilaku tidak berubah.
# Opsional: butuh node+npm. Bila esbuild tak bisa dipasang (mis. offline), langkah
# ini dilewati dan WebUI tetap dipakai apa adanya. MINIFY=0 untuk mematikan.
if [ "${MINIFY:-1}" = "1" ] && command -v node >/dev/null 2>&1 && [ -f minify-webui.cjs ]; then
  echo "== webui: minify =="
  if [ ! -d node_modules/esbuild ] && command -v npm >/dev/null 2>&1; then
    npm install --no-save --no-audit --no-fund --loglevel=error esbuild@0.27.7 >/dev/null 2>&1 || true
  fi
  node minify-webui.cjs module/webui || echo "minify dilewati (gagal), WebUI dipakai apa adanya"
fi

if [ "$FULL" = "1" ]; then
  echo "== geodata (satu set bersama) =="
  rm -rf module/geodata && mkdir -p module/geodata
  cp -f "$GEODATA_DIR/geoip.dat" "$GEODATA_DIR/geosite.dat" module/geodata/
else
  echo "== slim: tanpa core/geodata/jar bawaan (unduh lewat Tentang) =="
  rm -rf module/geodata
fi

echo "== paket zip (arm64 + v7a) =="
mkdir -p dist
rm -f "dist/taptapbox-$VER-arm64.zip" "dist/taptapbox-$VER-v7a.zip"
(cd module && zip -r -9 "../dist/taptapbox-$VER-arm64.zip" . -x '*.DS_Store' 'bin/armv7/*' 'bin/armv7')
ls -la "dist/taptapbox-$VER-arm64.zip"
if [ -f module/bin/armv7/taptapbox ]; then
  (cd module && zip -r -9 "../dist/taptapbox-$VER-v7a.zip" . -x '*.DS_Store' 'bin/arm64/*' 'bin/arm64')
  ls -la "dist/taptapbox-$VER-v7a.zip"
else
  echo "zip v7a dilewati (biner v7a tidak ada)"
fi

echo "== source zip (slim: tanpa zashboard, geodata, biner) =="
rm -f "dist/taptapbox-$VER-source.zip"
rm -rf /tmp/tapp-src && mkdir -p /tmp/tapp-src/taptapbox-src
cp -r backend build.sh /tmp/tapp-src/taptapbox-src/
sed -i '/replace github.com\/sagernet\/sing-box/d' /tmp/tapp-src/taptapbox-src/backend/go.mod
cp minify-webui.cjs /tmp/tapp-src/taptapbox-src/ 2>/dev/null || true
# buang artefak build yang mungkin tertinggal di folder backend
rm -f /tmp/tapp-src/taptapbox-src/backend/taptapbox /tmp/tapp-src/taptapbox-src/backend/taptapclash
mkdir -p /tmp/tapp-src/taptapbox-src/module
cp -r module/META-INF module/*.sh module/module.prop /tmp/tapp-src/taptapbox-src/module/
mkdir -p /tmp/tapp-src/taptapbox-src/webui/icons /tmp/tapp-src/taptapbox-src/webui/animasi
cp webui/index.html webui/style.css webui/app.js webui/manifest.json webui/sw.js webui/login.html /tmp/tapp-src/taptapbox-src/webui/
cp webui/icons/*.png /tmp/tapp-src/taptapbox-src/webui/icons/
cp webui/animasi/*.html /tmp/tapp-src/taptapbox-src/webui/animasi/
cp ../MERGE-NOTES.md /tmp/tapp-src/taptapbox-src/ 2>/dev/null || true
(cd /tmp/tapp-src && zip -r -9 "$OLDPWD/dist/taptapbox-$VER-source.zip" taptapbox-src -x '*.DS_Store')
ls -la "dist/taptapbox-$VER-source.zip"
echo "SELESAI"
