package main

// Animasi WebUI: file-file HTML di <webuiDir>/animasi dipanggil berdasarkan
// NOMOR, bukan nama file. Pola nama yang dikenali: "<nomor>. <bebas>.html"
// atau "<nomor>.html" (mis. "1. jaringan saraf ai.html" -> nomor 1), sehingga
// user bebas mengganti nama / menambah file bernomor lain.
// GET /api/animasi -> {"files":[1,2,...]} (urut menaik).

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

var animasiRe = regexp.MustCompile(`(?i)^(\d+).*\.html$`)

func (a *App) animasiFiles() []int {
	out := []int{}
	es, err := os.ReadDir(filepath.Join(a.webuiDir, "animasi"))
	if err != nil {
		return out
	}
	for _, e := range es {
		if e.IsDir() {
			continue
		}
		m := animasiRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

func (a *App) handleAnimasi(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	writeJSON(w, map[string]any{"files": a.animasiFiles()})
}

// animasiFile: cari file bernomor n (pola "<nomor>... .html"), kembalikan path.
// Nomor di-parse sebagai int dulu sehingga tidak ada path traversal.
func (a *App) animasiFile(n int) string {
	dir := filepath.Join(a.webuiDir, "animasi")
	es, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range es {
		if e.IsDir() {
			continue
		}
		m := animasiRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if nn, err := strconv.Atoi(m[1]); err == nil && nn == n {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// GET /api/animasi/file?n=1 -> isi file HTML bernomor n.
func (a *App) handleAnimasiFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	n, err := strconv.Atoi(r.URL.Query().Get("n"))
	if err != nil || n < 0 {
		errJSON(w, 400, "nomor tidak valid")
		return
	}
	p := a.animasiFile(n)
	if p == "" {
		errJSON(w, 404, "animasi nomor "+strconv.Itoa(n)+" tidak ada")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeFile(w, r, p)
}
