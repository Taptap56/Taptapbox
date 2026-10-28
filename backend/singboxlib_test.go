package main

import "testing"

func TestSingboxLibraryCheck(t *testing.T) {
	valid := []byte(`{"log":{"level":"warn"},"outbounds":[{"type":"direct","tag":"DIRECT"}],"route":{"final":"DIRECT"}}`)
	if err := checkSingboxConfigBytes(valid); err != nil {
		t.Fatalf("config valid ditolak library: %v", err)
	}
	invalid := []byte(`{"outbounds":[{"type":"tipe-tidak-ada","tag":"X"}],"route":{"final":"X"}}`)
	if err := checkSingboxConfigBytes(invalid); err == nil {
		t.Fatal("config tidak valid harus ditolak library")
	}
}
