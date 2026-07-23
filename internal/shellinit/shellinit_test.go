package shellinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScriptMatchesShellSource(t *testing.T) {
	tests := []struct {
		shell string
		path  string
	}{
		{shell: "bash", path: filepath.Join("..", "..", "shell", "bash", "clai.bash")},
		{shell: "fish", path: filepath.Join("..", "..", "shell", "fish", "clai.fish")},
		{shell: "zsh", path: filepath.Join("..", "..", "shell", "zsh", "clai.zsh")},
	}

	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			want, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("read source script: %v", err)
			}

			got, err := Script(tt.shell)
			if err != nil {
				t.Fatalf("Script(%q): %v", tt.shell, err)
			}
			if got != string(want) {
				t.Fatalf("Script(%q) does not match %s", tt.shell, tt.path)
			}
			if !strings.HasSuffix(got, "\n") {
				t.Fatalf("Script(%q) does not end in a newline", tt.shell)
			}
		})
	}
}

func TestScriptRejectsUnsupportedShell(t *testing.T) {
	got, err := Script("powershell")
	if err == nil {
		t.Fatal("Script(\"powershell\") returned nil error")
	}
	if got != "" {
		t.Fatalf("Script(\"powershell\") = %q, want empty string", got)
	}
	if !strings.Contains(err.Error(), "unsupported shell") {
		t.Fatalf("Script(\"powershell\") error = %q, want unsupported shell", err)
	}
}
