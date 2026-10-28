package main

// Grup proxy custom (selector / urltest / load-balance / fallback / relay)
// ala TaptapClash/Clash: anggota bisa
// dipilih manual per akun, atau dinamis lewat langganan + filter (regex /
// kata kunci) sehingga ikut berubah saat langganan di-update.

import (
	"fmt"
	"regexp"
	"strings"
)

type ProxyGroup struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`               // selector | urltest | loadbalance | fallback | relay
	Strategy string   `json:"strategy,omitempty"` // load-balance: consistent-hashing | round-robin
	Members  []string `json:"members,omitempty"`  // ID server / ID grup lain (yang di atasnya) / "DIRECT"
	SubIDs   []string `json:"sub_ids,omitempty"`  // anggota dinamis dari langganan ("manual" = server buatan sendiri)
	Filter   string   `json:"filter,omitempty"`   // regex (tanpa beda huruf besar/kecil) pada nama server
	Direct   bool     `json:"direct,omitempty"`   // tambahkan DIRECT sebagai anggota
}

// groupTags: tag unik untuk tiap grup (urut sama dengan groups).
func (a *App) groupTags(groups []ProxyGroup) []string {
	used := a.reservedTags()
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = uniqueTag(strings.TrimSpace(g.Name), used)
		used[out[i]] = true
	}
	return out
}

func compileFilter(f string) func(string) bool {
	f = strings.TrimSpace(f)
	if f == "" {
		return nil
	}
	if re, err := regexp.Compile("(?i)" + f); err == nil {
		return re.MatchString
	}
	low := strings.ToLower(f)
	return func(s string) bool { return strings.Contains(strings.ToLower(s), low) }
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// buildGroups membuat outbound grup custom. Mengembalikan outbound, peta
// ID grup -> tag (hanya grup yang punya anggota), urutan tag, dan proxy
// sintetik tambahan (untuk emulasi relay via dialer-proxy).
// Grup hanya boleh mengacu grup yang ada di atasnya (mencegah siklus).
// Pemanggil harus memegang store.mu (read).
func (a *App) buildGroups(built []builtServer) ([]any, map[string]string, []string, []map[string]any) {
	groups := a.store.Builder.Groups
	tags := a.groupTags(groups)
	srvTag := map[string]string{}
	byTag := map[string]builtServer{}
	for _, b := range built {
		srvTag[b.ID] = b.Tag
		byTag[b.Tag] = b
	}
	done := map[string]string{}
	out := []any{}
	order := []string{}
	extra := []map[string]any{}
	for i, g := range groups {
		members := []string{}
		seen := map[string]bool{}
		add := func(t string) {
			if t != "" && !seen[t] {
				seen[t] = true
				members = append(members, t)
			}
		}
		for _, m := range g.Members {
			if m == "DIRECT" {
				add("DIRECT")
			} else if t, ok := srvTag[m]; ok {
				add(t)
			} else if t, ok := done[m]; ok {
				add(t)
			}
		}
		if len(g.SubIDs) > 0 || strings.TrimSpace(g.Filter) != "" {
			match := compileFilter(g.Filter)
			for k := range a.store.Servers {
				sv := &a.store.Servers[k]
				t, ok := srvTag[sv.ID]
				if !ok {
					continue
				}
				if len(g.SubIDs) > 0 {
					key := sv.SubID
					if key == "" {
						key = "manual"
					}
					if !containsStr(g.SubIDs, key) {
						continue
					}
				}
				if match != nil && !match(sv.Name) {
					continue
				}
				add(t)
			}
		}
		if g.Direct {
			add("DIRECT")
		}
		if len(members) == 0 {
			a.logWarnf("grup %q dilewati: tidak ada anggota", g.Name)
			continue
		}
		tag := tags[i]
		ob := map[string]any{"name": tag, "proxies": members}
		switch g.Type {
		case "urltest":
			ob["type"] = "url-test"
			ob["url"] = a.healthURL()
			ob["interval"] = 600
			ob["tolerance"] = 50
		case "loadbalance":
			ob["type"] = "load-balance"
			ob["url"] = a.healthURL()
			ob["interval"] = 600
			st := strings.TrimSpace(g.Strategy)
			if st != "round-robin" {
				st = "consistent-hashing"
			}
			ob["strategy"] = st
		case "fallback":
			ob["type"] = "fallback"
			ob["url"] = a.healthURL()
			ob["interval"] = 600
		case "relay":
			// mihomo menghapus tipe relay; diemulasikan dengan dialer-proxy:
			// anggota [A, B, C] menjadi rantai A -> B -> C lewat proxy sintetik.
			// Hanya anggota server langsung yang bisa dirantai (grup/DIRECT dilewati).
			var chain []builtServer
			for _, m := range members {
				if b, ok := byTag[m]; ok {
					chain = append(chain, b)
				} else {
					a.logWarnf("grup relay %q: anggota %q bukan server langsung, dilewati", g.Name, m)
				}
			}
			if len(chain) == 0 {
				a.logWarnf("grup %q dilewati: tidak ada anggota relay yang bisa dirantai", g.Name)
				continue
			}
			prev := chain[0].Tag
			usedChain := map[string]bool{}
			for _, b := range chain[1:] {
				nm := fmt.Sprintf("%s ⛓ %s", tag, b.Tag)
				for k := 2; usedChain[nm]; k++ {
					nm = fmt.Sprintf("%s ⛓ %s#%d", tag, b.Tag, k)
				}
				usedChain[nm] = true
				cl := map[string]any{}
				for kk, vv := range b.Ob {
					cl[kk] = vv
				}
				cl["name"] = nm
				cl["dialer-proxy"] = prev
				extra = append(extra, cl)
				prev = nm
			}
			ob["type"] = "select"
			ob["proxies"] = []string{prev}
		default:
			ob["type"] = "select"
		}
		out = append(out, ob)
		done[g.ID] = tag
		order = append(order, tag)
	}
	return out, done, order, extra
}
