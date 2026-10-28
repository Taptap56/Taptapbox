package main

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestAnnexBPassThroughAndAVCC(t *testing.T) {
	annex := []byte{0, 0, 0, 1, 0x67, 0x01, 0, 0, 0, 1, 0x65, 0x02}
	if got := annexB(annex); !bytes.Equal(got, annex) {
		t.Fatalf("Annex B harus lolos apa adanya")
	}
	// AVCC: [len=2][0x67 0x01][len=2][0x65 0x02]
	avcc := []byte{0, 0, 0, 2, 0x67, 0x01, 0, 0, 0, 2, 0x65, 0x02}
	want := []byte{0, 0, 0, 1, 0x67, 0x01, 0, 0, 0, 1, 0x65, 0x02}
	if got := annexB(avcc); !bytes.Equal(got, want) {
		t.Fatalf("AVCC harus dikonversi ke Annex B, dapat %x", got)
	}
	// Sampah tak dikenal: kembali apa adanya.
	junk := []byte{0x11, 0x22, 0x33}
	if got := annexB(junk); !bytes.Equal(got, junk) {
		t.Fatalf("paket tak dikenal harus kembali apa adanya")
	}
}

func TestADTSHeader(t *testing.T) {
	// ASC AAC LC, 48000 Hz, 2 channel: 00010 0100 0010 = 0x12 0x10
	h := adtsHeader(100, []byte{0x12, 0x10})
	if len(h) != 7 || h[0] != 0xff || h[1]&0xf0 != 0xf0 {
		t.Fatalf("header ADTS tak valid: %x", h)
	}
	frameLen := int(h[3]&0x03)<<11 | int(h[4])<<3 | int(h[5]>>5)
	if frameLen != 107 {
		t.Fatalf("panjang frame ADTS harus 107, dapat %d", frameLen)
	}
	// profile LC (field 1) & sample rate index 4 (48kHz)
	if (h[2]>>6)&0x03 != 1 || (h[2]>>2)&0x0f != 4 {
		t.Fatalf("profil/rate ADTS salah: %x", h[2])
	}
	// ASC kosong -> default LC/48k/2ch, tetap 7 byte.
	h2 := adtsHeader(10, nil)
	if len(h2) != 7 || h2[0] != 0xff {
		t.Fatalf("ADTS default tak valid: %x", h2)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Pasangan SPS/PPS ASLI dari x264 (ffmpeg, 720x1280) — ground truth
// untuk splitter config mentah.
var spsPPSVectors = []struct {
	name string
	sps  string
	pps  string
}{
	{"baseline", "6742c01fd900b40a1b0110000003001000000303c0f1832480", "68cb83cb20"},
	{"main", "674d401fd900b40a1b0110000003001000000303c0f1832480", "68ebc3cb20"},
	{"high", "6764001facb2016814360220000003002000000781e30649", "68ebc3cb22c0"},
}

func TestNormalizeConfigRaw(t *testing.T) {
	for _, v := range spsPPSVectors {
		sps := mustHex(t, v.sps)
		pps := mustHex(t, v.pps)
		raw := append(append([]byte{}, sps...), pps...)
		want := append(append([]byte{0, 0, 0, 1}, sps...), append([]byte{0, 0, 0, 1}, pps...)...)
		if got := normalizeConfig(raw); !bytes.Equal(got, want) {
			t.Fatalf("%s: normalizeConfig salah:\n got %x\nwant %x", v.name, got, want)
		}
	}
}

func TestNormalizeConfigAnnexB(t *testing.T) {
	// Sudah Annex B -> dikembalikan apa adanya.
	in := []byte{0, 0, 0, 1, 0x67, 0x64, 0x00, 0x1f, 0, 0, 0, 1, 0x68, 0xeb, 0xc3}
	if got := normalizeConfig(in); !bytes.Equal(got, in) {
		t.Fatalf("Annex B harus lolos apa adanya, dapat %x", got)
	}
}

func TestNormalizeConfigCorrupt(t *testing.T) {
	// Blob rusak (bukan SPS) -> tak boleh panic, hasil tetap diawali
	// start code agar JMuxer minimal melihat satu NAL.
	got := normalizeConfig([]byte{0x11, 0x22, 0x33, 0x44, 0x55})
	if len(got) < 4 || !bytes.Equal(got[:4], []byte{0, 0, 0, 1}) {
		t.Fatalf("blob rusak harus tetap diawali start code, dapat %x", got)
	}
}

func TestStartsWithSPS(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"sps 4-byte", []byte{0, 0, 0, 1, 0x67, 1, 2}, true},
		{"sps 3-byte", []byte{0, 0, 1, 0x67, 1, 2}, true},
		{"idr langsung", []byte{0, 0, 0, 1, 0x65, 1, 2}, false},
		{"slice P", []byte{0, 0, 0, 1, 0x41, 1, 2}, false},
		{"pendek", []byte{0, 0, 1}, false},
		{"kosong", nil, false},
	}
	for _, c := range cases {
		if got := startsWithSPS(c.in); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
