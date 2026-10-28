package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// mihomoRules mengambil daftar rule (string) dari hasil buildMihomoMap.
func mihomoRules(t *testing.T, a *App) []string {
	t.Helper()
	cfg, err := a.buildMihomoMap()
	if err != nil {
		t.Fatal(err)
	}
	rl, ok := cfg["rules"].([]string)
	if !ok {
		t.Fatalf("config tanpa rules")
	}
	return rl
}

func findRuleWith(t *testing.T, rules []string, substr string) string {
	t.Helper()
	for _, r := range rules {
		if strings.Contains(r, substr) {
			return r
		}
	}
	t.Fatalf("tidak ada rule dengan %q", substr)
	return ""
}

// Aturan domain + package_names -> baris PROCESS-NAME & DOMAIN-SUFFIX mihomo.
func TestBuildPackageNameRule(t *testing.T) {
	a := testApp(t)
	a.store.Builder.Rules = []RouteRule{{
		ID:           "r-app",
		DomainSuffix: []string{"example.com"},
		PackageNames: []string{"com.android.chrome", "com.whatsapp"},
		Outbound:     "DIRECT",
	}}
	rules := mihomoRules(t, a)
	if got := findRuleWith(t, rules, "PROCESS-NAME,com.android.chrome,DIRECT"); got == "" {
		t.Fatalf("PROCESS-NAME hilang")
	}
	if got := findRuleWith(t, rules, "DOMAIN-SUFFIX,example.com,DIRECT"); got == "" {
		t.Fatalf("DOMAIN-SUFFIX hilang")
	}
}

// Aturan HANYA berisi package_names tetap di-generate (bukan catch-all).
func TestBuildPackageNameOnlyRule(t *testing.T) {
	a := testApp(t)
	a.store.Builder.Rules = []RouteRule{{
		ID:           "r-app-only",
		PackageNames: []string{"com.android.chrome"},
		Outbound:     "REJECT",
	}}
	r := findRuleWith(t, mihomoRules(t, a), "PROCESS-NAME,com.android.chrome,REJECT")
	if r == "" {
		t.Fatalf("PROCESS-NAME REJECT hilang")
	}
}

// /api/packages di lingkungan tanpa pm: 500 yang rapi, bukan crash.
func TestHandlePackagesNoPm(t *testing.T) {
	a := testApp(t)
	if _, err := pmOut("list", "packages"); err == nil {
		t.Skip("pm tersedia di lingkungan ini")
	}
	r := httptest.NewRequest("GET", "/api/packages", nil)
	w := httptest.NewRecorder()
	a.handlePackages(w, r)
	if w.Code != 500 {
		t.Fatalf("tanpa pm harus 500, dapat %d", w.Code)
	}
}
