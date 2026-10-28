# Rilis GitHub TaptapBox-Go (sederhana)

Paket ini = TaptapBox lokal v1.1.0, dinomori ulang ke **v1.0.0 /
versionCode 1** untuk rilis GitHub, sama seperti pola TaptapApp
kemarin. Fungsi sama. Yang tidak berubah: id modul (`taptapbox`),
data (`/data/adb/taptapbox`), port WebUI (5056), path WebUI
(`/taptapbox/`). Hanya nama tampil yang jadi TaptapBox-Go.

## Run workflow

Repo -> Actions -> release -> Run workflow -> pilih core sing-box
(10 versi stabil terakhir, default v1.14.4) -> Run workflow.

Hasil: **satu Release saja**, tag `v1.0.0`, judul
`TaptapBox-Go v1.0.0`, aset hanya `taptapbox-go-v1.0.0.zip`
(modul Magisk SLIM). Run ulang menimpa Release itu.

## Dua workflow: release vs artifact

Repo ini punya DUA workflow Run (keduanya bisa dipilih core
dari dropdown yang sama):

- **release** — untuk rilis resmi. Hasilnya menimpa Release
  v1.0.0. Pakai ini hanya bila versi core sudah diputuskan.
- **build-artifact** — untuk NYOBA versi lain. Hasilnya masuk
  ke Actions Artifact di halaman run itu (bagian Artifacts),
  nama `taptapbox-go-singbox-<versi-core>`, isi zip
  `taptapbox-go-v1.0.0-singbox-<versi-core>.zip`. Release
  v1.0.0 TIDAK disentuh sama sekali. Artifact disimpan 90 hari.
  Bila versi yang dicoba sudah cocok, baru Run workflow
  `release` dengan versi yang sama.

Jadi urutan yang aman: coba dulu lewat `build-artifact`,
flash hasil artifact di HP, cocok -> baru `release`.

## Rekomendasi core (per 2026-10-28)

- **v1.14.4** — versi terbaru yang sudah terbukti cocok
  (simulasi lokal: tidy + vet + tes + build SLIM lolos).
- **v1.14.3** — versi bawaan sekarang, paling aman; ini pilihan
  yang direncanakan untuk rilis v1.0.0 ini.

Versi lain di dropdown belum terbukti compile dengan kode ini;
bila gagal, workflow berhenti sebelum rilis dibuat. Mau versi
lain? Fork repo ini, Run workflow sendiri, pilih versinya.

## Pakai hasil rilis di HP

Flash zip di Magisk timpa instalasi lama (tanpa uninstall),
reboot, cek Tentang -> Core sing-box (library). Geodata,
zashboard, scrcpy diunduh lewat Tentang -> Download bila belum
terpasang. VersionCode turun 2 -> 1, jadi Magisk bisa menganggap
ini downgrade, tapi flash manual timpa tetap jalan dan data
tidak hilang karena id modul sama.

## Bila ada versi sing-box baru

Edit `options` di KEDUA workflow
(`.github/workflows/release.yml` dan
`.github/workflows/build-artifact.yml`): tambah di paling atas,
buang yang paling bawah supaya tetap 10.
