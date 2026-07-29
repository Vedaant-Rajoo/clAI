package config

import (
	"os"
	"path/filepath"
	"testing"
)

// swapUserConfigDir points the package at a temporary base directory,
// mirroring the internal/auth seam convention, and restores the real seam on
// cleanup.
func swapUserConfigDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	old := userConfigDir
	userConfigDir = func() (string, error) { return base, nil }
	t.Cleanup(func() { userConfigDir = old })
	return base
}

// writeConfigFile writes raw bytes to <base>/clai/config.json and returns the
// file path.
func writeConfigFile(t *testing.T, base string, data []byte) string {
	t.Helper()
	dir := filepath.Join(base, "clai")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadReadsHandWrittenConfigFile is the package half of the tracer: real
// JSON bytes on disk, through the userConfigDir seam, decode into the exact
// Config values a hand-written file carries (CONF-01).
func TestLoadReadsHandWrittenConfigFile(t *testing.T) {
	base := swapUserConfigDir(t)
	writeConfigFile(t, base, []byte(`{"contract":"config-file/v1","provider":"openrouter"}`))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Contract != ConfigContract {
		t.Errorf("Contract = %q, want %q", cfg.Contract, ConfigContract)
	}
	if cfg.Provider != "openrouter" {
		t.Errorf("Provider = %q, want openrouter", cfg.Provider)
	}
}

// TestLoadMissingFileIsSilent locks the CONF-03 empty-input edge: no file at
// all means a zero Config and no error, so a fresh install behaves exactly
// like today.
func TestLoadMissingFileIsSilent(t *testing.T) {
	swapUserConfigDir(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v, want nil", err)
	}
	if cfg != (Config{}) {
		t.Fatalf("cfg = %+v, want zero Config", cfg)
	}
}
