// ebpfdetect.go — backend sadar-eBPF: bila config yang diluncurkan memuat
// listener "type: ebpf" (core mihomo build with_ebpf + kit listener), listener
// itulah datapath penangkapan utama — persis posisi inbound ebpf sebagai warga
// config di sing-box. Backend TIDAK memasang scaffolding datapath-nya sendiri
// (rantai TPROXY + marking fwmark + ip rule, atau rantai REDIRECT) karena dua
// mesin penangkap untuk trafik yang sama saling berebut dan mematikan jalur
// eBPF (terbukti di lapangan 2026-10-22: mode tproxy + listener ebpf = hanya
// Telegram & thumbnail yang lolos; masing-masing sendirian jalan penuh).
// Fitur bantu level netfilter (MSS clamp, blokir IPv6, DNS hijack, blok QUIC)
// TETAP dipasang — di TaptapBox fitur yang sama persis menyala berdampingan
// dengan inbound ebpf tanpa masalah, jadi bukan mereka pembedanya.
package main

import (
	"os"
	"strings"
)

// yamlHasEBPFListener melaporkan apakah teks config YAML memuat entri
// listeners dengan type: ebpf. Pemindaian dibatasi seksi listeners: agar
// kata "ebpf" di tempat lain (nama proxy/grup, komentar) tidak memicu.
// Gaya flow ("- {name: x, type: ebpf}") ikut dikenali.
func yamlHasEBPFListener(data []byte) bool {
	inListeners := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Kunci top-level baru (kolom 0, bukan item "- ") menutup seksi.
		if line[0] != ' ' && line[0] != '\t' && line[0] != '-' {
			inListeners = strings.HasPrefix(trimmed, "listeners:")
			continue
		}
		if !inListeners {
			continue
		}
		if isEBPFTypeLine(trimmed) {
			return true
		}
	}
	return false
}

// isEBPFTypeLine: baris (sudah di-trim) menyatakan type: ebpf — bentuk blok
// ("type: ebpf") maupun di dalam flow map ("{name: ebpf-in, type: ebpf, ...}").
func isEBPFTypeLine(trimmed string) bool {
	idx := strings.Index(trimmed, "type:")
	if idx < 0 {
		return false
	}
	rest := strings.TrimSpace(trimmed[idx+len("type:"):])
	rest = strings.Trim(rest, `"'`)
	// Nilai berakhir di spasi/koma/penutup flow/komentar.
	if i := strings.IndexAny(rest, " ,}#"); i >= 0 {
		rest = rest[:i]
	}
	return rest == "ebpf"
}

// configHasEBPFListener membaca file config yang akan/sedang diluncurkan
// (hasil writeConfig: override tersalin / generate / manual — semuanya
// berakhir di activeConfigPath) dan memeriksa listener ebpf di dalamnya.
func (a *App) configHasEBPFListener() bool {
	data, err := os.ReadFile(a.activeConfigPath())
	if err != nil {
		return false
	}
	return yamlHasEBPFListener(data)
}

// effectiveDatapath menentukan mesin penangkap yang benar-benar dipasang
// backend untuk sebuah mode: listener ebpf selalu menang atas scaffolding
// modul (tproxy/iptables/mixed), mode tun/enhance memang tanpa scaffolding.
func effectiveDatapath(mode string, ebpf bool) string {
	if ebpf {
		return "ebpf"
	}
	switch mode {
	case "tproxy", "iptables", "mixed":
		return mode
	}
	return "none" // tun & enhance: routing diatur core (tun0)
}
