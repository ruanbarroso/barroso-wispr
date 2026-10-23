//go:build windows

// barroso-wispr: ditado por voz ao vivo. Segure Ctrl+Alt (esquerdos) e fale;
// solte e o texto é digitado onde está o cursor. Dois toques rápidos travam a
// gravação; um toque encerra.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/ruanbarroso/barroso-wispr/internal/app"
	"github.com/ruanbarroso/barroso-wispr/internal/config"
	"github.com/ruanbarroso/barroso-wispr/internal/gateway"
	"github.com/ruanbarroso/barroso-wispr/internal/plataforma"
)

// Versao é preenchida pelo build de release (-X main.Versao=...).
var Versao = "dev"

const dicionarioInicial = `# Dicionário do barroso-wispr: um termo por linha (nomes, empresas, jargões).
# A transcrição passa pelo barroso-quick, que usa esta grafia exata.
# Linhas com # são comentários.
`

func main() {
	versao := flag.Bool("version", false, "mostra a versão")
	arquivo := flag.String("arquivo", "", "dita um PCM s16le 16 kHz mono deste arquivo e imprime o texto (diagnóstico, sem microfone nem teclado)")
	flag.Parse()
	if *versao {
		fmt.Println(Versao)
		return
	}

	pref := config.Carregar()
	dest, err := config.Resolver(pref)
	if err != nil {
		fmt.Fprintln(os.Stderr, "barroso-wispr:", err)
		os.Exit(1)
	}
	if _, err := os.Stat(pref.Dicionario); os.IsNotExist(err) {
		_ = os.MkdirAll(filepath.Dir(pref.Dicionario), 0o700)
		_ = os.WriteFile(pref.Dicionario, []byte(dicionarioInicial), 0o600)
	}

	_ = os.MkdirAll(config.Home(), 0o700)
	logf, _ := os.OpenFile(filepath.Join(config.Home(), "wispr.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	var log io.Writer = os.Stderr
	if logf != nil {
		defer logf.Close()
		log = io.MultiWriter(os.Stderr, logf)
	}

	gw := gateway.Novo(dest.BaseURL, dest.APIKey)
	onde := dest.BaseURL
	if dest.Provider == config.ProviderGoogle {
		gw = gateway.NovoGoogle(dest.APIKey, dest.Modelo)
		onde = "Google AI Studio"
	}
	var mic app.Microfone = &plataforma.Microfone{}
	digitar := plataforma.Colar
	var feito chan struct{}
	if *arquivo != "" {
		pcm, err := os.ReadFile(*arquivo)
		if err != nil {
			fmt.Fprintln(os.Stderr, "barroso-wispr:", err)
			os.Exit(1)
		}
		mic = &app.MicArquivo{PCM: pcm, Bloco: plataforma.BlocoBytes}
		feito = make(chan struct{})
		digitar = func(s string) error { fmt.Println(s); close(feito); return nil }
	}
	a := app.Novo(app.Deps{
		Gateway:    gw,
		Chat:       gw,
		Abrir:      app.AbrirLive,
		Microfone:  mic,
		Digitar:    digitar,
		Dicionario: func() []string { return config.Dicionario(pref.Dicionario) },
		ModeloVoz:  dest.Modelo,
		ModeloQuik: dest.QuickModel,
		BeepReady:  plataforma.BeepReady,
		BeepDone:   plataforma.BeepDone,
		Log:        log,
	})

	a.Aquecer()

	if *arquivo != "" {
		// Com o app aberto o token já está pronto quando o usuário aperta;
		// o diagnóstico espera o aquecimento para medir esse caso real.
		time.Sleep(3 * time.Second)
		// Simula segurar o atalho pelo tempo do áudio e soltar.
		dur := time.Duration(len(mic.(*app.MicArquivo).PCM)/(plataforma.TaxaAmostras*2)) * time.Second
		a.Simular(dur + 300*time.Millisecond)
		select {
		case <-feito:
		case <-time.After(30 * time.Second):
			fmt.Fprintln(os.Stderr, "barroso-wispr: sem texto em 30 s")
			os.Exit(1)
		}
		return
	}

	teclado := &plataforma.Teclado{Pressionou: a.Pressionou, Soltou: a.Soltou, OutraTecla: a.OutraTecla}
	go func() {
		for range time.Tick(25 * time.Millisecond) {
			a.Tick()
		}
	}()
	sinal := make(chan os.Signal, 1)
	signal.Notify(sinal, os.Interrupt)
	go func() { <-sinal; teclado.Parar() }()

	fmt.Fprintf(log, "barroso-wispr %s — segure Ctrl+Alt para ditar (2 toques = trava). Gateway %s, voz %s, limpeza %s. Dicionário: %s. Ctrl+C sai.\n",
		Versao, onde, dest.Modelo, dest.QuickModel, pref.Dicionario)
	if err := teclado.Rodar(); err != nil {
		fmt.Fprintln(os.Stderr, "barroso-wispr: hook de teclado:", err)
		os.Exit(1)
	}
}
