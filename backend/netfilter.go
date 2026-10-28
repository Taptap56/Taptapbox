package main

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const nfChain = "TAPSLIM"
const nfFilterChain = "TAPSLIM_F"
const nfDNSChain = "TAPSLIM_DNS"

// mihomoDNSPort: port DNS bawaan mihomo (fake-ip). Dipakai untuk mode non-TUN
// (tproxy/redirect/mixed): UDP/53 aplikasi di-REDIRECT ke sini, BUKAN ke stub Go.
// Alasannya: tanpa DNS mihomo, aplikasi mendapat IP asli hasil resolve lokal
// (dan mihomo tak tahu domainnya) -> tiap domain baru (YouTube punya puluhan
// *.googlevideo.com) menunggu DNS dulu = "loading dulu". Dengan fake-ip jawaban
// instan dan domain asli diteruskan ke server proxy (sama seperti mode TUN).
const mihomoDNSPort = 1053

// dnsViaMihomo: true bila DNS aplikasi harus lewat DNS fake-ip mihomo
// (mode transparan non-TUN).
func dnsViaMihomo(st Settings) bool {
	if st.ActiveCore() != "clash" {
		return false // sing-box: DNS aplikasi lewat stub 127.0.0.1:5353 (pola TaptapBox)
	}
	if !st.TransparentProxy {
		return false
	}
	switch st.ProxyMode {
	case "tproxy", "iptables", "mixed":
		return true
	}
	return false
}

// TProxy (tanpa tun0): tangkap TCP+UDP transparan (pola sama dengan box_for_root).
//   - OUTPUT (trafik HP ini)  : MARK bit tinggi -> ip rule -> table lokal -> lo
//   - PREROUTING `-i lo`      : TPROXY ke inbound tproxy mihomo
//   - PREROUTING `-i <ap>`    : TPROXY klien hotspot (hanya bila fitur Hotspot aktif)
//
// PENTING: TPROXY TIDAK BOLEH dipasang tanpa `-i`. Paket balasan dari internet untuk
// koneksi biasa juga lewat mangle PREROUTING; TPROXY menjatuhkan paket yang soketnya
// bukan soket transparan -> seluruh internet mati. Mark juga harus di BIT TINGGI:
// Android menyimpan netId di bit 0-15, jadi mark 0x1 bertabrakan dengan jaringan ber-ID ganjil.
//
// Trafik root (uid 0, tempat mihomo jalan) di-skip agar tidak loop.
const tpChainPre = "TAPSLIM_TP"
const tpChainOut = "TAPSLIM_TM"
const tpChainDiv = "TAPSLIM_DV" // DIVERT: jalur cepat paket lanjutan koneksi TCP yang sudah ditangkap
const tpMarkVal = "0x1000000"
const tpMark = tpMarkVal + "/" + tpMarkVal // nilai/mask (bit 24)
const tpTable = "2024"
const tpPref = "100"

// nilai lama (v1.0.6 ke bawah) — hanya untuk dibersihkan
const tpLegacyMark = "0x1/0x1"
const tpLegacyTable = "100"

// Alamat yang tidak ditangkap (jaringan privat/lokal/multicast): langsung keluar.
var tpIntranet = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4",
}

// Nama interface hotspot/tethering (wildcard iptables). wlan0 sengaja TIDAK ada
// (itu uplink WiFi). Dipakai hanya bila Settings.Hotspot aktif.
var tpAPIfaces = []string{"ap+", "swlan+", "rndis+", "ncm+", "wlan1", "wlan2", "bt-pan"}

// localIPv4s: semua IPv4 milik HP ini (anti-loop, termasuk IP publik operator).
func localIPv4s() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, ad := range addrs {
		if ipn, ok := ad.(*net.IPNet); ok {
			if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
				out = append(out, ip4.String())
			}
		}
	}
	return out
}

// iptWait: pakai -w (tunggu xtables lock) hanya bila iptables perangkat mendukungnya;
// dideteksi sekali. Tanpa ini, dua pemanggilan iptables bersamaan bisa gagal acak.
var (
	iptWaitOnce  sync.Once
	iptWait      bool
	ip6tWaitOnce sync.Once
	ip6tWait     bool
)

func ipt(args ...string) error {
	iptWaitOnce.Do(func() {
		iptWait = exec.Command("iptables", "-w", "-t", "filter", "-S", "OUTPUT").Run() == nil
	})
	if iptWait {
		args = append([]string{"-w"}, args...)
	}
	return exec.Command("iptables", args...).Run()
}

// ip6t: ipt() versi IPv6.
func ip6t(args ...string) error {
	ip6tWaitOnce.Do(func() {
		ip6tWait = exec.Command("ip6tables", "-w", "-t", "filter", "-S", "OUTPUT").Run() == nil
	})
	if ip6tWait {
		args = append([]string{"-w"}, args...)
	}
	return exec.Command("ip6tables", args...).Run()
}

// applyIPv6Block: cegah bocor IPv6 saat VPN aktif (TaptapClash IPv4-only).
//   - OUTPUT: izinkan loopback (::1), REJECT sisanya kecuali dari root
//     (uid 0 = backend & mihomo, agar koneksi proksi tetap jalan).
//   - FORWARD: REJECT semua (klien hotspot/tether tak bisa bocor via IPv6).
//
// Pakai REJECT (bukan DROP) agar aplikasi LANGSUNG fallback ke IPv4 —
// DROP diam bikin aplikasi hang menunggu timeout (mis. video YouTube macet).
// Aplikasi otomatis fallback ke IPv4 yang lewat proxy (Happy Eyeballs).
func (a *App) applyIPv6Block() {
	if !a.store.Settings.BlockIPv6 && !hardenFull(a.store.Settings) {
		return
	}
	if _, err := exec.LookPath("ip6tables"); err != nil {
		a.logWarnf("blokir IPv6: ip6tables tidak ditemukan, dilewati")
		return
	}
	a.clearIPv6Block() // idempoten: bersihkan dulu agar tak ganda
	_ = ip6t("-I", "OUTPUT", "-o", "lo", "-j", "ACCEPT")
	_ = ip6t("-A", "OUTPUT", "-m", "owner", "!", "--uid-owner", "0", "-j", "REJECT")
	_ = ip6t("-I", "FORWARD", "-j", "REJECT")
	a.logf("blokir IPv6 aktif (anti-bocor)")
}

// clearIPv6Block: cabut semua rule blokir IPv6.
func (a *App) clearIPv6Block() {
	for {
		if err := ip6t("-D", "OUTPUT", "-o", "lo", "-j", "ACCEPT"); err != nil {
			break
		}
	}
	for {
		if err := ip6t("-D", "OUTPUT", "-m", "owner", "!", "--uid-owner", "0", "-j", "REJECT"); err != nil {
			break
		}
	}
	for {
		if err := ip6t("-D", "FORWARD", "-j", "REJECT"); err != nil {
			break
		}
	}
	// legacy: bersihkan sisa rule DROP dari build lama (sebelum REJECT)
	for {
		if err := ip6t("-D", "OUTPUT", "-m", "owner", "!", "--uid-owner", "0", "-j", "DROP"); err != nil {
			break
		}
	}
	for {
		if err := ip6t("-D", "FORWARD", "-j", "DROP"); err != nil {
			break
		}
	}
}

// applyTransparentProxy memasang:
//   - mode tun: mihomo yang atur routing via tun0, tidak perlu iptables.
//     Bersihkan sisa rule lama.
//   - mode tproxy: mangle TPROXY (PREROUTING) + MARK (OUTPUT) + ip rule/route.
//   - mode iptables: nat REDIRECT TCP 80,443 ;
//     trafik root (uid 0) di-skip agar tidak loop.
//   - DNS hijack (semua mode): nat REDIRECT UDP/53 -> stub 127.0.0.1:5353.
//   - Blok QUIC (semua mode): filter REJECT UDP/443 agar aplikasi fallback ke TCP.
//   - Blok IPv6 (semua mode): filter DROP IPv6 non-root (anti-bocor).
//   - mangle : TCPMSS clamp-to-pmtu untuk SYN keluar (anti MTU blackhole).
//
// DNS/QUIC/IPv6 di level netfilter sehingga user config manual pun kebagian.
//
// Sadar-eBPF: bila config yang diluncurkan memuat listener type ebpf,
// scaffolding mode di atas TIDAK dipasang (listener ebpf = datapath utama,
// lihat ebpfdetect.go); MSS/DNS/QUIC/IPv6 tetap dipasang.
func (a *App) applyTransparentProxy() error {
	st := a.store.Settings
	// Bersihkan semua mode dulu agar ganti mode tidak meninggalkan sisa rule.
	a.clearIptablesOnly()
	a.clearTproxy()
	// Sadar-eBPF: config yang diluncurkan memuat listener ebpf -> listener itu
	// datapath utamanya (posisi sing-box). Scaffolding penangkap modul tidak
	// dipasang sama sekali; fitur bantu di bawah tetap jalan seperti biasa.
	if a.store.Settings.ActiveCore() == "clash" && a.configHasEBPFListener() {
		a.logf("listener eBPF terdeteksi di config — datapath modul (mode %s) dilewati, eBPF jadi penangkap utama", st.ProxyMode)
		if st.Hotspot {
			a.logf("eBPF: klien hotspot dilayani plane shared listener (pastikan shared.enabled + interface AP di config)")
		}
	} else if err := a.applyDatapath(); err != nil {
		return err
	}
	// --- MSS clamp anti MTU blackhole ---
	_ = ipt("-t", "mangle", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN",
		"-j", "TCPMSS", "--clamp-mss-to-pmtu")
	// --- Fitur level netfilter (semua mode; user config manual pun kebagian) ---
	a.applyIPv6Block() // anti-bocor IPv6
	a.applyDNSHijack() // UDP/53 -> stub 127.0.0.1:5353
	a.applyQUICBlock() // REJECT UDP/443 (semua mode, ikut toggle Blokir QUIC)
	return nil
}

// applyDatapath memasang scaffolding penangkap sesuai mode (tproxy /
// iptables / mixed). Mode tun & enhance tidak memakai scaffolding —
// routing diatur core lewat tun0. Dipisah dari applyTransparentProxy
// agar jalur sadar-eBPF bisa melewatinya secara utuh.
func (a *App) applyDatapath() error {
	st := a.store.Settings
	switch st.ProxyMode {
	case "tproxy":
		if err := a.applyTproxy(); err != nil {
			return err
		}
	case "iptables": // label UI: Redirect
		if err := a.applyIptablesRedirect(); err != nil {
			return err
		}
	case "mixed":
		// TCP via REDIRECT + UDP via TPROXY (TCP aman di kernel lama,
		// UDP tetap native lewat TPROXY).
		if err := a.applyIptablesRedirect(); err != nil {
			return err
		}
		var aps []string
		if st.Hotspot {
			aps = tpAPIfaces
		}
		if err := a.applyTproxyOpts([]string{"udp"}, true, aps, true); err != nil {
			return err
		}
	default: // "tun" & "enhance": mihomo yang atur routing via tun0
	}
	return nil
}

// hardenFull: mode "enhance" memaksa proteksi penuh (DNS hijack, blokir QUIC,
// blokir IPv6) walau toggle-nya mati — "paket hardening" untuk mode TUN.
func hardenFull(st Settings) bool { return st.ProxyMode == "enhance" }

// applyTproxy memasang penangkapan transparan TCP+UDP penuh (mode tproxy).
func (a *App) applyTproxy() error {
	st := a.store.Settings
	var aps []string
	if st.Hotspot {
		aps = tpAPIfaces
	}
	return a.applyTproxyOpts([]string{"tcp", "udp"}, true, aps, true)
}

// applyTproxyOpts: varian TPROXY yang bisa diparameterisasi.
//
//	protos     = protokol yang ditangkap (["tcp","udp"] atau ["udp"])
//	lo         = tangkap juga trafik perangkat ini (via lo + OUTPUT)
//	aps        = interface tethering yang ditangkap di PREROUTING
//	withOutput = pasang chain OUTPUT (MARK); false = hanya PREROUTING
func (a *App) applyTproxyOpts(protos []string, lo bool, aps []string, withOutput bool) error {
	port := a.store.Settings.TproxyPort
	if port <= 0 {
		port = 15359
	}
	p := itoa(port)
	// tp: aturan TPROXY untuk satu interface (+ opsional satu dport)
	tp := func(iface, proto, dport string) []string {
		r := []string{"-t", "mangle", "-A", tpChainPre, "-i", iface, "-p", proto}
		if dport != "" {
			r = append(r, "--dport", dport)
		}
		return append(r, "-j", "TPROXY", "--on-port", p, "--tproxy-mark", tpMark)
	}
	ret := func(chain string, dst string) []string {
		return []string{"-t", "mangle", "-A", chain, "-d", dst, "-j", "RETURN"}
	}

	// --- chain PREROUTING ---
	_ = ipt("-t", "mangle", "-N", tpChainPre) // abaikan bila sudah ada
	_ = ipt("-t", "mangle", "-F", tpChainPre)
	// DIVERT (pola referensi TPROXY resmi kernel): paket lanjutan dari koneksi TCP yang
	// sudah dipegang mihomo (soket transparan) langsung diberi mark + ACCEPT, tanpa
	// melewati puluhan aturan RETURN/TPROXY lagi. Hanya paket pertama (SYN) yang
	// dievaluasi penuh. Best-effort: bila kernel tak punya xt_socket, dilewati saja.
	_ = ipt("-t", "mangle", "-N", tpChainDiv)
	_ = ipt("-t", "mangle", "-F", tpChainDiv)
	_ = ipt("-t", "mangle", "-A", tpChainDiv, "-j", "MARK", "--set-xmark", tpMark)
	_ = ipt("-t", "mangle", "-A", tpChainDiv, "-j", "ACCEPT")
	if err := ipt("-t", "mangle", "-A", tpChainPre, "-p", "tcp", "-m", "socket", "--transparent",
		"-j", tpChainDiv); err != nil {
		a.logWarnf("tproxy: DIVERT (socket match) tidak didukung kernel, dilewati (%v)", err)
	}
	// DNS klien hotspot ditangkap lebih dulu (tujuannya IP HP ini -> kalau tidak, kena bypass LOCAL)
	for _, ap := range aps {
		_ = ipt(tp(ap, "udp", "53")...)
		_ = ipt(tp(ap, "tcp", "53")...)
	}
	// trafik ke HP ini sendiri / jaringan privat tidak dibelokkan.
	// Paket yang SUDAH ditandai (trafik HP ini via OUTPUT) dikecualikan dari
	// cek addrtype: dengan policy routing "local 0.0.0.0/0", lookup addrtype
	// bisa menganggap SEMUA tujuan sebagai LOCAL (tergantung kernel) sehingga
	// TPROXY tak pernah tercapai -> internet mati total. Daftar tpIntranet &
	// localIPv4s di bawah tetap melindungi paket bertanda; aturan ini tinggal
	// berlaku untuk paket tak bertanda (mis. klien hotspot).
	_ = ipt("-t", "mangle", "-A", tpChainPre, "-m", "mark", "!", "--mark", tpMark,
		"-m", "addrtype", "--dst-type", "LOCAL", "-j", "RETURN") // best-effort
	for _, lip := range localIPv4s() {
		_ = ipt(ret(tpChainPre, lip+"/32")...)
	}
	for _, n := range tpIntranet {
		if err := ipt(ret(tpChainPre, n)...); err != nil {
			return fmt.Errorf("tproxy PREROUTING: %v", err)
		}
	}
	// TPROXY untuk paket dari lo (trafik HP ini yang di-mark) dan/atau interface hotspot
	if lo {
		for _, proto := range protos {
			if err := ipt(tp("lo", proto, "")...); err != nil {
				return fmt.Errorf("tproxy PREROUTING: %v", err)
			}
		}
	}
	for _, ap := range aps {
		for _, proto := range protos {
			_ = ipt(tp(ap, proto, "")...)
		}
	}

	// --- chain OUTPUT: MARK (TPROXY tidak valid di OUTPUT) ---
	_ = ipt("-t", "mangle", "-N", tpChainOut)
	_ = ipt("-t", "mangle", "-F", tpChainOut)
	if withOutput {
		if err := ipt("-t", "mangle", "-A", tpChainOut, "-m", "owner", "--uid-owner", "0", "-j", "RETURN"); err != nil {
			return fmt.Errorf("tproxy OUTPUT: %v", err)
		}
		_ = ipt("-t", "mangle", "-A", tpChainOut, "-m", "addrtype", "--dst-type", "LOCAL", "-j", "RETURN") // best-effort
		for _, lip := range localIPv4s() {
			_ = ipt(ret(tpChainOut, lip+"/32")...)
		}
		for _, n := range tpIntranet {
			if err := ipt(ret(tpChainOut, n)...); err != nil {
				return fmt.Errorf("tproxy OUTPUT: %v", err)
			}
		}
		for _, proto := range protos {
			// set-xmark: hanya mengubah bit mark kita, bit Android (netId dll) tidak ditimpa
			if err := ipt("-t", "mangle", "-A", tpChainOut, "-p", proto, "-j", "MARK", "--set-xmark", tpMark); err != nil {
				return fmt.Errorf("tproxy OUTPUT: %v", err)
			}
		}
	}
	// kaitkan ke PREROUTING (& OUTPUT bila dipakai), paling atas, hapus kaitan lama dulu
	hooks := [][2]string{{"PREROUTING", tpChainPre}}
	if withOutput {
		hooks = append(hooks, [2]string{"OUTPUT", tpChainOut})
	}
	for _, jc := range hooks {
		for {
			if err := ipt("-t", "mangle", "-D", jc[0], "-j", jc[1]); err != nil {
				break
			}
		}
		if err := ipt("-t", "mangle", "-I", jc[0], "1", "-j", jc[1]); err != nil {
			return fmt.Errorf("tproxy hook %s: %v", jc[0], err)
		}
	}
	// --- policy routing: paket bertanda -> antar lokal ke socket tproxy ---
	a.tproxyRoutingClear()
	ip := hsBin("ip")
	if out, err := exec.Command(ip, "rule", "add", "fwmark", tpMark, "table", tpTable, "pref", tpPref).CombinedOutput(); err != nil {
		return fmt.Errorf("tproxy ip rule: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command(ip, "route", "add", "local", "0.0.0.0/0", "dev", "lo", "table", tpTable).CombinedOutput(); err != nil {
		return fmt.Errorf("tproxy ip route: %v: %s", err, strings.TrimSpace(string(out)))
	}
	a.logf("tproxy aktif (port %s, fwmark %s table %s pref %s, hotspot=%v)", p, tpMark, tpTable, tpPref, a.store.Settings.Hotspot)
	return nil
}

// tproxyRoutingClear: hapus ip rule/route tproxy (nilai baru + nilai lama v1.0.6-).
func (a *App) tproxyRoutingClear() {
	ip := hsBin("ip")
	_ = exec.Command(ip, "rule", "del", "fwmark", tpMark, "table", tpTable, "pref", tpPref).Run()
	_ = exec.Command(ip, "route", "del", "local", "0.0.0.0/0", "dev", "lo", "table", tpTable).Run()
	_ = exec.Command(ip, "rule", "del", "fwmark", tpLegacyMark, "table", tpLegacyTable).Run()
	_ = exec.Command(ip, "route", "del", "local", "0.0.0.0/0", "dev", "lo", "table", tpLegacyTable).Run()
}

// clearTproxy membersihkan chain mangle + ip rule/route mode tproxy.
func (a *App) clearTproxy() {
	a.tproxyRoutingClear()
	for _, jc := range [][2]string{{"PREROUTING", tpChainPre}, {"OUTPUT", tpChainOut}} {
		for {
			if err := ipt("-t", "mangle", "-D", jc[0], "-j", jc[1]); err != nil {
				break
			}
		}
		_ = ipt("-t", "mangle", "-F", jc[1])
		_ = ipt("-t", "mangle", "-X", jc[1])
	}
	// chain DIVERT baru bisa dihapus setelah chain PREROUTING (yang merujuknya) hilang
	_ = ipt("-t", "mangle", "-F", tpChainDiv)
	_ = ipt("-t", "mangle", "-X", tpChainDiv)
}

func (a *App) applyIptablesRedirect() error {
	st := a.store.Settings
	rport := itoa(st.RedirectPort)
	ports := strings.TrimSpace(st.TcpPorts)
	if ports == "" {
		ports = "80,443"
	}
	_ = ipt("-t", "nat", "-N", nfChain) // abaikan bila sudah ada
	_ = ipt("-t", "nat", "-F", nfChain)
	rules := [][]string{
		{"-t", "nat", "-A", nfChain, "-m", "owner", "--uid-owner", "0", "-j", "RETURN"},
		{"-t", "nat", "-A", nfChain, "-d", "127.0.0.0/8", "-j", "RETURN"},
		{"-t", "nat", "-A", nfChain, "-p", "tcp", "-m", "multiport", "--dports", ports, "-j", "REDIRECT", "--to-ports", rport},
	}
	for _, r := range rules {
		if err := ipt(r...); err != nil {
			return err
		}
	}
	// sisipkan di paling atas agar dievaluasi duluan
	for _, ch := range []string{"OUTPUT", "PREROUTING"} {
		for {
			if err := ipt("-t", "nat", "-D", ch, "-p", "tcp", "-j", nfChain); err != nil {
				break
			}
		}
		if err := ipt("-t", "nat", "-I", ch, "1", "-p", "tcp", "-j", nfChain); err != nil {
			return err
		}
		// bersihkan sisa jump UDP lama (DNS kini chain tersendiri)
		for {
			if err := ipt("-t", "nat", "-D", ch, "-p", "udp", "-j", nfChain); err != nil {
				break
			}
		}
	}

	return nil
}

// applyDNSHijack: REDIRECT UDP/53 ke stub DNS 127.0.0.1:5353 di level netfilter.
// Berlaku di SEMUA mode (tun/tproxy/iptables) sehingga user config manual pun
// kebagian — tak tergantung rule mihomo. Root (uid 0) & 127/8 dikecualikan
// agar backend/mihomo tetap bisa resolve langsung.
func (a *App) applyDNSHijack() {
	a.clearDNSHijack()
	st := a.store.Settings
	if !st.DNSHijack && !hardenFull(st) {
		return
	}
	// Tujuan REDIRECT: mode non-TUN -> DNS fake-ip mihomo (bila sudah siap);
	// selain itu (atau mihomo DNS belum siap) -> stub Go 127.0.0.1:5353.
	// Rule hanya dipasang bila tujuannya benar-benar menjawab (kalau tidak, DNS mati).
	target := 0
	if dnsViaMihomo(st) {
		for i := 0; i < 10; i++ { // mihomo baru start: beri waktu DNS-nya listen
			if mihomoDNSReady() {
				target = mihomoDNSPort
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if target == 0 {
			a.logWarnf("dns hijack: DNS mihomo (:%d) belum menjawab, pakai stub", mihomoDNSPort)
		}
	}
	if target == 0 {
		if !dnsStubReady() {
			a.logErrf("dns hijack dilewati: stub DNS tidak aktif")
			return
		}
		target = dnsStubPort
	}
	_ = ipt("-t", "nat", "-N", nfDNSChain) // abaikan bila sudah ada
	_ = ipt("-t", "nat", "-F", nfDNSChain)
	_ = ipt("-t", "nat", "-A", nfDNSChain, "-m", "owner", "--uid-owner", "0", "-j", "RETURN")
	_ = ipt("-t", "nat", "-A", nfDNSChain, "-d", "127.0.0.0/8", "-j", "RETURN")
	_ = ipt("-t", "nat", "-A", nfDNSChain, "-p", "udp", "--dport", "53", "-j", "REDIRECT", "--to-ports", itoa(target))
	// OUTPUT (lokal) & PREROUTING (klien hotspot) — sisipkan paling atas
	for _, ch := range []string{"OUTPUT", "PREROUTING"} {
		_ = ipt("-t", "nat", "-I", ch, "1", "-p", "udp", "--dport", "53", "-j", nfDNSChain)
	}
	a.logf("dns hijack aktif (UDP/53 -> 127.0.0.1:%d)", target)
}

// clearDNSHijack: cabut REDIRECT DNS level netfilter.
func (a *App) clearDNSHijack() {
	for _, ch := range []string{"OUTPUT", "PREROUTING"} {
		for {
			if err := ipt("-t", "nat", "-D", ch, "-p", "udp", "--dport", "53", "-j", nfDNSChain); err != nil {
				break
			}
		}
	}
	_ = ipt("-t", "nat", "-F", nfDNSChain)
	_ = ipt("-t", "nat", "-X", nfDNSChain)
}

// applyQUICBlock: REJECT UDP/443 di level netfilter — berlaku di SEMUA mode
// (tun, iptables, tproxy) selama "Blokir QUIC" aktif. QUIC lewat proxy berbasis
// TCP (ws/grpc/trojan, dll) lambat & tersendat (UDP dibungkus TCP); dengan REJECT
// aplikasi langsung fallback ke HTTP/2 lewat TCP. Matikan toggle bila server
// proxy mendukung UDP native (hy2/tuic) dan QUIC ingin tetap dipakai.
// Root dikecualikan agar hy2/tuic tetap jalan. Manual-safe.
// IPv6 juga diblokir (QUIC jalan via IPv6 juga; kalau tidak, YouTube lolos
// lewat QUIC IPv6 langsung).
func (a *App) applyQUICBlock() {
	a.clearQUICBlock()
	st := a.store.Settings
	if !st.BlockQUIC && !hardenFull(st) {
		return
	}
	// IPv4
	_ = ipt("-t", "filter", "-N", nfFilterChain)
	_ = ipt("-t", "filter", "-F", nfFilterChain)
	_ = ipt("-t", "filter", "-A", nfFilterChain, "-m", "owner", "--uid-owner", "0", "-j", "RETURN")
	// best-effort: beberapa kernel tidak punya REJECT, abaikan bila gagal
	if err := ipt("-t", "filter", "-A", nfFilterChain, "-p", "udp", "--dport", "443",
		"-j", "REJECT", "--reject-with", "icmp-port-unreachable"); err != nil {
		a.logWarnf("quic block: REJECT tidak didukung kernel, lewati (%v)", err)
	} else {
		_ = ipt("-t", "filter", "-I", "OUTPUT", "1", "-j", nfFilterChain)
	}
	// IPv6 (QUIC via IPv6; root dikecualikan)
	if _, err := exec.LookPath("ip6tables"); err == nil {
		_ = ip6t("-I", "OUTPUT", "1", "-m", "owner", "--uid-owner", "0", "-j", "RETURN")
		_ = ip6t("-A", "OUTPUT", "-p", "udp", "--dport", "443", "-j", "REJECT")
	}
	a.logf("blok QUIC aktif (UDP/443)")
}

func (a *App) clearQUICBlock() {
	for {
		if err := ipt("-t", "filter", "-D", "OUTPUT", "-j", nfFilterChain); err != nil {
			break
		}
	}
	_ = ipt("-t", "filter", "-F", nfFilterChain)
	_ = ipt("-t", "filter", "-X", nfFilterChain)
	// IPv6 (direct rules)
	for {
		if err := ip6t("-D", "OUTPUT", "-m", "owner", "--uid-owner", "0", "-j", "RETURN"); err != nil {
			break
		}
	}
	for {
		if err := ip6t("-D", "OUTPUT", "-p", "udp", "--dport", "443", "-j", "REJECT"); err != nil {
			break
		}
	}
}

// clearIptablesOnly membersihkan chain nat/filter tanpa menyentuh mangle.
func (a *App) clearIptablesOnly() {
	for _, ch := range []string{"OUTPUT", "PREROUTING"} {
		for {
			if err := ipt("-t", "nat", "-D", ch, "-p", "tcp", "-j", nfChain); err != nil {
				break
			}
		}
		for {
			if err := ipt("-t", "nat", "-D", ch, "-p", "udp", "-j", nfChain); err != nil {
				break
			}
		}
	}
	_ = ipt("-t", "nat", "-F", nfChain)
	_ = ipt("-t", "nat", "-X", nfChain)
	a.clearQUICBlock()
}

func (a *App) clearTransparentProxy() {
	a.clearIptablesOnly()
	a.clearTproxy()
	a.clearIPv6Block()
	a.clearDNSHijack()
	for {
		if err := ipt("-t", "mangle", "-D", "OUTPUT", "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN",
			"-j", "TCPMSS", "--clamp-mss-to-pmtu"); err != nil {
			break
		}
	}
}

// reapplyNetfilter pasang ulang iptables sesuai settings terbaru.
// Dipanggil setelah ganti pengaturan tanpa restart mihomo.
func (a *App) reapplyNetfilter() {
	a.clearTransparentProxy()
	if a.store.Settings.TransparentProxy && a.mihomoAlive() {
		if err := a.applyTransparentProxy(); err != nil {
			a.logf("reapply iptables gagal: %v", err)
		}
	}
}
