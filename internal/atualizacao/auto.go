// Package atualizacao mantém o binário do wispr na última release estável:
// checa o GitHub uma vez por dia, baixa com verificação de SHA-256 e
// `-version`, e troca o executável por um helper quando o app está ocioso.
package atualizacao

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Deps liga o atualizador ao app sem ele conhecer o app.
type Deps struct {
	Versao     string
	Home       string
	Log        io.Writer
	Ocioso     func() bool
	PedirTroca func(func() bool)
}

// Rodar checa imediatamente e depois a cada 24 h. Erros só logam: o ditado
// nunca é afetado.
func Rodar(ctx context.Context, d Deps) {
	atual, ok := ParseVersao(d.Versao)
	if !ok {
		logf(d.Log, "atualização: versão %q não reconhecida (dev?); sem auto-update", d.Versao)
		return
	}
	if assetNome(runtime.GOARCH) == "" {
		logf(d.Log, "atualização: arquitetura %s sem asset; sem auto-update", runtime.GOARCH)
		return
	}
	for {
		checar(ctx, d, atual)
		select {
		case <-ctx.Done():
			return
		case <-time.After(Intervalo):
		}
	}
}

func checar(ctx context.Context, d Deps, atual Versao) {
	client := &http.Client{Timeout: 30 * time.Second}
	_, asset, v, achou, err := Buscar(ctx, client, atual, runtime.GOARCH)
	if err != nil {
		logf(d.Log, "atualização: %v", err)
		return
	}
	if !achou {
		return // já está na última; silencioso para não poluir o log diário
	}
	dir := filepath.Join(d.Home, "update")
	staged, err := Estagiar(ctx, client, asset, v, dir)
	if err != nil {
		logf(d.Log, "atualização %s: %v", v.Tag(), err)
		return
	}
	logf(d.Log, "atualização %s verificada (SHA-256 e -version ok), aguardando o ditado terminar", v.Tag())
	d.PedirTroca(func() bool {
		return PrepararTroca(staged, os.Getpid(), os.Args[1:], cwd(), d.Log)
	})
}

func cwd() string {
	dir, err := os.Getwd()
	if err != nil {
		if exe, err := os.Executable(); err == nil {
			return filepath.Dir(exe)
		}
		return "."
	}
	return dir
}

func logf(log io.Writer, format string, args ...any) {
	if log != nil {
		fmt.Fprintf(log, time.Now().Format("15:04:05.000")+" "+format+"\n", args...)
	}
}

// LimparBackupsAoIniciar apaga os backups .old-* deixados por updates
// anteriores: neste ponto o exe antigo já não está em execução.
func LimparBackupsAoIniciar() {
	if exe, err := os.Executable(); err == nil {
		LimparBackups(exe)
	}
}
