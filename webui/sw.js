// TaptapBox WebUI service worker — HANYA syarat agar WebUI bisa
// di-install sebagai aplikasi (PWA) dari Chrome. Sengaja TIDAK
// MENYIMPAN CACHE sama sekali (2026-10-27):
//   - WebUI ini admin lokal: tanpa daemon yang jalan, UI-nya tidak
//     ada gunanya (semua lewat /api/*), jadi UI offline tidak perlu.
//   - SW lama menyimpan seluruh UI dan menampilkannya saat network
//     gagal (caches.match), sehingga sesudah modul di-uninstall pun
//     WebUI lama tetap tampil "nyangkut" dari cache Chrome —
//     uninstall.sh tidak bisa menghapus cache Chrome, dan uninstall
//     PWA/modul pun tidak ikut menghapusnya.
//   - Dengan SW network-only + header no-store backend, WebUI hanya
//     tampil bila daemon jalan, dan sesudah uninstall tidak ada lagi
//     yang tertinggal di Chrome.
//
// SW_VERSION: marker versi per-build, diganti build.sh dengan token
// hash isi WebUI (bukan lagi nama cache — tidak ada cache yang dibuat).
// Tiap WebUI berubah, byte sw.js berubah, browser menginstal SW baru,
// dan activate di bawah menghapus sisa cache taptapbox-* dari SW lama.
const SW_VERSION = 'taptapbox-v1';

self.addEventListener('install', (e) => {
  // Tidak ada yang di-cache: langsung aktif.
  e.waitUntil(self.skipWaiting());
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    // Hapus HANYA cache bernama taptapbox-* (bekas SW lama). Jangan
    // hapus semua cache: Cache Storage itu per-origin, dan origin
    // 127.0.0.1:5056 ini juga dipakai zashboard (/zashboard/) —
    // cache-nya tidak boleh ikut terhapus.
    caches.keys()
      .then((ks) => Promise.all(ks.filter((k) => k.indexOf('taptapbox-') === 0).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (e) => {
  // Sengaja tidak intercept: biarkan browser fetch network seperti
  // situs biasa. Tanpa respondWith, tidak ada respons yang masuk
  // Cache Storage, dan tanpa cache pula WebUI tidak bisa nyangkut
  // sesudah daemon mati / modul di-uninstall.
  return;
});

self.addEventListener('message', (e) => {
  if (e.data === 'skip') self.skipWaiting();
});
