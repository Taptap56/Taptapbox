package main

// Daftar aplikasi Android terinstal (untuk per-app routing di aturan rute).
// GET /api/packages -> [{name, system}]. Nama paket diambil dari
// `pm list packages -f`; aplikasi sistem dikenali dari flag -s dan lokasi
// APK (/system, /product, /vendor, /system_ext). Hasil di-cache 60 detik.

import (
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

type appPkg struct {
	Name   string `json:"name"`
	System bool   `json:"system"`
}

var (
	pkgMu     sync.Mutex
	pkgCache  []appPkg
	pkgCacheT time.Time
)

func pmOut(args ...string) ([]byte, error) {
	if out, err := exec.Command("pm", args...).Output(); err == nil {
		return out, nil
	}
	return exec.Command("/system/bin/pm", args...).Output()
}

// listPackages membaca daftar paket via pm (cepat, satu panggilan).
func listPackages() ([]appPkg, error) {
	pkgMu.Lock()
	defer pkgMu.Unlock()
	if pkgCache != nil && time.Since(pkgCacheT) < 60*time.Second {
		return pkgCache, nil
	}
	out, err := pmOut("list", "packages", "-f")
	if err != nil {
		return nil, err
	}
	// himpunan paket sistem (lebih akurat dari sekadar lokasi APK)
	sysSet := map[string]bool{}
	if so, err := pmOut("list", "packages", "-s"); err == nil {
		for _, ln := range strings.Split(string(so), "\n") {
			if strings.HasPrefix(ln, "package:") {
				sysSet[strings.TrimSpace(ln[len("package:"):])] = true
			}
		}
	}
	var pkgs []appPkg
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(ln) // package:/data/app/xxx/base.apk=com.example
		if !strings.HasPrefix(ln, "package:") {
			continue
		}
		rest := ln[len("package:"):]
		i := strings.LastIndex(rest, "=")
		if i < 0 {
			continue
		}
		path, name := rest[:i], rest[i+1:]
		if name == "" {
			continue
		}
		sys := sysSet[name] ||
			strings.HasPrefix(path, "/system/") ||
			strings.HasPrefix(path, "/product/") ||
			strings.HasPrefix(path, "/vendor/") ||
			strings.HasPrefix(path, "/system_ext/")
		pkgs = append(pkgs, appPkg{Name: name, System: sys})
	}
	// aplikasi pengguna dulu, lalu abjad
	sort.Slice(pkgs, func(i, j int) bool {
		if pkgs[i].System != pkgs[j].System {
			return !pkgs[i].System
		}
		return pkgs[i].Name < pkgs[j].Name
	})
	pkgCache, pkgCacheT = pkgs, time.Now()
	return pkgs, nil
}

func (a *App) handlePackages(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		errJSON(w, 405, "method tidak didukung")
		return
	}
	pkgs, err := listPackages()
	if err != nil {
		errJSON(w, 500, "gagal membaca daftar aplikasi: "+err.Error())
		return
	}
	writeJSON(w, pkgs)
}
