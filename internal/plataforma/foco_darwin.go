//go:build darwin

package plataforma

import "github.com/ebitengine/purego/objc"

// FocoAtual registra somente o app em primeiro plano (nome, bundle ID, PID),
// sem título de janela nem conteúdo dos controles.
func FocoAtual() FocoSnapshot {
	f := FocoSnapshot{}
	if err := loadDarwinFrameworks(appKitPath); err != nil {
		f.MetadataError = err.Error()
		return f
	}
	comPool(func() {
		ws := objc.ID(objc.GetClass("NSWorkspace")).Send(objc.RegisterName("sharedWorkspace"))
		if ws == 0 {
			f.MetadataError = "NSWorkspace indisponível"
			return
		}
		app := ws.Send(objc.RegisterName("frontmostApplication"))
		if app == 0 {
			f.MetadataError = "sem app em primeiro plano"
			return
		}
		if s := app.Send(objc.RegisterName("localizedName")); s != 0 {
			if p := s.Send(objc.RegisterName("UTF8String")); p != 0 {
				f.App = goString(uintptr(p))
			}
		}
		if s := app.Send(objc.RegisterName("bundleIdentifier")); s != 0 {
			if p := s.Send(objc.RegisterName("UTF8String")); p != 0 {
				f.BundleID = goString(uintptr(p))
			}
		}
		f.PID = objc.Send[int32](app, objc.RegisterName("processIdentifier"))
	})
	return f
}
