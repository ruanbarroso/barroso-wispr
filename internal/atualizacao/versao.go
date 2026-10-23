package atualizacao

import (
	"fmt"
	"strconv"
	"strings"
)

// Versao é uma versão estável do wispr: vMAJOR.MINOR.PATCH-bw.N. O build `dev`
// e qualquer outro formato não participam da atualização.
type Versao struct {
	Maior, Menor, Patch, N int
}

// ParseVersao aceita "v0.1.0-bw.3". Sem o v também serve (saída de -version).
func ParseVersao(s string) (Versao, bool) {
	s = strings.TrimPrefix(s, "v")
	base, suf, ok := strings.Cut(s, "-bw.")
	if !ok {
		return Versao{}, false
	}
	partes := strings.Split(base, ".")
	if len(partes) != 3 {
		return Versao{}, false
	}
	var v Versao
	for i, p := range partes {
		if !digits(p) || (len(p) > 1 && p[0] == '0') {
			return Versao{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return Versao{}, false
		}
		switch i {
		case 0:
			v.Maior = n
		case 1:
			v.Menor = n
		case 2:
			v.Patch = n
		}
	}
	if !digits(suf) {
		return Versao{}, false
	}
	n, err := strconv.Atoi(suf)
	if err != nil {
		return Versao{}, false
	}
	v.N = n
	return v, true
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Tag devolve a tag da release: "v" + números.
func (v Versao) Tag() string {
	return fmt.Sprintf("v%d.%d.%d-bw.%d", v.Maior, v.Menor, v.Patch, v.N)
}

// Antes informa se v é anterior a outra versão.
func (v Versao) Antes(o Versao) bool {
	if v.Maior != o.Maior {
		return v.Maior < o.Maior
	}
	if v.Menor != o.Menor {
		return v.Menor < o.Menor
	}
	if v.Patch != o.Patch {
		return v.Patch < o.Patch
	}
	return v.N < o.N
}

// String é o formato de -version.
func (v Versao) String() string {
	return fmt.Sprintf("%d.%d.%d-bw.%d", v.Maior, v.Menor, v.Patch, v.N)
}
