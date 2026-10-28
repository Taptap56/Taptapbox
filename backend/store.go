package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Server adalah satu akun proxy ala v2rayNG.
type Server struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Proto    string            `json:"proto"` // vmess,vless,trojan,ss,hy2,tuic,wireguard,socks,http
	Link     string            `json:"link"`
	Enabled  bool              `json:"enabled"`
	Params   map[string]string `json:"params"`
	SubID    string            `json:"sub_id,omitempty"`
	DelayMs  int               `json:"delay_ms"` // -2 belum dites, -1 gagal/timeout
	TestedAt int64             `json:"tested_at"`
}

// Subscription = langganan ala v2rayNG.
type Subscription struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt int64  `json:"updated_at"`
	Count     int    `json:"count"`
}

type Settings struct {
	Ver              int    `json:"ver"`
	Mode             string `json:"mode"`           // v2rayng | clash
	Core             string `json:"core,omitempty"` // singbox | clash (kosong = clash)
	SelectedID       string `json:"selected_id"`
	AutoStart        bool   `json:"auto_start"`
	TransparentProxy bool   `json:"transparent_proxy"`
	TcpPorts         string `json:"tcp_ports"`
	WebPort          int    `json:"web_port"`
	WebListenAll     bool   `json:"web_listen_all"` // true = WebUI bisa dibuka dari HP lain di LAN (bind 0.0.0.0)
	RedirectPort     int    `json:"redirect_port"`
	MixedPort        int    `json:"mixed_port"`
	ClashPort        int    `json:"clash_port"`
	Secret           string `json:"secret"`
	AutoTest         bool   `json:"auto_test"`
	DnsRemote        string `json:"dns_remote"`
	DnsDirect        string `json:"dns_direct"`
	DNSHijack        bool   `json:"dns_hijack"`
	DNSUpstream      string `json:"dns_upstream"`
	BlockQUIC        bool   `json:"block_quic"`
	BadvpnEnabled    bool   `json:"badvpn_enabled"`    // UDP lewat SSH via BadVPN udpgw (hanya server SSH, mode OTOMATIS)
	BadvpnUdpgwPort  int    `json:"badvpn_udpgw_port"` // port udpgw di server SSH (0 = 7300)
	BlockIPv6        bool   `json:"block_ipv6"`        // blokir IPv6 saat VPN aktif (anti-bocor; TaptapClash IPv4-only)
	BypassDomains    string `json:"bypass_domains"`
	ProxyMode        string `json:"proxy_mode"` // tun | tproxy | iptables | mixed | enhance
	TproxyPort       int    `json:"tproxy_port"`
	TunMTU           int    `json:"tun_mtu"`
	ExcludePackages  string `json:"exclude_packages"`
	ManualConfig     bool   `json:"manual_config"` // true = pakai json/config.json apa adanya
	BlockAds         bool   `json:"block_ads"`
	BypassPrivate    bool   `json:"bypass_private"` // GEOIP,private -> DIRECT (geoip.dat bundle)
	HealthURL        string `json:"health_url"`
	Hotspot          bool   `json:"hotspot"`
	HotspotLockIP    bool   `json:"hotspot_lock_ip"`           // kunci IP interface hotspot ke HotspotIP
	HotspotIP        string `json:"hotspot_ip"`                // IP hotspot (default 192.168.56.1)
	MainGroupName    string `json:"main_group_name,omitempty"` // nama grup utama (default "Taptap"), bisa diubah user
	FtpEnabled       bool   `json:"ftp_enabled"`               // FTP server untuk berbagi file via hotspot
	FtpPort          int    `json:"ftp_port"`
	FtpRoot          string `json:"ftp_root"`
	FtpWrite         bool   `json:"ftp_write"`
	AnimasiBg        int    `json:"animasi_bg"`    // nomor animasi sbg background WebUI (0 = mati)
	AnimasiAlpha     int    `json:"animasi_alpha"` // transparansi kartu 0-100 (default 55)
	AnimasiBlur      int    `json:"animasi_blur"`  // blur kartu px 0-20 (default 8)
	LogLevel         string `json:"log_level"`     // level log mihomo: trace|debug|info|warn|error (default warn)
	WebPass          string `json:"web_pass"`      // password WebUI untuk akses dari jaringan (user "admin"); koneksi 127.0.0.1 tanpa password
}

// ProxyProvider: sumber proxy untuk mode clash. Backend me-resolve
// ini menjadi outbound inline mihomo saat config di-generate.
type RuleSet struct {
	ID     string `json:"id"`
	Tag    string `json:"tag"`
	Type   string `json:"type"`   // remote | local
	Format string `json:"format"` // binary | source
	URL    string `json:"url,omitempty"`
	Path   string `json:"path,omitempty"`
	Detour string `json:"detour,omitempty"`
	// Behavior: jenis isi provider file mihomo (domain | ipcidr).
	// Hanya dipakai entri local di core mihomo; kosong = domain.
	Behavior string `json:"behavior,omitempty"`
}

type RouteRule struct {
	ID           string   `json:"id"`
	RuleSets     []string `json:"rule_sets,omitempty"`
	DomainSuffix []string `json:"domain_suffix,omitempty"`
	IPCIDR       []string `json:"ip_cidr,omitempty"`
	Port         []int    `json:"port,omitempty"`
	Protocol     []string `json:"protocol,omitempty"`
	PackageNames []string `json:"package_names,omitempty"` // per-app routing (Android, mode TUN)
	GeoIP        []string `json:"geoip,omitempty"`         // kode negara, mis. ["id"] (dipakai generator sing-box; mihomo memakai GEOIP di aturan teks)
	GeoSite      []string `json:"geosite,omitempty"`       // kategori, mis. ["category-ads-all"] (dipakai generator sing-box)
	Outbound     string   `json:"outbound"`
	Disabled     bool     `json:"disabled,omitempty"`
}

// Builder: routing rules custom untuk mode v2rayNG.
// (Mode clash dihapus di v2.1.0; builder kini hanya mengatur rules.)
type Builder struct {
	RuleSets []RuleSet   `json:"rule_sets"`
	Rules    []RouteRule `json:"rules"`
	// Grup proxy custom (selector / urltest).
	// (Field Final dihapus: baris MATCH akhir selalu grup utama.)
	Groups []ProxyGroup `json:"groups"`
	// Toggle aturan bawaan (false = aktif seperti biasa)
	NoSniff         bool `json:"no_sniff,omitempty"`
	NoHijackDNS     bool `json:"no_hijack_dns,omitempty"`
	NoPrivateDirect bool `json:"no_private_direct,omitempty"`
}

type Store struct {
	mu       sync.RWMutex
	dir      string
	Servers  []Server       `json:"servers"`
	Subs     []Subscription `json:"subscriptions"`
	Settings Settings       `json:"settings"`
	Builder  Builder        `json:"builder"`
}

func defaultSettings() Settings {
	return Settings{
		Ver:              17,
		Mode:             "v2rayng",
		AutoStart:        true,
		TransparentProxy: true,
		TcpPorts:         "80,443",
		WebPort:          5056,
		WebPass:          "12345",
		WebListenAll:     true,
		RedirectPort:     12450,
		MixedPort:        10809,
		ClashPort:        9090,
		Secret:           "",
		AutoTest:         true,
		DnsRemote:        "https://1.1.1.1/dns-query",
		DnsDirect:        "8.8.8.8",
		DNSHijack:        true,
		DNSUpstream:      "8.8.8.8",
		BlockQUIC:        true,
		BlockIPv6:        true,
		BypassDomains:    "",
		ProxyMode:        "tun",
		TproxyPort:       15359,
		TunMTU:           9000,
		ExcludePackages:  "",
		BlockAds:         false,
		BypassPrivate:    true,
		HealthURL:        healthDefault,
		MainGroupName:    "Taptap",
		HotspotLockIP:    true,
		HotspotIP:        "192.168.56.1",
		FtpEnabled:       false,
		FtpPort:          2121,
		FtpRoot:          "/",
		FtpWrite:         true,
		LogLevel:         "warn",
	}
}

func defaultBuilder() Builder {
	return Builder{
		RuleSets: []RuleSet{},
		Rules: []RouteRule{
			// contoh: blokir iklan via rule-set publik (nonaktif sampai URL diisi)
			{ID: "r1", DomainSuffix: []string{"ads.example.com"}, Outbound: "REJECT"},
		},
	}
}

func NewStore(dir string) *Store {
	s := &Store{dir: dir, Settings: defaultSettings(), Builder: defaultBuilder()}
	_ = os.MkdirAll(dir, 0755)
	s.load("servers.json", &s.Servers)
	// Segarkan link SSH dari Params saat load (2026-10-27, lanjutan
	// perbaikan copy SSH): link lama yang tersimpan di servers.json
	// dibuat sebelum buildSSHLink membawa ssh_*, dan Bagikan/Export
	// memakai link tersimpan itu — jadi tanpa segarkan ini, copy
	// server lama tetap kehilangan payload walau build baru sudah
	// dipasang, kecuali servernya diedit & disimpan ulang dulu.
	// Hanya SSH yang disegarkan: untuk protokol lain, link asli
	// dipertahankan agar field yang tidak diparse tidak ikut hilang.
	for i := range s.Servers {
		if s.Servers[i].Proto == "ssh" {
			if nl := buildLink("ssh", s.Servers[i].Params, s.Servers[i].Name); nl != "" {
				s.Servers[i].Link = nl
			}
		}
	}
	s.load("subscriptions.json", &s.Subs)
	// Jangan pernah biarkan nil: API harus mengirim [] bukan null (WebUI mengasumsikan array).
	if s.Servers == nil {
		s.Servers = []Server{}
	}
	if s.Subs == nil {
		s.Subs = []Subscription{}
	}
	var st Settings
	if s.load("settings.json", &st) {
		// merge dengan default agar field baru tidak nol
		d := defaultSettings()
		if st.Ver < 2 {
			// migrasi v1 -> v2: fitur baru default ON
			st.DNSHijack = true
			st.DNSUpstream = d.DNSUpstream
			st.BlockQUIC = true
		}
		if st.Ver < 3 {
			st.BypassDomains = ""
			st.Ver = 3
		}
		if st.Ver < 4 {
			// migrasi ke TUN (ala TaptapClash) sebagai default
			st.ProxyMode = "tun"
			st.TunMTU = 9000
			st.ExcludePackages = ""
			st.Ver = 4
		}
		if st.Ver < 5 {
			// secret default dikosongkan agar zashboard langsung konek
			// tanpa password (API hanya listen di 127.0.0.1).
			// Secret custom milik user tidak disentuh.
			if st.Secret == "singbox-go" {
				st.Secret = ""
			}
			st.Ver = 5
		}
		if st.Ver < 6 {
			st.BlockAds = false
			st.Ver = 6
		}
		if st.Ver < 7 {
			// v1.4.0: grup utama punya nama yang bisa diubah user
			st.MainGroupName = "Taptap"
			st.Ver = 7
		}
		if st.Ver < 8 {
			// v1.4.6: fitur FTP server (berbagi file via hotspot)
			st.FtpPort = 2121
			st.FtpRoot = "/"
			st.FtpWrite = true
			st.Ver = 8
		}
		if st.Ver < 9 {
			// v1.4.9: background animasi WebUI (0 = mati)
			st.AnimasiBg = 0
			st.Ver = 9
		}
		if st.Ver < 10 {
			// v1.4.12: atur transparansi & blur kartu saat background animasi aktif
			st.AnimasiAlpha = 55
			st.AnimasiBlur = 8
			st.Ver = 10
		}
		if st.Ver < 11 {
			// v1.0.1: port WebUI pindah ke 5056 (hanya bila masih default lama 5050,
			// port pilihan user sendiri tidak disentuh) + WebUI bisa dibuka dari
			// HP lain di LAN secara default
			if st.WebPort == 5050 {
				st.WebPort = 5056
			}
			st.WebListenAll = true
			st.Ver = 11
		}
		if st.Ver < 12 {
			// v1.0.7: level log mihomo bisa diatur; default warn (hemat I/O & CPU)
			st.LogLevel = "warn"
			st.Ver = 12
		}
		if st.Ver < 13 {
			// kunci IP hotspot: default 192.168.56.1, bisa diedit user
			st.HotspotLockIP = true
			st.HotspotIP = "192.168.56.1"
			st.Ver = 13
		}
		if st.Ver < 14 {
			// blokir IPv6 anti-bocor: default ON (TaptapClash IPv4-only)
			st.BlockIPv6 = true
			st.Ver = 14
		}
		if st.Ver < 15 {
			// (TaptapBox: langkah versi dipertahankan, port TIDAK
			// dipindah — default gabungan kembali ke 5056)
			st.Ver = 15
		}
		if st.Ver < 16 {
			// bypass IP private via geoip.dat bundle: default ON
			st.BypassPrivate = true
			st.Ver = 16
		}
		if st.Ver < 17 {
			// mode eBPF & hybrid dihapus: migrasi ke tproxy
			if st.ProxyMode == "ebpf" || st.ProxyMode == "hybrid" {
				st.ProxyMode = "tproxy"
			}
			st.Ver = 17
		}
		if st.LogLevel == "" {
			st.LogLevel = d.LogLevel
		}
		if st.Mode == "" {
			st.Mode = d.Mode
		}
		if st.TcpPorts == "" {
			st.TcpPorts = d.TcpPorts
		}
		if st.WebPort == 0 {
			st.WebPort = d.WebPort
		}
		if st.RedirectPort == 0 {
			st.RedirectPort = d.RedirectPort
		}
		if st.TproxyPort == 0 {
			st.TproxyPort = d.TproxyPort
		}
		if st.MixedPort == 0 {
			st.MixedPort = d.MixedPort
		}
		if st.ClashPort == 0 {
			st.ClashPort = d.ClashPort
		}
		if st.Secret == "" {
			st.Secret = d.Secret
		}
		if st.DnsRemote == "" {
			st.DnsRemote = d.DnsRemote
		}
		if st.DnsDirect == "" {
			st.DnsDirect = d.DnsDirect
		}
		if st.HealthURL == "" {
			st.HealthURL = d.HealthURL
		}
		if st.MainGroupName == "" {
			st.MainGroupName = d.MainGroupName
		}
		s.Settings = st
	}
	var b Builder
	if s.load("builder.json", &b) {
		// field lama (providers/groups/final/...) diabaikan otomatis oleh JSON.
		// Buang rule tanpa filter (catch-all berbahaya, bawaan default lama).
		// PackageNames/GeoIP/GeoSite ikut dihitung sebagai filter agar aturan
		// per-aplikasi atau geo tidak ikut terbuang saat load (bug: aturan
		// aplikasi hilang tiap reboot).
		kept := b.Rules[:0]
		for _, r := range b.Rules {
			if len(r.RuleSets) == 0 && len(r.DomainSuffix) == 0 && len(r.IPCIDR) == 0 &&
				len(r.GeoIP) == 0 && len(r.GeoSite) == 0 &&
				len(r.Port) == 0 && len(r.Protocol) == 0 && len(r.PackageNames) == 0 {
				continue
			}
			kept = append(kept, r)
		}
		b.Rules = kept
		s.Builder = b
	}
	return s
}

func (s *Store) load(name string, v any) bool {
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil || len(data) == 0 {
		// coba backup bila file utama hilang/kosong (mis. reboot saat tulis)
		data, err = os.ReadFile(filepath.Join(s.dir, name+".bak"))
		if err != nil || len(data) == 0 {
			return false
		}
	}
	if err := json.Unmarshal(data, v); err != nil {
		// file rusak: simpan salinannya agar data tidak hilang saat file ditimpa default
		_ = os.WriteFile(filepath.Join(s.dir, name+".corrupt"), data, 0600)
		// coba backup
		if bdata, berr := os.ReadFile(filepath.Join(s.dir, name+".bak")); berr == nil && len(bdata) > 0 {
			if jerr := json.Unmarshal(bdata, v); jerr == nil {
				return true
			}
		}
		return false
	}
	return true
}

func (s *Store) save(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// backup versi lama dulu: bila tulis baru gagal di tengah jalan / HP
	// reboot saat menulis, versi sebelumnya masih bisa dipulihkan.
	if old, err := os.ReadFile(filepath.Join(s.dir, name)); err == nil && len(old) > 0 {
		_ = os.WriteFile(filepath.Join(s.dir, name+".bak"), old, 0644)
	}
	tmp := filepath.Join(s.dir, name+".tmp")
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, name))
}

func (s *Store) SaveServers() error  { return s.save("servers.json", s.Servers) }
func (s *Store) SaveSubs() error     { return s.save("subscriptions.json", s.Subs) }
func (s *Store) SaveSettings() error { return s.save("settings.json", s.Settings) }
func (s *Store) SaveBuilder() error  { return s.save("builder.json", s.Builder) }

func (s *Store) findServer(id string) *Server {
	for i := range s.Servers {
		if s.Servers[i].ID == id {
			return &s.Servers[i]
		}
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// TaptapBox: satu core saja, selalu sing-box (mihomo tidak ada).
func (s Settings) ActiveCore() string {
	return "singbox"
}
