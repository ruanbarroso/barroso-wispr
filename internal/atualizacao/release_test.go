//go:build windows

package atualizacao

import "testing"

func TestSelectStableReleaseByArch(t *testing.T) {
	current, _ := ParseVersao("v0.1.0-bw.1")
	releases := []Release{
		{TagName: "v0.1.0-bw.7", Draft: true, Assets: []Asset{{Name: assetAMD64}}},
		{TagName: "v0.1.0-bw.10", Prerelease: true, Assets: []Asset{{Name: assetAMD64}}},
		{TagName: "v0.1.0-bw.8", Assets: []Asset{{Name: assetAMD64}}},
		{TagName: "v0.1.0-bw.9", Assets: []Asset{{Name: assetAMD64}, {Name: assetARM64}}},
		{TagName: "v0.1.0-bw.11", Assets: []Asset{{Name: "wrong.exe"}}},
	}
	_, a, v, ok := SelecionaRelease(releases, current, "amd64")
	if !ok || v.Tag() != "v0.1.0-bw.9" || a.Name != assetAMD64 {
		t.Fatalf("amd64 selection = %q %q %v", v.Tag(), a.Name, ok)
	}
	_, a, v, ok = SelecionaRelease(releases, current, "arm64")
	if !ok || v.Tag() != "v0.1.0-bw.9" || a.Name != assetARM64 {
		t.Fatalf("arm64 selection = %q %q %v", v.Tag(), a.Name, ok)
	}
	if _, _, _, ok := SelecionaRelease(releases, current, "386"); ok {
		t.Fatal("unsupported architecture selected an asset")
	}
}

func TestSHA256SUMS(t *testing.T) {
	const sum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got, err := SomaSHA256([]byte(sum+"  "+assetAMD64+"\n"), assetAMD64)
	if err != nil || got != sum {
		t.Fatalf("valid checksum: %q %v", got, err)
	}
	for _, data := range [][]byte{
		[]byte(sum + "  other.exe\n"),
		[]byte("bad  " + assetAMD64 + "\n"),
		[]byte(sum + "  " + assetAMD64 + "\n" + sum + "  " + assetAMD64 + "\n"),
		[]byte(sum + "  " + assetAMD64 + "  extra\n"),
	} {
		if _, err := SomaSHA256(data, assetAMD64); err == nil {
			t.Errorf("SomaSHA256(%q) unexpectedly succeeded", data)
		}
	}
}
