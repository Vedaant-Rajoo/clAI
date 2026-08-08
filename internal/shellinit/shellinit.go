package shellinit

import (
	"embed"
	"fmt"
)

// scripts contains the shell adapter sources shipped with this version of clai.
//
//go:embed scripts/clai.bash scripts/clai.fish scripts/clai.zsh
var scripts embed.FS

var scriptFiles = map[string]string{
	"bash": "scripts/clai.bash",
	"fish": "scripts/clai.fish",
	"zsh":  "scripts/clai.zsh",
}

// Supported reports whether shell has a shipped adapter script. It is the
// single source of truth for the supported-shell set.
func Supported(shell string) bool {
	_, ok := scriptFiles[shell]
	return ok
}

// Script returns the initialization script for shell.
func Script(shell string) (string, error) {
	name, ok := scriptFiles[shell]
	if !ok {
		return "", fmt.Errorf("unsupported shell %q (supported: bash, fish, zsh)", shell)
	}

	contents, err := scripts.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("read embedded %s initialization script: %w", shell, err)
	}

	return string(contents), nil
}
