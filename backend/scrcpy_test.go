package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"testing"
)

// --- encoder pesan kontrol (dibandingkan byte-per-byte dengan format
// ControlMessageReader server scrcpy 4.1) ---

func TestCtrlKey(t *testing.T) {
	b := scCtrlKey(0, 4, 0) // down, KEYCODE_BACK
	if len(b) != 14 || b[0] != 0 || b[1] != 0 {
		t.Fatalf("key header salah: %x", b)
	}
	if binary.BigEndian.Uint32(b[2:]) != 4 {
		t.Fatalf("keycode harus 4, dapat %d", binary.BigEndian.Uint32(b[2:]))
	}
	b2 := scCtrlKey(1, 66, 4096) // up, ENTER, META_CTRL
	if b2[1] != 1 || binary.BigEndian.Uint32(b2[10:]) != 4096 {
		t.Fatalf("key up/meta salah: %x", b2)
	}
}

func TestCtrlTouch(t *testing.T) {
	// down di (100,200) layar 1080x2400, pointer id 3
	b := scCtrlTouch(0, 3, 100, 200, 1080, 2400)
	if len(b) != 32 || b[0] != 2 || b[1] != 0 {
		t.Fatalf("touch header salah (len=%d): %x", len(b), b)
	}
	if binary.BigEndian.Uint64(b[2:]) != 3 {
		t.Fatalf("pointer id salah")
	}
	if binary.BigEndian.Uint32(b[10:]) != 100 || binary.BigEndian.Uint32(b[14:]) != 200 {
		t.Fatalf("posisi salah")
	}
	if binary.BigEndian.Uint16(b[18:]) != 1080 || binary.BigEndian.Uint16(b[20:]) != 2400 {
		t.Fatalf("ukuran layar salah")
	}
	if binary.BigEndian.Uint16(b[22:]) != 0xffff {
		t.Fatalf("pressure harus 0xffff (1.0)")
	}
	if binary.BigEndian.Uint32(b[24:]) != 1 || binary.BigEndian.Uint32(b[28:]) != 1 {
		t.Fatalf("down harus actionButton=1, buttons=1")
	}
	// up: buttons kembali 0
	up := scCtrlTouch(1, 3, 100, 200, 1080, 2400)
	if binary.BigEndian.Uint32(up[24:]) != 0 || binary.BigEndian.Uint32(up[28:]) != 0 {
		t.Fatalf("up harus actionButton=0, buttons=0")
	}
	// move: buttons tetap 1
	mv := scCtrlTouch(2, 3, 110, 210, 1080, 2400)
	if binary.BigEndian.Uint32(mv[28:]) != 1 {
		t.Fatalf("move harus buttons=1")
	}
}

func TestCtrlScroll(t *testing.T) {
	b := scCtrlScroll(540, 1200, 1080, 2400, 0, -1)
	if len(b) != 21 || b[0] != 3 {
		t.Fatalf("scroll header salah (len=%d): %x", len(b), b)
	}
	// vScroll -1 -> raw -2048 (decode: -2048/32768*16 = -1.0)
	if int16(binary.BigEndian.Uint16(b[15:])) != -2048 {
		t.Fatalf("vScroll -1 harus jadi -2048, dapat %d", int16(binary.BigEndian.Uint16(b[15:])))
	}
	if int16(binary.BigEndian.Uint16(b[13:])) != 0 {
		t.Fatalf("hScroll 0 harus jadi 0")
	}
}

func TestCtrlSetClipboard(t *testing.T) {
	b := scCtrlSetClipboard(7, true, "halo")
	if b[0] != 9 || b[9] != 1 {
		t.Fatalf("set-clipboard header salah: %x", b[:10])
	}
	if binary.BigEndian.Uint64(b[1:]) != 7 {
		t.Fatalf("sequence harus 7")
	}
	if binary.BigEndian.Uint32(b[10:]) != 4 || string(b[14:]) != "halo" {
		t.Fatalf("isi clipboard salah: %x %s", b[10:14], b[14:])
	}
}

func TestScFixed16(t *testing.T) {
	if scFixed16(1) != 2048 || scFixed16(-1) != -2048 || scFixed16(0) != 0 {
		t.Fatalf("konversi fixed-point salah: %d %d %d", scFixed16(1), scFixed16(-1), scFixed16(0))
	}
	if scFixed16(100) != 32767 || scFixed16(-100) != -32768 {
		t.Fatalf("clamp fixed-point salah")
	}
}

// --- parser aliran ---

func TestScReadPacket(t *testing.T) {
	var buf bytes.Buffer
	// codec id h264
	binary.Write(&buf, binary.BigEndian, uint32(codecH264))
	// session meta: flags 0x80000000, w=1080, h=2400
	hdr := make([]byte, 12)
	binary.BigEndian.PutUint32(hdr[0:], 0x80000000)
	binary.BigEndian.PutUint32(hdr[4:], 1080)
	binary.BigEndian.PutUint32(hdr[8:], 2400)
	buf.Write(hdr)
	// frame keyframe: pts 1.000.000 us, flag key (1<<61), payload 5 byte
	hdr2 := make([]byte, 12)
	binary.BigEndian.PutUint64(hdr2[0:], (1<<61)|1000000)
	binary.BigEndian.PutUint32(hdr2[8:], 5)
	buf.Write(hdr2)
	buf.Write([]byte{1, 2, 3, 4, 5})
	// config: flag config (1<<62), payload 3 byte
	hdr3 := make([]byte, 12)
	binary.BigEndian.PutUint64(hdr3[0:], (1 << 62))
	binary.BigEndian.PutUint32(hdr3[8:], 3)
	buf.Write(hdr3)
	buf.Write([]byte{9, 9, 9})

	r := bytes.NewReader(buf.Bytes())
	id, err := scReadCodecID(r)
	if err != nil || id != codecH264 {
		t.Fatalf("codec id salah: %x %v", id, err)
	}
	p, err := scReadPacket(r)
	if err != nil || !p.session || p.width != 1080 || p.height != 2400 {
		t.Fatalf("session salah: %+v %v", p, err)
	}
	p, err = scReadPacket(r)
	if err != nil || !p.keyframe || p.config || p.ptsUs != 1000000 || len(p.data) != 5 {
		t.Fatalf("frame salah: %+v %v", p, err)
	}
	if p.ptsUs != 1000000 {
		t.Fatalf("ptsUs salah: %d", p.ptsUs)
	}
	p, err = scReadPacket(r)
	if err != nil || !p.config || p.keyframe || len(p.data) != 3 {
		t.Fatalf("config salah: %+v %v", p, err)
	}
}

// --- websocket ---

func TestScWsAcceptKey(t *testing.T) {
	// Vektor uji RFC 6455.
	if got := scWsAcceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("accept key salah: %s", got)
	}
	_ = sha1.Sum
	_ = base64.StdEncoding
}
