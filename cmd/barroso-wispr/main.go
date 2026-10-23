//go:build windows

// barroso-wispr: ditado por voz ao vivo. Segure Ctrl+Alt (esquerdos) e fale;
// solte e o texto é digitado onde está o cursor. Dois toques rápidos travam a
// gravação; um toque encerra.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ruanbarroso/barroso-wispr/internal/app"
	"github.com/ruanbarroso/barroso-wispr/internal/atualizacao"
	"github.com/ruanbarroso/barroso-wispr/internal/config"
	"github.com/ruanbarroso/barroso-wispr/internal/gateway"
	"github.com/ruanbarroso/barroso-wispr/internal/instalacao"
	"github.com/ruanbarroso/barroso-wispr/internal/plataforma"
)

// Versao é preenchida pelo build de release (-X main.Versao=...).
var Versao = "dev"

const dicionarioInicial = `# Dicionário do barroso-wispr: um termo por linha (nomes, empresas, jargões).
# A transcrição passa pelo barroso-quick, que usa esta grafia exata.
# Linhas com # são comentários.
`

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--wispr-update-helper" {
		os.Exit(atualizacao.RunHelper(os.Args[2:]))
	}
	os.Exit(run())
}

func run() int {
	readyPath := os.Getenv("BARROSO_WISPR_UPDATE_READY")
	if readyPath != "" {
		_ = os.Unsetenv("BARROSO_WISPR_UPDATE_READY")
	}
	versao := flag.Bool("version", false, "mostra a versão")
	instalar := flag.Bool("install", false, "instala o app e o inicia")
	arquivo := flag.String("arquivo", "", "dita um PCM s16le 16 kHz mono deste arquivo e imprime o texto (diagnóstico, sem microfone nem teclado)")
	flag.Parse()
	if *versao {
		fmt.Println(Versao)
		return 0
	}

	if *instalar {
		if err := instalacao.Install(instalacao.Deps{}); err != nil {
			// Do not create user files before Install has checked elevation.
			log := instalarLogSeExiste()
			mostrarErro(log, "Não foi possível instalar o Barroso Wispr", err)
			return 1
		}
		return 0 // Install launched the installed copy; do not launch this source copy.
	}

	_ = os.MkdirAll(config.Home(), 0o700)
	logf, logErr := os.OpenFile(filepath.Join(config.Home(), "wispr.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	var log io.Writer = io.Discard
	if logErr == nil {
		defer logf.Close()
		log = logf
	} else {
		mostrarMensagem("Barroso Wispr", "Não foi possível abrir wispr.log.\n\n"+logErr.Error())
	}

	pref := config.Carregar()
	dest, err := config.Resolver(pref)
	if err != nil {
		mostrarErro(log, "Não foi possível iniciar o Barroso Wispr", err)
		return 1
	}
	if _, err := os.Stat(pref.Dicionario); os.IsNotExist(err) {
		_ = os.MkdirAll(filepath.Dir(pref.Dicionario), 0o700)
		_ = os.WriteFile(pref.Dicionario, []byte(dicionarioInicial), 0o600)
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
			mostrarErro(log, "Não foi possível ler o arquivo de áudio", err)
			return 1
		}
		mic = &app.MicArquivo{PCM: pcm, Bloco: plataforma.BlocoBytes}
		feito = make(chan struct{})
		digitar = func(s string) error { fmt.Println(s); close(feito); return nil }
	}
	var indicador *plataforma.Indicador
	if *arquivo == "" {
		indicador = plataforma.NewIndicador()
		if err := indicador.Start(); err != nil {
			mostrarErro(log, "Não foi possível iniciar o indicador de gravação", err)
			return 1
		}
		defer indicador.Close()
	}

	deps := app.Deps{
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
	}
	if indicador != nil {
		deps.EstadoGravacao = indicador.SetActive
	}
	a := app.Novo(deps)

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
			err := fmt.Errorf("sem texto em 30 s")
			mostrarErro(log, "A transcrição não foi concluída", err)
			return 1
		}
		return 0
	}

	teclado := &plataforma.Teclado{Pressionou: a.Pressionou, Soltou: a.Soltou, OutraTecla: a.OutraTecla}
	a.DefinirParada(teclado.Parar)
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
	var updateCtx context.Context
	if *arquivo == "" {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		updateCtx = ctx
	}
	teclado.OnReady = func() {
		if readyPath != "" {
			_ = os.WriteFile(readyPath, []byte("ready"), 0o600)
		}
		// A helper-launched candidate keeps its backup until a later normal start.
		if readyPath == "" {
			atualizacao.LimparBackupsAoIniciar()
		}
		if updateCtx != nil {
			// O hook já está instalado: a partir daqui uma troca pedida pode
			// encerrar o processo com segurança.
			go atualizacao.Rodar(updateCtx, atualizacao.Deps{
				Versao:     Versao,
				Home:       config.Home(),
				Log:        log,
				Ocioso:     a.Ocioso,
				PedirTroca: a.PedirTroca,
			})
		}
	}
	if err := teclado.Rodar(); err != nil {
		mostrarErro(log, "Não foi possível iniciar o atalho global de teclado", err)
		return 1
	}
	return 0
}

const (
	mbOK            = 0x00000000
	mbIconError     = 0x00000010
	mbSetForeground = 0x00010000
)

var procMessageBoxW = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")

func mostrarMensagem(titulo, mensagem string) {
	t, _ := windows.UTF16PtrFromString(titulo)
	m, _ := windows.UTF16PtrFromString(mensagem)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbOK|mbIconError|mbSetForeground)
}

func instalarLogSeExiste() io.Writer {
	f, err := os.OpenFile(filepath.Join(config.Home(), "wispr.log"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return io.Discard
	}
	defer f.Close()
	return logWriter{path: filepath.Join(config.Home(), "wispr.log")}
}

type logWriter struct{ path string }

func (w logWriter) Write(p []byte) (int, error) {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.Write(p)
}

func mostrarErro(log io.Writer, mensagem string, err error) {
	if log != nil {
		fmt.Fprintf(log, "barroso-wispr: %s: %v\n", mensagem, err)
	}
	mostrarMensagem("Barroso Wispr", mensagem+".\n\n"+err.Error())
}
