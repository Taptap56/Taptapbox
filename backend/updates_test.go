package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStripTag(t *testing.T) {
	for in, want := range map[string]string{"v1.19.33": "1.19.33", "V2.0": "2.0", "1.2.3": "1.2.3", "  v0.1  ": "0.1"} {
		if got := stripTag(in); got != want {
			t.Fatalf("stripTag(%q) = %q, mau %q", in, got, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.19.32", "1.19.33", -1},
		{"1.19.33", "1.19.32", 1},
		{"1.19.33", "1.19.33", 0},
		{"v1.19.9", "1.19.33", -1}, // numerik, bukan leksikografis
		{"1.14.3", "1.14.3", 0},
		{"2.0", "1.99.99", 1},
		{"20261024", "20261023", 1},
		{"1.19.33-alpha", "1.19.33", 0}, // sufiks pra-rilis diabaikan
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Fatalf("compareVersions(%q, %q) = %d, mau %d", c.a, c.b, got, c.want)
		}
	}
}

// updateProxyURL: direct bila core mati (apa pun setting port-nya),
// proksi mixed lokal bila core jalan. Core jalan tidak diuji di sini
// (butuh proses hidup); yang diuji adalah gerbang-gerbangnya.
func TestUpdateProxyURL(t *testing.T) {
	if got := (&App{}).updateProxyURL(); got != "" {
		t.Fatalf("App kosong harus direct, dapat %q", got)
	}
	a := &App{store: &Store{Settings: Settings{MixedPort: 18080}}}
	if got := a.updateProxyURL(); got != "" {
		t.Fatalf("core mati harus direct, dapat %q", got)
	}
	b := &App{store: &Store{Settings: Settings{}}}
	if got := b.updateProxyURL(); got != "" {
		t.Fatalf("port 0 harus direct, dapat %q", got)
	}
}

// doUpdate: proksi yang connection refused harus jatuh ke direct,
// bukan membuat cek mati (regresi laporan 2026-10-25).
// Rantai updater v1.0.33: helper non-root + DNS eksplisit + fallback direct.
// (doUpdate lama diganti rantai ini; helper di-stub lewat runner var.)

// fetchViaTransport: host IP tanpa DNS + host tak dikenal dengan DNS mati.
func TestFetchHelperTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()
	rc, err := fetchViaTransport(srv.URL, []string{"127.0.0.1:1"})
	if err != nil {
		t.Fatalf("host IP harus jalan tanpa DNS: %v", err)
	}
	body, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(body) != "ok" {
		t.Fatalf("body = %q", body)
	}
	if _, err := fetchViaTransport("http://tidak-ada.invalid:9/x", []string{"127.0.0.1:1"}); err == nil {
		t.Fatal("DNS mati harus error, bukan menggantung")
	}
}

func TestParseHelperUIDsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "packages.list")
	_ = os.WriteFile(p, []byte("com.sistem 1000 0 /x\ncom.contoh 10123 0 /y\ncom.lain 10200 0 /z\n"), 0644)
	got := parseHelperUIDsFile(p)
	if len(got) != 2 || got[0].uid != 10123 || got[1].uid != 10200 {
		t.Fatalf("kandidat = %+v", got)
	}
	_ = os.WriteFile(p, []byte("com.sistem 1000 0 /x\n"), 0644)
	if len(parseHelperUIDsFile(p)) != 0 {
		t.Fatalf("tanpa UID aplikasi harus kosong")
	}
}

// v1.0.37: kandidat helper — paket preferensi (Chrome) dulu, lalu urutan
// file, dedup, dibatasi; fallback [10000] bila tak ada.
func TestHelperCandidateUIDs(t *testing.T) {
	old := packagesListPath
	defer func() { packagesListPath = old }()
	p := filepath.Join(t.TempDir(), "packages.list")
	_ = os.WriteFile(p, []byte("com.contoh 10123 0 /y\ncom.android.chrome 10101 0 /c\ncom.lain 10200 0 /z\ncom.android.chrome 10101 0 /c\n"), 0644)
	packagesListPath = p
	got := helperCandidateUIDs()
	want := []int{10101, 10123, 10200}
	if len(got) != len(want) {
		t.Fatalf("kandidat = %v, mau %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kandidat = %v, mau %v", got, want)
		}
	}
	packagesListPath = filepath.Join(t.TempDir(), "tidak-ada")
	if got := helperCandidateUIDs(); len(got) != 1 || got[0] != 10000 {
		t.Fatalf("fallback = %v, mau [10000]", got)
	}
}

// v1.0.37: fetchHelperOutput multi-UID — paralel, pemenang di-cache;
// gagal total menyebut tiap UID; UID cache dicoba dulu sendirian.
func TestFetchHelperOutputMultiUID(t *testing.T) {
	oldOut, oldPath := helperOutputRunner, packagesListPath
	defer func() { helperOutputRunner, packagesListPath = oldOut, oldPath }()
	p := filepath.Join(t.TempDir(), "packages.list")
	_ = os.WriteFile(p, []byte("com.contoh 10123 0 /y\ncom.android.chrome 10101 0 /c\ncom.lain 10200 0 /z\n"), 0644)
	packagesListPath = p
	a := &App{}
	helperOutputRunner = func(ctx context.Context, url, dns string, uid int) ([]byte, error) {
		if uid == 10200 {
			return []byte("dari-10200"), nil
		}
		return nil, fmt.Errorf("uid %d mati", uid)
	}
	body, err := a.fetchHelperOutput("https://example.com/x")
	if err != nil || string(body) != "dari-10200" {
		t.Fatalf("pemenang paralel = %q, %v", body, err)
	}
	if got := a.cachedHelperUID(); got != 10200 {
		t.Fatalf("cache = %d, mau 10200", got)
	}
	// Gagal total: semua UID disebut.
	helperOutputRunner = func(ctx context.Context, url, dns string, uid int) ([]byte, error) {
		return nil, fmt.Errorf("mati")
	}
	b := &App{}
	if _, err := b.fetchHelperOutput("https://example.com/x"); err == nil ||
		!strings.Contains(err.Error(), "uid 10101") || !strings.Contains(err.Error(), "uid 10200") {
		t.Fatalf("error gagal total = %v", err)
	}
	// UID cache dicoba dulu sendirian (urutan panggilan pertama = cache).
	var mu sync.Mutex
	var order []int
	helperOutputRunner = func(ctx context.Context, url, dns string, uid int) ([]byte, error) {
		mu.Lock()
		order = append(order, uid)
		mu.Unlock()
		if uid == 10200 {
			return []byte("dari-cache"), nil
		}
		return nil, fmt.Errorf("mati")
	}
	c := &App{}
	c.setCachedHelperUID(10200)
	body, err = c.fetchHelperOutput("https://example.com/x")
	if err != nil || string(body) != "dari-cache" || len(order) != 1 || order[0] != 10200 {
		t.Fatalf("cache-first: body=%q err=%v order=%v", body, err, order)
	}
}

// v1.0.37: fetchHelperStream multi-UID — berurutan, cache dulu, lalu
// kandidat; pemenang di-cache.
func TestFetchHelperStreamMultiUID(t *testing.T) {
	oldStream, oldPath := helperStreamRunner, packagesListPath
	defer func() { helperStreamRunner, packagesListPath = oldStream, oldPath }()
	p := filepath.Join(t.TempDir(), "packages.list")
	_ = os.WriteFile(p, []byte("com.android.chrome 10101 0 /c\ncom.lain 10200 0 /z\n"), 0644)
	packagesListPath = p
	a := &App{}
	helperStreamRunner = func(url, dns string, uid int) (*helperStream, error) {
		if uid != 10200 {
			return nil, fmt.Errorf("uid %d mati", uid)
		}
		cmd := exec.Command("true")
		_ = cmd.Start()
		return &helperStream{rc: io.NopCloser(strings.NewReader("dari-10200")), cmd: cmd, errBuf: &bytes.Buffer{}}, nil
	}
	stream, err := a.fetchHelperStream("https://example.com/x")
	if err != nil {
		t.Fatalf("stream = %v", err)
	}
	b, _ := io.ReadAll(stream)
	_ = stream.Close()
	if string(b) != "dari-10200" {
		t.Fatalf("isi stream = %q", b)
	}
	if got := a.cachedHelperUID(); got != 10200 {
		t.Fatalf("cache = %d, mau 10200", got)
	}
}

func TestBuildDNSList(t *testing.T) {
	got := buildDNSList("8.8.8.8", "::1")
	want := []string{"8.8.8.8", "1.1.1.1"}
	if len(got) != len(want) {
		t.Fatalf("dns = %v, mau %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dns = %v, mau %v", got, want)
		}
	}
}

// fetchReleasePath: helper sukses -> dipakai (direct tak disentuh);
// helper gagal -> jatuh ke direct.
func TestFetchReleaseChain(t *testing.T) {
	oldOut, oldStream := helperOutputRunner, helperStreamRunner
	defer func() { helperOutputRunner, helperStreamRunner = oldOut, oldStream }()
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9","assets":[]}`))
	}))
	defer direct.Close()
	oldAPI := githubAPI
	githubAPI = direct.URL
	defer func() { githubAPI = oldAPI }()
	a := &App{}

	helperOutputRunner = func(ctx context.Context, url, dns string, uid int) ([]byte, error) {
		return []byte(`{"tag_name":"v7.7.7","assets":[]}`), nil
	}
	rel, err := a.fetchReleasePath("/repos/x/y/releases/latest")
	if err != nil || rel.TagName != "v7.7.7" {
		t.Fatalf("helper harus dipakai: rel=%+v err=%v", rel, err)
	}
	helperOutputRunner = func(ctx context.Context, url, dns string, uid int) ([]byte, error) {
		return nil, fmt.Errorf("helper mati")
	}
	rel, err = a.fetchReleasePath("/repos/x/y/releases/latest")
	if err != nil || rel.TagName != "v9.9.9" {
		t.Fatalf("helper gagal harus jatuh ke direct: rel=%+v err=%v", rel, err)
	}
}

// downloadFile: helper stream sukses -> file terisi; helper gagal -> direct.
func TestDownloadFileChain(t *testing.T) {
	oldOut, oldStream := helperOutputRunner, helperStreamRunner
	defer func() { helperOutputRunner, helperStreamRunner = oldOut, oldStream }()
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("dari-direct"))
	}))
	defer direct.Close()
	a := &App{}
	dst := filepath.Join(t.TempDir(), "f.bin")

	helperStreamRunner = func(url, dns string, uid int) (*helperStream, error) {
		// Stream palsu: body dari bytes, Close sukses (tanpa proses).
		// helperStream butuh cmd non-nil untuk Close; pakai perintah
		// yang langsung keluar (true) agar Wait sukses.
		cmd := exec.Command("true")
		_ = cmd.Start()
		return &helperStream{rc: io.NopCloser(strings.NewReader("dari-helper")), cmd: cmd, errBuf: &bytes.Buffer{}}, nil
	}
	if err := a.downloadFile(direct.URL, dst); err != nil {
		t.Fatalf("helper stream: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "dari-helper" {
		t.Fatalf("file = %q", b)
	}
	helperStreamRunner = func(url, dns string, uid int) (*helperStream, error) {
		return nil, fmt.Errorf("helper mati")
	}
	if err := a.downloadFile(direct.URL, dst); err != nil {
		t.Fatalf("helper gagal harus jatuh ke direct: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "dari-direct" {
		t.Fatalf("file = %q", b)
	}
}

// scrcpyAssetMatch: aset resmi tanpa .jar harus cocok (regresi v1.0.31).
func TestScrcpyAssetMatch(t *testing.T) {
	for name, want := range map[string]bool{
		"scrcpy-server-v5.0.1":              true,
		"scrcpy-server.jar":                 true,
		"scrcpy-server-v4.1.jar":            true,
		"scrcpy-linux-x86_64-v5.0.1.tar.gz": false,
		"SHA256SUMS.txt":                    false,
	} {
		if got := scrcpyAssetMatch(name); got != want {
			t.Fatalf("%s: dapat %v, mau %v", name, got, want)
		}
	}
}

// validateJar: jar resmi berisi classes.dex (bukan Server.class).
func TestValidateJarDex(t *testing.T) {
	mk := func(entries map[string]string) string {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, content := range entries {
			w, _ := zw.Create(name)
			_, _ = w.Write([]byte(content))
		}
		_ = zw.Close()
		p := filepath.Join(t.TempDir(), "s.jar")
		_ = os.WriteFile(p, buf.Bytes(), 0644)
		return p
	}
	if err := validateJar(mk(map[string]string{"classes.dex": "xxLcom/genymobile/scrcpy/Server;yy"})); err != nil {
		t.Fatalf("dex resmi harus valid: %v", err)
	}
	if err := validateJar(mk(map[string]string{"classes.dex": "tanpa kelas server"})); err == nil {
		t.Fatal("dex tanpa Server harus ditolak")
	}
	if err := validateJar(mk(map[string]string{"README.txt": "bukan jar server"})); err == nil {
		t.Fatal("zip tanpa dex/kelas harus ditolak")
	}
}

func TestPickAsset(t *testing.T) {
	rel := &ghRelease{TagName: "v9.9.9"}
	rel.Assets = []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	}{
		{Name: "sing-box-9.9.9-linux-amd64.tar.gz", BrowserDownloadURL: "http://x/linux"},
		{Name: "sing-box-9.9.9-android-arm64.tar.gz", BrowserDownloadURL: "http://x/arm64"},
	}
	u, err := pickAsset(rel, func(n string) bool {
		return strings.Contains(n, "android-arm64") && strings.HasSuffix(n, ".tar.gz")
	})
	if err != nil || u != "http://x/arm64" {
		t.Fatalf("dapat %q err %v", u, err)
	}
	if _, err := pickAsset(rel, func(n string) bool { return n == "tidak-ada" }); err == nil {
		t.Fatal("aset tak ada harus error")
	}
}

// fakeGitHub: server GitHub tiruan (releases/latest + aset + daftar rilis).
type fakeGitHub struct {
	srv    *httptest.Server
	assets map[string][]byte // path -> isi
	tags   map[string]string // repo -> tag
	lists  map[string][]fakeRel
}

// fakeRel: satu entri daftar rilis (terbaru dulu).
type fakeRel struct {
	tag, date string
	draft     bool
	prerelease bool
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{assets: map[string][]byte{}, tags: map[string]string{}, lists: map[string][]fakeRel{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		// /repos/<owner>/<repo>/releases (daftar) atau
		// /repos/<owner>/<repo>/releases/latest atau /releases/tags/<tag>
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 4 || parts[0] != "repos" || parts[3] != "releases" {
			http.NotFound(w, r)
			return
		}
		repo := parts[1] + "/" + parts[2]
		if len(parts) == 4 {
			// Daftar: hormati per_page seperti GitHub.
			rels := f.lists[repo]
			if pp := r.URL.Query().Get("per_page"); pp != "" {
				var n int
				if _, err := fmt.Sscanf(pp, "%d", &n); err == nil && n < len(rels) {
					rels = rels[:n]
				}
			}
			var ents []string
			for _, rel := range rels {
				ents = append(ents, fmt.Sprintf(`{"tag_name":%q,"published_at":%q,"draft":%v,"prerelease":%v,"assets":[]}`,
					rel.tag, rel.date, rel.draft, rel.prerelease))
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `[%s]`, strings.Join(ents, ","))
			return
		}
		if len(parts) < 5 {
			http.NotFound(w, r)
			return
		}
		var tag string
		if len(parts) == 5 && parts[4] == "latest" {
			tag = f.tags[repo]
		} else if len(parts) == 6 && parts[4] == "tags" {
			tag = parts[5]
		} else {
			http.NotFound(w, r)
			return
		}
		var as []string
		for p := range f.assets {
			as = append(as, fmt.Sprintf(`{"name":%q,"browser_download_url":%q,"size":1}`, filepath.Base(p), f.srv.URL+p))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[%s]}`, tag, strings.Join(as, ","))
	})
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		if b, ok := f.assets[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(func() { f.srv.Close() })
	return f
}

func TestCheckUpdatesFakeGitHub(t *testing.T) {
	f := newFakeGitHub(t)
	f.tags = map[string]string{
		"SagerNet/sing-box": "v1.14.5", "MetaCubeX/mihomo": "v1.19.32",
		"Genymobile/scrcpy": "v5.0.2", "MetaCubeX/meta-rules-dat": "20261024",
	}
	old := githubAPI
	githubAPI = f.srv.URL
	t.Cleanup(func() { githubAPI = old })

	dir := t.TempDir()
	a := &App{dataDir: dir, binDir: filepath.Join(dir, "bin"),
		mihomoBin: filepath.Join(dir, "bin", "mihomo"), singboxBin: filepath.Join(dir, "bin", "sing-box")}
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0755)
	_ = os.WriteFile(a.mihomoBin, []byte("biner-palsu"), 0755)
	_ = os.WriteFile(a.singboxBin, []byte("biner-palsu"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "scrcpy-server.jar"), []byte("jar-palsu"), 0644)
	_ = os.MkdirAll(filepath.Join(dir, "geodata"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "geodata", "geoip.dat"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "geodata", "geosite.dat"), []byte("y"), 0644)
	a.verMihomo, a.verSingbox = "1.19.33", "1.14.3" // mihomo custom lebih baru dari upstream
	d := a.checkUpdates(true)
	if d.Singbox.Latest != "1.14.5" || !d.Singbox.Update {
		t.Fatalf("singbox: %+v", d.Singbox)
	}
	// TaptapBox library: mihomo tidak ada sama sekali (bukan downgrade,
	// bukan terpasang) — kotak Download tidak lagi menampilkan mihomo.
	if d.Mihomo.Installed || d.Mihomo.Current != "" {
		t.Fatalf("mihomo harus tidak ada di Slim library: %+v", d.Mihomo)
	}
	if d.Scrcpy.Current != "5.0.1" || d.Scrcpy.Latest != "5.0.2" || !d.Scrcpy.Update {
		t.Fatalf("scrcpy: %+v", d.Scrcpy)
	}
	if d.Geodata.Latest != "20261024" {
		t.Fatalf("geodata latest %q", d.Geodata.Latest)
	}
	// Cache: ubah tag upstream, cek tanpa force harus tetap pakai cache.
	f.tags["SagerNet/sing-box"] = "v9.9.9"
	d2 := a.checkUpdates(false)
	if d2.Singbox.Latest != "1.14.5" {
		t.Fatalf("cache harus dipakai, dapat %q", d2.Singbox.Latest)
	}
	d3 := a.checkUpdates(true)
	if d3.Singbox.Latest != "9.9.9" {
		t.Fatalf("force harus cek ulang, dapat %q", d3.Singbox.Latest)
	}
}

// Modul slim (v1.0.32): tanpa biner/jar/geodata terpasang, kotak Download
// harus jujur: Current kosong, Installed=false, tombol Download aktif,
// dan scrcpy dikunci ke 5.0.1 walau upstream latest lebih baru.
func TestCheckUpdatesMissingBinaries(t *testing.T) {
	f := newFakeGitHub(t)
	f.tags = map[string]string{
		"SagerNet/sing-box": "v1.14.5", "MetaCubeX/mihomo": "v1.19.33",
		"Genymobile/scrcpy": "v9.9.9", "MetaCubeX/meta-rules-dat": "20261024",
	}
	old := githubAPI
	githubAPI = f.srv.URL
	t.Cleanup(func() { githubAPI = old })

	dir := t.TempDir()
	a := &App{dataDir: dir, binDir: filepath.Join(dir, "bin"),
		mihomoBin: filepath.Join(dir, "bin", "mihomo"), singboxBin: filepath.Join(dir, "bin", "sing-box")}
	_ = os.MkdirAll(a.binDir, 0755)
	d := a.checkUpdates(true)
	if d.Mihomo.Current != "" || d.Mihomo.Installed || !d.Mihomo.Update || d.Mihomo.Latest != "1.19.33" {
		t.Fatalf("mihomo hilang: %+v", d.Mihomo)
	}
	// TaptapBox library: sing-box tetap terpasang 1.14.3 walau tanpa biner di disk.
	if d.Singbox.Current != singboxLibraryVersion || !d.Singbox.Installed || !d.Singbox.Update || d.Singbox.Latest != "1.14.5" {
		t.Fatalf("singbox library: %+v", d.Singbox)
	}
	if d.Scrcpy.Current != "" || d.Scrcpy.Installed || !d.Scrcpy.Pinned || d.Scrcpy.Latest != "5.0.1" || !d.Scrcpy.Update {
		t.Fatalf("scrcpy hilang harus terkunci 5.0.1: %+v", d.Scrcpy)
	}
	if d.Geodata.Geoip.Size != "-" || d.Geodata.Geosite.Size != "-" || d.Geodata.Latest != "20261024" {
		t.Fatalf("geodata hilang: %+v", d.Geodata)
	}
}

// Unduhan scrcpy pertama (jar belum ada) harus memasang 5.0.1 via
// endpoint tags, bukan latest.
func TestInstallScrcpyPinned(t *testing.T) {
	f := newFakeGitHub(t)
	var jarBuf bytes.Buffer
	zw := zip.NewWriter(&jarBuf)
	w, _ := zw.Create("classes.dex")
	_, _ = w.Write([]byte("xxLcom/genymobile/scrcpy/Server;yy"))
	_ = zw.Close()
	f.assets["/assets/scrcpy-server-v5.0.1"] = jarBuf.Bytes()
	old := githubAPI
	githubAPI = f.srv.URL
	t.Cleanup(func() { githubAPI = old })

	dir := t.TempDir()
	a := &App{dataDir: dir, binDir: filepath.Join(dir, "bin")}
	_ = os.MkdirAll(a.binDir, 0755)
	ver, err := a.installScrcpy(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if ver != "5.0.1" {
		t.Fatalf("versi terkunci harus 5.0.1, dapat %q", ver)
	}
	if got, _ := os.ReadFile(filepath.Join(a.binDir, "scrcpy-server.jar")); !bytes.Equal(got, jarBuf.Bytes()) {
		t.Fatal("jar terpasang harus sama dengan aset 5.0.1")
	}
	if _, err := os.Stat(filepath.Join(a.binDir, "scrcpy-server.jar.prev")); !os.IsNotExist(err) {
		t.Fatal("tanpa jar lama, .prev tidak boleh dibuat")
	}
}

// toolVersion: versi asli biner toolcache (agar tes tak bergantung angka).
func toolVersion(t *testing.T, bin string, args ...string) string {
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bin, "mihomo") {
		return parseMihomoVersion(string(out))
	}
	return parseSingboxVersion(string(out))
}

func TestDownloadSingboxEndToEnd(t *testing.T) {
	tool := os.Getenv("SINGBOX_BIN")
	if tool == "" {
		t.Skip("SINGBOX_BIN tidak diset")
	}
	ver := toolVersion(t, tool, "version")
	raw, err := os.ReadFile(tool)
	if err != nil {
		t.Fatal(err)
	}
	// Bungkus biner toolcache ke tar.gz seperti rilis sing-box asli.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "sing-box-" + ver + "-android-arm64/sing-box", Mode: 0755, Size: int64(len(raw))})
	if _, err := tw.Write(raw); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()

	f := newFakeGitHub(t)
	f.tags["SagerNet/sing-box"] = "v" + ver
	f.assets["/assets/sing-box-"+ver+"-android-arm64.tar.gz"] = buf.Bytes()
	old := githubAPI
	githubAPI = f.srv.URL
	t.Cleanup(func() { githubAPI = old })

	dir := t.TempDir()
	a := &App{dataDir: dir, binDir: filepath.Join(dir, "bin")}
	_ = os.MkdirAll(a.binDir, 0755)
	// Biner lama ada (harus ditimpa, tanpa .prev).
	_ = os.WriteFile(filepath.Join(a.binDir, "sing-box"), []byte("lama"), 0755)
	got, err := a.installCore("singbox", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != ver {
		t.Fatalf("versi terpasang %q, mau %q", got, ver)
	}
	inst, err := os.ReadFile(filepath.Join(a.binDir, "sing-box"))
	if err != nil || !bytes.Equal(inst, raw) {
		t.Fatal("biner terpasang bukan hasil unduhan")
	}
	if _, err := os.Stat(filepath.Join(a.binDir, "sing-box.prev")); !os.IsNotExist(err) {
		t.Fatal("v1.0.41: .prev tidak boleh dibuat")
	}
}

func TestDownloadMihomoEndToEnd(t *testing.T) {
	tool := os.Getenv("MIHOMO_BIN")
	if tool == "" {
		t.Skip("MIHOMO_BIN tidak diset")
	}
	ver := toolVersion(t, tool, "-v")
	raw, err := os.ReadFile(tool)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	_ = gz.Close()

	f := newFakeGitHub(t)
	f.tags["MetaCubeX/mihomo"] = "v" + ver
	f.assets["/assets/mihomo-android-arm64-v"+ver+".gz"] = buf.Bytes()
	old := githubAPI
	githubAPI = f.srv.URL
	t.Cleanup(func() { githubAPI = old })

	dir := t.TempDir()
	a := &App{dataDir: dir, binDir: filepath.Join(dir, "bin")}
	_ = os.MkdirAll(a.binDir, 0755)
	got, err := a.installCore("mihomo", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != ver {
		t.Fatalf("versi terpasang %q, mau %q", got, ver)
	}
	inst, err := os.ReadFile(filepath.Join(a.binDir, "mihomo"))
	if err != nil || !bytes.Equal(inst, raw) {
		t.Fatal("biner terpasang bukan hasil unduhan")
	}
}

func TestDownloadScrcpyAndGeodata(t *testing.T) {
	f := newFakeGitHub(t)
	f.tags["Genymobile/scrcpy"] = "v4.2"
	f.tags["MetaCubeX/meta-rules-dat"] = "20261024"
	var jarBuf bytes.Buffer
	zw := zip.NewWriter(&jarBuf)
	w, _ := zw.Create("com/genymobile/scrcpy/Server.class")
	_, _ = w.Write([]byte("class-palsu"))
	_ = zw.Close()
	f.assets["/assets/scrcpy-server.jar"] = jarBuf.Bytes()
	f.assets["/assets/geoip.dat"] = bytes.Repeat([]byte("g"), 1<<18)
	f.assets["/assets/geosite.dat"] = bytes.Repeat([]byte("s"), 1<<18)
	old := githubAPI
	githubAPI = f.srv.URL
	t.Cleanup(func() { githubAPI = old })

	dir := t.TempDir()
	a := &App{dataDir: dir, binDir: filepath.Join(dir, "bin")}
	_ = os.MkdirAll(a.binDir, 0755)
	_ = os.MkdirAll(filepath.Join(dir, "geodata"), 0755)
	_ = os.WriteFile(filepath.Join(a.binDir, "scrcpy-server.jar"), []byte("jar-lama"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "geodata", "geoip.dat"), []byte("geo-lama"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "geodata", "geosite.dat"), []byte("site-lama"), 0644)

	ver, err := a.installScrcpy(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if ver != "4.2" {
		t.Fatalf("versi scrcpy %q", ver)
	}
	if got, _ := os.ReadFile(filepath.Join(a.binDir, "scrcpy-server.version")); strings.TrimSpace(string(got)) != "4.2" {
		t.Fatalf("scrcpy-server.version harus 4.2, dapat %q", got)
	}
	if _, err := os.Stat(filepath.Join(a.binDir, "scrcpy-server.jar.prev")); !os.IsNotExist(err) {
		t.Fatal("v1.0.41: .prev tidak boleh dibuat")
	}
	// scrcpyServerVersion() harus membaca file versi baru.
	if v := a.scrcpyServerVersion(); v != "4.2" {
		t.Fatalf("scrcpyServerVersion() = %q", v)
	}

	ver, err = a.installGeodata(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if ver != "20261024" {
		t.Fatalf("versi geodata %q", ver)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "geodata", "geoip.dat")); len(b) != 1<<18 {
		t.Fatal("geoip.dat tidak terganti")
	}
	if _, err := os.Stat(filepath.Join(dir, "geodata", "geoip.dat.bak")); !os.IsNotExist(err) {
		t.Fatal("v1.0.41: .bak tidak boleh dibuat")
	}
}

// v1.0.35: salinan helper — force selalu menyalin (mode 0755), malas
// hanya bila belum ada (sentinel tidak tertimpa).
func TestEnsureHelperBinary(t *testing.T) {
	old := helperBinPath
	defer func() { helperBinPath = old }()
	helperBinPath = filepath.Join(t.TempDir(), "taptapbox-fetch")
	if err := ensureHelperBinary(true); err != nil {
		t.Fatalf("force copy: %v", err)
	}
	st, err := os.Stat(helperBinPath)
	if err != nil || st.Mode().Perm() != 0755 {
		t.Fatalf("salinan = %v mode=%v", err, st.Mode())
	}
	src, _ := os.Stat(os.Args[0])
	if st.Size() != src.Size() {
		t.Fatalf("ukuran salinan %d != biner %d", st.Size(), src.Size())
	}
	sentinel := []byte("sentinel-jangan-ditimpa")
	if err := os.WriteFile(helperBinPath, sentinel, 0755); err != nil {
		t.Fatal(err)
	}
	if err := ensureHelperBinary(false); err != nil {
		t.Fatalf("lazy: %v", err)
	}
	if b, _ := os.ReadFile(helperBinPath); string(b) != string(sentinel) {
		t.Fatalf("lazy menimpa salinan yang sudah ada")
	}
	if err := ensureHelperBinary(true); err != nil {
		t.Fatalf("force ulang: %v", err)
	}
	if b, _ := os.ReadFile(helperBinPath); string(b) == string(sentinel) {
		t.Fatalf("force tidak menyalin ulang")
	}
}

// v1.0.35: perintah helper menunjuk salinan helperBinPath (bukan
// /data/adb/...), dengan UID aplikasi, dan menolak nama salinan yang salah.
// Perintah hanya dibangun, tidak dijalankan (setuid butuh root).
func TestRealHelperCmdPath(t *testing.T) {
	old := helperBinPath
	defer func() { helperBinPath = old }()
	helperBinPath = filepath.Join(t.TempDir(), "taptapbox-fetch")
	if err := ensureHelperBinary(true); err != nil {
		t.Fatal(err)
	}
	cmd, err := realHelperCmd(context.Background(), "https://example.com/x", "8.8.8.8", 12345)
	if err != nil {
		t.Fatalf("realHelperCmd: %v", err)
	}
	if cmd.Path != helperBinPath {
		t.Fatalf("path = %q, mau %q", cmd.Path, helperBinPath)
	}
	if len(cmd.Args) != 4 || cmd.Args[1] != "fetch" {
		t.Fatalf("args = %v", cmd.Args)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil ||
		cmd.SysProcAttr.Credential.Uid != 12345 || cmd.SysProcAttr.Credential.Gid != 12345 {
		t.Fatalf("credential = %+v", cmd.SysProcAttr)
	}
	// v1.0.36: grup inet (3003) seperti proses aplikasi sungguhan.
	if len(cmd.SysProcAttr.Credential.Groups) != 1 || cmd.SysProcAttr.Credential.Groups[0] != 3003 {
		t.Fatalf("groups = %v, mau [3003]", cmd.SysProcAttr.Credential.Groups)
	}
	helperBinPath = filepath.Join(t.TempDir(), "bukan-helper")
	if _, err := realHelperCmd(context.Background(), "https://example.com/x", "", 10000); err == nil {
		t.Fatalf("nama salinan salah harus ditolak")
	}
}

// v1.0.35/36: shortFetchErr — buang prefix Get + samarkan port sumber
// (loopback maupun IPv4) agar dedup hint menggabungkan error identik.
func TestShortFetchErr(t *testing.T) {
	in := fmt.Errorf(`Get "https://api.github.com/repos/x/y/releases/latest": dial tcp: lookup api.github.com on [::1]:53: read udp [::1]:47691->[::1]:53: read: connection refused`)
	got := shortFetchErr(in)
	if strings.Contains(got, `Get "`) {
		t.Fatalf("prefix Get tidak dibuang: %q", got)
	}
	if !strings.Contains(got, "[::1]:*->[::1]:53") {
		t.Fatalf("port sumber loopback tidak disamarkan: %q", got)
	}
	in4 := fmt.Errorf(`lookup api.github.com on [::1]:53: write udp 10.10.14.1:45651->1.1.1.1:53: write: operation not permitted`)
	if got4 := shortFetchErr(in4); !strings.Contains(got4, "10.10.14.1:*->1.1.1.1:53") {
		t.Fatalf("port sumber IPv4 tidak disamarkan: %q", got4)
	}
	if shortFetchErr(fmt.Errorf("helper mati")) != "helper mati" {
		t.Fatalf("error biasa harus lolos apa adanya")
	}
	if shortFetchErr(nil) != "" {
		t.Fatalf("nil harus jadi string kosong")
	}
}

// v1.0.36: dohLookup — parse JSON DoH (type A saja, AAAA di-skip),
// error bila tak ada jawaban A / status bukan 200.
func TestDohLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "api.github.com" || r.URL.Query().Get("type") != "A" {
			t.Errorf("query DoH = %v", r.URL.RawQuery)
		}
		if r.Header.Get("Accept") != "application/dns-json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		_, _ = w.Write([]byte(`{"Answer":[{"name":"api.github.com.","type":28,"data":"2606:50c0::1"},{"name":"api.github.com.","type":1,"data":"140.82.114.6"}]}`))
	}))
	defer srv.Close()
	ep := dohEndpoint{ip: strings.TrimPrefix(srv.URL, "http://"), scheme: "http", path: "/dns-query"}
	ip, err := dohLookup(context.Background(), srv.Client(), ep, "api.github.com")
	if err != nil || ip != "140.82.114.6" {
		t.Fatalf("dohLookup = %q, %v", ip, err)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Answer":[]}`))
	}))
	defer empty.Close()
	epEmpty := dohEndpoint{ip: strings.TrimPrefix(empty.URL, "http://"), scheme: "http", path: "/dns-query"}
	if _, err := dohLookup(context.Background(), empty.Client(), epEmpty, "api.github.com"); err == nil {
		t.Fatalf("tanpa jawaban A harus error")
	}
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer fail.Close()
	epFail := dohEndpoint{ip: strings.TrimPrefix(fail.URL, "http://"), scheme: "http", path: "/dns-query"}
	if _, err := dohLookup(context.Background(), fail.Client(), epFail, "api.github.com"); err == nil {
		t.Fatalf("status 500 harus error")
	}
}

// v1.0.35: helper gagal + direct gagal -> error gabungan menyebut keduanya
// (regresi: dulu hint hanya menampilkan error direct sehingga akar masalah
// helper tak terlihat dari WebUI).
func TestFetchReleaseChainBothFail(t *testing.T) {
	oldOut := helperOutputRunner
	defer func() { helperOutputRunner = oldOut }()
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	oldAPI := githubAPI
	githubAPI = closedURL
	defer func() { githubAPI = oldAPI }()
	helperOutputRunner = func(ctx context.Context, url, dns string, uid int) ([]byte, error) {
		return nil, fmt.Errorf("helper mati")
	}
	_, err := (&App{}).fetchReleasePath("/repos/x/y/releases/latest")
	if err == nil {
		t.Fatalf("keduanya gagal harus error")
	}
	if !strings.Contains(err.Error(), "helper mati") || !strings.Contains(err.Error(), "direct: ") {
		t.Fatalf("error gabungan tidak menyebut helper+direct: %v", err)
	}
	if strings.Contains(err.Error(), `Get "`) {
		t.Fatalf("prefix Get harus dibuang: %v", err)
	}
}

// v1.0.35: unduhan helper gagal + direct gagal -> error gabungan juga.
func TestDownloadFileChainBothFail(t *testing.T) {
	oldStream := helperStreamRunner
	defer func() { helperStreamRunner = oldStream }()
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	helperStreamRunner = func(url, dns string, uid int) (*helperStream, error) {
		return nil, fmt.Errorf("helper mati")
	}
	err := (&App{}).downloadFile(closedURL+"/f.bin", filepath.Join(t.TempDir(), "f.bin"))
	if err == nil {
		t.Fatalf("keduanya gagal harus error")
	}
	if !strings.Contains(err.Error(), "helper mati") || !strings.Contains(err.Error(), "direct: ") {
		t.Fatalf("error gabungan tidak menyebut helper+direct: %v", err)
	}
}

// v1.0.38: zashboard — unduhan (data/zashboard) menang atas bawaan modul
// (webui/zashboard); tanpa unduhan, bawaan yang dipakai.
func TestZashboardDir(t *testing.T) {
	dir := t.TempDir()
	a := &App{dataDir: dir, webuiDir: filepath.Join(dir, "webui")}
	_ = os.MkdirAll(filepath.Join(dir, "webui", "zashboard"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "webui", "zashboard", "index.html"), []byte("bawaan"), 0644)
	if got := a.zashboardDir(); got != filepath.Join(dir, "webui", "zashboard") {
		t.Fatalf("tanpa unduhan harus bawaan, dapat %s", got)
	}
	_ = os.MkdirAll(filepath.Join(dir, "zashboard"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "zashboard", "index.html"), []byte("unduhan"), 0644)
	if got := a.zashboardDir(); got != filepath.Join(dir, "zashboard") {
		t.Fatalf("unduhan harus menang, dapat %s", got)
	}
	if !a.zashboardDownloaded() {
		t.Fatal("zashboardDownloaded harus true")
	}
}

// v1.0.38: installZashboard — zip datar lalu zip bersarang (satu subdir),
// tanpa .bak, zip tanpa index.html ditolak tanpa mengganti.
func TestInstallZashboard(t *testing.T) {
	mkZip := func(files map[string]string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, body := range files {
			w, _ := zw.Create(name)
			_, _ = w.Write([]byte(body))
		}
		_ = zw.Close()
		return buf.Bytes()
	}
	f := newFakeGitHub(t)
	f.tags["Zephyruso/zashboard"] = "v3.29.1"
	f.assets["/assets/dist.zip"] = mkZip(map[string]string{"index.html": "v1", "app.js": "x"})
	old := githubAPI
	githubAPI = f.srv.URL
	t.Cleanup(func() { githubAPI = old })

	dir := t.TempDir()
	a := &App{dataDir: dir, binDir: filepath.Join(dir, "bin"), webuiDir: filepath.Join(dir, "webui")}
	_ = os.MkdirAll(a.binDir, 0755)
	ver, err := a.installZashboard(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if ver != "3.29.1" {
		t.Fatalf("versi = %q, mau 3.29.1", ver)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "zashboard", "index.html")); string(got) != "v1" {
		t.Fatalf("index terpasang = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "zashboard.version")); strings.TrimSpace(string(got)) != "3.29.1" {
		t.Fatalf("zashboard.version = %q", got)
	}
	// Unduhan kedua: zip bersarang + tag lebih baru -> v2 terpasang, tanpa .bak.
	f.tags["Zephyruso/zashboard"] = "v3.30.0"
	f.assets["/assets/dist.zip"] = mkZip(map[string]string{"dist/index.html": "v2", "dist/app.js": "y"})
	ver, err = a.installZashboard(dir, "")
	if err != nil || ver != "3.30.0" {
		t.Fatalf("unduhan kedua: ver=%q err=%v", ver, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "zashboard.bak")); !os.IsNotExist(err) {
		t.Fatal("v1.0.41: zashboard.bak tidak boleh dibuat")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "zashboard", "index.html")); string(got) != "v2" {
		t.Fatalf("index baru = %q", got)
	}
	// Zip tanpa index.html: ditolak, unduhan lama tetap.
	f.assets["/assets/dist.zip"] = mkZip(map[string]string{"readme.txt": "z"})
	if _, err := a.installZashboard(dir, ""); err == nil {
		t.Fatal("zip tanpa index.html harus ditolak")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "zashboard", "index.html")); string(got) != "v2" {
		t.Fatalf("sesudah gagal, index harus tetap v2, dapat %q", got)
	}
}

// v1.0.42: pemilih versi — daftar rilis (filter draft/pra-rilis, cap),
// scrcpy terkunci bila jar belum ada, unduh per-tag lewat handler.
func TestListVersions(t *testing.T) {
	f := newFakeGitHub(t)
	var mihomo []fakeRel
	for i := 0; i < 22; i++ {
		mihomo = append(mihomo, fakeRel{tag: fmt.Sprintf("v1.19.%d", 40-i), date: "2026-10-24T12:00:00Z"})
	}
	mihomo[4].draft = true
	mihomo[9].prerelease = true
	mihomo[14].tag = ""
	f.lists["MetaCubeX/mihomo"] = mihomo
	var geo []fakeRel
	for i := 0; i < 12; i++ {
		geo = append(geo, fakeRel{tag: fmt.Sprintf("202610%02d", 24-i), date: "2026-10-24T12:00:00Z"})
	}
	f.lists["MetaCubeX/meta-rules-dat"] = geo
	old := githubAPI
	githubAPI = f.srv.URL
	t.Cleanup(func() { githubAPI = old })

	a := &App{dataDir: t.TempDir(), binDir: filepath.Join(t.TempDir(), "bin")}
	vers, err := a.listVersions("mihomo")
	if err != nil {
		t.Fatal(err)
	}
	// per_page=20 dipotong dari 22, lalu 3 entri terbuang (draft/pra-rilis/kosong).
	if len(vers) != 17 {
		t.Fatalf("mihomo: %d versi, mau 17", len(vers))
	}
	if vers[0].Version != "1.19.40" || vers[0].Tag != "v1.19.40" || vers[0].Date != "24 Okt 2026" {
		t.Fatalf("entri pertama = %+v", vers[0])
	}
	if vers[1].Version != "1.19.39" {
		t.Fatalf("urutan harus terbaru dulu, kedua = %+v", vers[1])
	}
	gvers, err := a.listVersions("geodata")
	if err != nil {
		t.Fatal(err)
	}
	if len(gvers) != 10 {
		t.Fatalf("geodata: %d versi, mau 10 (cap)", len(gvers))
	}
	if _, err := a.listVersions("tidak-ada"); err == nil {
		t.Fatal("item tak dikenal harus error")
	}
}

func TestUpdateVersionsPinnedScrcpy(t *testing.T) {
	dir := t.TempDir()
	a := &App{dataDir: dir, binDir: filepath.Join(dir, "bin")}
	_ = os.MkdirAll(a.binDir, 0755)
	req := httptest.NewRequest("GET", "/api/updates/versions?item=scrcpy", nil)
	rec := httptest.NewRecorder()
	a.handleUpdateVersions(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d, mau 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"version":"5.0.1"`) {
		t.Fatalf("scrcpy belum terpasang harus terkunci 5.0.1: %s", rec.Body.String())
	}
	req = httptest.NewRequest("GET", "/api/updates/versions?item=apaan", nil)
	rec = httptest.NewRecorder()
	a.handleUpdateVersions(rec, req)
	if rec.Code != 400 {
		t.Fatalf("item tak dikenal harus 400, dapat %d", rec.Code)
	}
}

func TestDownloadByTagViaHandler(t *testing.T) {
	mkZip := func(files map[string]string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, body := range files {
			w, _ := zw.Create(name)
			_, _ = w.Write([]byte(body))
		}
		_ = zw.Close()
		return buf.Bytes()
	}
	f := newFakeGitHub(t)
	f.assets["/assets/dist.zip"] = mkZip(map[string]string{"index.html": "v28", "app.js": "y"})
	old := githubAPI
	githubAPI = f.srv.URL
	t.Cleanup(func() { githubAPI = old })

	dir := t.TempDir()
	a := &App{dataDir: dir, binDir: filepath.Join(dir, "bin"), webuiDir: filepath.Join(dir, "webui")}
	_ = os.MkdirAll(a.binDir, 0755)
	// Tag tak valid ditolak sebelum unduhan.
	req := httptest.NewRequest("POST", "/api/updates/download", strings.NewReader(`{"item":"zashboard","tag":"../x"}`))
	rec := httptest.NewRecorder()
	a.handleUpdateDownload(rec, req)
	if rec.Code != 400 {
		t.Fatalf("tag tak valid harus 400, dapat %d", rec.Code)
	}
	// Unduh per-tag: v3.28.0 terpasang walau bukan latest (downgrade
	// eksplisit lewat pemilih versi harus lolos).
	req = httptest.NewRequest("POST", "/api/updates/download", strings.NewReader(`{"item":"zashboard","tag":"v3.28.0"}`))
	rec = httptest.NewRecorder()
	a.handleUpdateDownload(rec, req)
	if rec.Code != 200 {
		t.Fatalf("unduh per-tag: status %d body %s", rec.Code, rec.Body.String())
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "zashboard", "index.html")); string(got) != "v28" {
		t.Fatalf("index terpasang = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "zashboard.version")); strings.TrimSpace(string(got)) != "3.28.0" {
		t.Fatalf("zashboard.version = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "zashboard.bak")); !os.IsNotExist(err) {
		t.Fatal("v1.0.41: zashboard.bak tidak boleh dibuat")
	}
}

// v1.0.38: checkUpdates zashboard — bawaan modul (tanpa catatan versi)
// selalu bisa diunduh bila rilis diketahui; unduhan sama dengan latest
// -> Sudah terbaru; unduhan lebih lama -> bisa diunduh.
func TestCheckUpdatesZashboard(t *testing.T) {
	f := newFakeGitHub(t)
	f.tags["Zephyruso/zashboard"] = "v3.29.1"
	old := githubAPI
	githubAPI = f.srv.URL
	t.Cleanup(func() { githubAPI = old })

	dir := t.TempDir()
	a := &App{dataDir: dir, binDir: filepath.Join(dir, "bin"), webuiDir: filepath.Join(dir, "webui")}
	_ = os.MkdirAll(filepath.Join(dir, "webui", "zashboard"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "webui", "zashboard", "index.html"), []byte("bawaan"), 0644)
	d := a.checkUpdates(true)
	if d.Zashboard.Current != "" || !d.Zashboard.Installed || !d.Zashboard.Update || d.Zashboard.Latest != "3.29.1" {
		t.Fatalf("bawaan modul: %+v", d.Zashboard)
	}
	// Unduhan sama dengan latest -> bukan update.
	_ = os.MkdirAll(filepath.Join(dir, "zashboard"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "zashboard", "index.html"), []byte("v"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "zashboard.version"), []byte("3.29.1\n"), 0644)
	d = a.checkUpdates(true)
	if d.Zashboard.Current != "3.29.1" || d.Zashboard.Update {
		t.Fatalf("sama dengan latest harus bukan update: %+v", d.Zashboard)
	}
	// Unduhan lebih lama -> update.
	_ = os.WriteFile(filepath.Join(dir, "zashboard.version"), []byte("3.28.0\n"), 0644)
	d = a.checkUpdates(true)
	if d.Zashboard.Current != "3.28.0" || !d.Zashboard.Update {
		t.Fatalf("lebih lama harus update: %+v", d.Zashboard)
	}
}

func TestHandleSelfReleaseFakeGitHub(t *testing.T) {
	f := newFakeGitHub(t)
	f.tags["Taptap56/Taptapbox"] = "v1.0.0"
	f.assets["/assets/taptapbox-v1.0.0.zip"] = []byte("zip-palsu")
	old := githubAPI
	githubAPI = f.srv.URL
	t.Cleanup(func() { githubAPI = old })

	a := &App{dataDir: t.TempDir()}
	req := httptest.NewRequest("GET", "/api/release/latest", nil)
	rec := httptest.NewRecorder()
	a.handleSelfRelease(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("status %d, body %s", rec.Code, body)
	}
	if !strings.Contains(body, `"tag":"v1.0.0"`) || !strings.Contains(body, "taptapbox-v1.0.0.zip") {
		t.Fatalf("rilis sendiri harus memuat tag + aset zip: %s", body)
	}
	if strings.Contains(body, `"error"`) {
		t.Fatalf("tidak boleh ada error: %s", body)
	}
}


func TestHandleSelfReleaseGoAsset(t *testing.T) {
	// Repo Taptapbox menerbitkan aset taptapbox-go-<tag>.zip (bukan
	// taptapbox-<tag>.zip): kartu Rilis harus menawarkan aset itu.
	f := newFakeGitHub(t)
	f.tags["Taptap56/Taptapbox"] = "v1.0.0"
	f.assets["/assets/taptapbox-go-v1.0.0.zip"] = []byte("zip-palsu")
	old := githubAPI
	githubAPI = f.srv.URL
	t.Cleanup(func() { githubAPI = old })

	a := &App{dataDir: t.TempDir()}
	req := httptest.NewRequest("GET", "/api/release/latest", nil)
	rec := httptest.NewRecorder()
	a.handleSelfRelease(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("status %d, body %s", rec.Code, body)
	}
	if !strings.Contains(body, `"tag":"v1.0.0"`) || !strings.Contains(body, "taptapbox-go-v1.0.0.zip") {
		t.Fatalf("rilis Taptapbox harus memuat tag + aset go zip: %s", body)
	}
}

func TestHandleSelfReleaseCacheRefresh(t *testing.T) {
	f := newFakeGitHub(t)
	f.tags["Taptap56/Taptapbox"] = "v1.0.0"
	f.assets["/assets/taptapbox-v1.0.0.zip"] = []byte("zip-palsu")
	old := githubAPI
	githubAPI = f.srv.URL
	t.Cleanup(func() { githubAPI = old })
	a := &App{dataDir: t.TempDir()}
	call := func(url string) string {
		req := httptest.NewRequest("GET", url, nil)
		rec := httptest.NewRecorder()
		a.handleSelfRelease(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
		return rec.Body.String()
	}
	if got := call("/api/release/latest"); !strings.Contains(got, `"tag":"v1.0.0"`) {
		t.Fatalf("panggilan pertama harus v1.0.0: %s", got)
	}
	// Tag berubah di server, tapi cache 1 jam masih mengembalikan yang lama.
	f.tags["Taptap56/Taptapbox"] = "v1.0.1"
	if got := call("/api/release/latest"); !strings.Contains(got, `"tag":"v1.0.0"`) {
		t.Fatalf("panggilan kedua harus dari cache v1.0.0, dapat %s", got)
	}
	// Periksa lagi memaksa cek segar, seperti Download.
	if got := call("/api/release/latest?refresh=1"); !strings.Contains(got, `"tag":"v1.0.1"`) {
		t.Fatalf("refresh harus v1.0.1, dapat %s", got)
	}
}
