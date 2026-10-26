//go:build darwin

package atualizacao

func assetNome(arch string) string {
	switch arch {
	case "amd64":
		return "barroso-wispr-darwin-amd64"
	case "arm64":
		return "barroso-wispr-darwin-arm64"
	default:
		return ""
	}
}

func updateTempPattern() string { return "wispr-update-*" }
