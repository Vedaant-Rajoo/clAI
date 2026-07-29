//go:build darwin || linux

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vedaant-Rajoo/clai/internal/configroot"
)

func useMigrationRoots(t *testing.T) (preferred, legacy string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize migration test root: %v", err)
	}
	preferred = filepath.Join(root, "preferred")
	legacy = filepath.Join(root, "legacy")
	for _, path := range []string{preferred, legacy} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create migration root %q: %v", path, err)
		}
	}
	old := resolveConfigRoots
	resolveConfigRoots = func() (configroot.Roots, error) {
		return configroot.Roots{Preferred: preferred, Legacy: legacy}, nil
	}
	t.Cleanup(func() { resolveConfigRoots = old })
	return preferred, legacy
}

func configPathAt(root string) string {
	return filepath.Join(root, "clai", fileName)
}

func writeConfigFixture(t *testing.T, root string, data []byte) string {
	t.Helper()
	path := configPathAt(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create config fixture directory: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	return path
}

func readConfigFixture(t *testing.T, root string) []byte {
	t.Helper()
	data, err := os.ReadFile(configPathAt(root))
	if err != nil {
		t.Fatalf("read config fixture: %v", err)
	}
	return data
}

func assertLegacyConfigRemoved(t *testing.T, legacy string) {
	t.Helper()
	if _, err := os.Lstat(configPathAt(legacy)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy config remains after reconciliation: %v", err)
	}
}

func TestConfigMigrationOldOnly(t *testing.T) {
	preferred, legacy := useMigrationRoots(t)
	legacyConfig := Config{
		Contract:      "older-contract-value",
		Provider:      "openrouter",
		Model:         "anthropic/claude-test",
		Delivery:      "clipboard",
		InitCompleted: true,
	}
	data, err := json.Marshal(legacyConfig)
	if err != nil {
		t.Fatal(err)
	}
	writeConfigFixture(t, legacy, data)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load old-only config: %v", err)
	}
	want := legacyConfig
	want.Contract = ConfigContract
	if got != want {
		t.Fatalf("Load old-only config = %+v, want %+v", got, want)
	}
	assertLegacyConfigRemoved(t, legacy)

	info, err := os.Stat(configPathAt(preferred))
	if err != nil {
		t.Fatalf("stat migrated preferred config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("migrated preferred config mode = %o, want 600", perm)
	}
	if data := readConfigFixture(t, preferred); !bytes.Contains(data, []byte(`"contract": "config-file/v1"`)) {
		t.Fatalf("migrated config did not stamp %q:\n%s", ConfigContract, data)
	}
}

func TestConfigMigrationPreferredWins(t *testing.T) {
	preferred, legacy := useMigrationRoots(t)
	writeConfigFixture(t, preferred, []byte(`{"contract":"config-file/v1","provider":"rules","model":"preferred-model","delivery":"stdout","init_completed":true}`))
	writeConfigFixture(t, legacy, []byte(`{"contract":"config-file/v1","provider":"legacy-sentinel-provider","model":"legacy-sentinel-model","delivery":"clipboard","init_completed":false}`))

	got, err := Load()
	if err != nil {
		t.Fatalf("Load with preferred and legacy configs: %v", err)
	}
	want := Config{Contract: ConfigContract, Provider: "rules", Model: "preferred-model", Delivery: "stdout", InitCompleted: true}
	if got != want {
		t.Fatalf("Load = %+v, want preferred %+v", got, want)
	}
	assertLegacyConfigRemoved(t, legacy)
	if bytes.Contains(readConfigFixture(t, preferred), []byte("legacy-sentinel")) {
		t.Fatal("preferred config was contaminated with legacy bytes")
	}
}

func TestConfigMigrationCorruptPreferredNeverFallsBack(t *testing.T) {
	preferred, legacy := useMigrationRoots(t)
	corrupt := []byte(`{"provider":"preferred-broken"`)
	writeConfigFixture(t, preferred, corrupt)
	writeConfigFixture(t, legacy, []byte(`{"contract":"config-file/v1","provider":"legacy-secret-sentinel","model":"legacy-model-sentinel","init_completed":true}`))

	got, err := Load()
	if err == nil || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Load corrupt preferred error = %v, want ErrCorrupt", err)
	}
	if got != (Config{}) {
		t.Fatalf("Load corrupt preferred = %+v, want zero Config", got)
	}
	if strings.Contains(err.Error(), "legacy-secret-sentinel") {
		t.Fatalf("Load error disclosed or decoded legacy sentinel: %v", err)
	}
	assertLegacyConfigRemoved(t, legacy)
	if data := readConfigFixture(t, preferred); !bytes.Equal(data, corrupt) {
		t.Fatalf("corrupt preferred bytes changed:\n got: %q\nwant: %q", data, corrupt)
	}
}

func TestConfigMigrationPreservesInitCompleted(t *testing.T) {
	preferred, legacy := useMigrationRoots(t)
	writeConfigFixture(t, legacy, []byte(`{"contract":"config-file/v1","provider":"anthropic","init_completed":true}`))

	got, err := Load()
	if err != nil {
		t.Fatalf("Load old-only init state: %v", err)
	}
	if !got.InitCompleted {
		t.Fatal("InitCompleted = false after migration, want true")
	}
	var persisted Config
	if err := json.Unmarshal(readConfigFixture(t, preferred), &persisted); err != nil {
		t.Fatalf("decode migrated preferred config: %v", err)
	}
	if !persisted.InitCompleted {
		t.Fatal("persisted init_completed = false after migration")
	}
	assertLegacyConfigRemoved(t, legacy)
}

func TestConfigMigrationCannotAddCredentialFields(t *testing.T) {
	preferred, legacy := useMigrationRoots(t)
	writeConfigFixture(t, legacy, []byte(`{
		"contract":"future-contract",
		"provider":"openrouter",
		"model":"known-model",
		"delivery":"clipboard",
		"init_completed":true,
		"api_key":"credential-sentinel",
		"token":"token-sentinel",
		"secret":"secret-sentinel",
		"credentials":{"openrouter":"nested-sentinel"},
		"future_field":true
	}`))

	got, err := Load()
	if err != nil {
		t.Fatalf("Load credential-shaped legacy config: %v", err)
	}
	want := Config{Contract: ConfigContract, Provider: "openrouter", Model: "known-model", Delivery: "clipboard", InitCompleted: true}
	if got != want {
		t.Fatalf("Load = %+v, want known fields %+v", got, want)
	}
	data := readConfigFixture(t, preferred)
	for _, forbidden := range []string{"api_key", "token", "secret", "credentials", "future_field", "credential-sentinel", "nested-sentinel"} {
		if bytes.Contains(bytes.ToLower(data), []byte(forbidden)) {
			t.Fatalf("migrated config contains forbidden legacy field/value %q:\n%s", forbidden, data)
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("decode migrated config fields: %v", err)
	}
	known := map[string]bool{
		"contract":       true,
		"provider":       true,
		"model":          true,
		"delivery":       true,
		"init_completed": true,
	}
	if len(fields) != len(known) {
		t.Fatalf("migrated config fields = %v, want exactly %v", fields, known)
	}
	for field := range fields {
		if !known[field] {
			t.Fatalf("migrated config contains unknown field %q", field)
		}
	}
	assertLegacyConfigRemoved(t, legacy)
}

func TestSaveRetiresLegacyConfig(t *testing.T) {
	_, legacy := useMigrationRoots(t)
	writeConfigFixture(t, legacy, []byte(`{"provider":"legacy-sentinel"}`))

	want := Config{Provider: "rules", Model: "saved-model", Delivery: "stdout", InitCompleted: true}
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	assertLegacyConfigRemoved(t, legacy)
	got, err := Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	want.Contract = ConfigContract
	if got != want {
		t.Fatalf("Load after Save = %+v, want %+v", got, want)
	}
}
