package main

// Login WebUI via halaman HTML (tanpa popup Basic bawaan browser).
//
// Alur: buka WebUI dari HP lain -> guard mengarahkan ke /login.html ->
// POST /api/login {"password":"..."} -> cookie sesi httpOnly (1 tahun).
// Cukup masukkan password SEKALI; kunjungan berikutnya langsung masuk.
//
// Token stateless: v1.<expUnix>.<randHex>.<hmacHex>, ditandatangani secret
// per-perangkat di <dataDir>/session/websess.key, jadi sesi tetap valid walau
// backend restart. Kompatibilitas lama: Basic auth user "admin" tetap diterima.
// Lokasi lama <dataDir>/websess.key dipindahkan otomatis sekali jalan
// (sessionFile) agar akar data rapi; kunci yang sama DIPINDAHKAN, bukan
// dibuat baru, jadi sesi lama tetap valid.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	webSessionCookie = "ttb_sess"
	webSessionMaxAge = 365 * 24 * 3600 // 1 tahun
	webSessionDir    = "session"      // folder sendiri untuk kunci sesi (biar rapi)
	webSessionFile   = "websess.key"
)

var (
	sessKeysMu sync.Mutex
	sessKeys   = map[string][]byte{}
)

// sessionFile: path kunci sesi + migrasi sekali jalan dari lokasi lama.
// Kunci valid (32 byte) DIPINDAHKAN (rename, fallback salin-hapus) agar sesi
// lama tetap valid; file lama yang rusak dibuang (tak pernah bisa dipakai).
// Idempotent: bila path baru sudah ada, tidak menyentuh apa-apa.
func (a *App) sessionFile() string {
	newFp := filepath.Join(a.dataDir, webSessionDir, webSessionFile)
	if _, err := os.Stat(newFp); err == nil {
		return newFp
	}
	oldFp := filepath.Join(a.dataDir, webSessionFile)
	b, err := os.ReadFile(oldFp)
	if err != nil {
		return newFp
	}
	if len(b) != 32 {
		_ = os.Remove(oldFp)
		return newFp
	}
	if err := os.MkdirAll(filepath.Dir(newFp), 0700); err != nil {
		return newFp
	}
	if err := os.Rename(oldFp, newFp); err != nil {
		if err := os.WriteFile(newFp, b, 0600); err != nil {
			return newFp
		}
		_ = os.Remove(oldFp)
	}
	return newFp
}

// sessSecret: secret penandatangan sesi, dibuat sekali lalu disimpan (0600).
func (a *App) sessSecret() []byte {
	sessKeysMu.Lock()
	defer sessKeysMu.Unlock()
	if k, ok := sessKeys[a.dataDir]; ok {
		return k
	}
	fp := a.sessionFile()
	if b, err := os.ReadFile(fp); err == nil && len(b) == 32 {
		sessKeys[a.dataDir] = b
		return b
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		sum := sha256.Sum256([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
		b = sum[:]
	}
	_ = os.MkdirAll(filepath.Dir(fp), 0700)
	_ = os.WriteFile(fp, b, 0600)
	sessKeys[a.dataDir] = b
	return b
}

// newSessionToken membuat token sesi baru.
func (a *App) newSessionToken() string {
	exp := time.Now().Add(webSessionMaxAge * time.Second).Unix()
	rb := make([]byte, 16)
	_, _ = rand.Read(rb)
	body := "v1." + strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(rb)
	mac := hmac.New(sha256.New, a.sessSecret())
	mac.Write([]byte(body))
	return body + "." + hex.EncodeToString(mac.Sum(nil))
}

// validSessionToken memeriksa tanda tangan + masa berlaku token.
func (a *App) validSessionToken(tok string) bool {
	p := strings.Split(tok, ".")
	if len(p) != 4 || p[0] != "v1" {
		return false
	}
	exp, err := strconv.ParseInt(p[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	if _, err := hex.DecodeString(p[2]); err != nil || len(p[2]) != 32 {
		return false
	}
	mac := hmac.New(sha256.New, a.sessSecret())
	mac.Write([]byte(strings.Join(p[:3], ".")))
	want := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(p[3]), []byte(want)) == 1
}

// hasSession: cookie sesi valid?
func (a *App) hasSession(r *http.Request) bool {
	c, err := r.Cookie(webSessionCookie)
	if err != nil {
		return false
	}
	return a.validSessionToken(c.Value)
}

// authed: loopback selalu boleh; dari jaringan butuh sesi valid atau
// Basic auth user "admin" (kompatibilitas lama).
func (a *App) authed(r *http.Request) bool {
	if isLoopbackAddr(r.RemoteAddr) {
		return true
	}
	if a.hasSession(r) {
		return true
	}
	pass := a.cfg().WebPass
	if pass == "" {
		return false
	}
	user, got, ok := r.BasicAuth()
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(user), []byte("admin")) == 1 &&
		subtle.ConstantTimeCompare([]byte(got), []byte(pass)) == 1
}

// loginWhitelist: path yang boleh diakses tanpa login (halaman login + asetnya).
func loginWhitelist(path string) bool {
	if path == "/taptapbox/login.html" || path == "/api/login" {
		return true
	}
	return strings.HasPrefix(path, "/taptapbox/icons/")
}

// handleLogin: POST /api/login {"password":"..."} -> 200 + cookie sesi.
func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, 405, "pakai POST")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		errJSON(w, 400, "body tidak valid")
		return
	}
	pass := a.cfg().WebPass
	if pass == "" || subtle.ConstantTimeCompare([]byte(in.Password), []byte(pass)) != 1 {
		time.Sleep(500 * time.Millisecond) // jeda anti brute-force
		errJSON(w, 401, "password salah")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     webSessionCookie,
		Value:    a.newSessionToken(),
		Path:     "/",
		MaxAge:   webSessionMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, map[string]any{"ok": true})
}
