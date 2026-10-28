package main

import "testing"

func TestYAMLHasEBPFListener(t *testing.T) {
	cases := []struct {
		name string
		yml  string
		want bool
	}{
		{
			name: "listener ebpf blok (config user 2026-10-22)",
			yml: `tproxy-port: 15359
unified-delay: true
listeners:
  - name: ebpf-in
    type: ebpf
    port: 15361
    dns-listen: 127.0.0.1:1053
    local:
      enabled: true
      dns-mode: hijack
    shared:
      enabled: true
      interface: ["wlan1"]
proxies: []
`,
			want: true,
		},
		{
			name: "listener ebpf gaya flow",
			yml:  "listeners:\n  - {name: ebpf-in, type: ebpf, port: 15361}\n",
			want: true,
		},
		{
			name: "tanpa listeners sama sekali",
			yml:  "tproxy-port: 15359\nproxies: []\n",
			want: false,
		},
		{
			name: "listeners lain (tproxy/socks) bukan ebpf",
			yml: `listeners:
  - name: in1
    type: tproxy
    port: 15359
  - name: in2
    type: socks
    port: 7891
`,
			want: false,
		},
		{
			name: "kata ebpf di luar seksi listeners tidak memicu",
			yml: `proxies:
  - name: ebpf-server
    type: ss
    server: 1.2.3.4
proxy-groups:
  - name: ebpf
    type: select
`,
			want: false,
		},
		{
			name: "type ebpf di komentar tidak memicu",
			yml: `# listeners:
#   - type: ebpf
tproxy-port: 15359
`,
			want: false,
		},
		{
			name: "seksi listeners ditutup kunci top-level lain",
			yml: `listeners:
  - name: in1
    type: redir
    port: 12450
rules:
  - MATCH,PROXY
`,
			want: false,
		},
		{
			name: "type ebpf setelah seksi listeners tertutup tidak memicu",
			yml: `listeners:
  - name: in1
    type: redir
proxies:
  - name: x
    type: ebpf
`,
			want: false,
		},
	}
	for _, c := range cases {
		if got := yamlHasEBPFListener([]byte(c.yml)); got != c.want {
			t.Errorf("%s: yamlHasEBPFListener = %v, mau %v", c.name, got, c.want)
		}
	}
}

func TestEffectiveDatapath(t *testing.T) {
	cases := []struct {
		mode string
		ebpf bool
		want string
	}{
		{"tproxy", true, "ebpf"},
		{"iptables", true, "ebpf"},
		{"mixed", true, "ebpf"},
		{"tun", true, "ebpf"},
		{"tproxy", false, "tproxy"},
		{"iptables", false, "iptables"},
		{"mixed", false, "mixed"},
		{"tun", false, "none"},
		{"enhance", false, "none"},
	}
	for _, c := range cases {
		if got := effectiveDatapath(c.mode, c.ebpf); got != c.want {
			t.Errorf("effectiveDatapath(%q, %v) = %q, mau %q", c.mode, c.ebpf, got, c.want)
		}
	}
}
