package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// writeRawConfig writes raw bytes to <base>/clai/config.json and returns the
// file path. (Named to avoid colliding with the production writeConfigFile
// atomic writer in this package.)
func writeRawConfig(t *testing.T, base string, data []byte) string {
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
	writeRawConfig(t, base, []byte(`{"contract":"config-file/v1","provider":"openrouter"}`))

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
			writeRawConfig(t, base, tc.data)

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

// requireUnix skips writer tests on platforms where writeConfigFile fails
// closed (mirroring internal/auth's platform split).
func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("hardened config writer is unix-only; other platforms fail closed")
	}
}

// assertNoTempLitter fails if any transient .config-<hex>.tmp file survives
// under <base>/clai after Save returns.
func assertNoTempLitter(t *testing.T, base string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(base, "clai"))
	if err != nil {
		t.Fatalf("list config directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".config-") && strings.HasSuffix(name, ".tmp") {
			t.Errorf("temporary file %q left behind after Save", name)
		}
	}
}

// readConfigBytes reads the raw persisted config.json under <base>/clai.
func readConfigBytes(t *testing.T, base string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(base, "clai", fileName))
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	return data
}

// TestSaveLoadRoundTrip proves the CONF-01 write side: a populated Config
// survives Save then Load with the contract stamped, and Save leaves no
// transient temp files behind.
func TestSaveLoadRoundTrip(t *testing.T) {
	requireUnix(t)
	base := swapUserConfigDir(t)

	in := Config{Provider: "openrouter", Model: "anthropic/claude", Delivery: "clipboard", InitCompleted: true}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	want := in
	want.Contract = ConfigContract
	if got != want {
		t.Errorf("round-trip Config = %+v, want %+v", got, want)
	}
	assertNoTempLitter(t, base)
}

// TestSaveStampsContract locks the CONF-01 rule that every written file
// carries config-file/v1 — Save stamps the contract unconditionally, even on
// a zero-value Config.
func TestSaveStampsContract(t *testing.T) {
	requireUnix(t)
	base := swapUserConfigDir(t)

	if err := Save(Config{}); err != nil {
		t.Fatalf("Save zero Config: %v", err)
	}
	data := readConfigBytes(t, base)
	if !bytes.Contains(data, []byte(ConfigContract)) {
		t.Errorf("persisted bytes missing contract %q:\n%s", ConfigContract, data)
	}
}

// TestSavePermissions asserts the 0600 file / 0700 directory discipline the
// hardened writer must enforce (T-01-08).
func TestSavePermissions(t *testing.T) {
	requireUnix(t)
	base := swapUserConfigDir(t)

	if err := Save(Config{Provider: "rules"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	dirInfo, err := os.Stat(filepath.Join(base, "clai"))
	if err != nil {
		t.Fatalf("stat config directory: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("config directory mode = %o, want 700", perm)
	}
	fileInfo, err := os.Stat(filepath.Join(base, "clai", fileName))
	if err != nil {
		t.Fatalf("stat config file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file mode = %o, want 600", perm)
	}
}

// TestSaveIdempotent proves the CONF-01 idempotency edge: saving the same
// Config twice succeeds and produces byte-identical file content.
func TestSaveIdempotent(t *testing.T) {
	requireUnix(t)
	base := swapUserConfigDir(t)

	cfg := Config{Provider: "openrouter", Model: "m", InitCompleted: true}
	if err := Save(cfg); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	first := readConfigBytes(t, base)
	if err := Save(cfg); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	second := readConfigBytes(t, base)
	if !bytes.Equal(first, second) {
		t.Errorf("second Save changed bytes:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	assertNoTempLitter(t, base)
}

// TestConcurrentSave proves the CONF-01/CONF-02 concurrency edge: concurrent
// Saves serialize on the .config.lock flock, so the final file is always one
// complete writer's JSON — never interleaved bytes (T-01-07). Run with -race.
func TestConcurrentSave(t *testing.T) {
	requireUnix(t)
	base := swapUserConfigDir(t)

	const writers = 8
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Save(Config{Provider: "rules", Model: fmt.Sprintf("model-%d", i), InitCompleted: true})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil && !strings.Contains(err.Error(), "timed out acquiring config lock") {
			t.Errorf("writer %d: unexpected error: %v", i, err)
		}
	}
	data := readConfigBytes(t, base)
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("final config.json is not valid JSON (%v):\n%s", err, data)
	}
	matched := false
	for i := range writers {
		want := Config{Contract: ConfigContract, Provider: "rules", Model: fmt.Sprintf("model-%d", i), InitCompleted: true}
		if got == want {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("final config %+v does not equal any single writer's complete payload", got)
	}
	assertNoTempLitter(t, base)
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
	path := writeRawConfig(t, base, []byte(`{"provider":"rules"}`))
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
