//go:build darwin

package plataforma

import (
	"strings"
	"testing"
)

func campoBase() campoSnapshot {
	return campoSnapshot{Encontrado: true, Papel: axRoleTextField, Editavel: true, Valor: "prefixo "}
}

func TestCompararCampoConfirmaTextoPresente(t *testing.T) {
	antes := campoBase()
	depois := antes
	depois.Valor += "ditado de teste"
	v := compararCampo(antes, depois, "ditado de teste")
	if !v.TextoPresente || !v.Mudou || !strings.HasPrefix(v.Resultado, "confirmada:") {
		t.Fatalf("resultado inesperado: %+v", v)
	}
}

// A mesma frase já existente no campo não pode virar confirmação: só uma
// contagem maior de ocorrências confirma a colagem.
func TestCompararCampoNaoConfirmaFraseJaExistente(t *testing.T) {
	antes := campoBase()
	antes.Valor += "ditado de teste outro"
	depois := antes
	v := compararCampo(antes, depois, "ditado de teste")
	if v.TextoPresente || !strings.HasPrefix(v.Resultado, "falhou:") {
		t.Fatalf("valor inalterado deveria falhar: %+v", v)
	}
}

func TestCompararCampoLeituraAXFalhaEhInconclusiva(t *testing.T) {
	antes := campoBase()
	antes.Erro = "sem permissão AX (Acessibilidade)"
	depois := antes
	v := compararCampo(antes, depois, "ditado")
	if v.TextoPresente || !strings.HasPrefix(v.Resultado, "inconclusiva:") {
		t.Fatalf("leitura AX falha deveria ser inconclusiva: %+v", v)
	}
}

func TestCompararCampoSemElementoFocadoEhInconclusiva(t *testing.T) {
	antes := campoSnapshot{Encontrado: false, Erro: "nenhum atributo AXFocusedUIElement"}
	v := compararCampo(antes, antes, "ditado")
	if !strings.HasPrefix(v.Resultado, "inconclusiva:") {
		t.Fatalf("esperava inconclusiva, resultado = %q", v.Resultado)
	}
}

func TestCompararCampoNaoEditavelEhInconclusiva(t *testing.T) {
	antes := campoSnapshot{Encontrado: true, Papel: "AXStaticText", Editavel: false, Valor: "texto estático"}
	v := compararCampo(antes, antes, "ditado")
	if !strings.HasPrefix(v.Resultado, "inconclusiva:") {
		t.Fatalf("esperava inconclusiva para não editável: %q", v.Resultado)
	}
}

func TestResumoVerificacaoNaoVazaConteudo(t *testing.T) {
	antes := campoBase()
	antes.Valor = "ASENHA-MUITO-SECRETA no inicio "
	depois := antes
	depois.Valor += "DITADO-SECRETO"
	v := compararCampo(antes, depois, "DITADO-SECRETO")
	resumo := v.resumo()
	for _, secreto := range []string{"ASENHA-MUITO-SECRETA", "DITADO-SECRETO"} {
		if strings.Contains(resumo, secreto) {
			t.Fatalf("resumo vazou conteúdo %q: %q", secreto, resumo)
		}
	}
	if !strings.Contains(resumo, "textoPresente=true") {
		t.Fatalf("resumo deveria registrar confirmação: %q", resumo)
	}
	if resumoCampo := antes.resumo(); strings.Contains(resumoCampo, "ASENHA-MUITO-SECRETA") {
		t.Fatalf("snapshot vazou valor do campo: %q", resumoCampo)
	}
}

func TestAXErrorName(t *testing.T) {
	for code, want := range map[int32]string{
		axErrorSuccess:         "kAXErrorSuccess",
		axErrorAPIDisabled:     "kAXErrorAPIDisabled",
		axErrorNoValue:         "kAXErrorNoValue",
		axErrorCannotComplete:  "kAXErrorCannotComplete",
		axErrorIllegalArgument: "kAXErrorIllegalArgument",
		axErrorNotImplemented:  "kAXErrorNotImplemented",
		-12345:                 "AXError(-12345)",
	} {
		if got := axErrorName(code); got != want {
			t.Errorf("axErrorName(%d) = %q, want %q", code, got, want)
		}
	}
}
