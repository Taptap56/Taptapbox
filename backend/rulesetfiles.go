package main

// File-file rule-set di folder per-core (config/<core>/user otomatis/rulesets).
//
// WebUI menampilkan isi folder apa adanya agar:
//  1. file hasil copy manual (tanpa unduh) TERBACA di WebUI — sebelumnya
//     hanya entri builder.json yang tampil, file di disk tak terlihat;
//  2. hapus di WebUI ikut MENGHAPUS file di disk — sebelumnya hapus
//     rule-set hanya menghapus entri builder, file cache tertinggal
//     (mis. geoip-id.srs & geosite-ads.srs tetap ada di manager).
//
// Keamanan hapus: hanya file DI DALAM folder rulesets core yang boleh
// dihapus. Nama file divalidasi (tanpa pemisah path, tanpa ".."); mode
// tag hanya menghapus nama-nama turunan tag di dalam folder itu. Entri
// local yang path-nya di luar folder tidak ikut dihapus file-nya —
// hanya entri builder-nya yang dicabut (file luar dikelola user sendiri).

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type rulesetFile struct {
	Name       string `json:"name"` // mis. geoip-id.srs
	Size       int64  `json:"size"`
	Mtime      int64  `json:"mtime"`      // unix
	Tag        string `json:"tag"`        // nama tanpa ekstensi
	Ext        string `json:"ext"`        // ekstensi kecil, mis. .srs
	Registered bool   `json:"registered"` // ada entri builder dgn tag/path ini
	RelPath    string `json:"relpath"`    // path relatif thd dataDir (untuk entri local)
}

// rulesetDirFor: folder rulesets core yang diminta; kosong/tak dikenal =
// core aktif saat ini.
func (a *App) rulesetDirFor(core string) string {
	switch core {
	case "singbox":
		return a.boxRulesetDir()
	case "clash":
		return a.clashRulesetDir()
	default:
		if a.store.Settings.ActiveCore() == "singbox" {
			return a.boxRulesetDir()
		}
		return a.clashRulesetDir()
	}
}

// rulesetFileTag: tag yang dipakai generator untuk sebuah file — nama
// tanpa ekstensi (geoip-id.srs -> geoip-id; file tanpa ekstensi -> nama).
func rulesetFileTag(name string) string {
	if ext := filepath.Ext(name); ext != "" {
		return strings.TrimSuffix(name, ext)
	}
	return name
}

// Ekstensi file cache/turunan yang dihapus saat sebuah tag dihapus.
var rulesetTagExts = []string{".srs", ".dat", ".mrs", ".yaml", ".yml", ".json", ".txt", ".list"}

func (a *App) handleRulesetFiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		a.handleRulesetFilesList(w, r)
	case "DELETE":
		a.handleRulesetFilesDelete(w, r)
	default:
		errJSON(w, 405, "metode tidak didukung")
	}
}

func (a *App) handleRulesetFilesList(w http.ResponseWriter, r *http.Request) {
	core := r.URL.Query().Get("core")
	dir := a.rulesetDirFor(core)
	if core != "singbox" && core != "clash" {
		core = a.store.Settings.ActiveCore()
	}
	entries, _ := os.ReadDir(dir)

	a.store.mu.RLock()
	bld := a.store.Builder
	a.store.mu.RUnlock()

	files := []rulesetFile{}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		name := e.Name()
		tag := rulesetFileTag(name)
		registered := false
		for _, rs := range bld.RuleSets {
			if rs.Tag == tag {
				registered = true
				break
			}
			if rs.Type == "local" && rs.Path != "" && filepath.Base(rs.Path) == name {
				registered = true
				break
			}
		}
		rel, _ := filepath.Rel(a.dataDir, filepath.Join(dir, name))
		files = append(files, rulesetFile{
			Name:       name,
			Size:       info.Size(),
			Mtime:      info.ModTime().Unix(),
			Tag:        tag,
			Ext:        strings.ToLower(filepath.Ext(name)),
			Registered: registered,
			RelPath:    rel,
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	dirRel, _ := filepath.Rel(a.dataDir, dir)
	writeJSON(w, map[string]any{"core": core, "dir": dirRel, "files": files})
}

func (a *App) handleRulesetFilesDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Core string `json:"core"`
		Name string `json:"name"` // hapus satu file
		Tag  string `json:"tag"`  // hapus file turunan tag + entri builder-nya
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, 400, "body tidak valid")
		return
	}
	if req.Name == "" && req.Tag == "" {
		errJSON(w, 400, "isi name atau tag")
		return
	}
	dir := a.rulesetDirFor(req.Core)

	// Kumpulkan file yang boleh dihapus — semuanya di dalam dir.
	var targets []string
	if req.Name != "" {
		if strings.ContainsAny(req.Name, `/\`) || req.Name == "." || req.Name == ".." {
			errJSON(w, 400, "nama file tidak valid")
			return
		}
		p := filepath.Join(dir, req.Name)
		// Join + Clean sudah menetralkan "..", tapi pastikan tetap di dir.
		if filepath.Dir(p) != dir {
			errJSON(w, 400, "nama file tidak valid")
			return
		}
		targets = append(targets, p)
	}
	if req.Tag != "" {
		for _, ext := range rulesetTagExts {
			targets = append(targets, filepath.Join(dir, req.Tag+ext))
		}
	}

	deleted := []string{}
	for _, p := range targets {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			if err := os.Remove(p); err == nil {
				deleted = append(deleted, filepath.Base(p))
			}
		}
	}

	// Cabut entri builder yang merujuk ke file/tag yang dihapus.
	deletedNames := map[string]bool{}
	for _, d := range deleted {
		deletedNames[d] = true
	}
	a.store.mu.Lock()
	bld := a.store.Builder
	kept := bld.RuleSets[:0]
	changed := false
	for _, rs := range bld.RuleSets {
		drop := false
		if req.Tag != "" && rs.Tag == req.Tag {
			drop = true
		}
		if req.Name != "" && rs.Tag == rulesetFileTag(req.Name) {
			drop = true
		}
		// Entri local yang file-nya ikut terhapus (di mode apa pun)
		// tidak boleh menggantung tanpa file.
		if rs.Type == "local" && rs.Path != "" && deletedNames[filepath.Base(rs.Path)] {
			drop = true
		}
		if drop {
			changed = true
			continue
		}
		kept = append(kept, rs)
	}
	if changed {
		bld.RuleSets = kept
		a.store.Builder = bld
		_ = a.store.SaveBuilder()
	}
	a.store.mu.Unlock()

	writeJSON(w, map[string]any{"ok": true, "deleted": deleted, "builder_changed": changed})
}
