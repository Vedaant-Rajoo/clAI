package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

// TestLoadClassification is the CONF-05 failure-mode matrix: every readable
// but unparseable file state classifies as ErrCorrupt, while structurally
// valid JSON objects — however sparse or unfamiliar — load without error.
func TestLoadClassification(t *testing.T) {
	cases := []struct {
		name         string
		data         []byte
		wantCorrupt  bool
		wantErr      bool
		wantProvider string
	}{
		{name: "garbage bytes", data: []byte("not json{{"), wantCorrupt: true, wantErr: true},
		{name: "empty file", data: []byte{}, wantCorrupt: true, wantErr: true},
		{name: "truncated JSON", data: []byte(`{"provider": "openro`), wantCorrupt: true, wantErr: true},
		{name: "wrong-type array", data: []byte(`[]`), wantCorrupt: true, wantErr: true},
		{name: "wrong-type string", data: []byte(`"string"`), wantCorrupt: true, wantErr: true},
		{name: "wrong-type number", data: []byte(`42`), wantCorrupt: true, wantErr: true},
		{name: "wrong-type bool", data: []byte(`true`), wantCorrupt: true, wantErr: true},
		{name: "empty object is zero config", data: []byte(`{}`)},
		{name: "unknown fields ignored", data: []byte(`{"provider":"rules","future_field":true}`), wantProvider: "rules"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := swapUserConfigDir(t)
			writeConfigFile(t, base, tc.data)

			cfg, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load(%q) = %+v, nil; want error", tc.data, cfg)
				}
				if errors.Is(err, ErrCorrupt) != tc.wantCorrupt {
					t.Fatalf("errors.Is(err, ErrCorrupt) = %v, want %v (err: %v)",
						!tc.wantCorrupt, tc.wantCorrupt, err)
				}
				if cfg != (Config{}) {
					t.Fatalf("cfg = %+v, want zero Config on error", cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load(%q): %v, want nil", tc.data, err)
			}
			if cfg.Provider != tc.wantProvider {
				t.Fatalf("Provider = %q, want %q", cfg.Provider, tc.wantProvider)
			}
		})
	}
}

// TestLoadUnreadableFileIsIOErrorNotCorrupt proves the classification
// boundary: an I/O failure on an existing file is a wrapped read error, never
// ErrCorrupt, so callers can tell "bad bytes" from "bad filesystem" — while
// the cmd layer treats both as warn-and-continue (CONF-05).
func TestLoadUnreadableFileIsIOErrorNotCorrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0000 does not deny the owner on windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permission checks")
	}
	base := swapUserConfigDir(t)
	path := writeConfigFile(t, base, []byte(`{"provider":"rules"}`))
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	cfg, err := Load()
	if err == nil {
		t.Fatalf("Load on unreadable file = %+v, nil; want error", cfg)
	}
	if errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v classified as ErrCorrupt, want plain I/O error", err)
	}
	if cfg != (Config{}) {
		t.Fatalf("cfg = %+v, want zero Config on error", cfg)
	}
}
