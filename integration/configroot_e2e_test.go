package integration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const configRootProcessTimeout = 15 * time.Second

type configRootProcessResult struct {
	exitCode int
	stdout   string
	stderr   string
}

// TestConfigRootPreferredPathEndToEnd proves the shipped binary reads the
// absolute XDG_CONFIG_HOME root instead of HOME/.config on Darwin and Linux
// (REQ-CONFIG-001/002, REQ-ACCEPT-CONFIG-004).
func TestConfigRootPreferredPathEndToEnd(t *testing.T) {
	requireUnixConfigRoot(t)
	binary := buildCLAI(t)
	root := canonicalTempDir(t)
	home := filepath.Join(root, "home")
	xdg := filepath.Join(root, "custom-xdg")
	mustMkdirPrivate(t, home)
	mustMkdirPrivate(t, xdg)

	const preferredProvider = "preferred-xdg-provider-sentinel"
	writeConfigRootFixture(t, filepath.Join(xdg, "clai", "config.json"), `{
  "contract": "config-file/v1",
  "provider": "preferred-xdg-provider-sentinel",
  "model": "preferred-xdg-model",
  "delivery": "stdout",
  "init_completed": true
}
`)
	writeConfigRootFixture(t, filepath.Join(home, ".config", "clai", "config.json"), `{
  "contract": "config-file/v1",
  "provider": "wrong-home-provider-sentinel",
  "init_completed": true
}
`)

	result := runConfigRootProcess(t, binary, home, xdg)
	if result.exitCode != 1 {
		t.Fatalf("exit = %d, want 1 for the isolated unknown provider; stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
	}
	if result.stdout != "" {
		t.Fatalf("stdout = %q, want empty for provider construction failure", result.stdout)
	}
	if !strings.Contains(result.stderr, preferredProvider) {
		t.Fatalf("stderr = %q, want isolated XDG provider %q", result.stderr, preferredProvider)
	}
	if strings.Contains(result.stderr, "wrong-home-provider-sentinel") {
		t.Fatalf("stderr = %q, HOME fallback overrode absolute XDG_CONFIG_HOME", result.stderr)
	}
}

// TestConfigRootHomeFallbackEndToEnd proves an empty XDG_CONFIG_HOME selects
// HOME/.config. On Darwin, its migration subtest additionally proves an
// old-only legacy file securely creates a missing .config suffix on demand.
func TestConfigRootHomeFallbackEndToEnd(t *testing.T) {
	requireUnixConfigRoot(t)
	binary := buildCLAI(t)

	t.Run("reads HOME/.config when XDG_CONFIG_HOME is empty", func(t *testing.T) {
		root := canonicalTempDir(t)
		home := filepath.Join(root, "home")
		mustMkdirPrivate(t, home)
		const providerName = "home-fallback-provider-sentinel"
		writeConfigRootFixture(t, filepath.Join(home, ".config", "clai", "config.json"), `{
  "contract": "config-file/v1",
  "provider": "home-fallback-provider-sentinel",
  "init_completed": true
}
`)

		result := runConfigRootProcess(t, binary, home, "")
		if result.exitCode != 1 || result.stdout != "" || !strings.Contains(result.stderr, providerName) {
			t.Fatalf("HOME fallback result = exit %d stdout %q stderr %q, want isolated provider %q", result.exitCode, result.stdout, result.stderr, providerName)
		}
	})

	t.Run("Darwin migration creates a missing HOME/.config privately", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skipf("Darwin Application Support migration is unavailable on %s", runtime.GOOS)
		}
		root := canonicalTempDir(t)
		home := filepath.Join(root, "home")
		mustMkdirPrivate(t, home)
		legacy := filepath.Join(home, "Library", "Application Support", "clai", "config.json")
		writeConfigRootFixture(t, legacy, `{
  "contract": "config-file/v1",
  "provider": "home-migration-provider-sentinel",
  "init_completed": true
}
`)
		preferredBase := filepath.Join(home, ".config")
		if _, err := os.Lstat(preferredBase); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("precondition: preferred base unexpectedly exists: %v", err)
		}

		result := runConfigRootProcess(t, binary, home, "")
		if result.exitCode != 1 || result.stdout != "" || !strings.Contains(result.stderr, "home-migration-provider-sentinel") {
			t.Fatalf("migration result = exit %d stdout %q stderr %q", result.exitCode, result.stdout, result.stderr)
		}
		assertConfigRootDirectoryMode(t, preferredBase, 0o700)
		assertConfigRootDirectoryMode(t, filepath.Join(preferredBase, "clai"), 0o700)
		assertConfigRootFileMode(t, filepath.Join(preferredBase, "clai", "config.json"), 0o600)
		assertConfigRootPathMissing(t, legacy)
	})
}

// TestConfigRootLegacyUpgradeEndToEnd proves a requested-output invocation can
// complete the one-way Darwin upgrade without changing its exact stdout,
// emitting diagnostics, or starting an interactive path (REQ-CONFIG-003/008).
func TestConfigRootLegacyUpgradeEndToEnd(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("Darwin Application Support migration is unavailable on %s", runtime.GOOS)
	}
	binary := buildCLAI(t)
	root := canonicalTempDir(t)
	home := filepath.Join(root, "home")
	xdg := filepath.Join(root, "preferred-xdg")
	mustMkdirPrivate(t, home)
	mustMkdirPrivate(t, xdg)
	legacy := filepath.Join(home, "Library", "Application Support", "clai", "config.json")
	writeConfigRootFixture(t, legacy, `{
  "contract": "legacy-contract",
  "provider": "openrouter",
  "model": "legacy-model",
  "delivery": "clipboard",
  "init_completed": true
}
`)

	result := runConfigRootProcess(t, binary, home, xdg, "--version")
	if result.exitCode != 0 {
		t.Fatalf("--version exit = %d, want 0; stdout=%q stderr=%q", result.exitCode, result.stdout, result.stderr)
	}
	if result.stdout != "dev\n" {
		t.Fatalf("--version stdout = %q, want exact %q", result.stdout, "dev\n")
	}
	if result.stderr != "" {
		t.Fatalf("--version stderr = %q, want migration-noise-free output", result.stderr)
	}

	preferred := filepath.Join(xdg, "clai", "config.json")
	data, err := os.ReadFile(preferred)
	if err != nil {
		t.Fatalf("read migrated preferred config: %v", err)
	}
	for _, required := range []string{
		`"contract": "config-file/v1"`,
		`"provider": "openrouter"`,
		`"model": "legacy-model"`,
		`"delivery": "clipboard"`,
		`"init_completed": true`,
	} {
		if !strings.Contains(string(data), required) {
			t.Fatalf("migrated config = %s, want %s", data, required)
		}
	}
	assertConfigRootDirectoryMode(t, filepath.Join(xdg, "clai"), 0o700)
	assertConfigRootFileMode(t, preferred, 0o600)
	assertConfigRootPathMissing(t, legacy)
}

// TestConfigRootDoesNotReadHostState plants config and credential sentinels in
// an outside root, then proves the isolated process neither reports nor mutates
// them while selecting its own XDG config (T-01-GC-25/T-01-GC-29).
func TestConfigRootDoesNotReadHostState(t *testing.T) {
	requireUnixConfigRoot(t)
	binary := buildCLAI(t)
	root := canonicalTempDir(t)
	home := filepath.Join(root, "isolated-home")
	xdg := filepath.Join(root, "isolated-xdg")
	outside := filepath.Join(root, "outside-host-root")
	for _, path := range []string{home, xdg, outside} {
		mustMkdirPrivate(t, path)
	}

	writeConfigRootFixture(t, filepath.Join(xdg, "clai", "config.json"), `{
  "contract": "config-file/v1",
  "provider": "isolated-provider-sentinel",
  "init_completed": true
}
`)
	outsideConfig := filepath.Join(outside, "clai", "config.json")
	outsideCredentials := filepath.Join(outside, "clai", "credentials.json")
	writeConfigRootFixture(t, outsideConfig, `{"contract":"config-file/v1","provider":"outside-host-provider-sentinel","init_completed":true}`)
	writeConfigRootFixture(t, outsideCredentials, `{"openrouter":"outside-host-credential-sentinel"}`)
	beforeConfig := snapshotConfigRootFile(t, outsideConfig)
	beforeCredentials := snapshotConfigRootFile(t, outsideCredentials)

	result := runConfigRootProcess(t, binary, home, xdg)
	if result.exitCode != 1 || !strings.Contains(result.stderr, "isolated-provider-sentinel") {
		t.Fatalf("isolated process = exit %d stdout %q stderr %q", result.exitCode, result.stdout, result.stderr)
	}
	for _, forbidden := range []string{"outside-host-provider-sentinel", "outside-host-credential-sentinel", outside} {
		if strings.Contains(result.stdout, forbidden) || strings.Contains(result.stderr, forbidden) {
			t.Fatalf("isolated process disclosed outside-root sentinel %q: stdout=%q stderr=%q", forbidden, result.stdout, result.stderr)
		}
	}
	assertConfigRootFileUnchanged(t, outsideConfig, beforeConfig)
	assertConfigRootFileUnchanged(t, outsideCredentials, beforeCredentials)
}

type configRootFileSnapshot struct {
	data    []byte
	mode    os.FileMode
	modTime time.Time
}

func snapshotConfigRootFile(t *testing.T, path string) configRootFileSnapshot {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sentinel file %q: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sentinel file %q: %v", path, err)
	}
	return configRootFileSnapshot{data: data, mode: info.Mode(), modTime: info.ModTime()}
}

func assertConfigRootFileUnchanged(t *testing.T, path string, before configRootFileSnapshot) {
	t.Helper()
	after := snapshotConfigRootFile(t, path)
	if string(after.data) != string(before.data) || after.mode != before.mode || !after.modTime.Equal(before.modTime) {
		t.Fatalf("outside-root file %q changed: before=(%q %v %v) after=(%q %v %v)", path, before.data, before.mode, before.modTime, after.data, after.mode, after.modTime)
	}
}

func runConfigRootProcess(t *testing.T, binary, home, xdg string, args ...string) configRootProcessResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), configRootProcessTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = isolatedConfigRootEnvironment(home, xdg)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("clai %v exceeded %s; stdout=%q stderr=%q", args, configRootProcessTimeout, stdout.String(), stderr.String())
	}

	exitCode := 0
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		exitCode = exitErr.ExitCode()
	case err != nil:
		t.Fatalf("run clai %v: %v; stdout=%q stderr=%q", args, err, stdout.String(), stderr.String())
	}
	return configRootProcessResult{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
}

func isolatedConfigRootEnvironment(home, xdg string) []string {
	overrides := map[string]string{
		"HOME":               home,
		"XDG_CONFIG_HOME":    xdg,
		"CLAI_PROVIDER":      "",
		"CLAI_MODEL":         "",
		"CLAI_DELIVERY":      "",
		"OPENROUTER_API_KEY": "",
		"ANTHROPIC_API_KEY":  "",
		"OPENAI_API_KEY":     "",
	}
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
	for key, value := range overrides {
		environment = append(environment, key+"="+value)
	}
	return environment
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize test directory: %v", err)
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("temporary root %q is not absolute", root)
	}
	return root
}

func mustMkdirPrivate(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create private directory %q: %v", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatalf("set private directory mode %q: %v", path, err)
	}
}

func writeConfigRootFixture(t *testing.T, path, data string) {
	t.Helper()
	mustMkdirPrivate(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write fixture %q: %v", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("set fixture mode %q: %v", path, err)
	}
}

func assertConfigRootDirectoryMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect directory %q: %v", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != want {
		t.Fatalf("directory %q mode/type = %v, want non-symlink %o", path, info.Mode(), want)
	}
}

func assertConfigRootFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect file %q: %v", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != want {
		t.Fatalf("file %q mode/type = %v, want regular non-symlink %o", path, info.Mode(), want)
	}
}

func assertConfigRootPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy path %q remains: %v", path, err)
	}
}

func requireUnixConfigRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("XDG config-root end-to-end coverage is unavailable on %s; Windows retains its platform resolver", runtime.GOOS)
	}
}
