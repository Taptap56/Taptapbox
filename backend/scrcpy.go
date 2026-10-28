package main

// Scrcpy di WebUI TaptapBox.
//
// Satu daemon Go menjalankan scrcpy-server 5.0.1 (jar resmi Genymobile,
// dibundel di bin/scrcpy-server.jar) lewat app_process — server itu
// sendiri yang menurunkan haknya ke uid shell (2000) saat start — lalu
// menyambung ke soket abstrak "scrcpy" (video, audio, kontrol, sesuai
// urutan accept server) dan me-relay:
//   video H.264 Annex B + audio AAC mentah -> MPEG-TS (lihat tsmux.go)
//   -> WebSocket ke browser (dua instans JMuxer: video & audio)
//   perintah sentuh/keyboard/scroll browser -> pesan kontrol biner
//   protokol scrcpy (ControlMessageReader server; 4.1 -> 5.0.1 tak
//   mengubah format kawat — hanya AudioPlaybackCapture + CameraCapture).
// Versi klien HARUS "5.0.1" persis: server menolak versi lain.
//
// Halaman WebUI (view Scrcpy di app.js) bicara ke backend ini lewat
// origin yang sama: POST /api/scrcpy/* dan WS /api/scrcpy/ws.

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	scrcpyVersion  = "5.0.1"
	scrcpySocket   = "\x00scrcpy"
	scrcpyTmpJar   = "/data/local/tmp/scrcpy-server.jar"
	codecH264      = 0x68323634 // "h264"
	codecAAC       = 0x00616163 // "aac" (nol di depan = padding)
	scDialTimeout  = 10 * time.Second
	scDialInterval = 100 * time.Millisecond
)

// ---------- konfigurasi (config/scrcpy.json) ----------

// scCfgVer: versi skema/default config. Config berversi lebih lama
// (default lama 720p/2Mbps/30fps) tidak menimpa default kualitas baru.
const scCfgVer = 2

type scrcpyConfig struct {
	Ver          int  `json:"ver"`
	Res          int  `json:"res"`           // max_size: 0 = asli, 480/720/1080
	Bitrate      int  `json:"bitrate"`       // video_bit_rate
	Fps          int  `json:"fps"`           // max_fps, 0 = maks
	AudioBitrate int  `json:"audio_bitrate"` // audio_bit_rate
	AudioBuffer  int  `json:"audio_buffer"`  // khusus klien (flushing JMuxer audio, ms)
	AudioEnabled bool `json:"audio_enabled"`
	Keyboard     bool `json:"keyboard"`
	Scroll       bool `json:"scroll"`
	Floating     bool `json:"floating"`
	FPSCounter   bool `json:"fps_counter"`
}

func scDefaultConfig() scrcpyConfig {
	return scrcpyConfig{
		Ver: scCfgVer,
		Res: 1080, Bitrate: 8000000, Fps: 60,
		AudioBitrate: 128000, AudioBuffer: 50,
		AudioEnabled: false, Keyboard: true, Scroll: true,
		Floating: false, FPSCounter: true,
	}
}

var scCfgMu sync.Mutex

func (a *App) scConfigPath() string {
	return filepath.Join(a.jsonDir, "scrcpy.json")
}

func (a *App) scLoadConfig() scrcpyConfig {
	cfg := scDefaultConfig()
	scCfgMu.Lock()
	defer scCfgMu.Unlock()
	b, err := os.ReadFile(a.scConfigPath())
	if err != nil {
		return cfg
	}
	var c scrcpyConfig
	if json.Unmarshal(b, &c) == nil {
		// Kualitas video hanya dihormati bila config sudah berskema baru;
		// config lama dibiarkan memakai default baru (1080p/8Mbps/60fps).
		if c.Ver >= scCfgVer {
			if c.Res >= 0 {
				cfg.Res = c.Res
			}
			if c.Bitrate > 0 {
				cfg.Bitrate = c.Bitrate
			}
			if c.Fps >= 0 {
				cfg.Fps = c.Fps
			}
		}
		if c.AudioBitrate > 0 {
			cfg.AudioBitrate = c.AudioBitrate
		}
		if c.AudioBuffer >= 0 {
			cfg.AudioBuffer = c.AudioBuffer
		}
		cfg.AudioEnabled = c.AudioEnabled
		cfg.Keyboard = c.Keyboard
		cfg.Scroll = c.Scroll
		cfg.Floating = c.Floating
		cfg.FPSCounter = c.FPSCounter
	}
	return cfg
}

func (a *App) scSaveConfig(cfg scrcpyConfig) {
	scCfgMu.Lock()
	defer scCfgMu.Unlock()
	cfg.Ver = scCfgVer
	b, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(a.scConfigPath(), b, 0644)
}

// ---------- pesan kontrol biner (scrcpy) ----------
// Format persis ControlMessageReader server (big-endian).

func scCtrlKey(action byte, keycode, meta int32) []byte {
	b := make([]byte, 14)
	b[0] = 0 // TYPE_INJECT_KEYCODE
	b[1] = action
	binary.BigEndian.PutUint32(b[2:], uint32(keycode))
	binary.BigEndian.PutUint32(b[6:], 0) // repeat
	binary.BigEndian.PutUint32(b[10:], uint32(meta))
	return b
}

// scCtrlTouch: action 0=down, 1=up, 2=move, 3=cancel. buttons &
// actionButton diturunkan dari action (down/move = tombol utama aktif).
func scCtrlTouch(action byte, id int64, x, y int32, w, h uint16) []byte {
	var actionButton, buttons int32
	switch action {
	case 0: // down
		actionButton, buttons = 1, 1
	case 2: // move
		buttons = 1
	}
	b := make([]byte, 32)
	b[0] = 2 // TYPE_INJECT_TOUCH_EVENT
	b[1] = action
	binary.BigEndian.PutUint64(b[2:], uint64(id))
	binary.BigEndian.PutUint32(b[10:], uint32(x))
	binary.BigEndian.PutUint32(b[14:], uint32(y))
	binary.BigEndian.PutUint16(b[18:], w)
	binary.BigEndian.PutUint16(b[20:], h)
	binary.BigEndian.PutUint16(b[22:], 0xffff) // pressure 1.0
	binary.BigEndian.PutUint32(b[24:], uint32(actionButton))
	binary.BigEndian.PutUint32(b[28:], uint32(buttons))
	return b
}

func scFixed16(v float64) int16 {
	r := int64(math.Round(v * 2048)) // decode: raw/32768*16 = raw/2048
	if r > 32767 {
		r = 32767
	}
	if r < -32768 {
		r = -32768
	}
	return int16(r)
}

func scCtrlScroll(x, y int32, w, h uint16, hs, vs float64) []byte {
	b := make([]byte, 21)
	b[0] = 3 // TYPE_INJECT_SCROLL_EVENT
	binary.BigEndian.PutUint32(b[1:], uint32(x))
	binary.BigEndian.PutUint32(b[5:], uint32(y))
	binary.BigEndian.PutUint16(b[9:], w)
	binary.BigEndian.PutUint16(b[11:], h)
	binary.BigEndian.PutUint16(b[13:], uint16(scFixed16(hs)))
	binary.BigEndian.PutUint16(b[15:], uint16(scFixed16(vs)))
	binary.BigEndian.PutUint32(b[17:], 0) // buttons
	return b
}

func scCtrlSetClipboard(seq int64, paste bool, text string) []byte {
	raw := []byte(text)
	b := make([]byte, 14)
	b[0] = 9 // TYPE_SET_CLIPBOARD
	binary.BigEndian.PutUint64(b[1:], uint64(seq))
	if paste {
		b[9] = 1
	}
	binary.BigEndian.PutUint32(b[10:], uint32(len(raw)))
	return append(b, raw...)
}

// ---------- parser aliran scrcpy ----------

type scPacket struct {
	session  bool
	width    int
	height   int
	config   bool
	keyframe bool
	ptsUs    int64
	data     []byte
}

func scReadCodecID(r io.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}

// scReadPacket: satu header 12 byte + payload (bila frame). Header
// session (bit MSB byte pertama = 1) hanya 12 byte tanpa payload.
func scReadPacket(r io.Reader) (scPacket, error) {
	var h [12]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return scPacket{}, err
	}
	if h[0]&0x80 != 0 {
		return scPacket{
			session: true,
			width:   int(binary.BigEndian.Uint32(h[4:])),
			height:  int(binary.BigEndian.Uint32(h[8:])),
		}, nil
	}
	flags := binary.BigEndian.Uint64(h[:8])
	size := binary.BigEndian.Uint32(h[8:])
	if size == 0 || size > 1<<22 {
		return scPacket{}, fmt.Errorf("ukuran paket scrcpy tak wajar: %d", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return scPacket{}, err
	}
	return scPacket{
		config:   flags&(1<<62) != 0,
		keyframe: flags&(1<<61) != 0,
		ptsUs:    int64(flags & ((1 << 61) - 1)),
		data:     data,
	}, nil
}

// ---------- WebSocket (hand-rolled, tanpa dependensi luar) ----------

type wsHub struct {
	mu    sync.Mutex
	conns map[*wsConn]bool
	gop   [][]byte // khusus kanal video: keyframe terakhir + frame sesudahnya
}

func newWsHub() *wsHub { return &wsHub{conns: map[*wsConn]bool{}} }

func (h *wsHub) add(c *wsConn) {
	h.mu.Lock()
	h.conns[c] = true
	h.mu.Unlock()
}

// addWithReplay: daftarkan klien video baru setelah mengirim GOP tersimpan
// (keyframe + frame sesudahnya). Browser baru konek SESUDAH frame pertama
// lewat; tanpa ini layar hitam sampai keyframe berikutnya. Lock dipegang
// selama kirim: broadcastVideo menyalin daftar klien di bawah lock yang
// sama, jadi urutan frame klien baru tidak bisa tersela.
func (h *wsHub) addWithReplay(c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, f := range h.gop {
		if err := c.writeFrame(0x2, f); err != nil {
			c.close()
			return
		}
	}
	h.conns[c] = true
}

// broadcastVideo: broadcast frame video + simpan GOP berjalan untuk replay.
func (h *wsHub) broadcastVideo(data []byte, key bool) {
	h.mu.Lock()
	if key {
		h.gop = [][]byte{data}
	} else if len(h.gop) > 0 {
		if len(h.gop) >= 900 {
			h.gop = nil // GOP tak wajar panjang: tunggu keyframe berikutnya
		} else {
			h.gop = append(h.gop, data)
		}
	}
	list := make([]*wsConn, 0, len(h.conns))
	for c := range h.conns {
		list = append(list, c)
	}
	h.mu.Unlock()
	for _, c := range list {
		if err := c.writeFrame(0x2, data); err != nil {
			h.remove(c)
			c.close()
		}
	}
}

func (h *wsHub) remove(c *wsConn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

func (h *wsHub) closeAll() {
	h.mu.Lock()
	list := make([]*wsConn, 0, len(h.conns))
	for c := range h.conns {
		list = append(list, c)
	}
	h.mu.Unlock()
	for _, c := range list {
		c.close()
	}
}

// broadcast: frame biner ke semua klien kanal ini.
func (h *wsHub) broadcast(data []byte) {
	h.mu.Lock()
	list := make([]*wsConn, 0, len(h.conns))
	for c := range h.conns {
		list = append(list, c)
	}
	h.mu.Unlock()
	for _, c := range list {
		if err := c.writeFrame(0x2, data); err != nil {
			h.remove(c)
			c.close()
		}
	}
}

func (h *wsHub) broadcastText(data []byte) {
	h.mu.Lock()
	list := make([]*wsConn, 0, len(h.conns))
	for c := range h.conns {
		list = append(list, c)
	}
	h.mu.Unlock()
	for _, c := range list {
		if err := c.writeFrame(0x1, data); err != nil {
			h.remove(c)
			c.close()
		}
	}
}

type wsConn struct {
	conn   net.Conn
	wmu    sync.Mutex
	closed int32
}

func (c *wsConn) close() {
	if atomic.CompareAndSwapInt32(&c.closed, 0, 1) {
		_ = c.conn.Close()
	}
}

func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if atomic.LoadInt32(&c.closed) == 1 {
		return errors.New("ws tertutup")
	}
	var hdr []byte
	hdr = append(hdr, 0x80|opcode)
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n < 65536:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 127)
		for i := 7; i >= 0; i-- {
			hdr = append(hdr, byte(n>>(uint(i)*8)))
		}
	}
	// Klien macet jangan menahan videoPump selamanya (lag menumpuk di
	// soket server): batasi tiap tulis. Header+payload satu writev agar
	// tak jadi dua segmen TCP kecil per frame.
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	bufs := net.Buffers{hdr}
	if n > 0 {
		bufs = append(bufs, payload)
	}
	_, err := bufs.WriteTo(c.conn)
	return err
}

// wsReadMessage: baca satu pesan utuh (gabungkan fragmentasi).
// Mengembalikan opcode (0x1 teks, 0x2 biner) & payload; ping dibalas
// pong otomatis; close mengembalikan io.EOF.
func (c *wsConn) readMessage() (byte, []byte, error) {
	var opcode byte
	var full []byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(c.conn, h[:]); err != nil {
			return 0, nil, err
		}
		fin := h[0]&0x80 != 0
		op := h[0] & 0x0f
		masked := h[1]&0x80 != 0
		ln := int64(h[1] & 0x7f)
		switch ln {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(c.conn, b[:]); err != nil {
				return 0, nil, err
			}
			ln = int64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(c.conn, b[:]); err != nil {
				return 0, nil, err
			}
			ln = int64(binary.BigEndian.Uint64(b[:]))
		}
		if ln < 0 || ln > 1<<22 {
			return 0, nil, fmt.Errorf("frame ws tak wajar: %d", ln)
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(c.conn, mask[:]); err != nil {
				return 0, nil, err
			}
		}
		payload := make([]byte, ln)
		if _, err := io.ReadFull(c.conn, payload); err != nil {
			return 0, nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		switch op {
		case 0x8: // close
			_ = c.writeFrame(0x8, nil)
			return 0, nil, io.EOF
		case 0x9: // ping
			_ = c.writeFrame(0xa, payload)
			continue
		case 0xa: // pong
			continue
		case 0x0: // continuation
			full = append(full, payload...)
		case 0x1, 0x2:
			opcode = op
			full = append(full, payload...)
		default:
			continue
		}
		if fin {
			return opcode, full, nil
		}
	}
}

func scWsAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// ---------- sesi scrcpy ----------

type scrcpySession struct {
	mu       sync.Mutex
	running  bool
	cmd      *exec.Cmd
	logFile  io.Closer // capLogWriter (dulu *os.File); hanya disimpan, penutupan lewat lf lokal
	cfg      scrcpyConfig
	videoW   int
	videoH   int
	audioOn  bool
	videoC   net.Conn
	audioC   net.Conn
	controlC net.Conn
	ctrlMu   sync.Mutex
	seq      int64
	done     chan struct{}
	videoHub *wsHub
	audioHub *wsHub
	ctrlHub  *wsHub
}

func (s *scrcpySession) writeControl(b []byte) error {
	s.ctrlMu.Lock()
	defer s.ctrlMu.Unlock()
	if s.controlC == nil {
		return errors.New("kontrol scrcpy belum tersambung")
	}
	_, err := s.controlC.Write(b)
	return err
}

func (s *scrcpySession) stopLocked() {
	if !s.running && s.cmd == nil && s.videoC == nil {
		return
	}
	s.running = false
	if s.done != nil {
		close(s.done)
		s.done = nil
	}
	for _, c := range []net.Conn{s.videoC, s.audioC, s.controlC} {
		if c != nil {
			_ = c.Close()
		}
	}
	s.videoC, s.audioC, s.controlC = nil, nil, nil
	for _, h := range []*wsHub{s.videoHub, s.audioHub, s.ctrlHub} {
		if h != nil {
			h.closeAll()
		}
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.cmd = nil
}

func dialScrcpy() (net.Conn, error) {
	deadline := time.Now().Add(scDialTimeout)
	var err error
	for time.Now().Before(deadline) {
		var c net.Conn
		c, err = net.DialUnix("unix", nil, &net.UnixAddr{Name: scrcpySocket, Net: "unix"})
		if err == nil {
			return c, nil
		}
		time.Sleep(scDialInterval)
	}
	return nil, err
}

func (a *App) scrcpySess() *scrcpySession {
	if a.scrcpy == nil {
		a.scrcpy = &scrcpySession{}
	}
	return a.scrcpy
}

func (a *App) scStart(cfg scrcpyConfig) error {
	s := a.scrcpySess()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()

	// Jar: dari bin/ (layout.sh memindahkannya dari modul ke data/bin),
	// disalin ke /data/local/tmp seperti konvensi klien resmi.
	jar := filepath.Join(a.binDir, "scrcpy-server.jar")
	raw, err := os.ReadFile(jar)
	if err != nil {
		return fmt.Errorf("scrcpy-server.jar tidak ada di %s", a.binDir)
	}
	if err := os.WriteFile(scrcpyTmpJar, raw, 0644); err != nil {
		return fmt.Errorf("gagal salin jar scrcpy: %v", err)
	}
	// Bersihkan server sisa dari sesi sebelumnya (mis. daemon restart).
	_ = exec.Command("pkill", "-f", "com.genymobile.scrcpy.Server").Run()

	// Versi server HARUS persis sama dengan versi jar yang terpasang
	// (dibaca dari scrcpy-server.version; fallback = versi bawaan paket).
	args := []string{
		a.scrcpyServerVersion(),
		"tunnel_forward=true",
		"video=true",
		"control=true",
		"cleanup=false",
		"video_codec=h264",
		fmt.Sprintf("video_bit_rate=%d", cfg.Bitrate),
		fmt.Sprintf("max_size=%d", cfg.Res),
		"audio_codec=aac",
		fmt.Sprintf("audio_bit_rate=%d", cfg.AudioBitrate),
		"audio_source=output",
		"send_device_meta=false",
		"send_dummy_byte=false",
		"send_stream_meta=true",
		"send_frame_meta=true",
		"log_level=info",
	}
	if cfg.AudioEnabled {
		args = append(args, "audio=true")
	} else {
		args = append(args, "audio=false")
	}
	if cfg.Fps > 0 {
		args = append(args, fmt.Sprintf("max_fps=%d", cfg.Fps))
	}

	// Go tak mengizinkan argumen variadic campuran + ekspansi slice:
	// gabungkan dulu jadi satu slice.
	fullArgs := append([]string{"/", "com.genymobile.scrcpy.Server"}, args...)
	cmd := exec.Command("/system/bin/app_process", fullArgs...)
	cmd.Env = append(os.Environ(),
		"CLASSPATH="+scrcpyTmpJar, "ANDROID_ROOT=/system", "ANDROID_DATA=/data")
	lf := newCapLogWriter(filepath.Join(a.logDir, "scrcpy-server.log"))
	if lf != nil {
		cmd.Stdout = lf
		cmd.Stderr = lf
	}
	if err := cmd.Start(); err != nil {
		if lf != nil {
			_ = lf.Close()
		}
		return fmt.Errorf("gagal jalankan scrcpy-server: %v", err)
	}
	cleanup := func() {
		_ = cmd.Process.Kill()
		if lf != nil {
			_ = lf.Close()
		}
	}

	videoC, err := dialScrcpy()
	if err != nil {
		cleanup()
		return fmt.Errorf("server scrcpy tak menjawab (video): %v", err)
	}
	var audioC net.Conn
	if cfg.AudioEnabled {
		audioC, err = dialScrcpy()
		if err != nil {
			_ = videoC.Close()
			cleanup()
			return fmt.Errorf("server scrcpy tak menjawab (audio): %v", err)
		}
	}
	controlC, err := dialScrcpy()
	if err != nil {
		_ = videoC.Close()
		if audioC != nil {
			_ = audioC.Close()
		}
		cleanup()
		return fmt.Errorf("server scrcpy tak menjawab (kontrol): %v", err)
	}
	vidCodec, err := scReadCodecID(videoC)
	if err != nil || vidCodec != codecH264 {
		_ = videoC.Close()
		if audioC != nil {
			_ = audioC.Close()
		}
		_ = controlC.Close()
		cleanup()
		return fmt.Errorf("codec video scrcpy tak dikenali: %x", vidCodec)
	}
	audioOn := false
	if cfg.AudioEnabled {
		ac, err := scReadCodecID(audioC)
		if err != nil {
			_ = videoC.Close()
			_ = audioC.Close()
			_ = controlC.Close()
			cleanup()
			return fmt.Errorf("gagal baca codec audio: %v", err)
		}
		switch ac {
		case codecAAC:
			audioOn = true
		case 0:
			// Server menonaktifkan audio sendiri (mis. tak bisa
			// menangkap audio): lanjut video saja.
			_ = audioC.Close()
			audioC = nil
		default:
			_ = videoC.Close()
			_ = audioC.Close()
			_ = controlC.Close()
			cleanup()
			return fmt.Errorf("codec audio scrcpy tak dikenali: %x", ac)
		}
	}

	s.cmd = cmd
	s.logFile = lf
	s.cfg = cfg
	s.audioOn = audioOn
	s.videoC, s.audioC, s.controlC = videoC, audioC, controlC
	s.done = make(chan struct{})
	s.videoHub, s.audioHub, s.ctrlHub = newWsHub(), newWsHub(), newWsHub()
	s.running = true
	go s.videoPump()
	if audioOn {
		go s.audioPump()
	}
	go s.controlPump()
	go func() {
		_ = cmd.Wait()
		if lf != nil {
			_ = lf.Close()
		}
		s.mu.Lock()
		if s.cmd == cmd {
			s.stopLocked()
		}
		s.mu.Unlock()
	}()
	a.logf("scrcpy start: res=%d bitrate=%d fps=%d audio=%v", cfg.Res, cfg.Bitrate, cfg.Fps, audioOn)
	return nil
}

func (a *App) scStop() {
	s := a.scrcpySess()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	a.logf("scrcpy stop")
}

func (s *scrcpySession) videoPump() {
	// Relay Annex B MENTAH ke JMuxer (TANPA bungkus TS — lihat scmedia.go
	// soal kenapa TS membuat layar hitam). Satu pesan WS = satu frame
	// (config yang tertunda ditempel ke frame pertama).
	hub := s.videoHub // tangkap hub sesi ini (restart membuat hub baru)
	conn := s.videoC
	var pending []byte // blob config mentah (bisa beberapa paket)
	var cfg []byte     // config (SPS+PPS Annex B) terakhir yang dikenal
	for {
		p, err := scReadPacket(conn)
		if err != nil {
			return
		}
		if p.session {
			s.mu.Lock()
			s.videoW, s.videoH = p.width, p.height
			s.mu.Unlock()
			continue
		}
		if p.config {
			// SPS+PPS mentah, diakumulasi (ada encoder yang mengirim
			// SPS & PPS sebagai dua paket config terpisah).
			pending = append(pending, p.data...)
			continue
		}
		frame := annexB(p.data)
		if len(pending) > 0 {
			cfg = normalizeConfig(pending)
			pending = nil
			frame = append(append([]byte(nil), cfg...), frame...)
		} else if p.keyframe && len(cfg) > 0 && !startsWithSPS(frame) {
			// Tiap keyframe harus bisa berdiri sendiri (klien yang
			// bergabung belakangan / replay GOP): sisipkan config terakhir.
			frame = append(append([]byte(nil), cfg...), frame...)
		}
		hub.broadcastVideo(frame, p.keyframe)
	}
}

func (s *scrcpySession) audioPump() {
	// Relay AAC MENTAH ber-header ADTS ke JMuxer (tanpa TS).
	hub := s.audioHub
	conn := s.audioC
	var asc []byte
	for {
		p, err := scReadPacket(conn)
		if err != nil {
			return
		}
		if p.config {
			asc = p.data // AudioSpecificConfig
			continue
		}
		hub.broadcast(append(adtsHeader(len(p.data), asc), p.data...))
	}
}

func (s *scrcpySession) controlPump() {
	conn := s.controlC
	hub := s.ctrlHub
	for {
		var hb [1]byte
		if _, err := io.ReadFull(conn, hb[:]); err != nil {
			return
		}
		switch hb[0] {
		case 0: // TYPE_CLIPBOARD
			var lb [4]byte
			if _, err := io.ReadFull(conn, lb[:]); err != nil {
				return
			}
			ln := binary.BigEndian.Uint32(lb[:])
			if ln > 1<<18 {
				return
			}
			raw := make([]byte, ln)
			if _, err := io.ReadFull(conn, raw); err != nil {
				return
			}
			msg, _ := json.Marshal(map[string]string{"type": "clipboard_device", "text": string(raw)})
			hub.broadcastText(msg)
		case 1: // TYPE_ACK_CLIPBOARD
			var sb [8]byte
			if _, err := io.ReadFull(conn, sb[:]); err != nil {
				return
			}
		case 2: // TYPE_UHID_OUTPUT
			var hdr [4]byte
			if _, err := io.ReadFull(conn, hdr[:]); err != nil {
				return
			}
			ln := int(binary.BigEndian.Uint16(hdr[2:]))
			if ln > 0 {
				if _, err := io.CopyN(io.Discard, conn, int64(ln)); err != nil {
					return
				}
			}
		default:
			return
		}
	}
}

// ---------- handler HTTP ----------

func (a *App) handleScrcpyStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	s := a.scrcpySess()
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, map[string]any{
		"running": s.running, "video_w": s.videoW, "video_h": s.videoH,
		"audio": s.audioOn,
	})
}

func (a *App) handleScrcpyConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		writeJSON(w, a.scLoadConfig())
	case "POST":
		var cfg scrcpyConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			errJSON(w, 400, "config tidak valid")
			return
		}
		a.scSaveConfig(cfg)
		writeJSON(w, map[string]any{"ok": true})
	default:
		errJSON(w, 405, "method tidak didukung")
	}
}

func (a *App) handleScrcpyStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	cfg := a.scLoadConfig()
	// Parameter start boleh menimpa config tersimpan (halaman mengirim
	// nilai-nilai select terbaru); kosong = pakai tersimpan.
	var req struct {
		Res          *int  `json:"res"`
		Bitrate      *int  `json:"bitrate"`
		Fps          *int  `json:"fps"`
		AudioBitrate *int  `json:"audio_bitrate"`
		AudioEnabled *bool `json:"audio_enabled"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	if len(body) > 0 {
		_ = json.Unmarshal(body, &req)
	}
	if req.Res != nil {
		cfg.Res = *req.Res
	}
	if req.Bitrate != nil {
		cfg.Bitrate = *req.Bitrate
	}
	if req.Fps != nil {
		cfg.Fps = *req.Fps
	}
	if req.AudioBitrate != nil {
		cfg.AudioBitrate = *req.AudioBitrate
	}
	if req.AudioEnabled != nil {
		cfg.AudioEnabled = *req.AudioEnabled
	}
	if cfg.Res < 0 {
		cfg.Res = 1080 // 0 = resolusi asli (max_size=0), sah
	}
	if cfg.Bitrate <= 0 {
		cfg.Bitrate = 8000000
	}
	if cfg.AudioBitrate <= 0 {
		cfg.AudioBitrate = 128000
	}
	if err := a.scStart(cfg); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	a.scSaveConfig(cfg)
	writeJSON(w, map[string]any{"running": true})
}

func (a *App) handleScrcpyStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	a.scStop()
	writeJSON(w, map[string]any{"running": false})
}

func (a *App) handleScrcpyPaste(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err := json.Unmarshal(body, &req); err != nil || req.Text == "" {
		errJSON(w, 400, "teks kosong")
		return
	}
	s := a.scrcpySess()
	seq := atomic.AddInt64(&s.seq, 1)
	if err := s.writeControl(scCtrlSetClipboard(seq, true, req.Text)); err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleScrcpyWS(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") ||
		strings.ToLower(r.Header.Get("Upgrade")) != "websocket" {
		errJSON(w, 400, "bukan permintaan websocket")
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		errJSON(w, 400, "Sec-WebSocket-Key kosong")
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		errJSON(w, 500, "hijack tidak didukung")
		return
	}
	raw, _, err := hj.Hijack()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	accept := scWsAcceptKey(key)
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := raw.Write([]byte(resp)); err != nil {
		_ = raw.Close()
		return
	}
	c := &wsConn{conn: raw}
	op, first, err := c.readMessage()
	if err != nil || op != 0x1 {
		c.close()
		return
	}
	channel := string(first)
	s := a.scrcpySess()
	s.mu.Lock()
	var hub *wsHub
	switch channel {
	case "video":
		hub = s.videoHub
	case "audio":
		hub = s.audioHub
	case "control":
		hub = s.ctrlHub
	}
	s.mu.Unlock()
	if hub == nil {
		c.close()
		return
	}
	if channel == "video" {
		hub.addWithReplay(c)
	} else {
		hub.add(c)
	}
	defer func() {
		hub.remove(c)
		c.close()
	}()
	if channel != "control" {
		// Kanal video/audio: hanya terima (abaikan pesan masuk).
		for {
			if _, _, err := c.readMessage(); err != nil {
				return
			}
		}
	}
	// Kanal kontrol: JSON browser -> pesan kontrol biner server.
	for {
		op, msg, err := c.readMessage()
		if err != nil {
			return
		}
		if op != 0x1 {
			continue
		}
		a.scHandleControlMsg(s, msg)
	}
}

func (a *App) scHandleControlMsg(s *scrcpySession, msg []byte) {
	var m struct {
		Type    string  `json:"type"`
		Action  *int    `json:"action"`
		ID      *int64  `json:"id"`
		X       *int32  `json:"x"`
		Y       *int32  `json:"y"`
		W       *int    `json:"w"`
		H       *int    `json:"h"`
		Keycode *int32  `json:"keycode"`
		Meta    *int32  `json:"meta"`
		HScroll float64 `json:"hScroll"`
		VScroll float64 `json:"vScroll"`
	}
	if json.Unmarshal(msg, &m) != nil {
		return
	}
	switch m.Type {
	case "touch":
		if m.Action == nil || m.ID == nil || m.X == nil || m.Y == nil || m.W == nil || m.H == nil {
			return
		}
		_ = s.writeControl(scCtrlTouch(byte(*m.Action), *m.ID, *m.X, *m.Y, uint16(*m.W), uint16(*m.H)))
	case "key":
		if m.Action == nil || m.Keycode == nil {
			return
		}
		var meta int32
		if m.Meta != nil {
			meta = *m.Meta
		}
		_ = s.writeControl(scCtrlKey(byte(*m.Action), *m.Keycode, meta))
	case "scroll":
		if m.X == nil || m.Y == nil || m.W == nil || m.H == nil {
			return
		}
		_ = s.writeControl(scCtrlScroll(*m.X, *m.Y, uint16(*m.W), uint16(*m.H), m.HScroll, m.VScroll))
	}
}
