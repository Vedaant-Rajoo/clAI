package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vedaant-Rajoo/clai/internal/configroot"
	"github.com/zalando/go-keyring"
)

type fakeKeyring struct {
	values    map[string]string
	getErr    error
	setErr    error
	deleteErr error
}

func (f *fakeKeyring) Get(_, user string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	value, ok := f.values[user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}

func (f *fakeKeyring) Set(_, user, password string) error {
	if f.setErr != nil {
		return f.setErr
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[user] = password
	return nil
}

func (f *fakeKeyring) Delete(_, user string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.values[user]; !ok {
		return keyring.ErrNotFound
	}
	delete(f.values, user)
	return nil
}

func useTestBackends(t *testing.T, kr *fakeKeyring) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve test config directory: %v", err)
	}
	useTestBackendsAt(t, kr, base)
	return base
}

func useTestBackendsAt(t *testing.T, kr *fakeKeyring, base string) {
	t.Helper()
	oldKeyring := credentialKeyring
	oldRoots := resolveConfigRoots
	credentialKeyring = kr
	resolveConfigRoots = func() (configroot.Roots, error) {
		return configroot.Roots{Preferred: base}, nil
	}
	t.Cleanup(func() {
		credentialKeyring = oldKeyring
		resolveConfigRoots = oldRoots
	})
}

func credentialPaths(base string) (string, string) {
	dir := filepath.Join(base, "clai")
	return dir, filepath.Join(dir, fileName)
}

func writeCredentialFixture(t *testing.T, base, data string) string {
	t.Helper()
	dir, path := credentialPaths(base)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("Mkdir credential dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile credential fixture: %v", err)
	}
	return path
}

func TestResolvePrecedence(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		env      string
		keyring  map[string]string
		file     string
		want     string
	}{
		{name: "explicit beats all", explicit: "flag-key", env: "env-key", keyring: map[string]string{"openrouter": "keyring-key"}, file: `{"openrouter":"file-key"}`, want: "flag-key"},
		{name: "environment beats stored", env: "env-key", keyring: map[string]string{"openrouter": "keyring-key"}, file: `{"openrouter":"file-key"}`, want: "env-key"},
		{name: "keyring beats file", keyring: map[string]string{"openrouter": "keyring-key"}, file: `{"openrouter":"file-key"}`, want: "keyring-key"},
		{name: "not found falls back to file", file: `{"openrouter":"file-key"}`, want: "file-key"},
		{name: "not configured", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kr := &fakeKeyring{values: tt.keyring}
			base := useTestBackends(t, kr)
			t.Setenv("OPENROUTER_API_KEY", tt.env)
			if tt.file != "" {
				writeCredentialFixture(t, base, tt.file)
			}

			got, err := Resolve("openrouter", tt.explicit)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Resolve = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAnthropicAuthResolutionAndAmbientDisabled(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		env      string
		keyring  string
		file     string
		want     string
	}{
		{name: "explicit beats environment keyring and file", explicit: "flag-key", env: "env-key", keyring: "keyring-key", file: "file-key", want: "flag-key"},
		{name: "environment beats keyring and file", env: "env-key", keyring: "keyring-key", file: "file-key", want: "env-key"},
		{name: "keyring beats file", keyring: "keyring-key", file: "file-key", want: "keyring-key"},
		{name: "private config fallback", file: "file-key", want: "file-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := map[string]string{}
			if tt.keyring != "" {
				values["anthropic"] = tt.keyring
			}
			base := useTestBackends(t, &fakeKeyring{values: values})
			t.Setenv("ANTHROPIC_API_KEY", tt.env)
			// These ambient SDK credentials are intentionally irrelevant to clai's
			// resolver; the provider separately proves WithoutEnvironmentDefaults.
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "ambient-token")
			t.Setenv("ANTHROPIC_PROFILE", "ambient-profile")
			t.Setenv("ANTHROPIC_FEDERATION_RULE_ID", "ambient-rule")
			t.Setenv("ANTHROPIC_ORGANIZATION_ID", "ambient-org")
			t.Setenv("ANTHROPIC_SERVICE_ACCOUNT_ID", "ambient-account")
			t.Setenv("ANTHROPIC_IDENTITY_TOKEN", "ambient-identity")
			if tt.file != "" {
				writeCredentialFixture(t, base, `{"anthropic":"`+tt.file+`"}`)
			}
			got, err := Resolve("anthropic", tt.explicit)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Resolve = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEmptySuccessfulKeyringValueFallsBackConsistently(t *testing.T) {
	kr := &fakeKeyring{values: map[string]string{"openrouter": ""}}
	base := useTestBackends(t, kr)
	writeCredentialFixture(t, base, `{"openrouter":"fallback-key"}`)

	got, err := Resolve("openrouter", "")
	if err != nil || got != "fallback-key" {
		t.Fatalf("Resolve = %q, %v; want fallback-key", got, err)
	}
	source, err := SourceWithError("openrouter", "")
	if err != nil || source != "config file" {
		t.Fatalf("SourceWithError = %q, %v; want config file", source, err)
	}
}

func TestResolveOperationalKeyringError(t *testing.T) {
	backendErr := errors.New("keyring unavailable")
	kr := &fakeKeyring{getErr: backendErr}
	base := useTestBackends(t, kr)
	t.Setenv("OPENROUTER_API_KEY", "")

	if _, err := Resolve("openrouter", ""); !errors.Is(err, backendErr) {
		t.Fatalf("Resolve error = %v, want keyring error", err)
	}

	writeCredentialFixture(t, base, `{"openrouter":"fallback-key"}`)
	got, err := Resolve("openrouter", "")
	if err != nil {
		t.Fatalf("Resolve with fallback file: %v", err)
	}
	if got != "fallback-key" {
		t.Fatalf("Resolve with fallback file = %q, want fallback-key", got)
	}
}

func TestSourceWithErrorPrecedence(t *testing.T) {
	kr := &fakeKeyring{values: map[string]string{"openrouter": "keyring-key"}}
	base := useTestBackends(t, kr)
	writeCredentialFixture(t, base, `{"openrouter":"file-key"}`)

	if got, err := SourceWithError("openrouter", "flag-key"); err != nil || got != "flag" {
		t.Fatalf("SourceWithError flag = %q, %v", got, err)
	}
	t.Setenv("OPENROUTER_API_KEY", "env-key")
	if got, err := SourceWithError("openrouter", ""); err != nil || got != "env (OPENROUTER_API_KEY)" {
		t.Fatalf("SourceWithError env = %q, %v", got, err)
	}
	t.Setenv("OPENROUTER_API_KEY", "")
	if got, err := SourceWithError("openrouter", ""); err != nil || got != "keyring" {
		t.Fatalf("SourceWithError keyring = %q, %v", got, err)
	}
	kr.values = nil
	if got, err := SourceWithError("openrouter", ""); err != nil || got != "config file" {
		t.Fatalf("SourceWithError file = %q, %v", got, err)
	}
}

func TestFileFallbackCreatesAndVerifiesPrivatePaths(t *testing.T) {
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	base := useTestBackends(t, kr)
	dir, path := credentialPaths(base)

	if err := Store("openrouter", "secret-key"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	for name, target := range map[string]struct {
		path string
		mode os.FileMode
	}{
		"directory": {dir, 0o700},
		"file":      {path, 0o600},
	} {
		info, err := os.Lstat(target.path)
		if err != nil {
			t.Fatalf("Lstat %s: %v", name, err)
		}
		if info.Mode().Perm() != target.mode {
			t.Errorf("%s mode = %o, want %o", name, info.Mode().Perm(), target.mode)
		}
	}

	if err := Store("anthropic", "other-key"); err != nil {
		t.Fatalf("Store existing secure paths: %v", err)
	}
	openrouter, err := readFile("openrouter")
	if err != nil || openrouter != "secret-key" {
		t.Fatalf("preserved entry = %q, %v", openrouter, err)
	}
	anthropic, err := readFile("anthropic")
	if err != nil || anthropic != "other-key" {
		t.Fatalf("new entry = %q, %v", anthropic, err)
	}
}

func TestInsecurePathsRejectedAcrossOperations(t *testing.T) {
	operations := []struct {
		name string
		run  func() error
	}{
		{name: "read", run: func() error { _, err := Resolve("openrouter", ""); return err }},
		{name: "write", run: func() error { return Store("openrouter", "new-secret") }},
		{name: "source", run: func() error { _, err := SourceWithError("openrouter", ""); return err }},
		{name: "delete", run: func() error { return Delete("openrouter") }},
	}
	fixtures := []struct {
		name  string
		setup func(t *testing.T, base string)
	}{
		{name: "0755 directory", setup: func(t *testing.T, base string) {
			dir, _ := credentialPaths(base)
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "0644 file", setup: func(t *testing.T, base string) {
			path := writeCredentialFixture(t, base, `{"openrouter":"stored-secret"}`)
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, fixture := range fixtures {
		for _, operation := range operations {
			t.Run(fixture.name+"/"+operation.name, func(t *testing.T) {
				kr := &fakeKeyring{getErr: keyring.ErrNotFound, setErr: errors.New("unavailable")}
				base := useTestBackends(t, kr)
				fixture.setup(t, base)
				if err := operation.run(); err == nil {
					t.Fatal("operation unexpectedly succeeded")
				}
			})
		}
	}
}

func TestNonRegularAndSymlinkPathsRejected(t *testing.T) {
	fixtures := []struct {
		name  string
		setup func(t *testing.T, base string)
	}{
		{name: "directory path is file", setup: func(t *testing.T, base string) {
			dir, _ := credentialPaths(base)
			if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "directory path is symlink", setup: func(t *testing.T, base string) {
			target := t.TempDir()
			dir, _ := credentialPaths(base)
			if err := os.Symlink(target, dir); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "credential path is directory", setup: func(t *testing.T, base string) {
			dir, path := credentialPaths(base)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "credential path is symlink", setup: func(t *testing.T, base string) {
			dir, path := credentialPaths(base)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, []byte(`{"openrouter":"stored-secret"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			kr := &fakeKeyring{getErr: keyring.ErrNotFound}
			base := useTestBackends(t, kr)
			fixture.setup(t, base)
			if _, err := readFile("openrouter"); err == nil {
				t.Fatal("readFile unexpectedly succeeded")
			}
		})
	}
}

func TestMalformedJSONRejectedUnchanged(t *testing.T) {
	secret := "secret-must-not-leak"
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	base := useTestBackends(t, kr)
	path := writeCredentialFixture(t, base, `{"openrouter":"`+secret+`"`)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	err = Store("anthropic", "new-secret")
	if err == nil {
		t.Fatal("Store unexpectedly accepted malformed JSON")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "new-secret") {
		t.Fatalf("error disclosed a credential: %v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatalf("malformed file changed: got %q, want %q", after, before)
	}
}

func TestDeleteMissingKeyringRemovesExistingFileEntry(t *testing.T) {
	kr := &fakeKeyring{}
	base := useTestBackends(t, kr)
	writeCredentialFixture(t, base, `{"openrouter":"stored-secret","anthropic":"keep-me"}`)

	if err := Delete("openrouter"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := readFile("openrouter")
	if err != nil || got != "" {
		t.Fatalf("deleted entry = %q, %v", got, err)
	}
	got, err = readFile("anthropic")
	if err != nil || got != "keep-me" {
		t.Fatalf("preserved entry = %q, %v", got, err)
	}
}

func TestKeyringStoreWithoutFallbackDoesNotCreateConfigArtifacts(t *testing.T) {
	kr := &fakeKeyring{}
	base := useTestBackends(t, kr)
	if err := Store("openrouter", "secret-value"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	dir, _ := credentialPaths(base)
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential directory exists after keyring-only Store: %v", err)
	}
}

func TestStoreSurfacesStaleFallbackCleanupFailure(t *testing.T) {
	secret := "secret-must-not-leak"
	kr := &fakeKeyring{}
	base := useTestBackends(t, kr)
	path := writeCredentialFixture(t, base, `{"openrouter":"`+secret+`"}`)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	err := Store("openrouter", "replacement-secret")
	if err == nil {
		t.Fatal("Store unexpectedly hid stale cleanup failure")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "replacement-secret") {
		t.Fatalf("error disclosed a credential: %v", err)
	}
}

func TestDeleteSurfacesOperationalKeyringErrorAfterFileCleanup(t *testing.T) {
	backendErr := errors.New("keyring unavailable")
	kr := &fakeKeyring{deleteErr: backendErr}
	base := useTestBackends(t, kr)
	writeCredentialFixture(t, base, `{"openrouter":"stored-secret"}`)

	err := Delete("openrouter")
	if !errors.Is(err, backendErr) {
		t.Fatalf("Delete error = %v, want keyring error", err)
	}
	got, readErr := readFile("openrouter")
	if readErr != nil || got != "" {
		t.Fatalf("file cleanup after keyring error = %q, %v", got, readErr)
	}
}

func TestSourceCompatibilityReturnsNoneOnInspectionError(t *testing.T) {
	kr := &fakeKeyring{getErr: errors.New("keyring unavailable")}
	useTestBackends(t, kr)
	if got := Source("openrouter", ""); got != "none" {
		t.Fatalf("Source = %q, want none", got)
	}
}

func TestEnvVarForUnknownProvider(t *testing.T) {
	if got := EnvVarFor("not-a-provider"); got != "" {
		t.Fatalf("EnvVarFor unknown = %q, want empty", got)
	}
}
