package main

// Helpers media untuk relay scrcpy -> JMuxer (browser).
//
// KONTRAK JMUXER (terbukti lewat harness JMuxer asli + NAL x264 asli,
// 2026-10-24): JMuxer TIDAK mem-parse MPEG-TS sama sekali — feed({video})
// menerima H.264 Annex B mentah dan feed({audio}) menerima frame AAC
// ber-header ADTS. v1.0.19/v1.0.20 sempat membungkus aliran ke TS dan
// itu biang layar hitam: extractNALu JMuxer menafsirkan byte header TS
// 0x47 (& 0x1f = 7) sebagai NAL SPS palsu, sehingga avcC berisi sampah
// (terbukti: SPS avcC = 11 byte header paket TS, bukan SPS asli) dan
// decoder browser menolaknya; sementara itu paket config mentah
// (tanpa start code) membuat PPS tak pernah terlihat sehingga
// readyToDecode tak pernah true dan SourceBuffer tak terbentuk sama
// sekali. Karena itu backend mengirim Annex B/ADTS mentah, TANPA TS.
//
// Satu-satunya normalisasi yang dibutuhkan: paket config video dari
// encoder Android berisi SPS+PPS mentah TANPA start code (format
// csd-0/csd-1). JMuxer butuh keduanya sebagai NAL Annex B terpisah.
// normalizeConfig() memecah blob itu dengan mem-parse SPS (bit reader
// sadar emulation prevention); bila parse gagal, fallback ke pemindaian
// header PPS 0x68 dari belakang.

import (
	"encoding/binary"
)

// annexB: pastikan paket video berbentuk Annex B. Encoder Android
// mengeluarkan Annex B apa adanya; bila paket ternyata AVCC (prefix
// panjang 4 byte), konversi ke start code. Bila tak dikenali, paket
// dikembalikan apa adanya.
func annexB(pkt []byte) []byte {
	if len(pkt) >= 4 && (hasStartCode(pkt) || (len(pkt) >= 3 && pkt[0] == 0 && pkt[1] == 0 && pkt[2] == 1)) {
		return pkt
	}
	// Coba parse AVCC: deretan [len32][NAL] yang menghabiskan paket persis.
	out := []byte{}
	pos := 0
	for pos+4 <= len(pkt) {
		n := int(binary.BigEndian.Uint32(pkt[pos:]))
		if n <= 0 || pos+4+n > len(pkt) {
			return pkt // bukan AVCC valid -> kembalikan apa adanya
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, pkt[pos+4:pos+4+n]...)
		pos += 4 + n
	}
	if pos != len(pkt) {
		return pkt
	}
	return out
}

func hasStartCode(pkt []byte) bool {
	return len(pkt) >= 4 && pkt[0] == 0 && pkt[1] == 0 && pkt[2] == 0 && pkt[3] == 1
}

// adtsHeader: header ADTS 7 byte untuk satu frame AAC mentah.
// asc = AudioSpecificConfig (2 byte: 5 bit profile, 4 bit sample rate
// index, 4 bit channel). Bila asc tak valid, pakai default LC/48kHz/2ch.
func adtsHeader(payloadLen int, asc []byte) []byte {
	profile := 1  // AAC LC -> field profile = 1 (profile-1)
	srateIdx := 4 // 48000 Hz
	channels := 2
	if len(asc) >= 2 {
		profile = int(asc[0]>>3) - 1
		if profile < 0 || profile > 3 {
			profile = 1
		}
		srateIdx = int((asc[0]&0x07)<<1 | (asc[1] >> 7))
		if srateIdx > 12 {
			srateIdx = 4
		}
		channels = int(asc[1]>>3) & 0x0f
		if channels == 0 || channels > 7 {
			channels = 2
		}
	}
	frameLen := 7 + payloadLen
	return []byte{
		0xff, 0xf1, // sync, ID MPEG-4, layer 0, protection absent
		byte(0x40 | (profile << 6) | (srateIdx << 2) | (channels >> 2)),
		byte((channels&0x03)<<6 | (frameLen >> 11)),
		byte(frameLen >> 3),
		byte((frameLen&0x07)<<5 | 0x1f),
		0xfc, // buffer fullness 0x7FF (VBR) + 0 raw blocks
	}
}

// bitReader: pembaca bit atas aliran RBSP yang sadar emulation
// prevention (byte 0x03 sesudah 00 00 dibuang saat dibaca, tapi tetap
// dihitung dalam pos = panjang asli yang terkonsumsi).
type bitReader struct {
	data  []byte
	pos   int    // indeks byte asli berikutnya yang akan diambil
	buf   uint64 // buffer bit (uint64 agar tak meluap saat fetch)
	nbits int
	zeros int // jumlah byte 00 berurutan yang sudah diambil
	err   bool
}

func (b *bitReader) fetch() {
	for b.pos < len(b.data) {
		c := b.data[b.pos]
		b.pos++
		if b.zeros == 2 && c == 0x03 {
			b.zeros = 0
			continue // buang byte emulation prevention
		}
		if c == 0 {
			b.zeros++
		} else {
			b.zeros = 0
		}
		b.buf = b.buf<<8 | uint64(c)
		b.nbits += 8
		return
	}
	b.err = true
}

func (b *bitReader) u(n int) uint32 {
	if b.err || n < 0 || n > 32 {
		b.err = true
		return 0
	}
	for b.nbits < n {
		b.fetch()
		if b.err {
			return 0
		}
	}
	b.nbits -= n
	v := uint32((b.buf >> uint(b.nbits)) & ((uint64(1) << uint(n)) - 1))
	b.buf &= (uint64(1) << uint(b.nbits)) - 1
	return v
}

// ue: Exp-Golomb unsigned. Nilai hitung yang absurd (> 1 juta) dianggap
// rusak agar parser tak berputar lama pada data korup.
func (b *bitReader) ue() uint32 {
	zeros := 0
	for {
		if b.u(1) == 1 {
			break
		}
		zeros++
		if zeros > 31 || b.err {
			b.err = true
			return 0
		}
	}
	if zeros == 0 {
		return 0
	}
	v := b.u(zeros)
	if v >= 1<<20 {
		b.err = true
		return 0
	}
	return (uint32(1) << uint(zeros)) - 1 + v
}

func (b *bitReader) se() int32 {
	v := b.ue()
	if v%2 == 0 {
		return -int32(v / 2)
	}
	return int32((v + 1) / 2)
}

func highProfile(profile uint32) bool {
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		return true
	}
	return false
}

func parseHRD(b *bitReader) {
	cpbCnt := b.ue()
	if cpbCnt > 32 {
		b.err = true
		return
	}
	b.u(4) // bit_rate_scale
	b.u(4) // cpb_size_scale
	for i := uint32(0); i <= cpbCnt; i++ {
		b.ue() // bit_rate_value_minus1
		b.ue() // cpb_size_value_minus1
		b.u(1) // cbr_flag
	}
	b.u(5) // initial_cpb_removal_delay_length_minus1
	b.u(5) // cpb_removal_delay_length_minus1
	b.u(5) // dpb_output_delay_length_minus1
	b.u(5) // time_offset_length
}

func parseVUI(b *bitReader) {
	if b.u(1) == 1 { // aspect_ratio_info_present_flag
		if b.u(8) == 255 { // Extended_SAR
			b.u(16)
			b.u(16)
		}
	}
	if b.u(1) == 1 { // overscan_info_present_flag
		b.u(1)
	}
	if b.u(1) == 1 { // video_signal_type_present_flag
		b.u(3)
		b.u(1)
		if b.u(1) == 1 { // colour_description_present_flag
			b.u(8)
			b.u(8)
			b.u(8)
		}
	}
	if b.u(1) == 1 { // chroma_loc_info_present_flag
		b.ue()
		b.ue()
	}
	if b.u(1) == 1 { // timing_info_present_flag
		b.u(32)
		b.u(32)
		b.u(1)
	}
	nalHRD := b.u(1) == 1
	vclHRD := b.u(1) == 1
	if nalHRD {
		parseHRD(b)
	}
	if vclHRD {
		parseHRD(b)
	}
	if nalHRD || vclHRD {
		b.u(1) // low_delay_hrd_flag
	}
	b.u(1)           // pic_struct_present_flag
	if b.u(1) == 1 { // bitstream_restriction_flag
		b.u(1)
		b.u(1)
		b.ue()
		b.ue()
		b.ue()
		b.ue()
		b.ue()
		b.ue()
		b.ue()
	}
}

// spsLength: panjang NAL SPS (termasuk byte header) di dalam buf yang
// berisi SPS+PPS mentah berdempetan. -1 bila parse gagal.
//
// BEST-EFFORT: parser ini gagal pada ekor VUI SPS x264 asli (bagian
// bitstream_restriction) — field-fieldnya ter-parse sampai max_reorder,
// lalu stop bit tak tercapai. Penyebab pastinya belum ketemu (vektor
// baseline/main/high ffmpeg terverifikasi byte-for-byte, parse manual
// dan Go sepakat gagal di titik yang sama). normalizeConfig TIDAK
// bergantung padanya: bila spsLength gagal, jalur pindai PPS (0x68 dari
// belakang + ppsValid) mengambil alih dan TERBUKTI benar untuk ketiga
// vektor tersebut (TestNormalizeConfigRaw). Jangan jadikan spsLength
// satu-satunya jalan tanpa fallback.
func spsLength(buf []byte) int {
	if len(buf) < 2 || buf[0]&0x1f != 7 {
		return -1
	}
	b := &bitReader{data: buf}
	b.u(8) // NAL header
	profile := b.u(8)
	b.u(8) // constraint flags + reserved
	b.u(8) // level_idc
	b.ue() // seq_parameter_set_id
	chroma := uint32(1)
	if highProfile(profile) {
		chroma = b.ue()
		if chroma == 3 {
			b.u(1)
		}
		b.ue()           // bit_depth_luma_minus8
		b.ue()           // bit_depth_chroma_minus8
		b.u(1)           // qpprime_y_zero_transform_bypass_flag
		if b.u(1) == 1 { // seq_scaling_matrix_present_flag
			n := 8
			if chroma == 3 {
				n = 12
			}
			for i := 0; i < n; i++ {
				if b.u(1) == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					lastScale, nextScale := 8, 8
					for j := 0; j < size; j++ {
						if nextScale != 0 {
							delta := b.se()
							nextScale = (lastScale + int(delta) + 256) % 256
						}
						if nextScale == 0 {
							// scalingList[j] = lastScale
						} else {
							lastScale = nextScale
						}
					}
				}
			}
		}
	}
	b.ue() // log2_max_frame_num_minus4
	pocType := b.ue()
	if pocType == 0 {
		b.ue()
	} else if pocType == 2 {
		// pic_order_cnt_type 2: tak ada field tambahan (valid, sering
		// dipakai encoder baseline/main).
	} else if pocType == 1 {
		b.u(1)
		b.se()
		b.se()
		n := b.ue()
		if n > 256 {
			b.err = true
		}
		for i := uint32(0); i < n; i++ {
			b.se()
		}
	} else {
		return -1
	}
	b.ue() // max_num_ref_frames
	b.u(1) // gaps_in_frame_num_value_allowed_flag
	b.ue() // pic_width_in_mbs_minus1
	b.ue() // pic_height_in_map_units_minus1
	if b.u(1) == 0 {
		b.u(1) // mb_adaptive_frame_field_flag
	}
	b.u(1)           // direct_8x8_inference_flag
	if b.u(1) == 1 { // frame_cropping_flag
		b.ue()
		b.ue()
		b.ue()
		b.ue()
	}
	if b.u(1) == 1 { // vui_parameters_present_flag
		parseVUI(b)
	}
	if b.err || b.u(1) != 1 { // rbsp_stop_one_bit
		return -1
	}
	if b.pos <= 0 || b.pos > len(buf) {
		return -1
	}
	return b.pos
}

// ppsValid: cek kasar bahwa buf (dimulai byte header PPS 0x68) bisa
// di-parse sebagai PPS (beberapa field awal + stop bit tercapai).
func ppsValid(buf []byte) bool {
	if len(buf) < 2 || buf[0]&0x1f != 8 {
		return false
	}
	b := &bitReader{data: buf}
	b.u(8)
	b.ue()      // pic_parameter_set_id
	b.ue()      // seq_parameter_set_id
	b.u(1)      // entropy_coding_mode_flag
	b.u(1)      // bottom_field_pic_order_in_frame_present_flag
	n := b.ue() // num_slice_groups_minus1
	if n > 0 {
		t := b.ue() // slice_group_map_type
		if t == 0 {
			for i := uint32(0); i <= n; i++ {
				b.ue()
			}
		} else if t == 2 {
			for i := uint32(0); i < n; i++ {
				b.ue()
				b.ue()
			}
		} else if t >= 3 && t <= 5 {
			b.u(1)
		} else if t == 6 {
			b.ue()
		} else {
			return false
		}
	}
	b.ue()
	b.ue()
	b.u(1)
	b.u(2)
	b.se()
	b.se()
	b.se()
	b.u(1)
	b.u(1)
	b.u(1)
	return !b.err
}

var scPrefix = []byte{0, 0, 0, 1}

// normalizeConfig: ubah blob config video menjadi Annex B yang benar:
// tiap NAL diawali start code. Masukan yang sudah Annex B dikembalikan
// apa adanya; blob mentah (SPS+PPS) dipecah lewat parse SPS.
func normalizeConfig(buf []byte) []byte {
	if len(buf) == 0 {
		return buf
	}
	if hasStartCode(buf) || (len(buf) >= 3 && buf[0] == 0 && buf[1] == 0 && buf[2] == 1) {
		return buf // sudah Annex B
	}
	if n := spsLength(buf); n > 0 && n < len(buf) && buf[n]&0x1f == 8 && ppsValid(buf[n:]) {
		out := append(append([]byte{}, scPrefix...), buf[:n]...)
		out = append(out, scPrefix...)
		out = append(out, buf[n:]...)
		return out
	}
	// Fallback: pindai header PPS 0x68 dari belakang (PPS pendek di ujung).
	for i := len(buf) - 1; i > 0; i-- {
		if buf[i] == 0x68 && len(buf)-i <= 64 && ppsValid(buf[i:]) {
			out := append(append([]byte{}, scPrefix...), buf[:i]...)
			out = append(out, scPrefix...)
			out = append(out, buf[i:]...)
			return out
		}
	}
	// Jalur terakhir (seharusnya tak terjadi): start code di depan saja.
	return append(append([]byte{}, scPrefix...), buf...)
}

// startsWithSPS: apakah frame Annex B dibuka oleh NAL SPS (tipe 7)?
// Dipakai agar keyframe tanpa config in-band tetap bisa berdiri sendiri.
func startsWithSPS(frame []byte) bool {
	if len(frame) >= 5 && frame[0] == 0 && frame[1] == 0 && frame[2] == 0 && frame[3] == 1 {
		return frame[4]&0x1f == 7
	}
	if len(frame) >= 4 && frame[0] == 0 && frame[1] == 0 && frame[2] == 1 {
		return frame[3]&0x1f == 7
	}
	return false
}
