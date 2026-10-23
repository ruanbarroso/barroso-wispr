package atualizacao

import "testing"

func TestParseVersao(t *testing.T) {
	for _, s := range []string{"v0.1.0-bw.1", "0.1.0-bw.12"} {
		if _, ok := ParseVersao(s); !ok {
			t.Errorf("ParseVersao(%q) failed", s)
		}
	}
	for _, s := range []string{"dev", "v0.1-bw.1", "v0.1.0", "v0.1.0-beta.1", "v0.01.0-bw.1", "v0.1.0-bw.x", "v-1.1.0-bw.1"} {
		if _, ok := ParseVersao(s); ok {
			t.Errorf("ParseVersao(%q) unexpectedly succeeded", s)
		}
	}
}

func TestVersaoOrder(t *testing.T) {
	older, _ := ParseVersao("v0.1.0-bw.9")
	newer, _ := ParseVersao("v0.1.0-bw.10")
	patch, _ := ParseVersao("v0.1.1-bw.1")
	if !older.Antes(newer) || !newer.Antes(patch) || patch.Antes(newer) {
		t.Fatal("versions are not ordered numerically")
	}
}
