package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePrecedenceExplicitBeatsEverything(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "env-key")
	got, err := Resolve("openrouter", "flag-key")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "flag-key" {
		t.Errorf("got %q, want flag-key", got)
	}
}

func TestResolveEnvBeatsStore(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "env-key")
	got, err := Resolve("openrouter", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "env-key" {
		t.Errorf("got %q, want env-key", got)
	}
}

func TestResolveUnknownProvider(t *testing.T) {
	if got := EnvVarFor("not-a-provider"); got != "" {
		t.Errorf("EnvVarFor(unknown) = %q, want empty", got)
	}
}

func TestFileFallbackRoundTrip(t *testing.T) {
	// Redirect the config dir so the test doesn't touch the real one.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// Windows uses AppData; darwin uses UserConfigDir which honors
	// XDG_CONFIG_HOME only on Linux. Skip if path doesn't land under dir.
	path, err := configPath()
	if err != nil {
		t.Skipf("no config dir: %v", err)
	}
	if filepath.Dir(path) != filepath.Join(dir, "clai") {
		t.Skipf("configPath %q not controlled by XDG_CONFIG_HOME on this platform", path)
	}

	if err := writeFile("openrouter", "sk-or-test"); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 600", info.Mode().Perm())
	}

	got, err := readFile("openrouter")
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if got != "sk-or-test" {
		t.Errorf("got %q, want sk-or-test", got)
	}

	if err := deleteFileEntry("openrouter"); err != nil {
		t.Fatalf("deleteFileEntry: %v", err)
	}
	got, err = readFile("openrouter")
	if err != nil {
		t.Fatalf("readFile after delete: %v", err)
	}
	if got != "" {
		t.Errorf("after delete got %q, want empty", got)
	}
}

func TestReadFileMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got, err := readFile("openrouter")
	if err != nil {
		t.Fatalf("readFile missing: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
