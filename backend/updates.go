// updates.go — kotak Download di Tentang: cek rilis upstream (GitHub)
// + unduh & pasang core/scrcpy/geodata/zashboard dengan validasi,
// TANPA backup: biner/file lama langsung ditimpa (permintaan user
// v1.0.41: tanpa .prev/.bak, biar bersih).
//
// Perilaku yang dijanjikan ke user:
//   - "Versi baru" = rilis terbaru upstream (bukan sekadar "ada file").
//   - Tombol Download = unduh + pasang + restart core aktif bila yang
//     diganti itu core/geodata yang sedang jalan (scrcpy berlaku di sesi
//     berikutnya, tanpa restart).
//   - Validasi SEBELUM ganti: biner baru harus bisa jalan dan melaporkan
//     versi yang sama dengan tag rilis; gagal validasi = tidak diganti.
//   - Gagal restart setelah pasang = error dilaporkan (bukan dibiarkan
//     mati); tidak ada lagi backup untuk dikembalikan — unduh ulang
//     dari kotak Download bila biner baru ternyata bermasalah.
//   - Tidak pernah menawarkan DOWNGRADE: bila versi terpasang (mis. build
//     custom) lebih baru dari rilis upstream, tombol menjadi "Sudah terbaru".
//   - Pengecekan versi baru di-cache 1 jam (GitHub API tanpa token dibatasi
//     60 req/jam/IP); "Periksa lagi" di WebUI memaksa cek ulang.

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// githubAPI bisa diganti tes (httptest). URL aset unduhan diambil dari
// payload API (browser_download_url), jadi ikut mengarah ke server tes.
var githubAPI = "https://api.github.com"

var updateRepos = map[string]string{
	"singbox":   "SagerNet/sing-box",
	"mihomo":    "MetaCubeX/mihomo",
	"scrcpy":    "Genymobile/scrcpy",
	"geodata":   "MetaCubeX/meta-rules-dat",
	"zashboard": "Zephyruso/zashboard",
}

const updateCacheTTL = time.Hour

// ghRelease: subset respons GitHub releases/latest yang dipakai.
// Draft/Prerelease dipakai daftar versi (v1.0.42): hanya rilis final
// yang ditawarkan ke pemilih versi.
type ghRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

// stripTag: "v1.19.33" -> "1.19.33" (spasi dipangkas dulu, baru prefix).
func stripTag(tag string) string {
	return strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(tag), "v"), "V")
}

// compareVersions: -1 bila a<b, 0 bila sama, 1 bila a>b. Segmen numerik
// dibandingkan sebagai angka ("1.19.9" < "1.19.33"); sufiks pra-rilis
// ("-alpha") diabaikan; segmen non-numerik dibandingkan leksikografis.
func compareVersions(a, b string) int {
	norm := func(s string) []string {
		s = stripTag(s)
		if i := strings.IndexAny(s, "-+"); i >= 0 {
			s = s[:i]
		}
		return strings.Split(s, ".")
	}
	pa, pb := norm(a), norm(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var sa, sb string
		if i < len(pa) {
			sa = pa[i]
		}
		if i < len(pb) {
			sb = pb[i]
		}
		na, ea := strconv.Atoi(sa)
		nb, eb := strconv.Atoi(sb)
		if ea == nil && eb == nil {
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
			continue
		}
		if c := strings.Compare(sa, sb); c != 0 {
			return c
		}
	}
	return 0
}

// updateInfo: satu baris kotak Download.
// Installed=false -> "Belum terpasang" (modul slim: belum diunduh).
// Pinned=true -> unduhan scrcpy pertama dikunci ke versi teruji (bawaan),
// bukan rilis latest GitHub.
type updateInfo struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Update    bool   `json:"update"`
	Installed bool   `json:"installed"`
	Pinned    bool   `json:"pinned,omitempty"`
	Error     string `json:"error,omitempty"`
}

// geoFileInfo: keterangan file geodata (sudah diformat untuk WebUI).
type geoFileInfo struct {
	Size string `json:"size"`
	Date string `json:"date"`
}

// updatesData: respons GET /api/updates.
type updatesData struct {
	Singbox   updateInfo `json:"singbox"`
	Mihomo    updateInfo `json:"mihomo"`
	Scrcpy    updateInfo `json:"scrcpy"`
	Zashboard updateInfo `json:"zashboard"`
	Geodata struct {
		Geoip   geoFileInfo `json:"geoip"`
		Geosite geoFileInfo `json:"geosite"`
		Latest  string      `json:"latest"`
		Error   string      `json:"error,omitempty"`
	} `json:"geodata"`
}

// latestCache: hasil cek rilis upstream (hanya "latest", bukan "current"
// — current selalu dihitung segar agar sehabis Download langsung benar).
type latestCache struct {
	at     time.Time
	latest map[string]string
	err    map[string]string
}

// updateProxyURL: URL proksi mixed lokal bila core sedang jalan, "" bila tidak.
// Latar: daemon jalan sebagai root; trafik root dikecualikan dari tangkapan
// transparan (netfilter.go) sehingga egress daemon LANGSUNG — di paket XL
// user, jalur langsung non-Google diblokir, dan itu membuat cek GitHub
// gagal serentak untuk keempat item (laporan 2026-10-24). Lewat proksi
// mixed lokal, cek/unduhan ikut jalur VPN yang sudah terbukti jalan.
func (a *App) updateProxyURL() string {
	if !a.coreAlive() || a.store == nil {
		return ""
	}
	// Port efektif dari core yang jalan (manualinject.go) lebih utama:
	// config manual boleh punya mixed port sendiri, berbeda dari pengaturan.
	if p := a.getEffMixedPort(); p > 0 {
		return fmt.Sprintf("http://127.0.0.1:%d", p)
	}
	if port := a.store.Settings.MixedPort; port > 0 {
		return fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	return ""
}

// proxyClient: klien dengan proksi eksplisit ("" = direct tanpa proksi,
// Proxy diset nil agar tak terpengaruh variabel lingkungan).
func (a *App) proxyClient(proxyURL string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	} else {
		tr.Proxy = nil
	}
	return &http.Client{Transport: tr}
}

// fetchReleaseViaHTTP: satu GET rilis lewat klien HTTP (proksi/direct).
// connected=false berarti koneksi gagal total (pemanggil boleh coba jalur
// lain); connected=true berarti server menjawab — hasilnya dipakai apa
// adanya, termasuk status error (perilaku doUpdate lama).
func (a *App) fetchReleaseViaHTTP(client *http.Client, url string) (*ghRelease, error, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err, false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "TaptapBox")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("GitHub status %d", resp.StatusCode), true
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err, true
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("GitHub: tag rilis kosong"), true
	}
	return &rel, nil, true
}

func (a *App) fetchRelease(repo string) (*ghRelease, error) {
	return a.fetchReleasePath("/repos/" + repo + "/releases/latest")
}

// fetchReleaseTag: rilis berdasarkan tag (untuk unduhan scrcpy pertama
// yang dikunci ke versi bawaan teruji, bukan latest).
func (a *App) fetchReleaseTag(repo, tag string) (*ghRelease, error) {
	return a.fetchReleasePath("/repos/" + repo + "/releases/tags/" + tag)
}

// fetchReleasePath: cek rilis lewat rantai proksi core -> helper non-root
// -> direct (v1.0.33: helper membuat cek jalan lewat VPN apk/WiFi walau
// daemon root tidak ikut rute VPN dan resolver sistemnya rusak).
func (a *App) fetchReleasePath(path string) (*ghRelease, error) {
	url := githubAPI + path
	if proxyURL := a.updateProxyURL(); proxyURL != "" {
		rel, err, connected := a.fetchReleaseViaHTTP(a.proxyClient(proxyURL), url)
		if connected {
			return rel, err
		}
		a.logWarnf("cek via proksi %s gagal: %v; coba helper", proxyURL, err)
	}
	body, helperErr := a.fetchHelperOutput(url)
	if helperErr == nil {
		var rel ghRelease
		if err := json.Unmarshal(body, &rel); err != nil {
			return nil, err
		}
		if rel.TagName == "" {
			return nil, fmt.Errorf("GitHub: tag rilis kosong")
		}
		return &rel, nil
	}
	a.logWarnf("cek via helper gagal: %v; coba direct", helperErr)
	rel, err, _ := a.fetchReleaseViaHTTP(a.proxyClient(""), url)
	if err != nil {
		// v1.0.35: hint WebUI harus menunjukkan alasan helper juga,
		// bukan cuma error direct (di v1.0.33/34 hint hanya menampilkan
		// DNS [::1] padahal akar masalahnya helper tak bisa spawn).
		return nil, fmt.Errorf("helper: %s; direct: %s", shortFetchErr(helperErr), shortFetchErr(err))
	}
	return rel, nil
}

// fetchReleaseListViaHTTP: satu GET daftar rilis lewat klien HTTP.
func (a *App) fetchReleaseListViaHTTP(client *http.Client, url string) ([]ghRelease, error, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err, false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "TaptapBox")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("GitHub status %d", resp.StatusCode), true
	}
	var rels []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, err, true
	}
	return rels, nil, true
}

// fetchReleaseListPath: daftar rilis lewat rantai yang sama dengan
// fetchReleasePath (proksi core -> helper non-root -> direct).
func (a *App) fetchReleaseListPath(path string) ([]ghRelease, error) {
	url := githubAPI + path
	if proxyURL := a.updateProxyURL(); proxyURL != "" {
		rels, err, connected := a.fetchReleaseListViaHTTP(a.proxyClient(proxyURL), url)
		if connected {
			return rels, err
		}
		a.logWarnf("daftar rilis via proksi %s gagal: %v; coba helper", proxyURL, err)
	}
	body, helperErr := a.fetchHelperOutput(url)
	if helperErr == nil {
		var rels []ghRelease
		if err := json.Unmarshal(body, &rels); err != nil {
			return nil, err
		}
		return rels, nil
	}
	a.logWarnf("daftar rilis via helper gagal: %v; coba direct", helperErr)
	rels, err, _ := a.fetchReleaseListViaHTTP(a.proxyClient(""), url)
	if err != nil {
		return nil, fmt.Errorf("helper: %s; direct: %s", shortFetchErr(helperErr), shortFetchErr(err))
	}
	return rels, nil
}

// versionEntry: satu baris daftar pilih versi (tag mentah untuk unduh,
// version tampilan, date sudah diformat Indonesia).
type versionEntry struct {
	Tag     string `json:"tag"`
	Version string `json:"version"`
	Date    string `json:"date"`
}

// listVersionsLimit: batas daftar pilih versi — geodata rilis harian
// (ratusan) dibatasi 10, sisanya 20.
func listVersionsLimit(item string) int {
	if item == "geodata" {
		return 10
	}
	return 20
}

// listVersions: rilis final terbaru repo item (draft/pra-rilis dibuang,
// urutan GitHub = terbaru dulu), dibatasi listVersionsLimit.
func (a *App) listVersions(item string) ([]versionEntry, error) {
	repo, ok := updateRepos[item]
	if !ok {
		return nil, fmt.Errorf("item tidak dikenal")
	}
	limit := listVersionsLimit(item)
	rels, err := a.fetchReleaseListPath(fmt.Sprintf("/repos/%s/releases?per_page=%d", repo, limit))
	if err != nil {
		return nil, err
	}
	var out []versionEntry
	for _, rel := range rels {
		if rel.TagName == "" || rel.Draft || rel.Prerelease {
			continue
		}
		date := ""
		if t, err := time.Parse(time.RFC3339, rel.PublishedAt); err == nil {
			date = humanDate(t)
		}
		out = append(out, versionEntry{Tag: rel.TagName, Version: stripTag(rel.TagName), Date: date})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// handleUpdateVersions: GET /api/updates/versions?item=X — daftar versi
// untuk pemilih versi di Tentang. Scrcpy belum terpasang: hanya versi
// terkunci (unduhan pertama dikunci ke versi teruji, bukan latest).
func (a *App) handleUpdateVersions(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	item := r.URL.Query().Get("item")
	switch item {
	case "singbox", "mihomo", "scrcpy", "geodata", "zashboard":
	default:
		errJSON(w, 400, "item tidak dikenal")
		return
	}
	if item == "scrcpy" && !a.scrcpyInstalled() {
		writeJSON(w, map[string]any{"ok": true, "item": item, "versions": []versionEntry{{Tag: "v" + scrcpyVersion, Version: scrcpyVersion}}})
		return
	}
	vers, err := a.listVersions(item)
	if err != nil {
		a.logWarnf("daftar versi %s gagal: %v", item, err)
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "item": item, "versions": vers})
}

// scrcpyInstalled: jar server ada di data/bin.
func (a *App) scrcpyInstalled() bool {
	_, err := os.Stat(filepath.Join(a.binDir, "scrcpy-server.jar"))
	return err == nil
}

// checkLatest: rilis terbaru kelima item, memakai cache 1 jam kecuali force.
// Scrcpy BELUM terpasang tidak dicek ke GitHub: tawarannya dikunci ke
// versi bawaan teruji (scrcpyVersion), bukan latest yang belum tentu cocok.
func (a *App) checkLatest(force bool) latestCache {
	a.updMu.Lock()
	defer a.updMu.Unlock()
	if !force && a.updCache != nil && time.Since(a.updCache.at) < updateCacheTTL {
		return *a.updCache
	}
	items := []string{"singbox", "mihomo", "scrcpy", "geodata", "zashboard"}
	if !a.scrcpyInstalled() {
		items = []string{"singbox", "mihomo", "geodata", "zashboard"}
	}
	out := latestCache{at: time.Now(), latest: map[string]string{}, err: map[string]string{}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, item := range items {
		wg.Add(1)
		go func(item string) {
			defer wg.Done()
			rel, err := a.fetchRelease(updateRepos[item])
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.err[item] = err.Error()
				// Catat ke log (bukan cuma ke respons) agar penyebab
				// "Gagal memeriksa" bisa ditelusur dari backend.log.
				a.logWarnf("cek rilis %s gagal: %v", item, err)
				return
			}
			out.latest[item] = stripTag(rel.TagName)
		}(item)
	}
	wg.Wait()
	if !a.scrcpyInstalled() {
		out.latest["scrcpy"] = scrcpyVersion
	}
	a.updCache = &out
	return out
}

// scrcpyServerVersion: versi server scrcpy yang dipakai argumen server.
// Dibaca dari data/bin/scrcpy-server.version (ditulis layout.sh saat flash
// + diperbarui tiap Download scrcpy) agar selalu cocok dengan jar yang
// terpasang; fallback = versi bawaan paket.
func (a *App) scrcpyServerVersion() string {
	if raw, err := os.ReadFile(filepath.Join(a.binDir, "scrcpy-server.version")); err == nil {
		if v := strings.TrimSpace(string(raw)); v != "" {
			return v
		}
	}
	return scrcpyVersion
}

var idMonths = []string{"", "Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}

// humanSize: "15,82 MB" (koma Indonesia, seperti file manager).
func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return strings.Replace(fmt.Sprintf("%.2f MB", float64(n)/(1<<20)), ".", ",", 1)
	case n >= 1<<10:
		return strings.Replace(fmt.Sprintf("%.2f KB", float64(n)/(1<<10)), ".", ",", 1)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// humanDate: "23 Okt 2026".
func humanDate(t time.Time) string {
	m := int(t.Month())
	if m < 1 || m > 12 {
		m = 1
	}
	return fmt.Sprintf("%d %s %d", t.Day(), idMonths[m], t.Year())
}

func geoInfo(path string) geoFileInfo {
	st, err := os.Stat(path)
	if err != nil {
		return geoFileInfo{Size: "-", Date: "-"}
	}
	return geoFileInfo{Size: humanSize(st.Size()), Date: humanDate(st.ModTime())}
}

// checkUpdates: susun respons kotak Download (current segar + latest cache).
func (a *App) checkUpdates(force bool) updatesData {
	lat := a.checkLatest(force)
	// Jar scrcpy hilang di tengah masa cache -> tawaran tetap yang
	// terkunci (bukan latest basi dari saat jar masih ada).
	if !a.scrcpyInstalled() {
		lat.latest["scrcpy"] = scrcpyVersion
		delete(lat.err, "scrcpy")
	}
	var d updatesData
	mk := func(item, current string, installed bool) updateInfo {
		inf := updateInfo{Current: current, Latest: lat.latest[item], Error: lat.err[item], Installed: installed}
		if inf.Latest != "" && compareVersions(inf.Latest, current) > 0 {
			inf.Update = true
		}
		if !installed && inf.Latest != "" {
			inf.Update = true // belum terpasang + ada tawaran = bisa diunduh
		}
		return inf
	}
	d.Singbox = mk("singbox", a.coreVersion("singbox"), a.coreInstalled("singbox"))
	d.Mihomo = mk("mihomo", a.coreVersion("clash"), a.coreInstalled("clash"))
	scrcpyCurrent := a.scrcpyServerVersion()
	scrcpyInstalled := a.scrcpyInstalled()
	if !scrcpyInstalled {
		scrcpyCurrent = ""
	}
	d.Scrcpy = mk("scrcpy", scrcpyCurrent, scrcpyInstalled)
	if !scrcpyInstalled {
		d.Scrcpy.Pinned = true
	}
	gd := filepath.Join(a.dataDir, "geodata")
	d.Geodata.Geoip = geoInfo(filepath.Join(gd, "geoip.dat"))
	d.Geodata.Geosite = geoInfo(filepath.Join(gd, "geosite.dat"))
	d.Geodata.Latest = lat.latest["geodata"]
	d.Geodata.Error = lat.err["geodata"]
	// Zashboard: unduhan tercatat versinya (zashboard.version); bawaan
	// modul tidak punya catatan versi -> current kosong, tapi tetap
	// "terpasang" (bundel di webui/). Selama rilis diketahui, bawaan
	// modul selalu bisa diunduh (perbandingan normal berlaku sesudah
	// unduhan pertama mencatat tag).
	zbDownloaded := a.zashboardDownloaded()
	zbCurrent := ""
	if zbDownloaded {
		zbCurrent = a.zashboardVersion()
	}
	zbInstalled := zbDownloaded
	if !zbInstalled {
		if _, err := os.Stat(filepath.Join(a.webuiDir, "zashboard", "index.html")); err == nil {
			zbInstalled = true
		}
	}
	d.Zashboard = mk("zashboard", zbCurrent, zbInstalled)
	if zbInstalled && zbCurrent == "" && lat.latest["zashboard"] != "" {
		d.Zashboard.Update = true
	}
	return d
}

func (a *App) handleUpdates(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	writeJSON(w, a.checkUpdates(r.URL.Query().Get("refresh") == "1"))
}

// selfRelease: respons GET /api/release/latest — kartu "Rilis terbaru"
// di Tentang. Repo-nya repo modul ini sendiri (Taptap56/Taptapapp),
// bukan repo core: tag/nama/tanggal/body/URL halaman rilis diambil dari
// GitHub releases/latest lewat rantai yang sama dengan cek core
// (proksi core -> helper non-root -> direct), jadi jalan di jalur yang
// sama dengan kotak Download. Aset yang ditawarkan = zip instalasi
// (taptapbox-<tag>.zip bila ada, kalau tidak aset .zip pertama);
// Source code (zip/tar.gz) yang selalu ditambahkan GitHub TIDAK
// ditawarkan — itu arsip source repo, bukan paket instalasi.
// Gagal cek TIDAK jadi 500: WebUI tetap menampilkan tombol fallback
// ke https://github.com/Taptap56/Taptapapp/releases/latest, yang
// selalu meneruskan ke rilis terbaru.
type selfRelease struct {
	Current     string `json:"current"`
	Tag         string `json:"tag"`
	Name        string `json:"name"`
	PublishedAt string `json:"published_at"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	AssetName   string `json:"asset_name"`
	AssetSize   int64  `json:"asset_size"`
	AssetURL    string `json:"asset_url"`
	Error       string `json:"error,omitempty"`
}

// selfRelCache: cache kartu Rilis terbaru, mirip updCache di kotak
// Download: buka Tentang berulang dalam 1 jam tidak perlu fetch
// GitHub lagi (itu penyebab kartu Rilis selalu loading tiap dibuka),
// dan tombol Periksa lagi memaksa cek segar lewat ?refresh=1.
type selfRelCache struct {
	at  time.Time
	out selfRelease
}

func (a *App) handleSelfRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	force := r.URL.Query().Get("refresh") == "1"
	if !force {
		a.updMu.Lock()
		cached := a.selfRelCache
		a.updMu.Unlock()
		if cached != nil && time.Since(cached.at) < updateCacheTTL {
			writeJSON(w, cached.out)
			return
		}
	}
	rel, err := a.fetchRelease("Taptap56/Taptapapp")
	if err != nil {
		out := selfRelease{Current: appVersion, Error: err.Error()}
		a.updMu.Lock()
		a.selfRelCache = &selfRelCache{at: time.Now(), out: out}
		a.updMu.Unlock()
		writeJSON(w, out)
		return
	}
	out := selfRelease{
		Current:     appVersion,
		Tag:         rel.TagName,
		Name:        rel.Name,
		PublishedAt: rel.PublishedAt,
		Body:        rel.Body,
		HTMLURL:     rel.HTMLURL,
	}
	want := "taptapbox-" + rel.TagName + ".zip"
	for _, as := range rel.Assets {
		if as.Name == want {
			out.AssetName, out.AssetSize, out.AssetURL = as.Name, as.Size, as.BrowserDownloadURL
			break
		}
	}
	if out.AssetURL == "" {
		for _, as := range rel.Assets {
			if strings.HasSuffix(as.Name, ".zip") && strings.HasPrefix(as.Name, "taptapbox-") {
				out.AssetName, out.AssetSize, out.AssetURL = as.Name, as.Size, as.BrowserDownloadURL
				break
			}
		}
	}
	if out.AssetURL == "" && len(rel.Assets) > 0 {
		as := rel.Assets[0]
		out.AssetName, out.AssetSize, out.AssetURL = as.Name, as.Size, as.BrowserDownloadURL
	}
	a.updMu.Lock()
	a.selfRelCache = &selfRelCache{at: time.Now(), out: out}
	a.updMu.Unlock()
	writeJSON(w, out)
}

// downloadViaHTTP: satu unduhan lewat klien HTTP (proksi/direct).
// connected=false berarti koneksi gagal total (pemanggil boleh coba jalur
// lain); connected=true berarti server menjawab — hasilnya dipakai apa adanya.
func (a *App) downloadViaHTTP(client *http.Client, url, dst string) (error, bool) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err, false
	}
	req.Header.Set("User-Agent", "TaptapBox")
	resp, err := client.Do(req)
	if err != nil {
		return err, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("unduhan gagal: status %d", resp.StatusCode), true
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err, true
	}
	_, err = io.Copy(f, resp.Body)
	cerr := f.Close()
	if err != nil {
		return err, true
	}
	return cerr, true
}

// downloadViaHelper: unduhan lewat anak proses helper (streaming stdout
// -> file; gagal di tengah -> file parsial dihapus).
func (a *App) downloadViaHelper(url, dst string) error {
	stream, err := a.fetchHelperStream(url)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		stream.Kill()
		_ = stream.Close()
		return err
	}
	_, copyErr := io.Copy(f, stream)
	cerr := f.Close()
	if copyErr != nil {
		stream.Kill()
		_ = stream.Close()
		_ = os.Remove(dst)
		return copyErr
	}
	if err := stream.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return cerr
}

// downloadFile: unduh URL ke dst (tanpa timeout total — file bisa 60 MB)
// lewat rantai proksi core -> helper non-root -> direct.
func (a *App) downloadFile(url, dst string) error {
	if proxyURL := a.updateProxyURL(); proxyURL != "" {
		err, connected := a.downloadViaHTTP(a.proxyClient(proxyURL), url, dst)
		if connected {
			return err
		}
		a.logWarnf("unduh via proksi %s gagal: %v; coba helper", proxyURL, err)
	}
	if helperErr := a.downloadViaHelper(url, dst); helperErr == nil {
		return nil
	} else {
		a.logWarnf("unduh via helper gagal: %v; coba direct", helperErr)
		if derr, _ := a.downloadViaHTTP(a.proxyClient(""), url, dst); derr != nil {
			return fmt.Errorf("helper: %s; direct: %s", shortFetchErr(helperErr), shortFetchErr(derr))
		}
		return nil
	}
}

// pickAsset: cari aset rilis berdasarkan predikat nama; error jelas bila tak ada.
func pickAsset(rel *ghRelease, pred func(name string) bool) (string, error) {
	for _, as := range rel.Assets {
		if pred(as.Name) && as.BrowserDownloadURL != "" {
			return as.BrowserDownloadURL, nil
		}
	}
	return "", fmt.Errorf("aset tidak ditemukan di rilis %s", rel.TagName)
}

// extractSingbox: bongkar tar.gz sing-box, ambil biner "sing-box" ke dst.
func extractSingbox(tgz, dst string) error {
	f, err := os.Open(tgz)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("bukan gzip valid: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != "sing-box" {
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		cerr := out.Close()
		if err != nil {
			return err
		}
		return cerr
	}
	return fmt.Errorf("biner sing-box tidak ada di dalam tar.gz")
}

// gunzipFile: bongkar .gz (mihomo) ke dst.
func gunzipFile(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("bukan gzip valid: %v", err)
	}
	defer gz.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, gz)
	cerr := out.Close()
	if err != nil {
		return err
	}
	return cerr
}

// validateCoreBinary: biner hasil unduhan harus bisa jalan dan melaporkan
// versi yang sama dengan tag rilis (menangkap aset salah arsitektur/rusak).
func validateCoreBinary(bin, kind, want string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var args []string
	if kind == "mihomo" {
		args = []string{"-v"}
	} else {
		args = []string{"version"}
	}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("biner hasil unduhan tidak bisa jalan: %v", err)
	}
	var got string
	if kind == "mihomo" {
		got = parseMihomoVersion(string(out))
	} else {
		got = parseSingboxVersion(string(out))
	}
	if got == "" {
		return fmt.Errorf("versi biner hasil unduhan tak terbaca: %s", strings.TrimSpace(string(out)))
	}
	if got != want {
		return fmt.Errorf("versi biner %s, bukan %s yang diminta", got, want)
	}
	return nil
}

// validateJar: jar scrcpy harus arsip ZIP valid berisi kelas Server.
// validateJar: jar server scrcpy resmi adalah jar Android berisi
// classes.dex (bukan .class) — Server hidup di dalam dex. Terima bila ada
// scrcpy/Server.class (build klasik) ATAU classes.dex yang memuat
// deskriptor kelas Server. Koreksi v1.0.31: cek lama (Server.class saja)
// menolak jar resmi 4.1/5.0.1 yang juga hanya berisi classes.dex.
func validateJar(path string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("bukan jar valid: %v", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "scrcpy/Server.class") {
			return nil
		}
		if f.Name == "classes.dex" {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			dex, err := io.ReadAll(rc)
			_ = rc.Close()
			if err == nil && bytes.Contains(dex, []byte("Lcom/genymobile/scrcpy/Server;")) {
				return nil
			}
		}
	}
	return fmt.Errorf("kelas Server tidak ada di dalam jar")
}

// installFile: pasang file baru ke target, langsung timpa tanpa backup
// (v1.0.41: tanpa .prev/.bak — file lama tidak disimpan).
func installFile(tmp, target string) error {
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("gagal memasang %s: %v", target, err)
	}
	_ = os.Chmod(target, 0755)
	return nil
}

// restartCoreIfRunning: restart core aktif bila sedang jalan; gagal =
// jalankan restore (bila ada) dulu lalu start lagi, kembalikan error asal.
// Sejak v1.0.41 restore selalu nil (tanpa backup) — gagal start hanya
// dilaporkan, core dicoba di-start lagi dengan biner baru.
func (a *App) restartCoreIfRunning(restore func()) error {
	if !a.coreAlive() {
		return nil
	}
	a.stopCore()
	time.Sleep(time.Second)
	if err := a.startCore(); err != nil || a.verifyRunning() != nil {
		if restore != nil {
			restore()
		}
		_ = a.startCore()
		if err != nil {
			return err
		}
		return fmt.Errorf("core gagal start setelah pembaruan")
	}
	return nil
}

// tmpUpdatesDir: folder kerja unduhan (di bawah data, bukan /tmp —
// /tmp Android belum tentu bisa ditulisi daemon).
func (a *App) tmpUpdatesDir() (string, error) {
	d := filepath.Join(a.dataDir, "tmp-updates")
	if err := os.MkdirAll(d, 0755); err != nil {
		return "", err
	}
	return d, nil
}

// installCore: unduh + validasi + pasang core (singbox/mihomo).
// tag kosong = rilis latest; tag terisi = rilis tag itu (pemilih versi).
func (a *App) installCore(item, tmpDir, tag string) (string, error) {
	var rel *ghRelease
	var err error
	if tag != "" {
		rel, err = a.fetchReleaseTag(updateRepos[item], tag)
	} else {
		rel, err = a.fetchRelease(updateRepos[item])
	}
	if err != nil {
		return "", err
	}
	ver := stripTag(rel.TagName)
	var url string
	if item == "singbox" {
		url, err = pickAsset(rel, func(n string) bool {
			return strings.Contains(n, "android-arm64") && strings.HasSuffix(n, ".tar.gz")
		})
	} else {
		url, err = pickAsset(rel, func(n string) bool {
			return strings.Contains(n, "android-arm64") && strings.HasSuffix(n, ".gz")
		})
	}
	if err != nil {
		return "", err
	}
	arc := filepath.Join(tmpDir, item+".pkg")
	if err := a.downloadFile(url, arc); err != nil {
		return "", fmt.Errorf("unduh %s gagal: %v", item, err)
	}
	bin := filepath.Join(tmpDir, item)
	if item == "singbox" {
		if err := extractSingbox(arc, bin); err != nil {
			return "", err
		}
	} else {
		if err := gunzipFile(arc, bin); err != nil {
			return "", err
		}
	}
	if err := validateCoreBinary(bin, item, ver); err != nil {
		return "", err
	}
	target := filepath.Join(a.binDir, map[string]string{"singbox": "sing-box", "mihomo": "mihomo"}[item])
	wasRunning := a.coreAlive()
	runningCore := a.runningCore
	if err := installFile(bin, target); err != nil {
		return "", err
	}
	// Restart hanya bila core yang diganti itu yang sedang jalan.
	needRestart := wasRunning && ((item == "singbox" && runningCore == "singbox") || (item == "mihomo" && runningCore == "clash"))
	if needRestart {
		if err := a.restartCoreIfRunning(nil); err != nil {
			return "", err
		}
	} else {
		a.detectCoreVersions()
	}
	a.logf("pembaruan %s %s terpasang", item, ver)
	return ver, nil
}

// scrcpyAssetMatch: aset server resmi ("scrcpy-server-vX.Y" tanpa .jar,
// atau .jar klasik).
func scrcpyAssetMatch(n string) bool {
	return strings.Contains(n, "scrcpy-server") && (strings.HasSuffix(n, ".jar") || strings.HasPrefix(n, "scrcpy-server-v"))
}

// installScrcpy: unduh jar server + catat versinya (argumen server harus
// persis sama dengan versi jar, dibaca dari scrcpy-server.version).
// Jar BELUM ada -> rilis dikunci ke versi bawaan teruji (scrcpyVersion),
// bukan latest (versi baru belum tentu cocok dengan relay daemon);
// tag eksplisit saat jar belum ada yang bukan versi terkunci ditolak.
// tag kosong + jar sudah ada -> latest normal.
func (a *App) installScrcpy(tmpDir, tag string) (string, error) {
	var rel *ghRelease
	var err error
	if !a.scrcpyInstalled() {
		if tag != "" && tag != "v"+scrcpyVersion {
			return "", fmt.Errorf("scrcpy pertama dikunci ke %s (versi teruji)", scrcpyVersion)
		}
		rel, err = a.fetchReleaseTag(updateRepos["scrcpy"], "v"+scrcpyVersion)
	} else if tag != "" {
		rel, err = a.fetchReleaseTag(updateRepos["scrcpy"], tag)
	} else {
		rel, err = a.fetchRelease(updateRepos["scrcpy"])
	}
	if err != nil {
		return "", err
	}
	ver := stripTag(rel.TagName)
	// Aset rilis resmi bernama "scrcpy-server-vX.Y" TANPA akhiran .jar
	// (koreksi v1.0.31: predikat lama yang mewajibkan .jar tak pernah cocok).
	url, err := pickAsset(rel, scrcpyAssetMatch)
	if err != nil {
		return "", err
	}
	jar := filepath.Join(tmpDir, "scrcpy-server.jar")
	if err := a.downloadFile(url, jar); err != nil {
		return "", fmt.Errorf("unduh scrcpy gagal: %v", err)
	}
	if err := validateJar(jar); err != nil {
		return "", err
	}
	target := filepath.Join(a.binDir, "scrcpy-server.jar")
	if err := installFile(jar, target); err != nil {
		return "", err
	}
	// Catat versi jar baru; tanpa ini server menolak argumen versi lama.
	if err := os.WriteFile(filepath.Join(a.binDir, "scrcpy-server.version"), []byte(ver+"\n"), 0644); err != nil {
		a.logWarnf("gagal tulis scrcpy-server.version: %v", err)
	}
	a.logf("pembaruan scrcpy %s terpasang", ver)
	return ver, nil
}

// installGeodata: unduh geoip.dat + geosite.dat rilis terbaru, pasang,
// restart core aktif (geodata dibaca saat core start).
// tag kosong = latest; tag terisi = rilis tag itu (pemilih versi).
func (a *App) installGeodata(tmpDir, tag string) (string, error) {
	var rel *ghRelease
	var err error
	if tag != "" {
		rel, err = a.fetchReleaseTag(updateRepos["geodata"], tag)
	} else {
		rel, err = a.fetchRelease(updateRepos["geodata"])
	}
	if err != nil {
		return "", err
	}
	ver := stripTag(rel.TagName)
	gd := filepath.Join(a.dataDir, "geodata")
	if err := os.MkdirAll(gd, 0755); err != nil {
		return "", err
	}
	type dl struct {
		name string
		tmp  string
	}
	var dls []dl
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		url, err := pickAsset(rel, func(n string) bool { return n == name })
		if err != nil {
			return "", err
		}
		tmp := filepath.Join(tmpDir, name)
		if err := a.downloadFile(url, tmp); err != nil {
			return "", fmt.Errorf("unduh %s gagal: %v", name, err)
		}
		st, err := os.Stat(tmp)
		if err != nil || st.Size() < 1<<17 {
			return "", fmt.Errorf("%s hasil unduhan terlalu kecil/corrupt", name)
		}
		// Tangkap halaman error HTML yang tersamar sebagai .dat.
		head := make([]byte, 1)
		if f, err := os.Open(tmp); err == nil {
			_, _ = f.Read(head)
			f.Close()
			if head[0] == '<' {
				return "", fmt.Errorf("%s hasil unduhan bukan file dat", name)
			}
		}
		dls = append(dls, dl{name, tmp})
	}
	// Kedua file sudah divalidasi SEBELUM pasang, jadi gagal pasang
	// praktis hanya galat filesystem; kondisi campur (satu file baru,
	// satu lama) hanya bisa terjadi di titik itu.
	for _, d := range dls {
		if err := installFile(d.tmp, filepath.Join(gd, d.name)); err != nil {
			return "", err
		}
	}
	if err := a.restartCoreIfRunning(nil); err != nil {
		return "", err
	}
	a.logf("pembaruan geodata %s terpasang", ver)
	return ver, nil
}

// zashboardDownloadDir: folder zashboard hasil Download. SENGAJA di luar
// webui/: layout.sh MENGGANTI webui/ dengan salinan modul tiap boot,
// jadi unduhan yang ditaruh di webui/zashboard akan kembali ke bawaan
// modul saat reboot (jebakan yang sama seperti restore core dulu).
// Bundel modul di webui/zashboard tetap utuh sebagai fallback.
func (a *App) zashboardDownloadDir() string {
	return filepath.Join(a.dataDir, "zashboard")
}

// zashboardDownloaded: ada unduhan zashboard yang valid (index.html ada).
func (a *App) zashboardDownloaded() bool {
	st, err := os.Stat(filepath.Join(a.zashboardDownloadDir(), "index.html"))
	return err == nil && !st.IsDir()
}

// zashboardDir: folder zashboard yang dipakai — unduhan bila ada, sonst
// bawaan modul di webui/. Dipakai handler /zashboard/ + generator
// external-ui kedua core, agar semua jalur melihat versi yang sama.
func (a *App) zashboardDir() string {
	if a.zashboardDownloaded() {
		return a.zashboardDownloadDir()
	}
	return filepath.Join(a.webuiDir, "zashboard")
}

// zashboardVersion: tag unduhan dari zashboard.version ("" bila bawaan).
func (a *App) zashboardVersion() string {
	raw, err := os.ReadFile(filepath.Join(a.dataDir, "zashboard.version"))
	if err != nil {
		return ""
	}
	return stripTag(string(raw))
}

// extractZip: bongkar zip ke dst (tolak path absolut / lolos "..").
func extractZip(zipPath, dst string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("bukan zip valid: %v", err)
	}
	defer r.Close()
	for _, f := range r.File {
		name := filepath.Clean(f.Name)
		if filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(os.PathSeparator)) || name == ".." {
			return fmt.Errorf("entri zip tidak aman: %s", f.Name)
		}
		target := filepath.Join(dst, name)
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(target), 0755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		_ = out.Close()
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// findIndexRoot: folder hasil ekstrak yang memuat index.html — folder itu
// sendiri, atau satu subfolder tunggal (rilis yang membungkus dist/).
func findIndexRoot(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
		return dir
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(sub, "index.html")); err == nil {
			return sub
		}
	}
	return ""
}

// installZashboard: unduh dist.zip rilis terbaru (Zephyruso/zashboard),
// ekstrak, validasi index.html, pasang ke data/zashboard + catat tag,
// restart core bila jalan (external-ui menunjuk folder baru).
// tag kosong = latest; tag terisi = rilis tag itu (pemilih versi).
func (a *App) installZashboard(tmpDir, tag string) (string, error) {
	var rel *ghRelease
	var err error
	if tag != "" {
		rel, err = a.fetchReleaseTag(updateRepos["zashboard"], tag)
	} else {
		rel, err = a.fetchRelease(updateRepos["zashboard"])
	}
	if err != nil {
		return "", err
	}
	ver := stripTag(rel.TagName)
	url, err := pickAsset(rel, func(n string) bool { return n == "dist.zip" })
	if err != nil {
		return "", err
	}
	zipPath := filepath.Join(tmpDir, "dist.zip")
	if err := a.downloadFile(url, zipPath); err != nil {
		return "", fmt.Errorf("unduh zashboard gagal: %v", err)
	}
	extDir := filepath.Join(tmpDir, "extracted")
	if err := extractZip(zipPath, extDir); err != nil {
		return "", err
	}
	root := findIndexRoot(extDir)
	if root == "" {
		return "", fmt.Errorf("dist.zip tidak berisi index.html")
	}
	dst := a.zashboardDownloadDir()
	verPath := filepath.Join(a.dataDir, "zashboard.version")
	// Tanpa backup (v1.0.41): unduhan lama langsung dihapus, bukan
	// dipindah ke .bak. Rename satu filesystem (tmp-updates dan data
	// sama-sama di bawah dataDir) praktis tidak gagal; bila gagal,
	// dashboard lama sudah terhapus — unduh ulang memulihkannya.
	if _, err := os.Stat(dst); err == nil {
		if err := os.RemoveAll(dst); err != nil {
			return "", fmt.Errorf("gagal menghapus zashboard lama: %v", err)
		}
	}
	if err := os.Rename(root, dst); err != nil {
		return "", fmt.Errorf("gagal memasang zashboard: %v", err)
	}
	if err := os.WriteFile(verPath, []byte(ver+"\n"), 0644); err != nil {
		a.logWarnf("gagal tulis zashboard.version: %v", err)
	}
	if err := a.restartCoreIfRunning(nil); err != nil {
		return "", err
	}
	a.logf("pembaruan zashboard %s terpasang", ver)
	return ver, nil
}

// handleUpdateDownload: POST /api/updates/download {item}.
func (a *App) handleUpdateDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	var body struct {
		Item string `json:"item"`
		Tag  string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Item == "" {
		errJSON(w, 400, "item tidak valid")
		return
	}
	switch body.Item {
	case "singbox", "mihomo", "scrcpy", "geodata", "zashboard":
	default:
		errJSON(w, 400, "item tidak dikenal")
		return
	}
	// Tag eksplisit (pemilih versi) hanya boleh karakter tag GitHub yang
	// wajar — cegah path traversal lewat URL API.
	if body.Tag != "" {
		for _, c := range body.Tag {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
				errJSON(w, 400, "tag tidak valid")
				return
			}
		}
	}
	if !a.updDlMu.TryLock() {
		errJSON(w, 409, "pembaruan lain sedang berjalan")
		return
	}
	defer a.updDlMu.Unlock()
	tmpDir, err := a.tmpUpdatesDir()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	defer os.RemoveAll(tmpDir)
	var ver string
	switch body.Item {
	case "singbox", "mihomo":
		errJSON(w, 410, "core sudah tertanam sebagai library di TaptapBox — tidak perlu unduh")
		return
	case "scrcpy":
		ver, err = a.installScrcpy(tmpDir, body.Tag)
	case "geodata":
		ver, err = a.installGeodata(tmpDir, body.Tag)
	case "zashboard":
		ver, err = a.installZashboard(tmpDir, body.Tag)
	}
	if err != nil {
		a.logWarnf("pembaruan %s gagal: %v", body.Item, err)
		errJSON(w, 500, err.Error())
		return
	}
	// Segarkan cache "current" untuk Tentang (latest cache tetap dipakai).
	a.detectCoreVersions()
	writeJSON(w, map[string]any{"ok": true, "item": body.Item, "version": ver})
}
