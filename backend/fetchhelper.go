package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// fetchhelper.go — updater lewat "internet manapun" (v1.0.33).
//
// Masalah yang diperbaiki (laporan 2026-10-25, sesudah modul slim):
// daemon jalan sebagai root (uid 0). Dua hal membuat egress root gagal
// walau user sudah menyalakan v2rayng / VPN lain yang jelas-jelas memberi
// internet ke aplikasi:
//   1. Routing VPN Android berlaku untuk UID aplikasi (10000+); trafik
//      root tidak ikut terowongan VPN, melainkan jalur langsung — di paket
//      XL jalur langsung non-Google diblokir carrier.
//   2. Resolver Go (CGO_ENABLED=0) membaca DNS dari sistem dan berakhir
//      menanya [::1]:53, yang tidak ada yang mendengarkan di HP ini
//      ("read: connection refused") — padahal aplikasi (lewat netd)
//      resolve dengan normal.
//
// Perbaikan: fetch updater dijalankan anak proses dengan UID aplikasi
// (sehingga routing-nya sama seperti aplikasi: ikut VPN bila ada, direct
// bila tidak) DAN dengan DNS eksplisit (bukan resolver sistem yang rusak).
// Rantai percobaan: proksi mixed core (bila core jalan) -> helper -> direct.
// Helper hanya menulis body ke stdout; daemon yang menulis ke file, jadi
// helper tidak butuh akses tulis ke data dir.
//
// Catatan jujur: jalur langsung XL tanpa VPN/core TETAP tidak bisa (carrier
// yang memblokir, bukan modul) — helper hanya membuat updater memakai
// internet yang memang sudah jalan (VPN apk, WiFi, core TaptapBox).
//
// v1.0.35: helper TIDAK lagi dieksekusi dari /data/adb/... — folder itu
// hanya bisa dilewati root, sehingga anak proses UID aplikasi selalu
// gagal fork/exec "permission denied" (terbukti di backend.log user
// 2026-10-25, berulang kali; tes VM v1.0.33 tidak menangkapnya karena
// mengetes dari /tmp yang terbuka). Daemon kini menyalin dirinya ke
// /data/local/tmp/taptapbox-fetch (bisa dilewati UID non-root; folder ini
// sudah dipakai modul untuk scrcpy-server.jar) dan helper dijalankan
// dari salinan itu. Salinan tidak membaca config/rahasia apa pun:
// subcommand fetch hanya GET satu URL dan mencetak body ke stdout.

// packagesListPath bisa diganti tes.
var packagesListPath = "/data/system/packages.list"

// preferredHelperPackages: paket yang hampir pasti punya akses jaringan
// (aplikasi yang dipakai user sehari-hari) — UID-nya dicoba duluan.
// v1.0.37: UID aplikasi pertama di packages.list ternyata belum tentu
// punya jaringan yang sama seperti aplikasi aktif (bisa aplikasi sistem
// yang dibatasi / dikecualikan dari VPN) — buktinya TCP helper timeout
// walau internet user jalan. Jadi helper mencoba beberapa UID.
var preferredHelperPackages = []string{
	"com.android.chrome", "com.chrome.beta",
	"org.mozilla.firefox", "org.mozilla.firefox_beta",
	"org.telegram.messenger", "com.whatsapp",
}

// helperMaxCandidates: batas UID yang dicoba (agar tidak spawn puluhan proses).
const helperMaxCandidates = 5

type helperCandidate struct {
	pkg string
	uid int
}

// parseHelperUIDsFile: semua (paket, uid) aplikasi dari packages.list
// (10000<=uid<20000); nil bila tak ada / tak bisa dibaca.
func parseHelperUIDsFile(path string) []helperCandidate {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []helperCandidate
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		uid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		if uid >= 10000 && uid < 20000 {
			out = append(out, helperCandidate{pkg: f[0], uid: uid})
		}
	}
	return out
}

// helperCandidateUIDs: UID kandidat terurut — paket preferensi dulu, lalu
// urutan file, dedup, dibatasi helperMaxCandidates; fallback [10000] bila
// packages.list tak memberi apa-apa.
func helperCandidateUIDs() []int {
	cands := parseHelperUIDsFile(packagesListPath)
	var out []int
	seen := map[int]bool{}
	add := func(uid int) {
		if uid > 0 && !seen[uid] && len(out) < helperMaxCandidates {
			seen[uid] = true
			out = append(out, uid)
		}
	}
	for _, pref := range preferredHelperPackages {
		for _, c := range cands {
			if c.pkg == pref {
				add(c.uid)
			}
		}
	}
	for _, c := range cands {
		add(c.uid)
	}
	if len(out) == 0 {
		out = []int{10000}
	}
	return out
}

// buildDNSList: daftar DNS dari properti jaringan + publik, dibersihkan:
// kosong dan loopback (sumber ::1 yang rusak) dibuang, duplikat dihapus,
// DNS publik selalu ditambahkan sebagai cadangan.
func buildDNSList(prop1, prop2 string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || s == "127.0.0.1" || s == "::1" || s == "localhost" {
			return
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(prop1)
	add(prop2)
	add("8.8.8.8")
	add("1.1.1.1")
	return out
}

// fetchDNSList: DNS aktif jaringan ini (getprop net.dns1/dns2; gagal ->
// hanya publik). getprop tidak ada di lingkungan tes -> fallback publik.
func fetchDNSList() []string {
	prop := func(name string) string {
		out, err := exec.Command("getprop", name).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return buildDNSList(prop("net.dns1"), prop("net.dns2"))
}

// resolveViaServers: resolve hostname lewat daftar DNS eksplisit (bukan
// resolver sistem). IP langsung dikembalikan tanpa query; IPv4 diutamakan
// (IPv6 di HP ini diblokir).
func resolveViaServers(ctx context.Context, host string, servers []string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return host, nil
	}
	if host == "localhost" {
		return "127.0.0.1", nil
	}
	var lastErr error
	for _, server := range servers {
		srv := server
		r := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: 3 * time.Second}
				// Entri DNS umumnya IP polos (-> port 53); entri
				// ber-port (mis. untuk tes) dipakai apa adanya.
				target := srv
				if _, _, err := net.SplitHostPort(srv); err != nil {
					target = net.JoinHostPort(srv, "53")
				}
				return d.DialContext(ctx, network, target)
			},
		}
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ips, err := r.LookupIP(cctx, "ip", host)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		for _, ip := range ips {
			if ip.To4() != nil {
				return ip.String(), nil
			}
		}
		if len(ips) > 0 {
			return ips[0].String(), nil
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("tidak ada server DNS")
}

// dohEndpoint: penyedia DNS-over-HTTPS (JSON API). ip dipakai langsung
// (tanpa resolve dulu — menghindari ayam-telur), hostname untuk SNI + Host.
type dohEndpoint struct {
	ip       string // tanpa port (https -> 443); tes boleh berisi port
	hostname string // kosong = Host default dari URL (untuk tes HTTP)
	path     string
	scheme   string // "" = https
}

// dohEndpoints: urutan cadangan (Google lalu Cloudflare).
var dohEndpoints = []dohEndpoint{
	{ip: "8.8.8.8", hostname: "dns.google", path: "/resolve"},
	{ip: "1.1.1.1", hostname: "cloudflare-dns.com", path: "/dns-query"},
}

type dohResp struct {
	Answer []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

// dohLookup: satu query DoH JSON (type A) lewat client yang diberikan.
// Client produksi = HTTPS dengan dial ke IP + SNI hostname; tes memakai
// client HTTP biasa ke server lokal.
func dohLookup(ctx context.Context, client *http.Client, ep dohEndpoint, host string) (string, error) {
	scheme := ep.scheme
	if scheme == "" {
		scheme = "https"
	}
	u := scheme + "://" + ep.ip + ep.path + "?name=" + url.QueryEscape(host) + "&type=A"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/dns-json")
	if ep.hostname != "" {
		req.Host = ep.hostname
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("DoH %s: status %d", ep.hostname, resp.StatusCode)
	}
	var dr dohResp
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return "", err
	}
	for _, a := range dr.Answer {
		if a.Type != 1 {
			continue
		}
		if ip := net.ParseIP(a.Data); ip != nil && ip.To4() != nil {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("DoH %s: tidak ada jawaban A untuk %s", ep.hostname, host)
}

// resolveViaDoH: resolve hostname lewat DoH (TCP+TLS, tanpa UDP sama
// sekali). v1.0.36: cadangan saat UDP helper ditolak kernel ("operation
// not permitted") — TCP lewat terowongan VPN adalah jalur normal aplikasi.
// IP langsung / localhost dikembalikan tanpa query (sama seperti UDP).
func resolveViaDoH(ctx context.Context, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return host, nil
	}
	if host == "localhost" {
		return "127.0.0.1", nil
	}
	var lastErr error
	for _, ep := range dohEndpoints {
		ep := ep
		tr := &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				d := net.Dialer{Timeout: 8 * time.Second}
				return d.DialContext(ctx, network, net.JoinHostPort(ep.ip, "443"))
			},
			TLSClientConfig:     &tls.Config{ServerName: ep.hostname},
			TLSHandshakeTimeout: 8 * time.Second,
		}
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		client := &http.Client{Transport: tr}
		ip, err := dohLookup(cctx, client, ep, host)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		return ip, nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("tidak ada endpoint DoH")
}

// fetchViaTransport: satu GET lewat transport dengan DNS eksplisit dan
// tanpa proksi (routing diserahkan ke kernel: VPN bila UID ikut VPN,
// direct bila tidak). Dipakai subcommand helper; bisa dites langsung
// tanpa subproses.
func fetchViaTransport(url string, dnsServers []string) (io.ReadCloser, error) {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ip, err := resolveViaServers(ctx, host, dnsServers)
			if err != nil {
				// v1.0.36: UDP gagal (mis. ditolak kernel) -> DoH via TCP.
				// v1.0.37: gabungkan kedua error agar log/hint tidak buta
				// terhadap kegagalan UDP saat DoH juga gagal.
				udpErr := err
				ip, err = resolveViaDoH(ctx, host)
				if err != nil {
					return nil, fmt.Errorf("UDP: %s; DoH: %s", shortFetchErr(udpErr), shortFetchErr(err))
				}
			}
			d := net.Dialer{Timeout: 15 * time.Second}
			return d.DialContext(ctx, network, net.JoinHostPort(ip, port))
		},
		TLSHandshakeTimeout: 15 * time.Second,
	}
	client := &http.Client{Transport: tr}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "TaptapBox")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// runFetchHelper: subcommand "fetch" (taptapbox fetch <url> [dnsCSV]).
// Body ke stdout, error ke stderr, exit 0/1. Harus dipanggil paling awal
// di main(), sebelum daemon apa pun dimulai.
func runFetchHelper(args []string) int {
	if len(args) < 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "fetch: url kosong")
		return 1
	}
	var dns []string
	if len(args) >= 2 && args[1] != "" {
		dns = strings.Split(args[1], ",")
	} else {
		dns = []string{"8.8.8.8", "1.1.1.1"}
	}
	rc, err := fetchViaTransport(args[0], dns)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	defer rc.Close()
	if _, err := io.Copy(os.Stdout, rc); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	return 0
}

// helperStream: body dari anak proses helper (stdout pipe). Close()
// menunggu proses selesai dan melaporkan exit error (pesan stderr).
type helperStream struct {
	rc     io.ReadCloser
	cmd    *exec.Cmd
	errBuf *bytes.Buffer
}

func (s *helperStream) Read(p []byte) (int, error) { return s.rc.Read(p) }

func (s *helperStream) Close() error {
	err := s.cmd.Wait()
	if err != nil {
		if msg := strings.TrimSpace(s.errBuf.String()); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

func (s *helperStream) Kill() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

// helperBinPath: salinan daemon untuk anak proses helper (bisa diganti tes).
// Harus bernama taptapbox-fetch (dijaga realHelperCmd) agar biner tes yang
// salah panggil gagal cepat, bukan menjalankan suite tes sebagai "body".
// helperInetGroup: grup AID_INET Android (3003) — grup "internet" yang
// dimiliki proses aplikasi sungguhan. v1.0.36: anak proses helper semula
// hanya diberi Uid+Gid (tanpa grup apa pun) dan UDP-nya ditolak kernel
// ("write: operation not permitted") walau routing VPN-nya sudah benar;
// grup ini meniru proses aplikasi normal (tidak memberi hak root).
var helperInetGroup = uint32(3003)

var helperBinPath = "/data/local/tmp/taptapbox-fetch"

// ensureHelperBinary: salin biner daemon sendiri ke helperBinPath.
// force=true (saat daemon start): selalu salin ulang, agar salinan selalu
// cocok dengan daemon yang jalan. force=false (sebelum spawn): salin hanya
// bila belum ada (mis. /data/local/tmp dibersihkan di tengah sesi).
// Gagal menyalin bukan fatal: pemanggil tetap mencoba spawn dan rantai
// updater jatuh ke direct bila spawn gagal.
func ensureHelperBinary(force bool) error {
	if !force {
		if _, err := os.Stat(helperBinPath); err == nil {
			return nil
		}
	}
	src, err := os.Executable()
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(helperBinPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(helperBinPath, 0755)
}

// Seam untuk tes (seperti githubAPI): default = subproses sungguhan.
var helperOutputRunner = func(ctx context.Context, url, dnsCSV string, uid int) ([]byte, error) {
	return realHelperOutput(ctx, url, dnsCSV, uid)
}

var helperStreamRunner = func(url, dnsCSV string, uid int) (*helperStream, error) {
	return realHelperStream(url, dnsCSV, uid)
}

// realHelperCmd: perintah helper dari salinan helperBinPath (bukan dari
// /data/adb/... yang tak bisa dilewati UID aplikasi — regresi v1.0.33).
// Menolak berjalan bila salinan tidak bernama taptapbox-fetch, dan
// menyiapkan salinan dulu bila belum ada.
func realHelperCmd(ctx context.Context, url, dnsCSV string, uid int) (*exec.Cmd, error) {
	if filepath.Base(helperBinPath) != "taptapbox-fetch" {
		return nil, fmt.Errorf("helper tidak tersedia (salinan helper tidak valid)")
	}
	if err := ensureHelperBinary(false); err != nil {
		return nil, fmt.Errorf("salinan helper tidak bisa disiapkan: %v", err)
	}
	var cmd *exec.Cmd
	if ctx != nil {
		cmd = exec.CommandContext(ctx, helperBinPath, "fetch", url, dnsCSV)
	} else {
		cmd = exec.Command(helperBinPath, "fetch", url, dnsCSV)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(uid), Groups: []uint32{helperInetGroup}}}
	return cmd, nil
}

// shortFetchErr: error fetch yang pantas tampil di WebUI — buang prefix
// `Get "URL": ` (url.Error) dan samarkan port sumber (angka acak per
// percobaan yang membuat dedup hint gagal menggabungkan error identik:
// "write udp 10.10.14.1:45651->1.1.1.1:53" dan "read udp [::1]:47691->
// [::1]:53" menjadi "...:*->..."). Port tujuan tidak disentuh (tidak
// diikuti "->"). Log backend tetap memakai error lengkap (%v), bukan ini.
var fetchErrSrcPort = regexp.MustCompile(`:\d+->`)

func shortFetchErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.Index(s, `": `); strings.HasPrefix(s, `Get "`) && i > 0 {
		s = s[i+3:]
	}
	return fetchErrSrcPort.ReplaceAllString(s, ":*->")
}

func realHelperOutput(ctx context.Context, url, dnsCSV string, uid int) ([]byte, error) {
	// Konteks pemanggil dihormati (dibatalkan saat satu kandidat menang
	// di percobaan paralel); bila tanpa deadline, dibatasi 45 detik agar
	// rantai UDP (3 dtk/server) + DoH (8 dtk/endpoint) sempat selesai —
	// kasus umum (UDP gagal cepat + DoH lolos) tetap hitungan detik.
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
	}
	cmd, err := realHelperCmd(ctx, url, dnsCSV, uid)
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return out, nil
}

func realHelperStream(url, dnsCSV string, uid int) (*helperStream, error) {
	cmd, err := realHelperCmd(nil, url, dnsCSV, uid)
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &helperStream{rc: stdout, cmd: cmd, errBuf: &stderr}, nil
}

// cachedHelperUID / setCachedHelperUID: UID helper yang pernah berhasil
// (diingat agar unduhan tidak mengulang pencarian; 0 = belum ada).
func (a *App) cachedHelperUID() int {
	a.helperUIDMu.Lock()
	defer a.helperUIDMu.Unlock()
	return a.helperUID
}

func (a *App) setCachedHelperUID(uid int) {
	a.helperUIDMu.Lock()
	defer a.helperUIDMu.Unlock()
	a.helperUID = uid
}

// fetchHelperOutput: cek lewat helper, mencoba beberapa UID kandidat.
// UID yang pernah berhasil dicoba dulu sendirian; bila gagal, semua
// kandidat dicoba PARALEL (pertama berhasil menang, sisanya dibatalkan)
// karena satu percobaan yang menggantung tidak boleh memblokir yang lain.
// Gagal total -> error per-UID (tanpa prefix "helper:" — rantai di
// updates.go yang menambahkannya).
func (a *App) fetchHelperOutput(url string) ([]byte, error) {
	dns := strings.Join(fetchDNSList(), ",")
	cached := a.cachedHelperUID()
	var cachedErr error
	if cached > 0 {
		if body, err := helperOutputRunner(context.Background(), url, dns, cached); err == nil {
			return body, nil
		} else {
			cachedErr = err
		}
	}
	var uids []int
	for _, uid := range helperCandidateUIDs() {
		if uid != cached {
			uids = append(uids, uid)
		}
	}
	if len(uids) == 0 {
		return nil, cachedErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	type okRes struct {
		body []byte
		uid  int
	}
	okCh := make(chan okRes, 1)
	errCh := make(chan string, len(uids))
	var wg sync.WaitGroup
	for _, uid := range uids {
		wg.Add(1)
		go func(uid int) {
			defer wg.Done()
			body, err := helperOutputRunner(ctx, url, dns, uid)
			if err == nil {
				select {
				case okCh <- okRes{body, uid}:
				default:
				}
				cancel()
				return
			}
			errCh <- fmt.Sprintf("uid %d: %s", uid, shortFetchErr(err))
		}(uid)
	}
	wg.Wait()
	close(okCh)
	close(errCh)
	if r, ok := <-okCh; ok {
		a.setCachedHelperUID(r.uid)
		return r.body, nil
	}
	var errs []string
	if cachedErr != nil {
		errs = append(errs, fmt.Sprintf("uid %d: %s", cached, shortFetchErr(cachedErr)))
	}
	for e := range errCh {
		errs = append(errs, e)
	}
	return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
}

// fetchHelperStream: unduhan lewat helper, mencoba UID kandidat BERURUTAN
// (paralel boros untuk file besar) — UID yang pernah berhasil dulu, lalu
// sisanya. Gagal total -> error per-UID (prefix "helper:" dari rantai).
func (a *App) fetchHelperStream(url string) (*helperStream, error) {
	dns := strings.Join(fetchDNSList(), ",")
	cached := a.cachedHelperUID()
	var order []int
	if cached > 0 {
		order = append(order, cached)
	}
	for _, uid := range helperCandidateUIDs() {
		if uid != cached {
			order = append(order, uid)
		}
	}
	var errs []string
	for _, uid := range order {
		stream, err := helperStreamRunner(url, dns, uid)
		if err == nil {
			a.setCachedHelperUID(uid)
			return stream, nil
		}
		errs = append(errs, fmt.Sprintf("uid %d: %s", uid, shortFetchErr(err)))
	}
	return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
}
