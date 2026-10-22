package limpeza

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type chatFake struct {
	out          string
	err          error
	system, user string
}

func (c *chatFake) Chat(_ context.Context, _, system, user string) (string, error) {
	c.system, c.user = system, user
	return c.out, c.err
}

func TestSistemaLevaODicionario(t *testing.T) {
	s := Sistema([]string{"KIS", "Mariana_Ubuntu"})
	if !strings.Contains(s, "  - KIS\n") || !strings.Contains(s, "  - Mariana_Ubuntu\n") {
		t.Fatalf("dicionário ausente:\n%s", s)
	}
}

func TestLimparDevolveSaidaAparada(t *testing.T) {
	c := &chatFake{out: "```\nOlá, KIS.\n```"}
	got, err := Limpar(context.Background(), c, "barroso-quick", []string{"KIS"}, " olá kiss ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Olá, KIS." || c.user != "olá kiss" {
		t.Fatalf("got %q user %q", got, c.user)
	}
}

func TestLimparCaiNoCruQuandoFalha(t *testing.T) {
	got, err := Limpar(context.Background(), &chatFake{err: errors.New("503")}, "m", nil, "texto cru")
	if err == nil || got != "texto cru" {
		t.Fatalf("got %q err %v", got, err)
	}
	got, _ = Limpar(context.Background(), &chatFake{out: "  "}, "m", nil, "texto cru")
	if got != "texto cru" {
		t.Fatalf("vazio deveria cair no cru: %q", got)
	}
}

func TestLimparTextoVazioNaoChama(t *testing.T) {
	c := &chatFake{out: "nunca"}
	got, _ := Limpar(context.Background(), c, "m", nil, "   ")
	if got != "" || c.user != "" {
		t.Fatalf("got %q, chamou=%v", got, c.user != "")
	}
}

func TestApararAspas(t *testing.T) {
	if got := Aparar(`"Oi."`); got != "Oi." {
		t.Fatalf("got %q", got)
	}
}
