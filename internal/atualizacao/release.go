//go:build windows || darwin

package atualizacao

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	Owner      = "ruanbarroso"
	Repository = "barroso-wispr"
	Intervalo  = 24 * time.Hour
	maxAPI     = 2 << 20
	maxChecks  = 1 << 20
	maxBinary  = 64 << 20
)

var hashRE = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// Release representa os campos usados da API do GitHub.
type Release struct {
	TagName    string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

type Asset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	URL                string `json:"url"`
	Digest             string `json:"digest"`
}

// SelecionaRelease escolhe a release estável mais nova, com o asset exato.
func SelecionaRelease(releases []Release, atual Versao, arch string) (Release, Asset, Versao, bool) {
	nome := assetNome(arch)
	if nome == "" {
		return Release{}, Asset{}, Versao{}, false
	}
	var melhor Release
	var melhorAsset Asset
	var melhorV Versao
	achou := false
	for _, r := range releases {
		if r.Draft || r.Prerelease {
			continue
		}
		v, ok := ParseVersao(r.TagName)
		if !ok || !atual.Antes(v) {
			continue
		}
		var asset Asset
		for _, a := range r.Assets {
			if a.Name == nome {
				asset = a
				break
			}
		}
		if asset.Name == "" || (asset.Size > 0 && asset.Size > maxBinary) {
			continue
		}
		if !achou || melhorV.Antes(v) {
			melhor, melhorAsset, melhorV, achou = r, asset, v, true
		}
	}
	return melhor, melhorAsset, melhorV, achou
}

// Buscar consulta as releases públicas e escolhe a melhor versão aplicável.
func Buscar(ctx context.Context, client *http.Client, atual Versao, arch string) (Release, Asset, Versao, bool, error) {
	return BuscarURL(ctx, client, "https://api.github.com/repos/"+Owner+"/"+Repository+"/releases?per_page=100", atual, arch)
}

// BuscarURL consulta um endpoint compatível com a API de releases do GitHub.
func BuscarURL(ctx context.Context, client *http.Client, endpoint string, atual Versao, arch string) (Release, Asset, Versao, bool, error) {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Release{}, Asset{}, Versao{}, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, Asset{}, Versao{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, Asset{}, Versao{}, false, fmt.Errorf("GitHub releases: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPI+1))
	if err != nil {
		return Release{}, Asset{}, Versao{}, false, err
	}
	if len(data) > maxAPI {
		return Release{}, Asset{}, Versao{}, false, errors.New("GitHub releases response too large")
	}
	var releases []Release
	if err := json.Unmarshal(data, &releases); err != nil {
		return Release{}, Asset{}, Versao{}, false, err
	}
	r, a, v, ok := SelecionaRelease(releases, atual, arch)
	return r, a, v, ok, nil
}

// SomaSHA256 interpreta o formato sha256sum e exige uma entrada única do asset.
func SomaSHA256(data []byte, nome string) (string, error) {
	if len(data) > maxChecks {
		return "", errors.New("SHA256SUMS grande demais")
	}
	var soma string
	for _, linha := range strings.Split(string(data), "\n") {
		campos := strings.Fields(linha)
		if len(campos) != 2 {
			if strings.TrimSpace(linha) != "" {
				return "", fmt.Errorf("linha SHA256SUMS inválida")
			}
			continue
		}
		arquivo := strings.TrimPrefix(campos[1], "*")
		if !hashRE.MatchString(campos[0]) {
			return "", errors.New("hash SHA256 inválido")
		}
		if arquivo == nome {
			if soma != "" {
				return "", errors.New("hash duplicado em SHA256SUMS")
			}
			soma = strings.ToLower(campos[0])
		}
	}
	if soma == "" {
		return "", fmt.Errorf("SHA256SUMS não contém %s", nome)
	}
	return soma, nil
}

// Baixar grava em temp com limite, valida tamanho e SHA-256 e remove falhas.
func Baixar(ctx context.Context, client *http.Client, asset Asset, soma, dir string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if asset.Name != assetNome(runtime.GOARCH) {
		return "", errors.New("asset incompatível com a arquitetura")
	}
	if asset.Size < 0 || asset.Size > maxBinary || !hashRE.MatchString(soma) {
		return "", errors.New("tamanho ou SHA256 inválido")
	}
	if asset.BrowserDownloadURL == "" {
		return "", errors.New("URL do asset ausente")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: %s", resp.Status)
	}
	if resp.ContentLength > maxBinary || (asset.Size > 0 && resp.ContentLength >= 0 && resp.ContentLength != asset.Size) {
		return "", errors.New("tamanho do download inesperado")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, updateTempPattern())
	if err != nil {
		return "", err
	}
	path := f.Name()
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxBinary+1))
	if err != nil {
		return "", err
	}
	if n == 0 || n > maxBinary || (asset.Size > 0 && n != asset.Size) {
		return "", errors.New("tamanho do binário inesperado")
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(soma) {
		return "", errors.New("SHA-256 do binário não confere")
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

// Estagiar baixa, verifica digest e executa -version antes de liberar o arquivo.
func Estagiar(ctx context.Context, client *http.Client, asset Asset, v Versao, dir string) (string, error) {
	var esperado string
	if digest := strings.TrimPrefix(asset.Digest, "sha256:"); hashRE.MatchString(digest) {
		esperado = digest
	} else {
		sums := BuscarSomas(asset)
		p, err := BaixarChecksums(ctx, client, sums, dir)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(p)
		_ = os.Remove(p)
		if err != nil {
			return "", err
		}
		esperado, err = SomaSHA256(data, asset.Name)
		if err != nil {
			return "", err
		}
	}
	path, err := Baixar(ctx, client, asset, esperado, dir)
	if err != nil {
		return "", err
	}
	// CreateTemp grava 0600: no Unix o candidato não executaria.
	if err := os.Chmod(path, 0o755); err != nil {
		os.Remove(path)
		return "", err
	}
	cmd := exec.Command(path, "-version")
	out, err := cmd.Output()
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("binário staged -version: %w", err)
	}
	got, ok := ParseVersao(strings.TrimSpace(string(out)))
	if !ok || got != v {
		os.Remove(path)
		return "", fmt.Errorf("binário staged informa versão inesperada: %q", strings.TrimSpace(string(out)))
	}
	return path, nil
}

func BaixarChecksums(ctx context.Context, client *http.Client, asset Asset, dir string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("SHA256SUMS download: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxChecks+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxChecks {
		return "", errors.New("SHA256SUMS grande demais")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return "", err
	}
	return p, nil
}

// BuscarSomas baixa SHA256SUMS da mesma release pelo caminho do executável.
func BuscarSomas(asset Asset) Asset {
	a := asset
	a.Name = "SHA256SUMS"
	a.BrowserDownloadURL = strings.TrimSuffix(asset.BrowserDownloadURL, asset.Name) + "SHA256SUMS"
	a.Digest = ""
	return a
}
