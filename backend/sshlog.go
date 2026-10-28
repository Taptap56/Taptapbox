package main

// ssh.log + reconnect otomatis untuk SSH — BUKAN tombol reconnect.
//
// Alasan fitur ini ada: SSH di TaptapBox beda dari vmess/trojan/vless.
// Protokol lain langsung ditangani core, sedangkan SSH lewat jembatan
// lokal daemon (dial ke proxy jarak jauh, kirim payload, baca respons,
// lalu frame WS / pipa mentah). Kalau jembatan gagal, core cuma
// menampilkan Timeout tanpa alasan, dan log backend yang campur
// (200 baris, freeze per sesi) susah dibaca.
//
// Yang dibangun di sini:
//   1. ssh.log: satu baris per percobaan jembatan, bahasa Indonesia
//      polos, selalu menyebut nama akun — jadi semua akun SSH bisa
//      dibaca, bukan cuma akun terpilih. File: log/ssh.log (pola
//      capLogWriter yang sama dengan log lain, segar tiap start
//      daemon, mentok 200 baris). Ring in-memory terakhir 100 entri
//      juga disimpan agar WebUI bisa menampilkan ringkasan per akun
//      tanpa membaca file.
//   2. Reconnect otomatis: bila dial jembatan gagal — penolakan
//      cepat (mis. proxy membalas 301/403/400) MAUPUN timeout
//      (sejak 2026-10-27, lihat sshRetryable) — jembatan mencoba
//      lagi sendiri sampai 3 kali dengan jeda 1 detik lalu 2 detik,
//      tanpa perlu tekan apa pun dan tanpa restart core. Koneksi
//      core berikutnya juga selalu dial ulang dari nol, jadi putus
//      di tengah jalan pun pulih sendiri saat trafik berikutnya
//      datang — persis perilaku HTTP Custom yang reconnect sendiri.
//      TIDAK ada tombol reconnect: tidak ada aksi manual yang
//      perlu ditekan user.
//   3. Bila dial tetap gagal sesudah 3 percobaan, koneksi core itu
//      ditutup dan kegagalannya tercatat di ssh.log dengan
//      alasannya, supaya user tidak perlu menebak.

import (
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// sshLogEntry: satu percobaan jembatan, dalam bahasa yang mudah dibaca.
type sshLogEntry struct {
	Time      string   `json:"time"` // HH:MM:SS
	ServerID  string   `json:"server_id"`
	Name      string   `json:"name"`
	Proxy     string   `json:"proxy"`
	Mode      string   `json:"mode"`      // WebSocket | Pipa mentah
	Responses []string `json:"responses"` // status HTTP per request, mis. ["HTTP/1.1 301 ...", "HTTP/1.1 101 ..."]
	OK        bool     `json:"ok"`
	Attempts  int      `json:"attempts"` // percobaan dial yang dipakai (1..3)
	Error     string   `json:"error,omitempty"`
	Detail    string   `json:"detail"` // kalimat polos, mis. "Terhubung — respons: 301 lalu 101"
}

// sshDialTimeout: batas dial TCP ke proxy (dulu 15 detik) dan
// sshPayloadDeadline: batas baca respons payload (dulu 15 detik).
// Dipersingkat 2026-10-27 agar satu percobaan gagal tidak memakan
// waktu core sing-box (connect_timeout 20 detik): proxy sehat
// menjawab dalam <2 detik, jadi 5 detik sudah longgar 2,5x lipat,
// dan percobaan yang gagal di 5 detik masih menyisakan waktu untuk
// dicoba lagi dalam anggaran 20 detik itu. Variabel (bukan
// konstanta) agar tes bisa mempercepatnya tanpa menunggu detik.
var sshDialTimeout = 5 * time.Second
var sshPayloadDeadline = 5 * time.Second

// sshRetryBackoff: jeda antar percobaan otomatis. Variabel (bukan
// konstanta) agar tes bisa mempercepatnya tanpa menunggu detik.
var sshRetryBackoff = []time.Duration{time.Second, 2 * time.Second}

// sshRetryable: kegagalan yang layak dicoba lagi. Sejak 2026-10-27
// timeout JUGA diulang, karena pola lapangan (log ssh.log
// 2026-10-27 21:10-21:13) menunjukkan timeout ke 127.x itu
// intermiten: 12 kali timeout tiap ~15 detik, lalu satu dial lolos
// dan sesi jalan 2m20s. Dulu timeout tidak diulang dengan alasan
// percobaan 15 detik sudah memakan waktu core; sekarang percobaan
// dipersingkat (dial 5 detik, payload 5 detik), jadi timeout yang
// terjadi dalam <12 detik masih layak dicoba lagi — total 3
// percobaan terburuk (5+1+5+2+5 detik) masih di bawah anggaran
// connect_timeout sing-box 20 detik. Percobaan yang sendiri sudah
// >=12 detik tidak diulang, karena core sudah hampir kehabisan
// waktu dan mengulang hanya memperpanjang Timeout.
func sshRetryable(err error, elapsed time.Duration) bool {
	if err == nil || elapsed >= 12*time.Second {
		return false
	}
	return true
}

// dialSSHWithRetry: dial jembatan dengan reconnect otomatis.
// dialFn adalah sshBridgeDial di produksi; di tes bisa diganti
// fungsi tiruan yang gagal dulu lalu berhasil. Mengembalikan
// koneksi, daftar respons percobaan TERAKHIR, dan jumlah percobaan.
func dialSSHWithRetry(prof sshBridgeProfile, dialFn func(sshBridgeProfile) (net.Conn, []string, error)) (net.Conn, []string, int, error) {
	var lastErr error
	var lastResp []string
	for attempt := 1; attempt <= 3; attempt++ {
		start := time.Now()
		conn, resp, err := dialFn(prof)
		lastResp = resp
		if err == nil {
			return conn, resp, attempt, nil
		}
		lastErr = err
		if attempt == 3 || !sshRetryable(err, time.Since(start)) {
			return nil, resp, attempt, err
		}
		time.Sleep(sshRetryBackoff[attempt-1])
	}
	return nil, lastResp, 3, lastErr
}

// sshModeName: nama mode yang mudah dibaca di log/WebUI.
func sshModeName(prof sshBridgeProfile) string {
	if prof.Raw {
		return "Pipa mentah"
	}
	return "WebSocket"
}

// sshDetail: kalimat polos untuk satu entri log.
func sshDetail(e sshLogEntry) string {
	codes := []string{}
	for _, r := range e.Responses {
		codes = append(codes, httpStatusCode(r))
	}
	resp := ""
	if len(codes) > 0 {
		resp = " — respons: " + strings.Join(codes, " lalu ")
	}
	if e.OK {
		if e.Attempts > 1 {
			return fmt.Sprintf("Terhubung sesudah %d percobaan%s", e.Attempts, resp)
		}
		return "Terhubung" + resp
	}
	if e.Error != "" {
		return "Gagal" + resp + " — " + e.Error
	}
	return "Gagal" + resp
}

// recordSSHLog: simpan entri ke ring in-memory + tulis baris polos
// ke log/ssh.log. Aman dipanggil dari goroutine jembatan mana pun.
func (a *App) recordSSHLog(e sshLogEntry) {
	e.Detail = sshDetail(e)
	a.sshLogMu.Lock()
	a.sshLogEntries = append(a.sshLogEntries, e)
	if len(a.sshLogEntries) > 100 {
		a.sshLogEntries = a.sshLogEntries[len(a.sshLogEntries)-100:]
	}
	if a.logDir != "" && a.sshLogW == nil {
		a.sshLogW = newCapLogWriter(filepath.Join(a.logDir, "ssh.log"))
	}
	w := a.sshLogW
	a.sshLogMu.Unlock()
	if w != nil {
		status := "TERHUBUNG"
		if !e.OK {
			status = "GAGAL"
		}
		line := fmt.Sprintf("[%s] %s: %s via %s, mode %s — %s\n", e.Time, e.Name, status, e.Proxy, e.Mode, e.Detail)
		_, _ = w.Write([]byte(line))
	}
}

// recordSSHRelayLine: satu baris PUTUS di log/ssh.log, tanpa mengubah
// ringkasan akun (yang tetap menunjuk entri TERHUBUNG/GAGAL terakhir).
// Ini menjawab pertanyaan yang tidak dijawab log lama: sesudah
// TERHUBUNG, apakah server SSH benar-benar membalas? Bila byte dari
// server = 0, server tidak pernah mengirim banner SSH — framing
// WS/pipa tidak cocok, atau ujung proxy bukan server SSH. Bila >0,
// handshake SSH sempat jalan, gagalnya di tahap sesudahnya (mis. auth).
func (a *App) recordSSHRelayLine(name, proxy, mode string, dur time.Duration, fromCore, fromServer int64) {
	a.sshLogMu.Lock()
	if a.logDir != "" && a.sshLogW == nil {
		a.sshLogW = newCapLogWriter(filepath.Join(a.logDir, "ssh.log"))
	}
	w := a.sshLogW
	a.sshLogMu.Unlock()
	if w == nil {
		return
	}
	note := ""
	if fromServer == 0 {
		note = " — server tidak membalas sama sekali"
	}
	line := fmt.Sprintf("[%s] %s: PUTUS sesudah %s via %s, mode %s — %d byte dari core, %d byte dari server%s\n",
		time.Now().Format("15:04:05"), name, dur.Round(time.Second), proxy, mode, fromCore, fromServer, note)
	_, _ = w.Write([]byte(line))
}

// handleSSHLog: GET /api/ssh-log — ringkasan semua akun SSH +
// entri log terbaru (terbaru dulu). Akun SSH polos (tanpa payload)
// tetap muncul dengan keterangan tanpa jembatan, supaya daftarnya
// lengkap dan tidak ada akun SSH yang hilang dari pandangan.
func (a *App) handleSSHLog(w http.ResponseWriter, r *http.Request) {
	a.sshLogMu.Lock()
	entries := append([]sshLogEntry(nil), a.sshLogEntries...)
	a.sshLogMu.Unlock()
	// Terbaru dulu, sesuai cara baca log.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	writeJSON(w, map[string]any{
		"accounts":       a.sshAccountsSummary(),
		"entries":        entries,
		"auto_reconnect": true, // reconnect otomatis aktif, bukan tombol
	})
}

// sshAccountsSummary dipakai handleSSHLog; dipisah agar bisa dites
// tanpa HTTP: semua server SSH + entri terakhirnya + port jembatan.
func (a *App) sshAccountsSummary() []map[string]any {
	a.store.mu.RLock()
	servers := append([]Server(nil), a.store.Servers...)
	ports, profs := a.sshBridgeAssignmentsLocked()
	a.store.mu.RUnlock()
	a.sshLogMu.Lock()
	last := map[string]sshLogEntry{}
	for _, e := range a.sshLogEntries {
		last[e.ServerID] = e
	}
	a.sshLogMu.Unlock()
	out := []map[string]any{}
	for _, sv := range servers {
		if sv.Proto != "ssh" {
			continue
		}
		m := map[string]any{
			"id": sv.ID, "name": sv.Name,
			"enabled": sv.Enabled,
			"host":    sv.Params["server"],
			"proxy":   sv.Params["ssh_proxy"],
		}
		if prof, ok := profs[sv.ID]; ok {
			m["bridge"] = true
			m["bridge_port"] = ports[sv.ID]
			m["mode"] = sshModeName(prof)
		} else {
			m["bridge"] = false
			m["mode"] = "SSH polos (tanpa jembatan)"
		}
		if e, ok := last[sv.ID]; ok {
			m["last"] = e
		}
		out = append(out, m)
	}
	return out
}
