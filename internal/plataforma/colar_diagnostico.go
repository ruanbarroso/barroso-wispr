//go:build windows || darwin

package plataforma

import (
	"fmt"
	"time"
)

// FocoSnapshot registra somente metadados do app ativo: nunca título, conteúdo
// de campo, nem texto ditado.
type FocoSnapshot struct {
	App           string
	BundleID      string
	PID           int32
	MetadataError string
}

func (f FocoSnapshot) String() string {
	return fmt.Sprintf("app=%q bundle=%q pid=%d metadataError=%q", f.App, f.BundleID, f.PID, f.MetadataError)
}

// DiagnosticoColagem registra em que etapa a entrega falhou, foco, clipboard,
// modificadores e eventos enviados. O SO não confirma que o app inseriu texto.
type DiagnosticoColagem struct {
	Etapa                  string
	FocoAntes              FocoSnapshot
	FocoDepoisDaEspera     FocoSnapshot
	FocoNoEnvio            FocoSnapshot
	FocoDepois             FocoSnapshot
	EsperaModificadores    time.Duration
	ModificadoresPendentes []string
	ClipboardVerificado    bool
	ClipboardSeqAntes      int64
	ClipboardSeqDepois     int64
	ClipboardReadError     string
	ClipboardRestoreError  string
	EventosPaste           int
	EventosDigitacao       int
	Fallback               string
	// VerificacaoCampo (macOS) registra a leitura AXValue do campo focado antes e
	// depois do Command+V e a conclusão: confirmada, inconclusiva ou falhou.
	// Fica vazio no Windows, onde não há leitura equivalente sem injeção.
	VerificacaoCampo   string
	InsercaoConfirmada bool // true só quando o controle focado confirmou o texto via AX
	Duracao            time.Duration
}

func (d DiagnosticoColagem) String() string {
	return fmt.Sprintf("etapa=%q focoAntes={%s} focoDepoisDaEspera={%s} focoNoEnvio={%s} focoDepois={%s} esperaModificadores=%s modificadoresPendentes=%q clipboardVerificado=%t clipboardSeq=%d->%d clipboardReadError=%q clipboardRestoreError=%q eventosPaste=%d eventosDigitacao=%d fallback=%q insercaoConfirmada=%t verificacaoCampo=%q duracao=%s",
		d.Etapa, d.FocoAntes, d.FocoDepoisDaEspera, d.FocoNoEnvio, d.FocoDepois,
		d.EsperaModificadores, d.ModificadoresPendentes, d.ClipboardVerificado,
		d.ClipboardSeqAntes, d.ClipboardSeqDepois, d.ClipboardReadError,
		d.ClipboardRestoreError, d.EventosPaste, d.EventosDigitacao, d.Fallback,
		d.InsercaoConfirmada, d.VerificacaoCampo, d.Duracao)
}
