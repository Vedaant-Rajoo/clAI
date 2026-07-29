package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Vedaant-Rajoo/clai/internal/configroot"
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

// swapConfigRoots points the shared resolver at an isolated preferred base.
// Until Path consumes this seam, userConfigDir is also redirected to a distinct
// safe directory so the RED test cannot inspect the developer's real config.
func swapConfigRoots(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	oldRoots := resolveConfigRoots
	oldUserConfigDir := userConfigDir
	resolveConfigRoots = func() (configroot.Roots, error) {
		return configroot.Roots{Preferred: base}, nil
	}
	userConfigDir = func() (string, error) { return filepath.Join(base, "old-policy"), nil }
	t.Cleanup(func() {
		resolveConfigRoots = oldRoots
		userConfigDir = oldUserConfigDir
	})
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

func TestPreferredConfigPathUsesSharedRoot(t *testing.T) {
	base := swapConfigRoots(t)

	got, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join(base, "clai", fileName)
	if got != want {
		t.Fatalf("Path = %q, want shared preferred path %q", got, want)
	}
}

func TestLoadReadsPreferredRoot(t *testing.T) {
	base := swapConfigRoots(t)
	writeRawConfig(t, base, []byte(`{"contract":"config-file/v1","provider":"anthropic","model":"claude-test","delivery":"stdout","init_completed":true}`))

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Contract:      ConfigContract,
		Provider:      "anthropic",
		Model:         "claude-test",
		Delivery:      "stdout",
		InitCompleted: true,
	}
	if got != want {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
}

// TestLoadReadsHandWrittenConfigFile is the package half of the tracer: real
// JSON bytes on disk, through the configured root seam, decode into the exact
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

// TestConfigMarshalContainsNoCredentialFields is the CONF-04 structural
// tripwire: the marshaled form of a fully-populated Config must contain no
// credential-shaped substring, and the type must have exactly the known five
// fields — any future field addition fails here and forces a human re-check
// that it cannot carry key material (T-01-05). Because the struct is the only
// serialization source, this holds for every write path, including
// interrupted ones.
func TestConfigMarshalContainsNoCredentialFields(t *testing.T) {
	out, err := json.Marshal(Config{
		Contract: ConfigContract, Provider: "openrouter",
		Model: "m", Delivery: "clipboard", InitCompleted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"key", "token", "secret", "credential"} {
		if strings.Contains(strings.ToLower(string(out)), banned) {
			t.Fatalf("marshaled config contains credential-shaped field %q: %s", banned, out)
		}
	}
	// Structural guarantee: the type itself has no string field beyond the known set.
	if reflect.TypeOf(Config{}).NumField() != 5 {
		t.Fatal("Config gained a field — re-verify it cannot carry key material")
	}
}

// TestInitCompletedRoundTrip proves CONF-02: init_completed persists as a
// JSON field inside config.json, round-trips through Save/Load, and no
// separate marker file of any name is created — proven by exhaustive
// directory listing, not by absence of one known name.
func TestInitCompletedRoundTrip(t *testing.T) {
	requireUnix(t)
	base := swapUserConfigDir(t)

	if err := Save(Config{InitCompleted: true}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.InitCompleted {
		t.Error("InitCompleted = false after Save(Config{InitCompleted: true})")
	}
	if data := readConfigBytes(t, base); !bytes.Contains(data, []byte(`"init_completed"`)) {
		t.Errorf("persisted bytes missing the init_completed JSON key:\n%s", data)
	}
	entries, err := os.ReadDir(filepath.Join(base, "clai"))
	if err != nil {
		t.Fatalf("list config directory: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != fileName {
			t.Errorf("unexpected entry %q in config directory — init state must live inside config.json, never a marker file", entry.Name())
		}
	}
}

// TestSaveDropsUnknownFields is the executable record of the accepted A4
// decision (RESEARCH Assumptions Log): reads are lenient so unknown fields
// from a future config-file version never brick the CLI, but Save serializes
// the known struct only — an older binary rewriting the file silently drops
// fields it does not know. Accepted for a single-binary CLI; the contract
// field plus lenient reads keep future versions loadable. Changing to
// raw-field round-tripping later is additive.
func TestSaveDropsUnknownFields(t *testing.T) {
	requireUnix(t)
	base := swapUserConfigDir(t)
	writeRawConfig(t, base, []byte(`{"provider":"rules","future_field":true}`))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with unknown field: %v", err)
	}
	if cfg.Provider != "rules" {
		t.Fatalf("Provider = %q, want rules (known fields must parse)", cfg.Provider)
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if data := readConfigBytes(t, base); bytes.Contains(data, []byte("future_field")) {
		t.Errorf("rewritten config still contains the unknown field:\n%s", data)
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
