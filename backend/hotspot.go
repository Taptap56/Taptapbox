package main

// Hotspot / tethering: bagikan koneksi VPN (tun0) ke klien hotspot.
// Port dari service.sh (ip rule + iptables FORWARD/DNAT + ip_forward stub)
// ke Go supaya tidak bergantung pada shell. Rule dipantau dan diterapkan
// ulang otomatis bila table tun0 berubah atau rule terhapus (mis. mihomo
// direstart).

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	hsTun          = "tun0"
	hsRtTables     = "/data/misc/net/rt_tables"
	hsStub         = "/dev/ip_forward_stub"
	hsIPFwd        = "/proc/sys/net/ipv4/ip_forward"
	hsDefaultTable = "2022" // table default auto_route mihomo bila tun0 tidak ada di rt_tables
	// hsDefaultIP: IP kunci default interface hotspot (bisa diedit user di Pengaturan).
	hsDefaultIP = "192.168.56.1"
)

// hsTunName: nama interface TUN mihomo. Config selalu meminta "tun0";
// bila tidak ada (mis. device gagal dibuat dengan nama itu), fallback ke
// nama default mihomo ("Meta").
func hsTunName() string {
	for _, iface := range []string{hsTun, "Meta"} {
		if _, err := os.Stat("/sys/class/net/" + iface); err == nil {
			return iface
		}
	}
	return hsTun
}

// pola nama interface AP/tethering (wlan0 = uplink WiFi, sengaja dikecualikan)
var hsAPGlobs = []string{"ap*", "swlan*", "rndis*", "ncm*", "wlan1", "wlan2", "bt-pan"}

var (
	hsMu      sync.Mutex
	hsStop    chan struct{}
	hsApplied string
)

func hsBin(name string) string {
	p := "/system/bin/" + name
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if lp, err := exec.LookPath(name); err == nil {
		return lp
	}
	return name
}

func hsRun(bin string, args ...string) error {
	return exec.Command(bin, args...).Run()
}

// hsReadTable membaca index routing table sebuah interface dari rt_tables.
func hsReadTable(iface string) string {
	f, err := os.Open(hsRtTables)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) >= 2 && fs[1] == iface {
			return fs[0]
		}
	}
	return ""
}

func hsTableFor() string {
	if t := hsReadTable(hsTunName()); t != "" {
		return t
	}
	return hsDefaultTable
}

func hsIPRules(table string) []string {
	return []string{
		"iif lo goto 6000 pref 5000",
		fmt.Sprintf("iif %s lookup main suppress_prefixlength 0 pref 5010", hsTunName()),
		fmt.Sprintf("iif %s goto 6000 pref 5020", hsTunName()),
		fmt.Sprintf("from 10.0.0.0/8 lookup %s pref 5030", table),
		fmt.Sprintf("from 172.16.0.0/12 lookup %s pref 5040", table),
		fmt.Sprintf("from 192.168.0.0/16 lookup %s pref 5050", table),
		"nop pref 6000",
	}
}

func hsIptRules() []string {
	return []string{
		fmt.Sprintf("FORWARD -s 10.0.0.0/8 -o %s -j ACCEPT", hsTunName()),
		fmt.Sprintf("FORWARD -s 172.16.0.0/12 -o %s -j ACCEPT", hsTunName()),
		fmt.Sprintf("FORWARD -s 192.168.0.0/16 -o %s -j ACCEPT", hsTunName()),
		fmt.Sprintf("FORWARD -i %s -j ACCEPT", hsTunName()),
		fmt.Sprintf("PREROUTING -t nat ! -i %s -s 10.0.0.0/8 -p udp --dport 53 -j DNAT --to 1.1.1.1", hsTunName()),
		fmt.Sprintf("PREROUTING -t nat ! -i %s -s 172.16.0.0/12 -p udp --dport 53 -j DNAT --to 1.1.1.1", hsTunName()),
		fmt.Sprintf("PREROUTING -t nat ! -i %s -s 192.168.0.0/16 -p udp --dport 53 -j DNAT --to 1.1.1.1", hsTunName()),
	}
}

// hsCleanup membuang semua rule hotspot (aman dipanggil berulang).
func hsCleanup(table string) {
	ipt, ip, ip6t := hsBin("iptables"), hsBin("ip"), hsBin("ip6tables")
	for _, r := range hsIptRules() {
		args := append([]string{"-D"}, strings.Fields(r)...)
		for i := 0; i < 4; i++ { // hapus duplikat bila ada
			if hsRun(ipt, args...) != nil {
				break
			}
		}
	}
	if table != "" {
		for _, r := range hsIPRules(table) {
			args := append([]string{"rule", "del"}, strings.Fields(r)...)
			for i := 0; i < 4; i++ {
				if hsRun(ip, args...) != nil {
					break
				}
			}
		}
	}
	for i := 0; i < 4; i++ {
		if hsRun(ip6t, "-D", "FORWARD", "-j", "REJECT", "--reject-with", "icmp6-no-route") != nil {
			break
		}
	}
}

// hsSetup menerapkan rule baru. Error dikembalikan bila ada rule yang gagal.
func hsSetup(table string) error {
	ipt, ip, ip6t := hsBin("iptables"), hsBin("ip"), hsBin("ip6tables")
	hsCleanup(table)
	var errs []string
	for _, r := range hsIptRules() {
		args := append([]string{"-I"}, strings.Fields(r)...)
		if out, err := exec.Command(ipt, args...).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("iptables %s: %s", r, strings.TrimSpace(string(out))))
		}
	}
	for _, r := range hsIPRules(table) {
		args := append([]string{"rule", "add"}, strings.Fields(r)...)
		if out, err := exec.Command(ip, args...).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("ip rule %s: %s", r, strings.TrimSpace(string(out))))
		}
	}
	// cegah kebocoran IPv6 dari klien hotspot (tun hanya IPv4)
	_ = hsRun(ip6t, "-I", "FORWARD", "-j", "REJECT", "--reject-with", "icmp6-no-route")
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func hsRulesPresent() bool {
	out, err := exec.Command(hsBin("ip"), "rule", "show").Output()
	if err != nil {
		return false
	}
	s := string(out)
	return strings.Contains(s, "5030:") && strings.Contains(s, "6000:")
}

func hsMounted(path string) bool {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		fs := strings.Fields(ln)
		if len(fs) > 4 && fs[4] == path {
			return true
		}
	}
	return false
}

// hsEnableForward menyalakan ip_forward dan mengunci nilainya dengan bind-mount
// file stub (agar Android tidak mengembalikannya ke 0).
func hsEnableForward() {
	_ = os.WriteFile(hsIPFwd, []byte("1"), 0644)
	if _, err := os.Stat(hsStub); err != nil {
		if f, e := os.OpenFile(hsStub, os.O_CREATE|os.O_RDWR, 0644); e == nil {
			f.Close()
		}
	}
	var st syscall.Stat_t
	if err := syscall.Stat(hsRtTables, &st); err == nil {
		_ = os.Chown(hsStub, int(st.Uid), int(st.Gid))
	}
	if out, err := exec.Command("ls", "-Z", hsRtTables).Output(); err == nil {
		fs := strings.Fields(string(out))
		if len(fs) > 0 && fs[0] != "?" {
			_ = exec.Command("chcon", fs[0], hsStub).Run()
		}
	}
	if !hsMounted(hsIPFwd) {
		_ = syscall.Mount(hsStub, hsIPFwd, "", syscall.MS_BIND, "")
	}
}

// hsParseCIDR menormalkan IP hotspot versi user: "192.168.56.1" -> "192.168.56.1/24",
// "192.168.56.1/24" dipakai apa adanya (IPv4 saja).
func hsParseCIDR(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if strings.Contains(s, "/") {
		ip, _, err := net.ParseCIDR(s)
		if err != nil || ip.To4() == nil {
			return "", false
		}
		return s, true
	}
	if ip := net.ParseIP(s); ip == nil || ip.To4() == nil {
		return "", false
	}
	return s + "/24", true
}

// hsAPIfaces: interface AP/tethering yang sedang UP (untuk kunci IP).
func hsAPIfaces() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, fi := range ifs {
		if fi.Flags&net.FlagUp == 0 {
			continue
		}
		name := fi.Name
		for _, g := range hsAPGlobs {
			ok := false
			if strings.HasSuffix(g, "*") {
				ok = strings.HasPrefix(name, strings.TrimSuffix(g, "*"))
			} else {
				ok = name == g
			}
			if ok {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// hsIfaceHasIP: cek cepat apakah iface sudah punya CIDR tersebut.
func hsIfaceHasIP(iface, cidr string) bool {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return false
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(ip) && ipn.Mask.String() == ipnet.Mask.String() {
			return true
		}
	}
	return false
}

var hsIPApplied []string // "iface/cidr" yang dipasang kunci IP
var hsIPCidr string      // CIDR kunci IP yang sedang aktif

// hsNoAPWarned: peringatan "tidak ada interface AP" sudah ditulis untuk
// kondisi ini (v1.0.35: tanpa ini, hsEnsureLockIP yang dipanggil berkala
// menulis peringatan yang sama tiap ~10 detik dan memenuhi batas 200 baris
// log per sesi, mengubur baris penting seperti error helper).
var hsNoAPWarned bool

// hsLockIPApply memasang IP kunci ke semua interface AP yang UP (idempoten).
// Mengembalikan daftar "iface/cidr" yang terpasang + daftar error (string).
func hsLockIPApply(cidr string) ([]string, []string) {
	ip := hsBin("ip")
	var applied, errs []string
	for _, iface := range hsAPIfaces() {
		if hsIfaceHasIP(iface, cidr) {
			applied = append(applied, iface+"/"+cidr)
			continue
		}
		// replace = tambah bila belum ada (idempoten, tanpa error "already exists")
		if out, err := exec.Command(ip, "address", "replace", cidr, "dev", iface).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("kunci IP %s dev %s: %s", cidr, iface, strings.TrimSpace(string(out))))
			continue
		}
		applied = append(applied, iface+"/"+cidr)
	}
	return applied, errs
}

// hsLockIPRemove melepas IP kunci yang pernah dipasang.
func hsLockIPRemove() {
	ip := hsBin("ip")
	for _, e := range hsIPApplied {
		fs := strings.SplitN(e, "/", 2)
		if len(fs) != 2 {
			continue
		}
		_ = exec.Command(ip, "address", "del", fs[1], "dev", fs[0]).Run()
	}
	hsIPApplied = nil
}

// hsEnsureLockIP memastikan IP kunci terpasang sesuai Settings.
// Harus dipanggil dengan hsMu dipegang.
func (a *App) hsEnsureLockIP() {
	st := a.cfg()
	cidr, ok := hsParseCIDR(st.HotspotIP)
	if !ok || !st.HotspotLockIP {
		if len(hsIPApplied) > 0 {
			hsLockIPRemove()
			hsIPCidr = ""
			a.logf("hotspot: kunci IP dilepas")
		}
		return
	}
	need := cidr != hsIPCidr || len(hsIPApplied) == 0
	if !need {
		for _, e := range hsIPApplied {
			fs := strings.SplitN(e, "/", 2)
			if len(fs) != 2 || !hsIfaceHasIP(fs[0], fs[1]) {
				need = true
				break
			}
		}
	}
	if !need {
		return
	}
	hsLockIPRemove()
	applied, errs := hsLockIPApply(cidr)
	hsIPApplied = applied
	hsIPCidr = cidr
	for _, e := range errs {
		a.logf("hotspot: %s", e)
	}
	if len(applied) > 0 {
		hsNoAPWarned = false
		a.logf("hotspot: IP dikunci %s pada %v", cidr, hsAPIfaces())
	} else if !hsNoAPWarned {
		// Sekali per kondisi hilang (bukan tiap tick): tulis lalu diam
		// sampai AP muncul lagi (yang me-reset flag di cabang atas).
		hsNoAPWarned = true
		a.logWarnf("hotspot: kunci IP %s: tidak ada interface AP yang UP", cidr)
	}
}

// hotspotSet mengaktifkan / mematikan fitur hotspot.
func (a *App) hotspotSet(on bool) error {
	hsMu.Lock()
	defer hsMu.Unlock()
	if hsStop != nil {
		close(hsStop)
		hsStop = nil
	}
	if !on {
		hsLockIPRemove()
		hsIPCidr = ""
		hsCleanup(hsApplied)
		hsCleanup(hsTableFor())
		hsApplied = ""
		if hsMounted(hsIPFwd) {
			_ = syscall.Unmount(hsIPFwd, 0)
		}
		a.logf("hotspot: dimatikan")
		return nil
	}
	hsEnableForward()
	table := hsTableFor()
	err := hsSetup(table)
	hsApplied = table
	a.hsEnsureLockIP()
	stop := make(chan struct{})
	hsStop = stop
	go a.hotspotMonitor(stop)
	if err != nil {
		a.logWarnf("hotspot: sebagian rule gagal: %v", err)
		return err
	}
	a.logf("hotspot: aktif (table %s)", table)
	return nil
}

// handleHotspotStatus: status fitur hotspot + kunci IP (untuk indikator di Pengaturan).
func (a *App) handleHotspotStatus(w http.ResponseWriter, r *http.Request) {
	st := a.cfg()
	cidr, _ := hsParseCIDR(st.HotspotIP)
	hsMu.Lock()
	applied := append([]string{}, hsIPApplied...)
	hsMu.Unlock()
	if applied == nil {
		applied = []string{}
	}
	ifaces := hsAPIfaces()
	if ifaces == nil {
		ifaces = []string{}
	}
	writeJSON(w, map[string]any{
		"active":    st.Hotspot,
		"lock_ip":   st.HotspotLockIP,
		"ip":        cidr,
		"ap_ifaces": ifaces,
		"applied":   applied,
	})
}

// hotspotMonitor: terapkan ulang rule bila table tun0 berubah / rule hilang.
func (a *App) hotspotMonitor(stop chan struct{}) {
	t := time.NewTicker(10 * time.Second) // cukup sering; tiap 3 dtk memboroskan baterai (spawn proses `ip`)
	defer t.Stop()
	lastErr := ""
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		hsMu.Lock()
		select {
		case <-stop:
			hsMu.Unlock()
			return
		default:
		}
		if !hsMounted(hsIPFwd) {
			hsEnableForward()
		}
		a.hsEnsureLockIP() // pasang ulang IP kunci bila interface AP baru muncul / IP hilang
		table := hsTableFor()
		if table != hsApplied || !hsRulesPresent() {
			hsCleanup(hsApplied)
			if err := hsSetup(table); err != nil {
				if err.Error() != lastErr {
					a.logWarnf("hotspot: gagal menerapkan ulang: %v", err)
					lastErr = err.Error()
				}
			} else {
				lastErr = ""
				a.logf("hotspot: rule diterapkan ulang (table %s)", table)
			}
			hsApplied = table
		}
		hsMu.Unlock()
	}
}
