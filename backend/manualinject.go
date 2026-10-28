package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config manual/override: file user dipakai apa adanya — tapi daemon tetap
// butuh satu jalur egress lewat core (cek/unduh pembaruan). Bila file user
// tak punya mixed port, core tak membuka proksi lokal sehingga updater
// mati (laporan 2026-10-25: connection refused 10809, lalu direct gagal
// di DNS [::1]:53). Solusi: saat core start, mixed port disisipkan ke
// SALINAN runtime (config.runtime.yaml/json di folder yang sama); file
// user TIDAK PERNAH disentuh, dan hanya salinan runtime yang diumpankan
// ke core. Port mixed efektif dicatat (effMixedPort) agar updater memakai
// port yang benar-benar didengar core, bukan sekadar port di pengaturan.

func (a *App) mixedSetting() int {
	if a.store != nil && a.store.Settings.MixedPort > 0 {
		return a.store.Settings.MixedPort
	}
	return 7890
}

func (a *App) setEffMixedPort(p int) {
	a.effMu.Lock()
	defer a.effMu.Unlock()
	a.effMixedPort = p
}

func (a *App) getEffMixedPort() int {
	a.effMu.Lock()
	defer a.effMu.Unlock()
	return a.effMixedPort
}

// toPort: baca nilai port dari hasil parse YAML/JSON (int, float, string).
func toPort(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		if n > 0 {
			return n, true
		}
	case int64:
		if n > 0 {
			return int(n), true
		}
	case float64:
		if n > 0 {
			return int(n), true
		}
	case string:
		if p, err := strconv.Atoi(strings.TrimSpace(n)); err == nil && p > 0 {
			return p, true
		}
	}
	return 0, false
}

// mihomoMixedPort: port mixed-port di YAML mihomo (false bila tak ada).
func mihomoMixedPort(data []byte) (int, bool) {
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return 0, false
	}
	if v, ok := cfg["mixed-port"]; ok {
		return toPort(v)
	}
	return 0, false
}

// injectMixedPortYAML: YAML mihomo dengan mixed-port disetel.
func injectMixedPortYAML(data []byte, port int) ([]byte, error) {
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["mixed-port"] = port
	return yaml.Marshal(cfg)
}

// singboxMixedPort: port inbound mixed di JSON sing-box (false bila tak ada).
func singboxMixedPort(data []byte) (int, bool) {
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return 0, false
	}
	inbs, _ := cfg["inbounds"].([]any)
	for _, ib := range inbs {
		m, ok := ib.(map[string]any)
		if !ok || m["type"] != "mixed" {
			continue
		}
		if p, ok := toPort(m["listen_port"]); ok {
			return p, true
		}
	}
	return 0, false
}

// injectMixedInboundJSON: JSON sing-box dengan inbound mixed ditambahkan.
func injectMixedInboundJSON(data []byte, port int) ([]byte, error) {
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	inbs, _ := cfg["inbounds"].([]any)
	inbs = append(inbs, map[string]any{
		"type": "mixed", "tag": "mixed-in",
		"listen": "127.0.0.1", "listen_port": port,
	})
	cfg["inbounds"] = inbs
	return json.MarshalIndent(cfg, "", "  ")
}

// manualMihomoFeed: file yang diumpankan ke core untuk mode manual mihomo.
// Sudah ada mixed-port -> file user apa adanya. Belum ada -> salinan
// runtime dengan mixed-port disisipkan (divalidasi dulu). Gagal apa pun
// -> file user apa adanya (perilaku lama) + port 0 (tak diketahui).
func (a *App) manualMihomoFeed(userPath string) (string, int) {
	data, err := os.ReadFile(userPath)
	if err != nil {
		return userPath, 0
	}
	if p, ok := mihomoMixedPort(data); ok {
		return userPath, p
	}
	injected, err := injectMixedPortYAML(data, a.mixedSetting())
	if err != nil {
		a.logWarnf("sisip mixed-port manual mihomo gagal: %v", err)
		return userPath, 0
	}
	rt := filepath.Join(a.manualDir(), "config.runtime.yaml")
	if err := os.WriteFile(rt, injected, 0644); err != nil {
		return userPath, 0
	}
	if _, err := a.mihomoTest(rt); err != nil {
		a.logWarnf("config runtime manual mihomo tak valid, pakai file user: %v", err)
		return userPath, 0
	}
	return rt, a.mixedSetting()
}

// manualSingboxFeed: sama untuk mode manual sing-box.
func (a *App) manualSingboxFeed(userPath string) (string, int) {
	data, err := os.ReadFile(userPath)
	if err != nil {
		return userPath, 0
	}
	if p, ok := singboxMixedPort(data); ok {
		return userPath, p
	}
	injected, err := injectMixedInboundJSON(data, a.mixedSetting())
	if err != nil {
		a.logWarnf("sisip mixed inbound manual sing-box gagal: %v", err)
		return userPath, 0
	}
	rt := filepath.Join(a.boxManualDir(), "config.runtime.json")
	if err := os.WriteFile(rt, injected, 0644); err != nil {
		return userPath, 0
	}
	if err := a.singboxTest(rt); err != nil {
		a.logWarnf("config runtime manual sing-box tak valid, pakai file user: %v", err)
		return userPath, 0
	}
	return rt, a.mixedSetting()
}

// ensureMixedMihomo: untuk config override (file hasil edit user yang
// disalin ke path aktif): kembalikan data final + port efektif. Bila
// mixed-port sudah ada, data tak diubah; bila belum, disisipkan.
func (a *App) ensureMixedMihomo(data []byte) ([]byte, int) {
	if p, ok := mihomoMixedPort(data); ok {
		return data, p
	}
	injected, err := injectMixedPortYAML(data, a.mixedSetting())
	if err != nil {
		return data, 0
	}
	return injected, a.mixedSetting()
}

// ensureMixedSingbox: sama untuk override sing-box.
func (a *App) ensureMixedSingbox(data []byte) ([]byte, int) {
	if p, ok := singboxMixedPort(data); ok {
		return data, p
	}
	injected, err := injectMixedInboundJSON(data, a.mixedSetting())
	if err != nil {
		return data, 0
	}
	return injected, a.mixedSetting()
}
