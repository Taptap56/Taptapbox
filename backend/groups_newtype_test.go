package main

import "testing"

// Tipe grup baru: load-balance, fallback, relay.
func TestBuildGroupsNewTypes(t *testing.T) {
	a := sampleApp()
	a.store.Servers = []Server{
		{ID: "s1", Name: "sg1", Proto: "vmess", Enabled: true, Params: map[string]string{
			"server": "sg1.example.com", "port": "443", "uuid": "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}},
		{ID: "s2", Name: "sg2", Proto: "vmess", Enabled: true, Params: map[string]string{
			"server": "sg2.example.com", "port": "443", "uuid": "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}},
	}
	built := a.buildServers()
	a.store.Builder.Groups = []ProxyGroup{
		{ID: "g1", Name: "LB", Type: "loadbalance", Members: []string{"s1", "s2"}},
		{ID: "g2", Name: "FB", Type: "fallback", Members: []string{"s1", "s2"}},
		{ID: "g3", Name: "RL", Type: "relay", Members: []string{"s1", "s2"}},
	}
	objs, _, _, extra := a.buildGroups(built)
	if len(objs) != 3 {
		t.Fatalf("harus 3 grup, dapat %d", len(objs))
	}
	got := map[string]map[string]any{}
	for _, o := range objs {
		m := o.(map[string]any)
		got[m["name"].(string)] = m
	}
	lb := got["LB"]
	if lb["type"] != "load-balance" {
		t.Fatalf("LB type harus load-balance, dapat %v", lb["type"])
	}
	if lb["strategy"] != "consistent-hashing" {
		t.Fatalf("LB strategy default harus consistent-hashing, dapat %v", lb["strategy"])
	}
	if lb["url"] == nil || lb["interval"] == nil {
		t.Fatal("LB harus punya url & interval untuk health check")
	}
	if got["FB"]["type"] != "fallback" {
		t.Fatalf("FB type harus fallback, dapat %v", got["FB"]["type"])
	}
	// relay diemulasikan: grup jadi select 1 anggota (proxy rantai sintetik),
	// proxy sintetik memakai dialer-proxy.
	rl := got["RL"]
	if rl["type"] != "select" {
		t.Fatalf("RL type harus select (emulasi), dapat %v", rl["type"])
	}
	rp, ok := rl["proxies"].([]string)
	if !ok || len(rp) != 1 {
		t.Fatalf("RL harus punya 1 proxy rantai, dapat %v", rl["proxies"])
	}
	if len(extra) != 1 {
		t.Fatalf("harus 1 proxy sintetik relay, dapat %d", len(extra))
	}
	if extra[0]["dialer-proxy"] != "sg1" {
		t.Fatalf("rantai relay harus dial lewat sg1, dapat %v", extra[0]["dialer-proxy"])
	}
}

// Strategi round-robin diteruskan.
func TestBuildGroupsLoadBalanceStrategy(t *testing.T) {
	a := sampleApp()
	a.store.Servers = []Server{
		{ID: "s1", Name: "sg1", Proto: "vmess", Enabled: true, Params: map[string]string{
			"server": "sg1.example.com", "port": "443", "uuid": "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}},
	}
	built := a.buildServers()
	a.store.Builder.Groups = []ProxyGroup{
		{ID: "g1", Name: "LB", Type: "loadbalance", Strategy: "round-robin", Members: []string{"s1"}},
	}
	objs, _, _, _ := a.buildGroups(built)
	m := objs[0].(map[string]any)
	if m["strategy"] != "round-robin" {
		t.Fatalf("strategy harus round-robin, dapat %v", m["strategy"])
	}
}
