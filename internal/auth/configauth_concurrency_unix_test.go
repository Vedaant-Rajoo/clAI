//go:build darwin || linux

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vedaant-Rajoo/clai/internal/config"
	"github.com/Vedaant-Rajoo/clai/internal/configroot"
	"github.com/zalando/go-keyring"
)

const (
	configAuthHelperRoleEnv       = "CLAI_CONFIG_AUTH_TEST_ROLE"
	configAuthHelperLegacyEnv     = "CLAI_CONFIG_AUTH_TEST_LEGACY"
	configAuthReadyFD             = 3
	configAuthReleaseFD           = 4
	configAuthCombinedOutputLimit = 64 << 10

	configAuthConfigSentinel     = "config-credential-sentinel-01-08"
	configAuthLegacyOpenRouter   = "legacy-openrouter-secret-01-08"
	configAuthLegacyAnthropic    = "legacy-anthropic-secret-01-08"
	configAuthFinalAnthropic     = "final-anthropic-secret-01-08"
	configAuthKeyringUnavailable = "test keyring unavailable"
)

var configAuthRoles = []string{
	"config-load",
	"config-save",
	"auth-resolve",
	"auth-store",
	"auth-delete",
}

type configAuthFixture struct {
	root                       string
	home                       string
	preferred                  string
	legacy                     string
	createdPreferredComponents []string
	seedLegacy                 bool
}

type configAuthOperationResult struct {
	role  string
	cfg   config.Config
	value string
	err   error
}

type configAuthChild struct {
	role    string
	command *exec.Cmd
	ready   *os.File
	release *os.File
	done    chan error
}

type configAuthReadyResult struct {
	role string
	err  error
}

type configAuthChildResult struct {
	role string
	err  error
}

type configAuthBoundedOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (o *configAuthBoundedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	remaining := o.limit - o.buffer.Len()
	if remaining > 0 {
		write := p
		if len(write) > remaining {
			write = write[:remaining]
		}
		_, _ = o.buffer.Write(write)
	}
	if len(p) > remaining {
		o.overflow = true
	}
	return len(p), nil
}

func (o *configAuthBoundedOutput) snapshot() (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String(), o.overflow
}

func TestConfigAuthSharedRootConcurrentSameProcess(t *testing.T) {
	t.Run("portable shared preferred root", func(t *testing.T) {
		runConfigAuthSameProcessCase(t, false)
	})
	t.Run("darwin legacy roots", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("Darwin Application Support legacy root is unavailable on this platform")
		}
		runConfigAuthSameProcessCase(t, true)
	})
}

func TestConfigAuthDualRootConcurrentProcesses(t *testing.T) {
	t.Run("portable shared preferred root", func(t *testing.T) {
		runConfigAuthChildProcessCase(t, false)
	})
	t.Run("darwin legacy roots", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("Darwin Application Support legacy root is unavailable on this platform")
		}
		runConfigAuthChildProcessCase(t, true)
	})
}

func runConfigAuthSameProcessCase(t *testing.T, seedLegacy bool) {
	t.Helper()
	fixture := newConfigAuthFixture(t, seedLegacy)
	useConfigAuthProductionRoots(t, fixture)
	if seedLegacy {
		seedConfigAuthLegacyState(t, fixture)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ready := make(chan string, len(configAuthRoles))
	start := make(chan struct{})
	results := make(chan configAuthOperationResult, len(configAuthRoles))

	for _, role := range configAuthRoles {
		go func(role string) {
			ready <- role
			<-start
			results <- executeConfigAuthOperation(role)
		}(role)
	}

	seenReady := make(map[string]bool, len(configAuthRoles))
	for range configAuthRoles {
		select {
		case role := <-ready:
			if seenReady[role] {
				t.Fatalf("same-process role %q reached the start barrier twice", role)
			}
			seenReady[role] = true
		case <-ctx.Done():
			t.Fatal("timed out waiting for all same-process roles to reach the start barrier")
		}
	}
	close(start)

	seenResults := make(map[string]bool, len(configAuthRoles))
	var operationErrors []configAuthOperationResult
	for range configAuthRoles {
		select {
		case result := <-results:
			if seenResults[result.role] {
				t.Fatalf("same-process role %q returned twice", result.role)
			}
			seenResults[result.role] = true
			if result.err != nil {
				assertConfigAuthSecretFree(t, result.err.Error())
				operationErrors = append(operationErrors, result)
			}
			assertConfigAuthOperationResult(t, result, seedLegacy)
		case <-ctx.Done():
			t.Fatal("mixed same-process config/auth operations did not finish before the deadline")
		}
	}
	if len(operationErrors) != 0 {
		t.Fatalf("same-process role %q failed: %v", operationErrors[0].role, operationErrors[0].err)
	}

	assertConfigAuthFinalState(t, fixture)
}

func runConfigAuthChildProcessCase(t *testing.T, seedLegacy bool) {
	t.Helper()
	fixture := newConfigAuthFixture(t, seedLegacy)
	useConfigAuthProductionRoots(t, fixture)
	if seedLegacy {
		seedConfigAuthLegacyState(t, fixture)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output := &configAuthBoundedOutput{limit: configAuthCombinedOutputLimit}
	children := make([]configAuthChild, 0, len(configAuthRoles))
	for _, role := range configAuthRoles {
		children = append(children, startConfigAuthChild(t, ctx, fixture, role, output))
	}
	t.Cleanup(func() {
		for _, child := range children {
			_ = child.ready.Close()
			_ = child.release.Close()
			if child.command.Process != nil && child.command.ProcessState == nil {
				_ = child.command.Process.Kill()
			}
		}
	})

	readyResults := make(chan configAuthReadyResult, len(children))
	for _, child := range children {
		go func(child configAuthChild) {
			var signal [1]byte
			_, err := io.ReadFull(child.ready, signal[:])
			readyResults <- configAuthReadyResult{role: child.role, err: err}
		}(child)
	}
	seenReady := make(map[string]bool, len(children))
	for range children {
		select {
		case result := <-readyResults:
			if result.err != nil {
				t.Fatalf("child role %q did not announce readiness: %v", result.role, result.err)
			}
			if seenReady[result.role] {
				t.Fatalf("child role %q announced readiness twice", result.role)
			}
			seenReady[result.role] = true
		case <-ctx.Done():
			t.Fatal("timed out waiting for all child roles to announce readiness")
		}
	}

	for _, child := range children {
		if _, err := child.release.Write([]byte{1}); err != nil {
			t.Fatalf("release child role %q: %v", child.role, err)
		}
		if err := child.release.Close(); err != nil {
			t.Fatalf("close release pipe for child role %q: %v", child.role, err)
		}
	}

	childResults := make(chan configAuthChildResult, len(children))
	for _, child := range children {
		go func(child configAuthChild) {
			childResults <- configAuthChildResult{role: child.role, err: <-child.done}
		}(child)
	}
	var childErrors []configAuthChildResult
	for range children {
		select {
		case result := <-childResults:
			if result.err != nil {
				childErrors = append(childErrors, result)
			}
		case <-ctx.Done():
			t.Fatal("mixed child-process config/auth operations did not finish before the deadline")
		}
	}

	captured, overflow := output.snapshot()
	assertConfigAuthSecretFree(t, captured)
	if overflow {
		t.Fatalf("combined child output exceeded the %d-byte limit", configAuthCombinedOutputLimit)
	}
	if len(childErrors) != 0 {
		t.Fatalf("child role %q failed: %v\n%s", childErrors[0].role, childErrors[0].err, captured)
	}

	assertConfigAuthFinalState(t, fixture)
}

func newConfigAuthFixture(t *testing.T, seedLegacy bool) configAuthFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize mixed-concurrency test root: %v", err)
	}
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatalf("create isolated HOME: %v", err)
	}
	preferredParent := filepath.Join(root, "preferred")
	preferred := filepath.Join(preferredParent, "xdg")
	return configAuthFixture{
		root:                       root,
		home:                       home,
		preferred:                  preferred,
		legacy:                     filepath.Join(home, "Library", "Application Support"),
		createdPreferredComponents: []string{preferredParent, preferred},
		seedLegacy:                 seedLegacy,
	}
}

func useConfigAuthProductionRoots(t *testing.T, fixture configAuthFixture) {
	t.Helper()
	for key, value := range configAuthEnvironment(fixture, "") {
		t.Setenv(key, value)
	}

	oldKeyring := credentialKeyring
	oldRoots := resolveConfigRoots
	credentialKeyring = configAuthFileOnlyKeyring()
	resolveConfigRoots = configroot.Resolve
	t.Cleanup(func() {
		credentialKeyring = oldKeyring
		resolveConfigRoots = oldRoots
	})
}

func configAuthFileOnlyKeyring() *fakeKeyring {
	return &fakeKeyring{
		getErr:    keyring.ErrNotFound,
		setErr:    errors.New(configAuthKeyringUnavailable),
		deleteErr: keyring.ErrNotFound,
	}
}

func configAuthEnvironment(fixture configAuthFixture, role string) map[string]string {
	legacy := ""
	if fixture.seedLegacy {
		legacy = "1"
	}
	return map[string]string{
		"HOME":                            fixture.home,
		"XDG_CONFIG_HOME":                 fixture.preferred,
		"CLAI_PROVIDER":                   "",
		"CLAI_MODEL":                      "",
		"CLAI_DELIVERY":                   "",
		"OPENROUTER_API_KEY":              "",
		"ANTHROPIC_API_KEY":               "",
		"OPENAI_API_KEY":                  "",
		configAuthHelperRoleEnv:           role,
		configAuthHelperLegacyEnv:         legacy,
		"CLAI_AUTH_HELPER_BASE":           "",
		"CLAI_AUTH_MIGRATION_HELPER_BASE": "",
	}
}

func seedConfigAuthLegacyState(t *testing.T, fixture configAuthFixture) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Fatal("legacy config/auth fixture is valid only on Darwin")
	}
	app := filepath.Join(fixture.legacy, "clai")
	if err := os.MkdirAll(app, 0o700); err != nil {
		t.Fatalf("create Darwin legacy app directory: %v", err)
	}
	if err := os.Chmod(app, 0o700); err != nil {
		t.Fatalf("set Darwin legacy app directory permissions: %v", err)
	}
	legacyConfig := map[string]any{
		"contract":       "legacy-config-contract",
		"provider":       "openrouter",
		"model":          "legacy-model",
		"delivery":       "clipboard",
		"init_completed": false,
		"api_key":        configAuthConfigSentinel,
	}
	writeConfigAuthJSONFixture(t, filepath.Join(app, "config.json"), legacyConfig)
	writeConfigAuthJSONFixture(t, filepath.Join(app, fileName), map[string]string{
		"openrouter": configAuthLegacyOpenRouter,
		"anthropic":  configAuthLegacyAnthropic,
	})
}

func writeConfigAuthJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode mixed-concurrency fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write mixed-concurrency fixture: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("set mixed-concurrency fixture permissions: %v", err)
	}
}

func executeConfigAuthOperation(role string) configAuthOperationResult {
	result := configAuthOperationResult{role: role}
	switch role {
	case "config-load":
		result.cfg, result.err = config.Load()
	case "config-save":
		result.err = config.Save(configAuthFinalConfig())
	case "auth-resolve":
		result.value, result.err = Resolve("openrouter", "")
	case "auth-store":
		result.err = Store("anthropic", configAuthFinalAnthropic)
	case "auth-delete":
		result.err = Delete("openrouter")
	default:
		result.err = fmt.Errorf("unknown mixed-concurrency role %q", role)
	}
	return result
}

func assertConfigAuthOperationResult(t *testing.T, result configAuthOperationResult, seedLegacy bool) {
	t.Helper()
	if result.err != nil {
		return
	}
	switch result.role {
	case "config-load":
		if result.cfg == (config.Config{}) || result.cfg == configAuthFinalPersistedConfig() {
			return
		}
		if seedLegacy && result.cfg == configAuthLegacyPersistedConfig() {
			return
		}
		t.Fatal("concurrent config Load returned a state outside the serialized outcomes")
	case "auth-resolve":
		if result.value == "" || seedLegacy && result.value == configAuthLegacyOpenRouter {
			return
		}
		t.Fatal("concurrent auth Resolve returned a state outside the serialized outcomes")
	}
}

func configAuthFinalConfig() config.Config {
	return config.Config{
		Provider:      "anthropic",
		Model:         "claude-config-auth-final",
		Delivery:      "stdout",
		InitCompleted: true,
	}
}

func configAuthFinalPersistedConfig() config.Config {
	cfg := configAuthFinalConfig()
	cfg.Contract = config.ConfigContract
	return cfg
}

func configAuthLegacyPersistedConfig() config.Config {
	return config.Config{
		Contract:      config.ConfigContract,
		Provider:      "openrouter",
		Model:         "legacy-model",
		Delivery:      "clipboard",
		InitCompleted: false,
	}
}

func startConfigAuthChild(
	t *testing.T,
	ctx context.Context,
	fixture configAuthFixture,
	role string,
	output *configAuthBoundedOutput,
) configAuthChild {
	t.Helper()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create ready pipe for child role %q: %v", role, err)
	}
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		_ = readyRead.Close()
		_ = readyWrite.Close()
		t.Fatalf("create release pipe for child role %q: %v", role, err)
	}

	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConfigAuthProcessHelper$")
	command.Env = configAuthChildEnvironment(fixture, role)
	command.ExtraFiles = []*os.File{readyWrite, releaseRead}
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		_ = readyRead.Close()
		_ = readyWrite.Close()
		_ = releaseRead.Close()
		_ = releaseWrite.Close()
		t.Fatalf("start child role %q: %v", role, err)
	}
	if err := errors.Join(readyWrite.Close(), releaseRead.Close()); err != nil {
		_ = command.Process.Kill()
		t.Fatalf("close inherited pipe copies for child role %q: %v", role, err)
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	return configAuthChild{
		role:    role,
		command: command,
		ready:   readyRead,
		release: releaseWrite,
		done:    done,
	}
}

func configAuthChildEnvironment(fixture configAuthFixture, role string) []string {
	overrides := configAuthEnvironment(fixture, role)
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replaced := overrides[key]; replaced {
				continue
			}
		}
		environment = append(environment, entry)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environment = append(environment, key+"="+overrides[key])
	}
	return environment
}

func TestConfigAuthProcessHelper(t *testing.T) {
	role := os.Getenv(configAuthHelperRoleEnv)
	if role == "" {
		return
	}
	credentialKeyring = configAuthFileOnlyKeyring()
	resolveConfigRoots = configroot.Resolve

	ready := os.NewFile(configAuthReadyFD, "config-auth-ready")
	release := os.NewFile(configAuthReleaseFD, "config-auth-release")
	if ready == nil || release == nil {
		t.Fatal("mixed-concurrency helper did not inherit both synchronization pipes")
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		t.Fatalf("announce mixed-concurrency helper readiness: %v", err)
	}
	if err := ready.Close(); err != nil {
		t.Fatalf("close mixed-concurrency helper ready pipe: %v", err)
	}
	var signal [1]byte
	if _, err := io.ReadFull(release, signal[:]); err != nil {
		t.Fatalf("wait for mixed-concurrency helper release: %v", err)
	}
	if err := release.Close(); err != nil {
		t.Fatalf("close mixed-concurrency helper release pipe: %v", err)
	}

	result := executeConfigAuthOperation(role)
	if result.err != nil {
		assertConfigAuthSecretFree(t, result.err.Error())
		t.Fatalf("mixed-concurrency helper role %q failed: %v", role, result.err)
	}
	assertConfigAuthOperationResult(t, result, os.Getenv(configAuthHelperLegacyEnv) == "1")
}

func assertConfigAuthFinalState(t *testing.T, fixture configAuthFixture) {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		assertConfigAuthSecretFree(t, err.Error())
		t.Fatalf("final config Load failed: %v", err)
	}
	if cfg != configAuthFinalPersistedConfig() {
		t.Fatalf("final config = %+v, want fixed Save payload", cfg)
	}

	anthropic, err := Resolve("anthropic", "")
	if err != nil {
		assertConfigAuthSecretFree(t, err.Error())
		t.Fatalf("final anthropic Resolve failed: %v", err)
	}
	if anthropic != configAuthFinalAnthropic {
		t.Fatal("final anthropic credential did not equal the fixed Store payload")
	}
	if source, err := SourceWithError("anthropic", ""); err != nil {
		assertConfigAuthSecretFree(t, err.Error())
		t.Fatalf("final anthropic SourceWithError failed: %v", err)
	} else {
		assertConfigAuthSecretFree(t, source)
		if source != "config file" || Source("anthropic", "") != "config file" {
			t.Fatal("final anthropic credential source was not the private config file")
		}
	}
	openrouter, err := Resolve("openrouter", "")
	if err != nil {
		assertConfigAuthSecretFree(t, err.Error())
		t.Fatalf("final openrouter Resolve failed: %v", err)
	}
	if openrouter != "" {
		t.Fatal("final credential state resurrected the deleted openrouter provider")
	}
	if source, err := SourceWithError("openrouter", ""); err != nil {
		assertConfigAuthSecretFree(t, err.Error())
		t.Fatalf("final openrouter SourceWithError failed: %v", err)
	} else {
		assertConfigAuthSecretFree(t, source)
		if source != "none" || Source("openrouter", "") != "none" {
			t.Fatal("final deleted openrouter credential still reported a source")
		}
	}

	assertConfigAuthStorageTree(t, fixture)
}

func assertConfigAuthStorageTree(t *testing.T, fixture configAuthFixture) {
	t.Helper()
	preferredApp := filepath.Join(fixture.preferred, "clai")
	legacyApp := filepath.Join(fixture.legacy, "clai")
	preferredConfig := filepath.Join(preferredApp, "config.json")
	preferredCredentials := filepath.Join(preferredApp, fileName)
	legacyConfig := filepath.Join(legacyApp, "config.json")
	legacyCredentials := filepath.Join(legacyApp, fileName)

	for _, path := range fixture.createdPreferredComponents {
		assertConfigAuthDirectoryMode(t, path, 0o700)
	}
	assertConfigAuthDirectoryMode(t, preferredApp, 0o700)
	assertConfigAuthFileMode(t, preferredConfig, 0o600)
	assertConfigAuthFileMode(t, preferredCredentials, 0o600)
	assertConfigAuthPathMissing(t, legacyConfig)
	assertConfigAuthPathMissing(t, legacyCredentials)

	configBytes, err := os.ReadFile(preferredConfig)
	if err != nil {
		t.Fatalf("read final preferred config: %v", err)
	}
	assertConfigAuthSecretFree(t, string(configBytes))
	var configFields map[string]json.RawMessage
	if err := json.Unmarshal(configBytes, &configFields); err != nil {
		t.Fatalf("decode final preferred config: %v", err)
	}
	wantConfigFields := []string{"contract", "delivery", "init_completed", "model", "provider"}
	gotConfigFields := make([]string, 0, len(configFields))
	for field := range configFields {
		gotConfigFields = append(gotConfigFields, field)
	}
	sort.Strings(gotConfigFields)
	if !equalConfigAuthStrings(gotConfigFields, wantConfigFields) {
		t.Fatalf("final config fields = %v, want exactly %v", gotConfigFields, wantConfigFields)
	}

	credentialBytes, err := os.ReadFile(preferredCredentials)
	if err != nil {
		t.Fatalf("read final preferred credentials: %v", err)
	}
	var credentials map[string]string
	if err := json.Unmarshal(credentialBytes, &credentials); err != nil {
		t.Fatalf("decode final preferred credentials: %v", err)
	}
	if len(credentials) != 1 || credentials["anthropic"] != configAuthFinalAnthropic {
		keys := make([]string, 0, len(credentials))
		for provider := range credentials {
			keys = append(keys, provider)
		}
		sort.Strings(keys)
		t.Fatalf("final credential providers = %v, want only anthropic with the fixed Store payload", keys)
	}

	configCount := 0
	credentialCount := 0
	err = filepath.WalkDir(fixture.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if name == ".config.lock" {
			t.Errorf("transient config lock remains at %s", path)
		}
		if isConfigAuthTemporaryName(name) {
			t.Errorf("config/auth temporary file remains at %s", path)
		}
		switch name {
		case "config.json":
			configCount++
			if path != preferredConfig {
				t.Errorf("duplicate or legacy config file remains at %s", path)
			}
		case fileName:
			credentialCount++
			if path != preferredCredentials {
				t.Errorf("duplicate or legacy credential file remains at %s", path)
			}
		case lockName:
			assertConfigAuthCredentialLock(t, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk mixed config/auth storage tree: %v", err)
	}
	if configCount != 1 || credentialCount != 1 {
		t.Fatalf("preferred data-file counts = config:%d credentials:%d, want 1/1", configCount, credentialCount)
	}

	assertConfigAuthDirectoryEntries(t, preferredApp, []string{lockName, "config.json", fileName})
	if fixture.seedLegacy {
		assertConfigAuthDirectoryMode(t, legacyApp, 0o700)
		assertConfigAuthDirectoryEntries(t, legacyApp, []string{lockName})
	}
}

func assertConfigAuthDirectoryMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect directory %q: %v", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != want {
		t.Fatalf("directory %q mode/type = %v, want non-symlink directory %o", path, info.Mode(), want)
	}
}

func assertConfigAuthFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect file %q: %v", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != want {
		t.Fatalf("file %q mode/type = %v, want regular non-symlink %o", path, info.Mode(), want)
	}
}

func assertConfigAuthCredentialLock(t *testing.T, path string) {
	t.Helper()
	assertConfigAuthFileMode(t, path, 0o600)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credential lock %q: %v", path, err)
	}
	assertConfigAuthSecretFree(t, string(data))
	if len(data) != 0 {
		t.Fatalf("credential lock %q contains data", path)
	}
}

func assertConfigAuthDirectoryEntries(t *testing.T, path string, want []string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("list storage directory %q: %v", path, err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	sort.Strings(got)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if !equalConfigAuthStrings(got, want) {
		t.Fatalf("storage directory %q entries = %v, want %v", path, got, want)
	}
}

func assertConfigAuthPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy data file %q remains: %v", path, err)
	}
}

func isConfigAuthTemporaryName(name string) bool {
	return strings.HasPrefix(name, ".config-") && strings.HasSuffix(name, ".tmp") ||
		strings.HasPrefix(name, ".credentials-") && strings.HasSuffix(name, ".tmp")
}

func assertConfigAuthSecretFree(t *testing.T, text string) {
	t.Helper()
	for _, sentinel := range []string{
		configAuthConfigSentinel,
		configAuthLegacyOpenRouter,
		configAuthLegacyAnthropic,
		configAuthFinalAnthropic,
	} {
		if strings.Contains(text, sentinel) {
			t.Fatal("config, diagnostic, source, lock, or child output disclosed a planted secret sentinel")
		}
	}
}

func equalConfigAuthStrings(left, right []string) bool {
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
