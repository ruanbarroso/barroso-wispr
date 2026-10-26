//go:build windows

package atualizacao

func assetNome(arch string) string {
	switch arch {
	case "amd64":
		return "barroso-wispr-windows-amd64.exe"
	case "arm64":
		return "barroso-wispr-windows-arm64.exe"
	default:
		return ""
	}
}

func updateTempPattern() string { return "wispr-update-*.exe" }
