package blacklist

import (
	_ "embed"
	"os"
	"path/filepath"
)

const generatedDirName = ".vpn-manager"

//go:embed update_blacklist.sh
var embeddedScript []byte

func GeneratedScriptPath(rootDir string) string {
	return filepath.Join(rootDir, generatedDirName, "update_blacklist.sh")
}

func EnsureGeneratedScript(rootDir string) (string, error) {
	scriptPath := GeneratedScriptPath(rootDir)

	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		return "", err
	}

	if err := os.WriteFile(scriptPath, embeddedScript, 0o755); err != nil {
		return "", err
	}

	return scriptPath, nil
}
