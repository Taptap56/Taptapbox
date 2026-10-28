// config.go — manajemen file config Mihomo (config.yaml) TaptapClash.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func parseDomainList(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(s, ",") {
		d := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(p, ".")))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

func uniqueTag(base string, used map[string]bool) string {
	if base == "" {
		base = "proxy"
	}
	if !used[base] {
		return base
	}
	for n := 2; ; n++ {
		c := fmt.Sprintf("%s #%d", base, n)
		if !used[c] {
			return c
		}
	}
}

// autoDir / manualDir: folder config mode otomatis & manual.
func (a *App) autoDir() string   { return filepath.Join(a.jsonDir, "clash", "user otomatis") }
func (a *App) manualDir() string { return filepath.Join(a.jsonDir, "clash", "user manual") }

// Direktori mode sing-box (core kedua) — dipakai CoreManager saat
// core aktif = singbox; di Fase 0 baru disiapkan strukturnya.
func (a *App) boxAutoDir() string   { return filepath.Join(a.jsonDir, "singbox", "user otomatis") }
func (a *App) boxManualDir() string { return filepath.Join(a.jsonDir, "singbox", "user manual") }

// activeConfigPath: file config.yaml yang dipakai mihomo sesuai mode aktif.
func (a *App) activeConfigPath() string {
	if a.store.Settings.ManualConfig {
		return filepath.Join(a.manualDir(), "config.yaml")
	}
	return filepath.Join(a.autoDir(), "config.yaml")
}

// migrateTaptapSlim: impor sekali jalan dari data TaptapSlim lama
// (/data/adb/taptapslim/config/) ke data TaptapBox baru, bila Box masih
// kosong. Disalin: 4 JSON bersama di akar config/, override sing-box,
// file manual sing-box, dan ruleset .srs. Folder clash tidak disentuh.
func (a *App) migrateTaptapSlim() {
	if _, err := os.Stat(filepath.Join(a.jsonDir, "settings.json")); err == nil {
		return // Box sudah ada data — migrasi hanya sekali
	}
	slim := filepath.Join("/data/adb/taptapslim", "config")
	if _, err := os.Stat(filepath.Join(slim, "settings.json")); err != nil {
		return // tidak ada data Slim
	}
	_ = os.MkdirAll(a.jsonDir, 0755)
	for _, n := range []string{"servers.json", "subscriptions.json", "settings.json", "builder.json"} {
		copyFileIfExists(filepath.Join(slim, n), filepath.Join(a.jsonDir, n))
	}
	_ = os.MkdirAll(a.boxAutoDir(), 0755)
	_ = os.MkdirAll(a.boxManualDir(), 0755)
	copyFileIfExists(filepath.Join(slim, "singbox", "user otomatis", "config.override.json"), filepath.Join(a.boxAutoDir(), "config.override.json"))
	if ents, err := os.ReadDir(filepath.Join(slim, "singbox", "user manual")); err == nil {
		for _, e := range ents {
			if !e.IsDir() {
				copyFileIfExists(filepath.Join(slim, "singbox", "user manual", e.Name()), filepath.Join(a.boxManualDir(), e.Name()))
			}
		}
	}
	if ents, err := os.ReadDir(filepath.Join(slim, "singbox", "user otomatis", "rulesets")); err == nil {
		_ = os.MkdirAll(a.boxRulesetDir(), 0755)
		for _, e := range ents {
			if !e.IsDir() {
				copyFileIfExists(filepath.Join(slim, "singbox", "user otomatis", "rulesets", e.Name()), filepath.Join(a.boxRulesetDir(), e.Name()))
			}
		}
	}
	a.logf("migrasi data TaptapSlim -> TaptapBox (config/ singbox)")
}

// migrateStoreFiles: kumpulkan file data (servers/settings/builder/
// subscriptions/cache.db) ke akar config/ — lokasi Store gabungan.
// Sumber warisan: subfolder per-core (user otomatis/) bila file di akar
// belum ada (mis. data yang sempat tersimpan oleh tata letak lama).
func (a *App) migrateStoreFiles() {
	for _, name := range []string{"servers.json", "subscriptions.json", "settings.json", "builder.json", "cache.db"} {
		dst := filepath.Join(a.jsonDir, name)
		if _, err := os.Stat(dst); err == nil {
			continue // sudah di akar, jangan timpa
		}
		for _, d := range []string{a.boxAutoDir()} {
			src := filepath.Join(d, name)
			if _, err := os.Stat(src); err == nil {
				_ = os.Rename(src, dst)
				break
			}
		}
	}
}

// moveDirContents: pindahkan semua file dari src ke dst (nama yang sudah
// ada di dst dipertahankan), lalu hapus src bila sudah kosong.
func moveDirContents(src, dst string) {
	ents, err := os.ReadDir(src)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		to := filepath.Join(dst, e.Name())
		if _, err := os.Stat(to); err != nil {
			_ = os.Rename(filepath.Join(src, e.Name()), to)
		}
	}
	_ = os.Remove(src) // hanya berhasil bila sudah kosong
}

// migrateRulesetDir: sebar cache rule-set ke folder per-core di user
// otomatis masing-masing — .srs/.json sing-box ke singbox/user
// otomatis/rulesets, .dat provider mihomo ke clash/user
// otomatis/rulesets. Sumber: folder per-core bernama _rulesets
// (v1.0.17), folder bersama config/_rulesets (v1.0.9–v1.0.16) &
// nama-nama lamanya config/rulesets dan config/umum (dibagi menurut
// ekstensi), dan cache provider .dat di workdir mihomo.
// geoip.dat/geosite.dat di workdir TIDAK ikut (itu geodata core,
// tempatnya memang di sana).
func (a *App) migrateRulesetDir() {
	// TaptapBox: hanya sing-box — folder clash tidak dibuat; file .dat/.mrs
	// mihomo lama dibiarkan (tidak dipindah ke config/clash).
	box := a.boxRulesetDir()
	moveDirContents(filepath.Join(a.boxAutoDir(), "_rulesets"), box) // nama v1.0.17
	for _, old := range []string{"_rulesets", "rulesets", "umum"} {
		splitRulesetDir(filepath.Join(a.jsonDir, old), box, "")
	}
}

// splitRulesetDir: pindahkan isi src ke folder per-core menurut
// ekstensi — .srs/.json ke sing-box, .dat/.mrs/.yaml/.yml ke mihomo;
// selain itu dibiarkan. File yg sudah ada di tujuan dipertahankan;
// src dihapus bila akhirnya kosong.
func splitRulesetDir(src, box, clash string) {
	// clash kosong = abaikan file mihomo (tidak dibuat folder clash)
	ents, err := os.ReadDir(src)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		var dst string
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".srs", ".json":
			dst = box
		case ".dat", ".mrs", ".yaml", ".yml":
			dst = clash
		default:
			continue
		}
		if dst == "" {
			continue // TaptapBox: file mihomo diabaikan, folder clash tidak dibuat
		}
		to := filepath.Join(dst, e.Name())
		if _, err := os.Stat(to); err != nil {
			_ = os.Rename(filepath.Join(src, e.Name()), to)
		}
	}
	_ = os.Remove(src) // hanya berhasil bila sudah kosong
}

func copyFileIfExists(src, dst string) {
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0755)
	_ = os.WriteFile(dst, data, 0644)
}

// legacyModule: modul lama yang datanya bisa diimpor sekali jalan.
type legacyModule struct {
	name, core, root string
}

var legacyModuleRoots = []legacyModule{
	// TaptapBox adalah penerus TaptapSlim: data Slim diimpor sekali jalan
	// bila Box masih kosong (lihat migrateTaptapSlim). Data TaptapClash
	// TIDAK diimpor — core-nya mihomo dan folder clash dihapus di Box.
	{name: "taptapslim", core: "singbox", root: "/data/adb/taptapslim"},
}

// migrateLegacyModules: impor data dari modul lama (TaptapBox/TaptapClash)
// saat data gabungan masih kosong. Pemenang (settings.json paling baru
// diubah) mengisi 4 JSON bersama di akar config/; yang kalah diarsip ke
// config/migration-backup/<nama>/. File override & manual TIAP core
// disalin ke folder core-nya dari modul asal core itu, apa pun pemenangnya.
// Settings pemenang disesuaikan: field core diisi menurut asal pemenang,
// web_port 5556 (bawaan paksa TaptapClash) dipetakan ke 5056.
func (a *App) migrateLegacyModules() {
	if _, err := os.Stat(filepath.Join(a.jsonDir, "settings.json")); err == nil {
		return // data gabungan sudah ada — migrasi hanya sekali
	}
	type cand struct {
		legacyModule
		mtime time.Time
	}
	var cands []cand
	for _, l := range legacyModuleRoots {
		if filepath.Clean(l.root) == filepath.Clean(a.dataDir) {
			continue
		}
		st, err := os.Stat(filepath.Join(l.root, "json", "user otomatis", "settings.json"))
		if err != nil {
			continue
		}
		cands = append(cands, cand{l, st.ModTime()})
	}
	if len(cands) == 0 {
		return
	}
	// artefak per-core dari modul asal masing-masing
	for _, l := range cands {
		auto := filepath.Join(l.root, "json", "user otomatis")
		manual := filepath.Join(l.root, "json", "user manual")
		dstAuto, dstManual := a.autoDir(), a.manualDir()
		ovName := "config.override.yaml"
		if l.core == "singbox" {
			dstAuto, dstManual = a.boxAutoDir(), a.boxManualDir()
			ovName = "config.override.json"
		}
		copyFileIfExists(filepath.Join(auto, ovName), filepath.Join(dstAuto, ovName))
		if ents, err := os.ReadDir(manual); err == nil {
			for _, e := range ents {
				if !e.IsDir() {
					copyFileIfExists(filepath.Join(manual, e.Name()), filepath.Join(dstManual, e.Name()))
				}
			}
		}
	}
	// pemenang: settings.json paling baru diubah
	win := cands[0]
	for _, l := range cands[1:] {
		if l.mtime.After(win.mtime) {
			win = l
		}
	}
	names := []string{"servers.json", "subscriptions.json", "settings.json", "builder.json"}
	_ = os.MkdirAll(a.jsonDir, 0755)
	winAuto := filepath.Join(win.root, "json", "user otomatis")
	for _, n := range names {
		copyFileIfExists(filepath.Join(winAuto, n), filepath.Join(a.jsonDir, n))
	}
	for _, l := range cands {
		if l.name == win.name {
			continue
		}
		dst := filepath.Join(a.jsonDir, "migration-backup", l.name)
		auto := filepath.Join(l.root, "json", "user otomatis")
		for _, n := range names {
			copyFileIfExists(filepath.Join(auto, n), filepath.Join(dst, n))
		}
	}
	// sesuaikan settings pemenang
	sp := filepath.Join(a.jsonDir, "settings.json")
	if data, err := os.ReadFile(sp); err == nil {
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			m["core"] = win.core
			if wp, ok := m["web_port"].(float64); ok && int(wp) == 5556 {
				m["web_port"] = 5056
			}
			if out, err := json.MarshalIndent(m, "", "  "); err == nil {
				_ = os.WriteFile(sp, out, 0644)
			}
		}
	}
	a.logf("migrasi data modul lama: sumber utama %s (core %s)", win.name, win.core)
}

// ensureConfigDirs: siapkan folder user otomatis/ & user manual/,
// migrasi config.json lama bila ada (dikonversi? tidak — config sing-box
// lama tidak kompatibel; user manual lama diarsipkan).
func (a *App) ensureConfigDirs() {
	// TaptapBox: hanya folder singbox — folder clash tidak dibuat lagi.
	_ = os.MkdirAll(a.boxAutoDir(), 0755)
	_ = os.MkdirAll(a.boxManualDir(), 0755)
	// arsipkan config.json lama (format sing-box) agar tidak dipakai
	for _, d := range []string{a.boxAutoDir(), a.boxManualDir(), a.jsonDir} {
		legacy := filepath.Join(d, "config.json")
		if _, err := os.Stat(legacy); err == nil {
			arch := legacy + ".singbox-bak"
			if _, err := os.Stat(arch); os.IsNotExist(err) {
				_ = os.Rename(legacy, arch)
			}
		}
	}
}

// overridePath: file config override untuk mode otomatis. Bila ada, isinya
// dipakai sebagai config aktif (setelah divalidasi) menggantikan hasil
// generate — tanpa pindah ke mode manual penuh.
func (a *App) overridePath() string { return filepath.Join(a.autoDir(), "config.override.yaml") }

// hasOverride: true bila mode otomatis sedang memakai config override.
func (a *App) hasOverride() bool {
	if a.store.Settings.ManualConfig {
		return false
	}
	st, err := os.Stat(a.overridePath())
	return err == nil && st.Size() > 0
}

// applyOverrideConfig: validasi lalu simpan config override untuk mode otomatis.
// yml kosong = hapus override (kembali ke config bawaan hasil generate).
func (a *App) applyOverrideConfig(yml string) error {
	path := a.overridePath()
	if strings.TrimSpace(yml) == "" {
		_ = os.Remove(path)
		return nil
	}
	tmp := filepath.Join(a.autoDir(), "config.override.check.yaml")
	if err := os.WriteFile(tmp, []byte(yml), 0600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if out, err := a.mihomoTest(tmp); err != nil {
		return fmt.Errorf("config tidak valid: %v\n%s", err, string(out))
	}
	return os.WriteFile(path, []byte(yml), 0644)
}

// rebaseOverride: segarkan bagian-bagian config yang digenerate dari builder
// (proxies, proxy-groups, rules) di dalam file override, tanpa menyentuh
// editan manual user di bagian lain (dns, port, dsb). Inbound yang tergantung
// mode (tproxy-port/redir-port/tun/dns.listen) juga disinkronkan agar ganti
// mode saat override aktif tidak mematikan internet.
// rule-providers digabung: provider hasil generate menimpa tag yang sama,
// provider manual user dipertahankan.
//
// Dipanggil setelah builder/settings/langganan berubah selagi override aktif,
// agar edit grup (dan rute/server) langsung berlaku tanpa harus "kembali ke
// otomatis" dulu. Hasil selalu divalidasi mihomo -t; file lama tidak diubah
// bila rebase gagal. No-op bila tidak ada override.
// rebaseActiveOverride: segarkan override milik core AKTIF
// (JSON untuk sing-box, YAML untuk mihomo).
func (a *App) rebaseActiveOverride() error {
	if a.store.Settings.ActiveCore() == "singbox" {
		return a.rebaseOverrideJSON()
	}
	return a.rebaseOverride()
}

func (a *App) rebaseOverride() error {
	if !a.hasOverride() {
		return nil
	}
	data, err := os.ReadFile(a.overridePath())
	if err != nil {
		return err
	}
	if _, err := yamlToMap(string(data)); err != nil {
		return fmt.Errorf("override tidak bisa diurai: %v", err)
	}
	base, err := a.buildMihomoMap()
	if err != nil {
		return err
	}
	// SADAR-TEKS: hanya section yang dikelola builder yang diganti teksnya;
	// section lain (listeners, sniffer, komentar, urutan kunci, spasi kosong)
	// dipertahankan byte-for-byte. Cara lama (urai ke map lalu marshal ulang
	// seluruh file) mengacak urutan kunci jadi alfabetis dan membuang
	// komentar/format setiap kali rebase jalan — editan rapi user rusak.
	header, secs := splitYAMLSections(string(data))
	find := func(key string) int {
		for i := range secs {
			if secs[i].key == key {
				return i
			}
		}
		return -1
	}
	setSec := func(key, text string) {
		if i := find(key); i >= 0 {
			secs[i].text = text
		} else {
			secs = append(secs, yamlSection{key, text})
		}
	}
	delSec := func(key string) {
		if i := find(key); i >= 0 {
			secs = append(secs[:i], secs[i+1:]...)
		}
	}
	// Bagian yang 100% milik builder: timpa penuh dari hasil generate terbaru.
	for _, k := range []string{"proxies", "proxy-groups", "rules"} {
		if v, ok := base[k]; ok {
			txt, err := marshalYAMLSection(k, v)
			if err != nil {
				return err
			}
			setSec(k, txt)
		}
	}
	// Inbound yang tergantung MODE proxy (tproxy-port/redir-port/tun)
	// mengikuti mode aktif — bukan editan manual user. Ada di hasil
	// generate -> timpa/tambah; tidak ada -> hapus (sisa mode lain;
	// mis. "tun:" basi saat pindah ke tproxy melawan iptables tproxy,
	// dan tproxy-port yang hilang = internet mati).
	for _, k := range []string{"tproxy-port", "redir-port", "tun"} {
		if v, ok := base[k]; ok {
			txt, err := marshalYAMLSection(k, v)
			if err != nil {
				return err
			}
			setSec(k, txt)
		} else {
			delSec(k)
		}
	}
	// dns: hanya baris listen yang disinkronkan; isi dns lain milik user.
	if i := find("dns"); i >= 0 {
		var baseListen any
		hasListen := false
		if bdns, ok := base["dns"].(map[string]any); ok {
			baseListen, hasListen = bdns["listen"]
		}
		secs[i].text = spliceDNSListen(secs[i].text, baseListen, hasListen)
	}
	// rule-providers digabung: provider hasil generate menimpa nama yang
	// sama, provider manual user dipertahankan.
	if bp, ok := base["rule-providers"].(map[string]any); ok && len(bp) > 0 {
		genTxt, err := marshalYAMLSection("rule-providers", bp)
		if err != nil {
			return err
		}
		if i := find("rule-providers"); i >= 0 {
			secs[i].text = mergeYAMLProviders(secs[i].text, genTxt)
		} else {
			setSec("rule-providers", genTxt)
		}
	}
	var sb strings.Builder
	sb.WriteString(header)
	for _, s := range secs {
		sb.WriteString(s.text)
		if !strings.HasSuffix(s.text, "\n") {
			sb.WriteString("\n")
		}
	}
	tmp := filepath.Join(a.autoDir(), "config.override.rebase.yaml")
	if err := os.WriteFile(tmp, []byte(sb.String()), 0600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if out, err := a.mihomoTest(tmp); err != nil {
		return fmt.Errorf("hasil rebase tidak valid: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(a.overridePath(), []byte(sb.String()), 0644); err != nil {
		return err
	}
	a.logf("override: direbase dari builder terbaru")
	return nil
}

// ---------- utilitas YAML sadar-teks (untuk rebaseOverride) ----------

type yamlSection struct {
	key  string
	text string
}

// yamlTopKey: nama kunci top-level dari baris kolom-0 "kunci:" / "kunci: ...".
func yamlTopKey(ln string) (string, bool) {
	i := strings.IndexByte(ln, ':')
	if i <= 0 {
		return "", false
	}
	k := ln[:i]
	for _, c := range k {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", false
		}
	}
	return k, true
}

// splitYAMLSections: bagi teks YAML menjadi header (baris sebelum kunci
// top-level pertama — komentar dsb) + section top-level berurutan. Tiap
// section memuat baris kuncinya dan semua baris sesudahnya sampai kunci
// top-level berikutnya.
func splitYAMLSections(text string) (string, []yamlSection) {
	var header strings.Builder
	var secs []yamlSection
	for _, ln := range strings.SplitAfter(text, "\n") {
		if ln == "" {
			continue
		}
		if k, ok := yamlTopKey(ln); ok {
			secs = append(secs, yamlSection{k, ln})
			continue
		}
		if len(secs) == 0 {
			header.WriteString(ln)
		} else {
			secs[len(secs)-1].text += ln
		}
	}
	return header.String(), secs
}

// marshalYAMLSection: marshal satu kunci top-level beserta isinya.
func marshalYAMLSection(key string, val any) (string, error) {
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(map[string]any{key: val}); err != nil {
		return "", err
	}
	_ = enc.Close()
	return sb.String(), nil
}

func yamlScalarText(v any) string {
	b, _ := yaml.Marshal(v)
	return strings.TrimRight(string(b), "\n")
}

// spliceDNSListen: sisipkan/ganti/hapus baris "  listen: ..." di dalam
// section dns tanpa menyentuh baris dns lainnya.
func spliceDNSListen(secText string, listen any, has bool) string {
	var out []string
	done := false
	for i, ln := range strings.SplitAfter(secText, "\n") {
		if ln == "" {
			continue
		}
		if i > 0 && strings.HasPrefix(ln, "  listen:") {
			if has && !done {
				out = append(out, "  listen: "+yamlScalarText(listen)+"\n")
				done = true
			}
			continue
		}
		out = append(out, ln)
		if i == 0 && has && !done {
			out = append(out, "  listen: "+yamlScalarText(listen)+"\n")
			done = true
		}
	}
	return strings.Join(out, "")
}

// splitYAMLProviders: pecah section rule-providers menjadi kepala
// (baris "rule-providers:") + blok per nama provider (indent 2 spasi).
func splitYAMLProviders(sec string) (string, []yamlSection) {
	var head string
	var subs []yamlSection
	for i, ln := range strings.SplitAfter(sec, "\n") {
		if ln == "" {
			continue
		}
		if i == 0 {
			head = ln
			continue
		}
		if strings.HasPrefix(ln, "  ") && !strings.HasPrefix(ln, "   ") {
			if k, ok := yamlTopKey(ln[2:]); ok {
				subs = append(subs, yamlSection{k, ln})
				continue
			}
		}
		if len(subs) > 0 {
			subs[len(subs)-1].text += ln
		} else {
			head += ln
		}
	}
	return head, subs
}

// mergeYAMLProviders: gabung section rule-providers override dengan hasil
// generate per nama provider — nama yang sama diganti di tempatnya,
// provider manual user utuh, provider generate yang baru ditambah di akhir.
func mergeYAMLProviders(ovSec, genSec string) string {
	head, osubs := splitYAMLProviders(ovSec)
	if len(osubs) == 0 {
		return genSec
	}
	_, gsubs := splitYAMLProviders(genSec)
	used := map[string]bool{}
	for i := range osubs {
		for _, g := range gsubs {
			if g.key == osubs[i].key {
				osubs[i].text = g.text
				used[g.key] = true
			}
		}
	}
	for _, g := range gsubs {
		if !used[g.key] {
			osubs = append(osubs, g)
		}
	}
	var sb strings.Builder
	sb.WriteString(head)
	for _, s := range osubs {
		sb.WriteString(s.text)
		if !strings.HasSuffix(s.text, "\n") {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// writeConfig: tulis config.yaml aktif.
// Mode manual: pakai config.yaml user manual apa adanya, KECUALI mixed
// port disisipkan ke salinan runtime bila belum ada (manualinject.go).
// Mode otomatis: generate fresh dari data.
// Port mixed efektif selalu dicatat untuk updater (updateProxyURL).
func (a *App) writeConfig() (string, error) {
	path := a.activeConfigPath()
	if a.store.Settings.ManualConfig {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("mode manual aktif tapi %s tidak ada", path)
		}
		feed, port := a.manualMihomoFeed(path)
		a.setEffMixedPort(port)
		if out, err := a.mihomoTest(feed); err != nil {
			return "", fmt.Errorf("config manual tidak valid: %v\n%s", err, string(out))
		}
		return feed, nil
	}
	// Mode otomatis + override: pakai hasil edit user (sudah divalidasi saat disimpan,
	// validasi ulang di sini sebagai pengaman).
	if a.hasOverride() {
		ov := a.overridePath()
		if out, err := a.mihomoTest(ov); err != nil {
			return "", fmt.Errorf("config override tidak valid: %v\n%s", err, string(out))
		}
		data, err := os.ReadFile(ov)
		if err != nil {
			return "", err
		}
		// Pastikan mixed port ada di salinan aktif (file override tak disentuh).
		data, port := a.ensureMixedMihomo(data)
		a.setEffMixedPort(port)
		if err := os.WriteFile(path, data, 0644); err != nil {
			return "", err
		}
		return path, nil
	}
	yml, err := a.buildConfigYAML()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(yml), 0644); err != nil {
		return "", err
	}
	a.setEffMixedPort(a.mixedSetting())
	return path, nil
}

// applyRawConfig memvalidasi lalu menyimpan config YAML mentah dari user
// ke folder user manual/ dan mengaktifkan mode manual.
func (a *App) applyRawConfig(yml string) error {
	path := filepath.Join(a.manualDir(), "config.yaml")
	tmp := filepath.Join(a.manualDir(), "config.check.yaml")
	if err := os.WriteFile(tmp, []byte(yml), 0600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if out, err := a.mihomoTest(tmp); err != nil {
		return fmt.Errorf("config tidak valid: %v\n%s", err, string(out))
	}
	return os.WriteFile(path, []byte(yml), 0644)
}

// prefetchRuleSets: tidak diperlukan untuk mihomo — rule-provider bertipe
// http diunduh & di-cache oleh mihomo sendiri saat berjalan.
func (a *App) prefetchRuleSets() []string {
	if a.store.Settings.ActiveCore() == "singbox" {
		return a.prefetchRuleSetsSingbox()
	}
	return nil
}

// ---------- clash YAML -> Server (untuk proxy provider) ----------

// parseFlowInto mengurai isi mapping gaya flow "{a: 1, b: {c: 2}}" (tanpa
// kurung terluar) ke map datar; nested digabung dengan ".".
func parseFlowInto(dst map[string]string, body, prefix string) {
	depth, start := 0, 0
	inQ := byte(0)
	parts := []string{}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inQ != 0 {
			if c == inQ {
				inQ = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inQ = c
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, body[start:])
	for _, part := range parts {
		part = strings.TrimSpace(part)
		i := strings.Index(part, ":")
		if i <= 0 {
			continue
		}
		k := strings.Trim(strings.TrimSpace(part[:i]), `"'`)
		v := strings.TrimSpace(part[i+1:])
		if strings.HasPrefix(v, "{") && strings.HasSuffix(v, "}") {
			parseFlowInto(dst, v[1:len(v)-1], prefix+k+".")
			continue
		}
		if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
			v = strings.ReplaceAll(v[1:len(v)-1], " ", "")
			v = strings.ReplaceAll(v, `"`, "")
			v = strings.ReplaceAll(v, "'", "")
		}
		v = strings.Trim(v, `"'`)
		if v != "" {
			dst[prefix+k] = v
		}
	}
}

// parseSimpleYAML mengurai subset YAML clash: blok proxies (gaya blok
// maupun flow "- {name: x, ...}"). Mengembalikan daftar map datar.
func parseSimpleYAML(text string) []map[string]string {
	type lvl struct {
		indent int
		key    string
	}
	var items []map[string]string
	var cur map[string]string
	var stack []lvl
	inProxies := false
	itemIndent := -1
	for _, raw := range strings.Split(text, "\n") {
		ln := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(ln) == "" || strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		indent := len(ln) - len(strings.TrimLeft(ln, " "))
		t := strings.TrimSpace(ln)
		if indent == 0 && !strings.HasPrefix(t, "- ") {
			inProxies = strings.HasPrefix(t, "proxies:")
			stack = nil
			itemIndent = -1
			cur = nil
			continue
		}
		if !inProxies {
			continue
		}
		stripped := strings.TrimLeft(ln, " ")
		if strings.HasPrefix(stripped, "- ") {
			if itemIndent < 0 {
				itemIndent = indent
			}
			if indent > itemIndent {
				// elemen list bersarang (mis. alpn) -> gabung dengan koma
				if cur != nil {
					key := ""
					for _, s := range stack {
						if s.key != "" {
							key += s.key + "."
						}
					}
					key = strings.TrimSuffix(key, ".")
					val := strings.Trim(strings.TrimSpace(strings.TrimPrefix(stripped, "- ")), `"'`)
					if key != "" && val != "" {
						if old := cur[key]; old != "" {
							cur[key] = old + "," + val
						} else {
							cur[key] = val
						}
					}
				}
				continue
			}
			cur = map[string]string{}
			items = append(items, cur)
			stack = []lvl{{indent: indent, key: ""}}
			t = strings.TrimSpace(strings.TrimPrefix(stripped, "- "))
			if t == "" {
				continue
			}
			if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
				parseFlowInto(cur, t[1:len(t)-1], "")
				continue
			}
		}
		if cur == nil {
			continue
		}
		for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		if !strings.Contains(t, ":") {
			continue
		}
		kv := strings.SplitN(t, ":", 2)
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		if strings.HasPrefix(v, "{") && strings.HasSuffix(v, "}") {
			prefix := ""
			for _, s := range stack {
				if s.key != "" {
					prefix += s.key + "."
				}
			}
			parseFlowInto(cur, v[1:len(v)-1], prefix+k+".")
			continue
		}
		if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
			v = strings.ReplaceAll(v[1:len(v)-1], " ", "")
		}
		v = strings.Trim(v, `"'`)
		prefix := ""
		for _, s := range stack {
			if s.key != "" {
				prefix += s.key + "."
			}
		}
		if v == "" {
			stack = append(stack, lvl{indent: indent, key: k})
			continue
		}
		cur[prefix+k] = v
	}
	return items
}

func clashYAMLToServers(text string) []*Server {
	var out []*Server
	for _, m := range parseSimpleYAML(text) {
		if sv := clashMapToServer(m); sv != nil {
			if nl := buildLink(sv.Proto, sv.Params, sv.Name); nl != "" {
				sv.Link = nl
			}
			out = append(out, sv)
		}
	}
	return out
}

func clashMapToServer(m map[string]string) *Server {
	typ := strings.ToLower(m["type"])
	name := m["name"]
	if name == "" {
		name = m["server"] + ":" + m["port"]
	}
	p := map[string]string{"server": m["server"], "port": m["port"]}
	skipVerify := m["skip-cert-verify"] == "true"
	if skipVerify {
		p["insecure"] = "1"
	}
	tlsOn := m["tls"] == "true"
	sni := orDefault(m["servername"], m["sni"])
	netw := m["network"]
	var proto string
	switch typ {
	case "ss", "shadowsocks":
		proto = "ss"
		p["method"] = m["cipher"]
		p["password"] = m["password"]
		if m["plugin"] != "" {
			p["plugin"] = m["plugin"]
		}
	case "vmess":
		proto = "vmess"
		p["uuid"] = m["uuid"]
		p["alter_id"] = orDefault(m["alterId"], "0")
		p["security"] = orDefault(m["cipher"], "auto")
		p["net"] = netw
		p["path"] = orDefault(m["ws-path"], m["ws-opts.path"])
		p["host"] = orDefault(m["ws-headers.Host"], m["ws-opts.headers.Host"])
		p["service_name"] = orDefault(m["grpc-service-name"], m["grpc-opts.grpc-service-name"])
		p["fp"] = fpOf(m)
		p["sni"] = sni
		if tlsOn {
			p["tls"] = "1"
		}
	case "vless":
		proto = "vless"
		p["uuid"] = m["uuid"]
		p["flow"] = m["flow"]
		p["encryption"] = "none"
		p["net"] = netw
		p["path"] = orDefault(m["ws-path"], m["ws-opts.path"])
		p["host"] = orDefault(m["ws-headers.Host"], m["ws-opts.headers.Host"])
		p["service_name"] = orDefault(m["grpc-service-name"], m["grpc-opts.grpc-service-name"])
		p["fp"] = fpOf(m)
		p["sni"] = sni
		p["pbk"] = m["reality-opts.public-key"]
		p["sid"] = m["reality-opts.short-id"]
		if p["pbk"] != "" {
			p["reality"] = "1"
		}
		if tlsOn || p["reality"] == "1" {
			p["tls"] = "1"
		}
	case "trojan":
		proto = "trojan"
		p["password"] = m["password"]
		p["sni"] = sni
		p["alpn"] = m["alpn"]
		p["fp"] = fpOf(m)
		p["net"] = netw
		p["path"] = orDefault(m["ws-path"], m["ws-opts.path"])
		p["host"] = orDefault(m["ws-headers.Host"], m["ws-opts.headers.Host"])
		p["service_name"] = orDefault(m["grpc-service-name"], m["grpc-opts.grpc-service-name"])
		if tlsOn || sni != "" {
			p["tls"] = "1"
		}
	case "hysteria2", "hy2", "hysteria":
		proto = "hy2"
		p["auth"] = orDefault(m["password"], m["auth"])
		p["obfs"] = m["obfs"]
		p["obfs_password"] = orDefault(m["obfs-password"], m["obfs_password"])
		p["sni"] = sni
		p["up_mbps"] = bwToMbps(m["up"])
		p["down_mbps"] = bwToMbps(m["down"])
		p["tls"] = "1"
	case "tuic":
		proto = "tuic"
		p["uuid"] = m["uuid"]
		p["password"] = m["password"]
		p["congestion_control"] = m["congestion-control"]
		p["udp_relay_mode"] = m["udp-relay-mode"]
		p["sni"] = sni
		p["alpn"] = orDefault(m["alpn"], "h3")
		p["tls"] = "1"
	case "wireguard", "wg":
		proto = "wireguard"
		p["private_key"] = m["private-key"]
		p["peer_public_key"] = m["public-key"]
		p["pre_shared_key"] = m["pre-shared-key"]
		p["local_address"] = strings.ReplaceAll(orDefault(m["ip"], "172.16.0.2/32"), " ", ",")
		p["mtu"] = m["mtu"]
		p["reserved"] = m["reserved"]
	case "anytls":
		proto = "anytls"
		p["password"] = m["password"]
		p["sni"] = sni
		p["tls"] = "1"
	case "socks5", "socks":
		proto = "socks"
		p["username"] = m["username"]
		p["password"] = m["password"]
		if tlsOn {
			p["tls"] = "1"
		}
	case "http":
		proto = "http"
		p["username"] = m["username"]
		p["password"] = m["password"]
		if tlsOn {
			p["tls"] = "1"
		}
	default:
		return nil
	}
	if p["sni"] == "" && (p["tls"] == "1") {
		p["sni"] = p["server"]
	}
	return &Server{Name: name, Proto: proto, Enabled: true, Params: p, DelayMs: -2}
}

func bwToMbps(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimSuffix(s, "bps")
	s = strings.TrimSpace(s)
	mult := 1.0
	if strings.HasSuffix(s, "gbps") {
		mult = 1000
		s = strings.TrimSuffix(s, "gbps")
	} else if strings.HasSuffix(s, "mbps") {
		s = strings.TrimSuffix(s, "mbps")
	} else if strings.HasSuffix(s, "kbps") {
		mult = 0.001
		s = strings.TrimSuffix(s, "kbps")
	}
	var f float64
	fmt.Sscanf(strings.TrimSpace(s), "%f", &f)
	if f <= 0 {
		return ""
	}
	return itoa(int(f * mult))
}

// sortKeys deterministik untuk debug (tidak dipakai di output utama).
var _ = sort.Strings
var _ = json.Marshal
