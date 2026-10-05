package ai

import "testing"

func TestKeyHints(t *testing.T) {
	if claudeShapeHint("sk-ant-api03-x8cNMJHzlnL5vFqFa1-zCWGQ06iU--lkpcPn9jaUR5gPD0A7zZP_tiBZXvzIQIxR") == "" {
		t.Fatal("short key not flagged")
	}
	full := "sk-ant-api03-" + string(make([]byte, 0))
	for len(full) < 106 {
		full += "x"
	}
	full += "AA"
	if h := claudeShapeHint(full); h != "" {
		t.Fatal(h)
	}
	if ElevenKeyHint("ogi2DyUAKJb7CEdqqvlU") == "" || ElevenKeyHint("sk_123") == "" {
		t.Fatal("eleven hints")
	}
	k := "sk_"
	for len(k) < 51 {
		k += "a"
	}
	if h := ElevenKeyHint(k); h != "" {
		t.Fatal(h)
	}
}
