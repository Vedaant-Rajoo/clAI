//go:build darwin || linux

package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

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

func useConfigFilesystemTestHooks(t *testing.T, hooks configFilesystemHooks) {
	t.Helper()
	old := configHooks
	configHooks = hooks
	t.Cleanup(func() { configHooks = old })
}

func assertNoConfigLitter(t *testing.T, preferred, legacy string) {
	t.Helper()
	configFiles := 0
	for _, root := range []string{preferred, legacy} {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := entry.Name()
			if name == fileName {
				configFiles++
			}
			if name == lockName || strings.HasPrefix(name, ".config-") && strings.HasSuffix(name, ".tmp") {
				t.Errorf("config storage litter remains at %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk config root %q: %v", root, err)
		}
	}
	if configFiles != 1 {
		t.Fatalf("config.json file count = %d, want exactly one preferred file", configFiles)
	}
	preferredEntries, err := os.ReadDir(filepath.Join(preferred, "clai"))
	if err != nil {
		t.Fatalf("list preferred config directory: %v", err)
	}
	if len(preferredEntries) != 1 || preferredEntries[0].Name() != fileName {
		t.Fatalf("preferred config entries = %v, want only %s", preferredEntries, fileName)
	}
	legacyEntries, err := os.ReadDir(filepath.Join(legacy, "clai"))
	if err != nil {
		t.Fatalf("list legacy config directory: %v", err)
	}
	if len(legacyEntries) != 0 {
		t.Fatalf("legacy config entries = %v, want empty directory", legacyEntries)
	}
}

func TestConfigMigrationIdempotent(t *testing.T) {
	preferred, legacy := useMigrationRoots(t)
	writeConfigFixture(t, legacy, []byte(`{"contract":"old","provider":"openrouter","model":"stable-model","delivery":"clipboard","init_completed":true}`))

	first, err := Load()
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	firstBytes := append([]byte(nil), readConfigFixture(t, preferred)...)
	second, err := Load()
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if second != first {
		t.Fatalf("second Load = %+v, want %+v", second, first)
	}
	if err := Save(second); err != nil {
		t.Fatalf("Save migrated config: %v", err)
	}
	if got := readConfigFixture(t, preferred); !bytes.Equal(got, firstBytes) {
		t.Fatalf("repeated reconciliation changed preferred bytes:\nfirst:\n%s\nafter:\n%s", firstBytes, got)
	}
	assertNoConfigLitter(t, preferred, legacy)
}

func TestConfigMigrationInterruptedRecovery(t *testing.T) {
	preferred, legacy := useMigrationRoots(t)
	writeConfigFixture(t, legacy, []byte(`{"provider":"openrouter","model":"complete-model","delivery":"stdout","init_completed":true}`))
	injected := errors.New("injected post-install interruption")
	calls := 0
	useConfigFilesystemTestHooks(t, configFilesystemHooks{
		afterPreferredInstallBeforeLegacyUnlink: func(configFileLocations) error {
			calls++
			return injected
		},
	})

	got, err := Load()
	if !errors.Is(err, injected) {
		t.Fatalf("interrupted Load error = %v, want injected interruption", err)
	}
	if got != (Config{}) {
		t.Fatalf("interrupted Load = %+v, want zero Config on error", got)
	}
	if calls != 1 {
		t.Fatalf("post-install checkpoint calls = %d, want 1", calls)
	}
	preferredBytes := append([]byte(nil), readConfigFixture(t, preferred)...)
	var installed Config
	if err := json.Unmarshal(preferredBytes, &installed); err != nil {
		t.Fatalf("installed preferred config is incomplete: %v", err)
	}
	if installed.Contract != ConfigContract || installed.Model != "complete-model" || !installed.InitCompleted {
		t.Fatalf("installed preferred config = %+v, want complete migrated values", installed)
	}
	if _, err := os.Stat(configPathAt(legacy)); err != nil {
		t.Fatalf("legacy config was removed before interruption: %v", err)
	}

	configHooks.afterPreferredInstallBeforeLegacyUnlink = nil
	if err := os.WriteFile(configPathAt(legacy), []byte(`{"provider":"legacy-corrupt-sentinel"`), 0o600); err != nil {
		t.Fatalf("corrupt retained legacy config: %v", err)
	}
	recovered, err := Load()
	if err != nil {
		t.Fatalf("retry Load: %v", err)
	}
	want := Config{Contract: ConfigContract, Provider: "openrouter", Model: "complete-model", Delivery: "stdout", InitCompleted: true}
	if recovered != want {
		t.Fatalf("retry Load = %+v, want preferred %+v", recovered, want)
	}
	if got := readConfigFixture(t, preferred); !bytes.Equal(got, preferredBytes) {
		t.Fatalf("recovery rewrote preferred bytes:\nfirst:\n%s\nafter:\n%s", preferredBytes, got)
	}
	assertNoConfigLitter(t, preferred, legacy)
}

func TestConfigMigrationInterruptedRecoveryProcessKill(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	preferred := filepath.Join(root, "preferred")
	legacy := filepath.Join(root, "legacy")
	for _, path := range []string{preferred, legacy} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeConfigFixture(t, legacy, []byte(`{"provider":"openrouter","model":"killed-process-model","init_completed":true}`))
	signal := filepath.Join(root, "crash-signal")
	neverRelease := filepath.Join(root, "never-release")
	order := filepath.Join(root, "crash-order.json")
	helper := startConfigMigrationHelper(t, preferred, legacy, "", signal, neverRelease, order, "load")
	waitForConfigTestPath(t, signal, 5*time.Second)
	if err := helper.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill config migration helper: %v", err)
	}
	select {
	case err := <-helper.done:
		if err == nil {
			t.Fatal("killed config migration helper exited successfully")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for killed config migration helper")
	}
	if !hasConfigTransientEntry(t, preferred) && !hasConfigTransientEntry(t, legacy) {
		t.Fatal("killed helper left no observable lock or temporary artifact; interruption checkpoint was too early")
	}

	old := resolveConfigRoots
	resolveConfigRoots = func() (configroot.Roots, error) {
		return configroot.Roots{Preferred: preferred, Legacy: legacy}, nil
	}
	t.Cleanup(func() { resolveConfigRoots = old })
	got, err := Load()
	if err != nil {
		t.Fatalf("Load after killed migration: %v", err)
	}
	want := Config{Contract: ConfigContract, Provider: "openrouter", Model: "killed-process-model", InitCompleted: true}
	if got != want {
		t.Fatalf("Load after killed migration = %+v, want %+v", got, want)
	}
	assertNoConfigLitter(t, preferred, legacy)
}

func TestConfigMigrationInterruptedRecoveryInitialSaveKill(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	preferred := filepath.Join(root, "preferred")
	legacy := filepath.Join(root, "legacy")
	for _, path := range []string{preferred, legacy} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	signal := filepath.Join(root, "save-crash-signal")
	neverRelease := filepath.Join(root, "save-never-release")
	order := filepath.Join(root, "save-crash-order.json")
	helper := startConfigMigrationHelper(t, preferred, legacy, "", signal, neverRelease, order, "save")
	waitForConfigTestPath(t, signal, 5*time.Second)
	if err := helper.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill config save helper: %v", err)
	}
	select {
	case err := <-helper.done:
		if err == nil {
			t.Fatal("killed config save helper exited successfully")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for killed config save helper")
	}
	if !hasConfigTransientEntry(t, preferred) {
		t.Fatal("killed initial Save left no lock or temporary artifact")
	}

	old := resolveConfigRoots
	resolveConfigRoots = func() (configroot.Roots, error) {
		return configroot.Roots{Preferred: preferred, Legacy: legacy}, nil
	}
	t.Cleanup(func() { resolveConfigRoots = old })
	got, err := Load()
	if err != nil {
		t.Fatalf("Load after killed initial Save: %v", err)
	}
	if got != (Config{}) {
		t.Fatalf("Load after killed initial Save = %+v, want zero Config", got)
	}
	if hasConfigTransientEntry(t, preferred) || hasConfigTransientEntry(t, legacy) {
		t.Fatal("Load did not clean interrupted initial Save artifacts")
	}
	assertPathMissing(t, configPathAt(preferred))
	assertPathMissing(t, configPathAt(legacy))
}

func hasConfigTransientEntry(t *testing.T, root string) bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "clai"))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatalf("list interrupted config directory: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == lockName || isConfigTransientName(entry.Name()) {
			return true
		}
	}
	return false
}

func TestConfigMigrationConcurrent(t *testing.T) {
	t.Run("same process", func(t *testing.T) {
		preferred, legacy := useMigrationRoots(t)
		writeConfigFixture(t, legacy, []byte(`{"provider":"anthropic","model":"shared-model","delivery":"clipboard","init_completed":true}`))
		want := Config{Contract: ConfigContract, Provider: "anthropic", Model: "shared-model", Delivery: "clipboard", InitCompleted: true}

		const workers = 12
		start := make(chan struct{})
		results := make(chan Config, workers)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				cfg, err := Load()
				results <- cfg
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent Load: %v", err)
			}
		}
		for cfg := range results {
			if cfg != want {
				t.Fatalf("concurrent Load = %+v, want %+v", cfg, want)
			}
		}
		assertNoConfigLitter(t, preferred, legacy)
	})

	t.Run("cross process ordered locks", func(t *testing.T) {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		preferred := filepath.Join(root, "z-preferred")
		legacy := filepath.Join(root, "a-legacy")
		for _, path := range []string{preferred, legacy} {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		writeConfigFixture(t, legacy, []byte(`{"provider":"rules","model":"process-model","init_completed":true}`))

		signalA := filepath.Join(root, "signal-a")
		signalB := filepath.Join(root, "signal-b")
		startedB := filepath.Join(root, "started-b")
		release := filepath.Join(root, "release")
		orderA := filepath.Join(root, "order-a.json")
		orderB := filepath.Join(root, "order-b.json")

		first := startConfigMigrationHelper(t, preferred, legacy, "", signalA, release, orderA, "load")
		waitForConfigTestPath(t, signalA, 5*time.Second)
		second := startConfigMigrationHelper(t, preferred, legacy, startedB, signalB, release, orderB, "load")
		waitForConfigTestPath(t, startedB, 5*time.Second)
		select {
		case err := <-second.done:
			t.Fatalf("second helper completed while first held ordered locks: %v\n%s", err, second.output.String())
		case <-time.After(250 * time.Millisecond):
		}
		if _, err := os.Stat(signalB); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("second helper passed ordered-lock checkpoint while first held locks: %v", err)
		}
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		waitForConfigMigrationHelper(t, "first", first)
		waitForConfigMigrationHelper(t, "second", second)
		waitForConfigTestPath(t, signalB, time.Second)

		orderData, err := os.ReadFile(orderA)
		if err != nil {
			t.Fatalf("read acquisition order: %v", err)
		}
		var order configLockOrderEvidence
		if err := json.Unmarshal(orderData, &order); err != nil {
			t.Fatalf("decode lock order evidence: %v", err)
		}
		wantAcquire := []string{filepath.Join(legacy, "clai"), filepath.Join(preferred, "clai")}
		wantRelease := []string{filepath.Join(preferred, "clai"), filepath.Join(legacy, "clai")}
		if !sort.StringsAreSorted(order.Acquire) || !equalStrings(order.Acquire, wantAcquire) {
			t.Fatalf("lock acquisition order = %v, want ascending canonical paths %v", order.Acquire, wantAcquire)
		}
		if !equalStrings(order.Release, wantRelease) {
			t.Fatalf("lock release order = %v, want reverse acquisition order %v", order.Release, wantRelease)
		}
		assertNoConfigLitter(t, preferred, legacy)
	})
}

type configLockOrderEvidence struct {
	Acquire []string `json:"acquire"`
	Release []string `json:"release"`
}

type configMigrationHelper struct {
	cancel context.CancelFunc
	cmd    *exec.Cmd
	output bytes.Buffer
	done   chan error
}

func startConfigMigrationHelper(t *testing.T, preferred, legacy, started, signal, release, order, action string) *configMigrationHelper {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	helper := &configMigrationHelper{cancel: cancel, done: make(chan error, 1)}
	helper.cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConfigMigrationProcessHelper$")
	helper.cmd.Env = append(os.Environ(),
		"CLAI_CONFIG_HELPER_PREFERRED="+preferred,
		"CLAI_CONFIG_HELPER_LEGACY="+legacy,
		"CLAI_CONFIG_HELPER_STARTED="+started,
		"CLAI_CONFIG_HELPER_SIGNAL="+signal,
		"CLAI_CONFIG_HELPER_RELEASE="+release,
		"CLAI_CONFIG_HELPER_ORDER="+order,
		"CLAI_CONFIG_HELPER_ACTION="+action,
	)
	helper.cmd.Stdout = &helper.output
	helper.cmd.Stderr = &helper.output
	if err := helper.cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start config migration helper: %v", err)
	}
	go func() { helper.done <- helper.cmd.Wait() }()
	t.Cleanup(cancel)
	return helper
}

func waitForConfigMigrationHelper(t *testing.T, name string, helper *configMigrationHelper) {
	t.Helper()
	select {
	case err := <-helper.done:
		helper.cancel()
		if err != nil {
			t.Fatalf("%s config migration helper: %v\n%s", name, err, helper.output.String())
		}
	case <-time.After(10 * time.Second):
		helper.cancel()
		t.Fatalf("timed out waiting for %s config migration helper\n%s", name, helper.output.String())
	}
}

func TestConfigMigrationProcessHelper(t *testing.T) {
	preferred := os.Getenv("CLAI_CONFIG_HELPER_PREFERRED")
	if preferred == "" {
		return
	}
	legacy := os.Getenv("CLAI_CONFIG_HELPER_LEGACY")
	resolveConfigRoots = func() (configroot.Roots, error) {
		return configroot.Roots{Preferred: preferred, Legacy: legacy}, nil
	}
	if started := os.Getenv("CLAI_CONFIG_HELPER_STARTED"); started != "" {
		if err := os.WriteFile(started, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configHooks.afterOrderedLocksAcquired = func(checkpoint configFilesystemCheckpoint) error {
		if order := os.Getenv("CLAI_CONFIG_HELPER_ORDER"); order != "" {
			data, err := json.Marshal(configLockOrderEvidence{
				Acquire: checkpoint.AppPaths,
				Release: checkpoint.ReleasePaths,
			})
			if err != nil {
				return err
			}
			if err := os.WriteFile(order, data, 0o600); err != nil {
				return err
			}
		}
		if signal := os.Getenv("CLAI_CONFIG_HELPER_SIGNAL"); signal != "" {
			if err := os.WriteFile(signal, nil, 0o600); err != nil {
				return err
			}
		}
		if release := os.Getenv("CLAI_CONFIG_HELPER_RELEASE"); release != "" {
			return waitForConfigPath(release, 10*time.Second)
		}
		return nil
	}
	var err error
	if os.Getenv("CLAI_CONFIG_HELPER_ACTION") == "save" {
		err = Save(Config{Provider: "rules", Model: "helper-save-model", InitCompleted: true})
	} else {
		_, err = Load()
	}
	if err != nil {
		t.Fatal(err)
	}
}

func waitForConfigPath(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for config migration checkpoint")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForConfigTestPath(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	if err := waitForConfigPath(path, timeout); err != nil {
		t.Fatalf("wait for config test path %q: %v", path, err)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestConfigRootSymlinkPolicy(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	preferredTarget := filepath.Join(root, "preferred-target")
	legacyTarget := filepath.Join(root, "legacy-target")
	for _, path := range []string{preferredTarget, legacyTarget} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	preferredLink := filepath.Join(root, "preferred-link")
	legacyLink := filepath.Join(root, "legacy-link")
	if err := os.Symlink(preferredTarget, preferredLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(legacyTarget, legacyLink); err != nil {
		t.Fatal(err)
	}
	old := resolveConfigRoots
	resolveConfigRoots = func() (configroot.Roots, error) {
		return configroot.Roots{Preferred: preferredLink, Legacy: legacyLink}, nil
	}
	t.Cleanup(func() { resolveConfigRoots = old })
	writeConfigFixture(t, legacyTarget, []byte(`{"provider":"openrouter","model":"symlink-model","init_completed":true}`))

	got, err := Load()
	if err != nil {
		t.Fatalf("Load through approved root symlinks: %v", err)
	}
	want := Config{Contract: ConfigContract, Provider: "openrouter", Model: "symlink-model", InitCompleted: true}
	if got != want {
		t.Fatalf("Load through root symlinks = %+v, want %+v", got, want)
	}
	assertNoConfigLitter(t, preferredTarget, legacyTarget)
}

func TestConfigRootSubstitutionFailsClosed(t *testing.T) {
	const sentinel = "credential-like-content-sentinel"
	original := []byte(`{"contract":"config-file/v1","provider":"rules","model":"credential-like-content-sentinel","init_completed":true}`)
	newConfig := Config{Provider: "openrouter", Model: "replacement-model", InitCompleted: true}

	t.Run("root replacement", func(t *testing.T) {
		preferred, _ := useMigrationRoots(t)
		writeConfigFixture(t, preferred, original)
		moved := preferred + ".moved"
		useConfigFilesystemTestHooks(t, configFilesystemHooks{
			afterOrderedLocksAcquired: func(configFilesystemCheckpoint) error {
				if err := os.Rename(preferred, moved); err != nil {
					return err
				}
				if err := os.Mkdir(preferred, 0o700); err != nil {
					return err
				}
				return os.Mkdir(filepath.Join(preferred, "clai"), 0o700)
			},
		})
		err := Save(newConfig)
		assertConfigSubstitutionFailure(t, err, sentinel)
		assertFileBytes(t, filepath.Join(moved, "clai", fileName), original)
		assertPathMissing(t, filepath.Join(moved, "clai", lockName))
	})

	t.Run("app directory replacement", func(t *testing.T) {
		preferred, _ := useMigrationRoots(t)
		writeConfigFixture(t, preferred, original)
		app := filepath.Join(preferred, "clai")
		moved := app + ".moved"
		useConfigFilesystemTestHooks(t, configFilesystemHooks{
			afterOrderedLocksAcquired: func(configFilesystemCheckpoint) error {
				if err := os.Rename(app, moved); err != nil {
					return err
				}
				return os.Mkdir(app, 0o700)
			},
		})
		err := Save(newConfig)
		assertConfigSubstitutionFailure(t, err, sentinel)
		assertFileBytes(t, filepath.Join(moved, fileName), original)
		assertPathMissing(t, filepath.Join(moved, lockName))
	})

	t.Run("lock replacement", func(t *testing.T) {
		preferred, _ := useMigrationRoots(t)
		path := writeConfigFixture(t, preferred, original)
		app := filepath.Dir(path)
		useConfigFilesystemTestHooks(t, configFilesystemHooks{
			afterOrderedLocksAcquired: func(configFilesystemCheckpoint) error {
				lockPath := filepath.Join(app, lockName)
				if err := os.Rename(lockPath, lockPath+".moved"); err != nil {
					return err
				}
				return os.WriteFile(lockPath, nil, 0o600)
			},
		})
		err := Save(newConfig)
		assertConfigSubstitutionFailure(t, err, sentinel)
		assertFileBytes(t, path, original)
	})

	t.Run("absent config appearance", func(t *testing.T) {
		preferred, _ := useMigrationRoots(t)
		substitute := []byte(`{"provider":"appeared-attacker"}`)
		useConfigFilesystemTestHooks(t, configFilesystemHooks{
			afterOrderedLocksAcquired: func(checkpoint configFilesystemCheckpoint) error {
				return os.WriteFile(checkpoint.PreferredPath, substitute, 0o600)
			},
		})
		err := Save(newConfig)
		assertConfigSubstitutionFailure(t, err, sentinel)
		assertFileBytes(t, configPathAt(preferred), substitute)
	})

	t.Run("config unlink and recreate", func(t *testing.T) {
		preferred, _ := useMigrationRoots(t)
		path := writeConfigFixture(t, preferred, original)
		substitute := []byte(`{"provider":"recreated-attacker"}`)
		useConfigFilesystemTestHooks(t, configFilesystemHooks{
			afterOrderedLocksAcquired: func(configFilesystemCheckpoint) error {
				if err := os.Remove(path); err != nil {
					return err
				}
				return os.WriteFile(path, substitute, 0o600)
			},
		})
		err := Save(newConfig)
		assertConfigSubstitutionFailure(t, err, sentinel)
		assertFileBytes(t, path, substitute)
	})

	t.Run("config replacement", func(t *testing.T) {
		preferred, _ := useMigrationRoots(t)
		path := writeConfigFixture(t, preferred, original)
		moved := path + ".moved"
		useConfigFilesystemTestHooks(t, configFilesystemHooks{
			afterOrderedLocksAcquired: func(configFilesystemCheckpoint) error {
				if err := os.Rename(path, moved); err != nil {
					return err
				}
				return os.WriteFile(path, []byte(`{"provider":"attacker"}`), 0o600)
			},
		})
		err := Save(newConfig)
		assertConfigSubstitutionFailure(t, err, sentinel)
		assertFileBytes(t, moved, original)
		assertFileBytes(t, path, []byte(`{"provider":"attacker"}`))
	})

	t.Run("temporary symlink replacement", func(t *testing.T) {
		preferred, _ := useMigrationRoots(t)
		path := writeConfigFixture(t, preferred, original)
		target := filepath.Join(preferred, "attacker-target")
		targetBytes := []byte("target-must-not-change")
		if err := os.WriteFile(target, targetBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		useConfigFilesystemTestHooks(t, configFilesystemHooks{
			afterOrderedLocksAcquired: func(checkpoint configFilesystemCheckpoint) error {
				if checkpoint.TempName == "" {
					return errors.New("temporary checkpoint did not expose a temp name")
				}
				tempPath := filepath.Join(preferred, "clai", checkpoint.TempName)
				if err := os.Remove(tempPath); err != nil {
					return err
				}
				return os.Symlink(target, tempPath)
			},
		})
		err := Save(newConfig)
		assertConfigSubstitutionFailure(t, err, sentinel)
		assertFileBytes(t, path, original)
		assertFileBytes(t, target, targetBytes)
	})

	t.Run("clai symlink", func(t *testing.T) {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		preferred := filepath.Join(root, "preferred")
		legacy := filepath.Join(root, "legacy")
		targetApp := filepath.Join(root, "target-app")
		for _, path := range []string{preferred, legacy, targetApp} {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(targetApp, filepath.Join(preferred, "clai")); err != nil {
			t.Fatal(err)
		}
		old := resolveConfigRoots
		resolveConfigRoots = func() (configroot.Roots, error) {
			return configroot.Roots{Preferred: preferred, Legacy: legacy}, nil
		}
		t.Cleanup(func() { resolveConfigRoots = old })
		if _, loadErr := Load(); loadErr == nil {
			t.Fatal("Load unexpectedly accepted a symlinked clai directory")
		}
		err = Save(newConfig)
		assertConfigSubstitutionFailure(t, err, sentinel)
	})

	t.Run("preferred config symlink", func(t *testing.T) {
		preferred, _ := useMigrationRoots(t)
		app := filepath.Join(preferred, "clai")
		if err := os.Mkdir(app, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(preferred, "target-config")
		if err := os.WriteFile(target, original, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(app, fileName)); err != nil {
			t.Fatal(err)
		}
		err := Save(newConfig)
		assertConfigSubstitutionFailure(t, err, sentinel)
		assertFileBytes(t, target, original)
	})

	t.Run("legacy retirement replacement", func(t *testing.T) {
		_, legacy := useMigrationRoots(t)
		writeConfigFixture(t, legacy, original)
		legacyPath := configPathAt(legacy)
		substitute := []byte(`{"provider":"legacy-retirement-attacker"}`)
		useConfigFilesystemTestHooks(t, configFilesystemHooks{
			beforeNamedRetirement: func(dir, name string) error {
				if dir != filepath.Dir(legacyPath) || name != fileName {
					return nil
				}
				if err := os.Remove(legacyPath); err != nil {
					return err
				}
				return os.WriteFile(legacyPath, substitute, 0o600)
			},
		})
		_, err := Load()
		assertConfigSubstitutionFailure(t, err, sentinel)
		assertFileBytes(t, legacyPath, substitute)
	})

	t.Run("lock retirement replacement", func(t *testing.T) {
		preferred, _ := useMigrationRoots(t)
		writeConfigFixture(t, preferred, original)
		lockPath := filepath.Join(preferred, "clai", lockName)
		marker := []byte("replacement-lock-marker")
		useConfigFilesystemTestHooks(t, configFilesystemHooks{
			beforeNamedRetirement: func(dir, name string) error {
				if dir != filepath.Dir(lockPath) || name != lockName {
					return nil
				}
				if err := os.Remove(lockPath); err != nil {
					return err
				}
				return os.WriteFile(lockPath, marker, 0o600)
			},
		})
		err := Save(newConfig)
		assertConfigSubstitutionFailure(t, err, sentinel)
		assertFileBytes(t, lockPath, marker)
	})

	t.Run("legacy config symlink", func(t *testing.T) {
		preferred, legacy := useMigrationRoots(t)
		path := writeConfigFixture(t, preferred, original)
		legacyApp := filepath.Join(legacy, "clai")
		if err := os.Mkdir(legacyApp, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(legacy, "target-config")
		if err := os.WriteFile(target, []byte(`{"provider":"legacy"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(legacyApp, fileName)); err != nil {
			t.Fatal(err)
		}
		_, err := Load()
		assertConfigSubstitutionFailure(t, err, sentinel)
		assertFileBytes(t, path, original)
	})
}

func assertConfigSubstitutionFailure(t *testing.T, err error, secret string) {
	t.Helper()
	if err == nil {
		t.Fatal("config operation unexpectedly accepted path substitution")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("config substitution error disclosed config contents: %v", err)
	}
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file %q changed:\n got: %q\nwant: %q", path, got, want)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected storage artifact %q remains: %v", path, err)
	}
}

func TestConfigMigrationLeavesNoLitter(t *testing.T) {
	preferred, legacy := useMigrationRoots(t)
	writeConfigFixture(t, legacy, []byte(`{"provider":"rules","model":"litter-model","delivery":"stdout","init_completed":true}`))

	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	for range 3 {
		if err := Save(Config{Provider: "rules", Model: "litter-model", Delivery: "stdout", InitCompleted: true}); err != nil {
			t.Fatalf("repeated Save: %v", err)
		}
		if _, err := Load(); err != nil {
			t.Fatalf("repeated Load: %v", err)
		}
	}
	assertNoConfigLitter(t, preferred, legacy)
	info, err := os.Stat(configPathAt(preferred))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("preferred config mode/type = %v, want regular 0600", info.Mode())
	}
}
