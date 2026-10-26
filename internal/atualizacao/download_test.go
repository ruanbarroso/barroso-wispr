//go:build windows || darwin

package atualizacao

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuscarURLAndDownload(t *testing.T) {
	payload := []byte("verified fake executable payload")
	hash := sha256.Sum256(payload)
	digest := hex.EncodeToString(hash[:])
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			_, _ = io.WriteString(w, `[{"tag_name":"v0.1.0-bw.4","draft":false,"prerelease":false,"assets":[{"name":"`+assetNome(runtime.GOARCH)+`","size":`+itoa(len(payload))+`,"browser_download_url":"`+server.URL+`/binary","digest":"sha256:`+digest+`"}]}]`)
		case "/binary":
			_, _ = w.Write(payload)
		case "/SHA256SUMS":
			_, _ = io.WriteString(w, digest+"  "+assetNome(runtime.GOARCH)+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	current, _ := ParseVersao("v0.1.0-bw.1")
	_, asset, version, ok, err := BuscarURL(context.Background(), server.Client(), server.URL+"/releases", current, runtime.GOARCH)
	if err != nil || !ok || version.Tag() != "v0.1.0-bw.4" {
		t.Fatalf("BuscarURL = %v, %v, %q", ok, err, version.Tag())
	}
	path, err := Baixar(context.Background(), server.Client(), asset, digest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(payload) {
		t.Fatalf("download = %q", got)
	}
}

func TestEstagiarValidatesCandidateVersion(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "candidate.go")
	binary := filepath.Join(dir, "candidate.exe")
	code := `package main
import ("fmt"; "os")
func main() { if len(os.Args) > 1 && os.Args[1] == "-version" { fmt.Println("v0.1.0-bw.4") } }
`
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", binary, source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build candidate: %v: %s", err, out)
	}
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(payload)
	digest := hex.EncodeToString(h[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	v, _ := ParseVersao("v0.1.0-bw.4")
	staged, err := Estagiar(context.Background(), srv.Client(), Asset{
		Name: assetNome(runtime.GOARCH), Size: int64(len(payload)),
		BrowserDownloadURL: srv.URL, Digest: "sha256:" + digest,
	}, v, filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("staged executable missing: %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestDownloadRejectsAndPreservesTarget(t *testing.T) {
	installed := filepath.Join(t.TempDir(), "app.exe")
	if err := os.WriteFile(installed, []byte("current executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(installed)
	for _, tc := range []struct {
		name, body string
		status     int
		size       int64
		sum        string
	}{
		{name: "mismatch", body: "other", status: http.StatusOK, sum: strings.Repeat("0", 64)},
		{name: "http error", body: "no", status: http.StatusNotFound, sum: strings.Repeat("0", 64)},
		{name: "oversized metadata", body: "x", status: http.StatusOK, size: maxBinary + 1, sum: strings.Repeat("0", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := Baixar(context.Background(), srv.Client(), Asset{Name: assetNome(runtime.GOARCH), Size: tc.size, BrowserDownloadURL: srv.URL}, tc.sum, t.TempDir())
			if err == nil {
				t.Fatal("expected download error")
			}
			got, _ := os.ReadFile(installed)
			if string(got) != string(before) {
				t.Fatal("failed download changed installed executable")
			}
		})
	}
}
