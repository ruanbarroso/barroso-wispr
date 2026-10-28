//go:build darwin

package plataforma

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ebitengine/purego/objc"
)

// AXUIElementCopyAttributeValue error codes.
const (
	axErrorSuccess         = 0
	axErrorIllegalArgument = -25201
	axErrorInvalidElement  = -25202
	axErrorCannotComplete  = -25204
	axErrorNotImplemented  = -25208
	axErrorAPIDisabled     = -25211
	axErrorNoValue         = -25212

	axTimeoutSeconds = 0.5
	maxAXStringBytes = 8 << 20
)

const appServicesPath = "/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices"

const (
	axAttrFocusedUIElement = "AXFocusedUIElement"
	axAttrRole             = "AXRole"
	axAttrSubrole          = "AXSubrole"
	axAttrIsEditable       = "AXIsEditable"
	axAttrValue            = "AXValue"

	axRoleTextField   = "AXTextField"
	axRoleTextArea    = "AXTextArea"
	axRoleComboBox    = "AXComboBox"
	axRoleSearchField = "AXSearchField"
	axRoleWebArea     = "AXWebArea"

	axSubroleSecureField = "AXSecureTextField"
)

// campoSnapshot keeps the AX value only in memory, for comparison with the
// dictated text. The diagnostic summary deliberately contains no field value.
type campoSnapshot struct {
	Encontrado bool
	Papel      string
	Editavel   bool
	Runas      int
	Valor      string
	Erro       string
}

func (c campoSnapshot) resumo() string {
	return fmt.Sprintf("encontrado=%t papel=%q editavel=%t runas=%d erro=%q",
		c.Encontrado, c.Papel, c.Editavel, c.Runas, c.Erro)
}

// verificarCampoFocado reads the focused UI element in the target app and
// checks its AXValue. Neither the value nor the dictated text is logged.
func verificarCampoFocado(pid int32) campoSnapshot {
	c := campoSnapshot{}
	if pid == 0 {
		c.Erro = "PID do destino desconhecido"
		return c
	}
	if err := initAX(); err != nil {
		c.Erro = err.Error()
		return c
	}
	if !axIsProcessTrusted() {
		c.Erro = "sem permissão AX (Acessibilidade)"
		return c
	}

	comPool(func() {
		app := axCreateApplication(pid)
		if app == 0 {
			c.Erro = "AXUIElementCreateApplication falhou"
			return
		}
		defer cfReleaseRef(app)
		axSetMessagingTimeout(app)

		elemento, code := axCopyAttribute(app, axAttrFocusedUIElement)
		if code != axErrorSuccess || elemento == 0 {
			c.Erro = "AXFocusedUIElement: " + axErrorName(code)
			return
		}
		defer cfReleaseRef(elemento)
		c.Encontrado = true
		axSetMessagingTimeout(elemento)

		c.Papel, _ = axStringAttribute(elemento, axAttrRole)
		if subrole, ok := axStringAttribute(elemento, axAttrSubrole); ok && subrole != "" {
			c.Papel += "/" + subrole
			if subrole == axSubroleSecureField {
				c.Erro = "campo seguro; valor omitido por privacidade"
				return
			}
		}
		if value, code := axCopyAttribute(elemento, axAttrIsEditable); code == axErrorSuccess && value != 0 {
			c.Editavel = objc.Send[bool](objc.ID(value), objc.RegisterName("boolValue"))
			cfReleaseRef(value)
		}
		if !c.Editavel {
			switch c.Papel {
			case axRoleTextField, axRoleTextArea, axRoleComboBox, axRoleSearchField:
				c.Editavel = true // AXIsEditable is optional on native/web text controls.
			}
		}

		value, code := axCopyAttribute(elemento, axAttrValue)
		if code != axErrorSuccess || value == 0 {
			c.Erro = "AXValue: " + axErrorName(code)
			return
		}
		defer cfReleaseRef(value)
		var ok bool
		c.Valor, ok = axStringValue(value)
		if !ok {
			c.Erro = "AXValue não é texto"
			return
		}
		c.Runas = len([]rune(c.Valor))
	})
	return c
}

type verificacaoCampo struct {
	Antes, Depois        string
	Mudou, TextoPresente bool
	Resultado            string
}

func (v verificacaoCampo) resumo() string {
	return fmt.Sprintf("campoAntes={%s} campoDepois={%s} campoMudou=%t textoPresente=%t resultado=%q",
		v.Antes, v.Depois, v.Mudou, v.TextoPresente, v.Resultado)
}

func compararCampo(antes, depois campoSnapshot, texto string) verificacaoCampo {
	v := verificacaoCampo{
		Antes: antes.resumo(), Depois: depois.resumo(),
		Mudou: antes.Valor != depois.Valor || antes.Papel != depois.Papel || antes.Encontrado != depois.Encontrado,
	}
	texto = strings.TrimRight(texto, " \t\r\n")
	if texto == "" {
		v.Resultado = "inconclusiva: texto vazio"
		return v
	}
	// A value-count delta avoids a false positive if the same phrase already
	// existed elsewhere in the field before the paste.
	if depois.Encontrado && strings.Count(depois.Valor, texto) > strings.Count(antes.Valor, texto) {
		v.TextoPresente = true
		v.Resultado = "confirmada: ditado apareceu no AXValue do campo focado"
		return v
	}
	if antes.Erro != "" || depois.Erro != "" {
		v.Resultado = "inconclusiva: leitura AX falhou (antes=" + erroAXSeguro(antes.Erro) + ", depois=" + erroAXSeguro(depois.Erro) + ")"
		return v
	}
	if !depois.Encontrado {
		v.Resultado = "inconclusiva: app não expôs campo com foco"
		return v
	}
	if !depois.Editavel {
		v.Resultado = "inconclusiva: elemento focado não foi identificado como editável"
		return v
	}
	v.Resultado = "falhou: campo editável acessível, mas o ditado não apareceu após Command+V"
	return v
}

func erroAXSeguro(erro string) string {
	if erro == "" {
		return "nenhum"
	}
	return erro
}

var (
	axOnce       sync.Once
	axInitErr    error
	axIsTrusted  func() bool
	axCreateApp  func(int32) uintptr
	axCopyValue  func(uintptr, uintptr, *uintptr) int32
	axSetTimeout func(uintptr, float32) int32
)

func initAX() error {
	axOnce.Do(func() {
		for _, entry := range []struct {
			name string
			fn   any
		}{
			{"AXIsProcessTrusted", &axIsTrusted},
			{"AXUIElementCreateApplication", &axCreateApp},
			{"AXUIElementCopyAttributeValue", &axCopyValue},
			{"AXUIElementSetMessagingTimeout", &axSetTimeout},
		} {
			if err := registerDarwinSymbol(appServicesPath, entry.name, entry.fn); err != nil {
				axInitErr = err
				return
			}
		}
	})
	return axInitErr
}

func axSetMessagingTimeout(element uintptr) {
	axSetTimeout(element, axTimeoutSeconds)
}

func axIsProcessTrusted() bool { return axIsTrusted() }

func axCreateApplication(pid int32) uintptr { return axCreateApp(pid) }

func axCopyAttribute(element uintptr, name string) (uintptr, int32) {
	key := nsString(name) // NSString is toll-free bridged to CFString.
	var value uintptr
	code := axCopyValue(element, uintptr(key), &value)
	return value, code
}

func axStringAttribute(element uintptr, name string) (string, bool) {
	value, code := axCopyAttribute(element, name)
	if code != axErrorSuccess || value == 0 {
		return "", false
	}
	defer cfReleaseRef(value)
	return axStringValue(value)
}

func axStringValue(value uintptr) (string, bool) {
	id := objc.ID(value)
	if !objc.Send[bool](id, objc.RegisterName("isKindOfClass:"), objc.GetClass("NSString")) {
		return "", false
	}
	p := uintptr(id.Send(objc.RegisterName("UTF8String")))
	if p == 0 {
		return "", true
	}
	var bytes []byte
	for n := uintptr(0); n < maxAXStringBytes; n++ {
		b := *(*byte)(cPointer(p + n))
		if b == 0 {
			return string(bytes), true
		}
		bytes = append(bytes, b)
	}
	return string(bytes), false
}

func axErrorName(code int32) string {
	switch code {
	case axErrorSuccess:
		return "kAXErrorSuccess"
	case axErrorNoValue:
		return "kAXErrorNoValue"
	case axErrorAPIDisabled:
		return "kAXErrorAPIDisabled"
	case axErrorInvalidElement:
		return "kAXErrorInvalidUIElement"
	case axErrorNotImplemented:
		return "kAXErrorNotImplemented"
	case axErrorIllegalArgument:
		return "kAXErrorIllegalArgument"
	case axErrorCannotComplete:
		return "kAXErrorCannotComplete"
	default:
		return fmt.Sprintf("AXError(%d)", code)
	}
}
