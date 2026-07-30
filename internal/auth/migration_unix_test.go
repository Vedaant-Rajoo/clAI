//go:build darwin || linux

package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vedaant-Rajoo/clai/internal/configroot"
	"github.com/zalando/go-keyring"
)

func useCredentialMigrationRoots(t *testing.T, kr *fakeKeyring) (preferred, legacy string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize credential migration root: %v", err)
	}
	preferred = filepath.Join(root, "preferred")
	legacy = filepath.Join(root, "legacy")
	for _, path := range []string{preferred, legacy} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create credential migration root %q: %v", path, err)
		}
	}

	oldKeyring := credentialKeyring
	oldRoots := resolveConfigRoots
	credentialKeyring = kr
	resolveConfigRoots = func() (configroot.Roots, error) {
		return configroot.Roots{Preferred: preferred, Legacy: legacy}, nil
	}
	t.Cleanup(func() {
		credentialKeyring = oldKeyring
		resolveConfigRoots = oldRoots
	})
	for _, env := range []string{"OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(env, "")
	}
	return preferred, legacy
}

func credentialFileAt(root string) string {
	return filepath.Join(root, "clai", fileName)
}

func writeCredentialMapFixture(t *testing.T, root string, credentials map[string]string) string {
	t.Helper()
	data, err := json.Marshal(credentials)
	if err != nil {
		t.Fatalf("encode credential fixture: %v", err)
	}
	return writeRawCredentialFixture(t, root, data)
}

func writeRawCredentialFixture(t *testing.T, root string, data []byte) string {
	t.Helper()
	path := credentialFileAt(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create credential fixture directory: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write credential fixture: %v", err)
	}
	return path
}

func readCredentialMapFixture(t *testing.T, root string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(credentialFileAt(root))
	if err != nil {
		t.Fatalf("read credential fixture: %v", err)
	}
	var credentials map[string]string
	if err := json.Unmarshal(data, &credentials); err != nil {
		t.Fatalf("decode credential fixture: %v", err)
	}
	return credentials
}

func assertLegacyCredentialRemoved(t *testing.T, legacy string) {
	t.Helper()
	if _, err := os.Lstat(credentialFileAt(legacy)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy credential remains after reconciliation: %v", err)
	}
}

func assertCredentialFileMode(t *testing.T, root string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(credentialFileAt(root))
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("credential file mode = %o, want %o", got, want)
	}
}

func assertSecretFree(t *testing.T, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("diagnostic disclosed planted credential sentinel")
		}
	}
}

func TestCredentialMigrationOldOnly(t *testing.T) {
	preferred, legacy := useCredentialMigrationRoots(t, &fakeKeyring{getErr: keyring.ErrNotFound})
	writeCredentialMapFixture(t, legacy, map[string]string{
		"openrouter": "legacy-openrouter-secret",
		"anthropic":  "legacy-anthropic-secret",
	})

	key, err := Resolve("openrouter", "")
	if err != nil {
		t.Fatalf("Resolve old-only credential: %v", err)
	}
	if key != "legacy-openrouter-secret" {
		t.Fatal("Resolve did not return the requested migrated provider")
	}
	source, err := SourceWithError("anthropic", "")
	if err != nil {
		t.Fatalf("SourceWithError migrated credential: %v", err)
	}
	if source != "config file" {
		t.Fatalf("SourceWithError = %q, want config file", source)
	}

	credentials := readCredentialMapFixture(t, preferred)
	if len(credentials) != 2 || credentials["openrouter"] != "legacy-openrouter-secret" || credentials["anthropic"] != "legacy-anthropic-secret" {
		t.Fatal("old-only migration did not preserve the complete provider map")
	}
	assertCredentialFileMode(t, preferred, 0o600)
	assertLegacyCredentialRemoved(t, legacy)
}

func TestCredentialMigrationPreferredWins(t *testing.T) {
	preferred, legacy := useCredentialMigrationRoots(t, &fakeKeyring{getErr: keyring.ErrNotFound})
	writeCredentialMapFixture(t, preferred, map[string]string{"openrouter": "preferred-secret"})
	writeRawCredentialFixture(t, legacy, []byte(`{"anthropic":"legacy-only-secret","broken"`))

	key, err := Resolve("openrouter", "")
	if err != nil {
		t.Fatalf("Resolve preferred credential: %v", err)
	}
	if key != "preferred-secret" {
		t.Fatal("Resolve did not use the preferred credential")
	}
	legacyOnly, err := Resolve("anthropic", "")
	if err != nil {
		t.Fatalf("Resolve provider present only in legacy: %v", err)
	}
	if legacyOnly != "" {
		t.Fatal("legacy-only provider outranked global preferred authority")
	}
	if got := readCredentialMapFixture(t, preferred); len(got) != 1 || got["openrouter"] != "preferred-secret" {
		t.Fatal("preferred credential map was merged with legacy state")
	}
	assertLegacyCredentialRemoved(t, legacy)
}

func TestCredentialMigrationPreservesProviders(t *testing.T) {
	preferred, legacy := useCredentialMigrationRoots(t, &fakeKeyring{setErr: errors.New("keyring unavailable")})
	writeCredentialMapFixture(t, legacy, map[string]string{
		"openrouter": "old-openrouter-secret",
		"anthropic":  "keep-anthropic-secret",
		"openai":     "keep-openai-secret",
	})

	if err := Store("openrouter", "updated-openrouter-secret"); err != nil {
		t.Fatalf("Store after old-only migration: %v", err)
	}
	credentials := readCredentialMapFixture(t, preferred)
	if len(credentials) != 3 ||
		credentials["openrouter"] != "updated-openrouter-secret" ||
		credentials["anthropic"] != "keep-anthropic-secret" ||
		credentials["openai"] != "keep-openai-secret" {
		t.Fatal("Store did not preserve unrelated migrated providers")
	}
	assertLegacyCredentialRemoved(t, legacy)
}

func TestCredentialPreferredCorruptNeverReadsLegacy(t *testing.T) {
	preferred, legacy := useCredentialMigrationRoots(t, &fakeKeyring{getErr: keyring.ErrNotFound})
	corrupt := []byte(`{"openrouter":"preferred-secret-sentinel"`)
	writeRawCredentialFixture(t, preferred, corrupt)
	writeCredentialMapFixture(t, legacy, map[string]string{"openrouter": "legacy-secret-sentinel"})

	_, err := Resolve("openrouter", "")
	if err == nil {
		t.Fatal("Resolve unexpectedly accepted corrupt preferred credentials")
	}
	assertSecretFree(t, err.Error(), "preferred-secret-sentinel", "legacy-secret-sentinel")
	assertLegacyCredentialRemoved(t, legacy)
	data, readErr := os.ReadFile(credentialFileAt(preferred))
	if readErr != nil {
		t.Fatalf("read corrupt preferred credential: %v", readErr)
	}
	if string(data) != string(corrupt) {
		t.Fatal("corrupt preferred credential bytes changed during reconciliation")
	}
}

func TestCredentialLifecycleCleansBothRoots(t *testing.T) {
	preferred, legacy := useCredentialMigrationRoots(t, &fakeKeyring{
		setErr:    errors.New("keyring unavailable"),
		deleteErr: keyring.ErrNotFound,
	})
	writeCredentialMapFixture(t, legacy, map[string]string{
		"openrouter": "legacy-target-secret",
		"anthropic":  "preserve-secret",
	})

	if err := Store("openrouter", "preferred-target-secret"); err != nil {
		t.Fatalf("Store fallback credential: %v", err)
	}
	assertLegacyCredentialRemoved(t, legacy)

	// Simulate stale state recreated by a pre-migration process. Preferred remains
	// globally authoritative, and logout must retire this file without merging it.
	writeCredentialMapFixture(t, legacy, map[string]string{
		"openrouter": "resurrected-target-secret",
		"openai":     "legacy-only-secret",
	})
	if err := Delete("openrouter"); err != nil {
		t.Fatalf("Delete reconciled credential: %v", err)
	}
	credentials := readCredentialMapFixture(t, preferred)
	if _, exists := credentials["openrouter"]; exists {
		t.Fatal("Delete left the target provider in preferred credentials")
	}
	if credentials["anthropic"] != "preserve-secret" {
		t.Fatal("Delete did not preserve an unrelated preferred provider")
	}
	if _, exists := credentials["openai"]; exists {
		t.Fatal("Delete merged a legacy-only provider into preferred credentials")
	}
	assertLegacyCredentialRemoved(t, legacy)
}

func TestCredentialKeyringCleanupCoversBothRoots(t *testing.T) {
	kr := &fakeKeyring{}
	preferred, legacy := useCredentialMigrationRoots(t, kr)
	writeCredentialMapFixture(t, legacy, map[string]string{
		"openrouter": "stale-file-secret",
		"anthropic":  "preserve-file-secret",
	})

	if err := Store("openrouter", "current-keyring-secret"); err != nil {
		t.Fatalf("Store keyring credential: %v", err)
	}
	if kr.values["openrouter"] != "current-keyring-secret" {
		t.Fatal("Store did not retain the credential in the keyring")
	}
	credentials := readCredentialMapFixture(t, preferred)
	if _, exists := credentials["openrouter"]; exists {
		t.Fatal("successful keyring Store left the target provider in preferred credentials")
	}
	if credentials["anthropic"] != "preserve-file-secret" {
		t.Fatal("successful keyring cleanup did not preserve unrelated providers")
	}
	assertLegacyCredentialRemoved(t, legacy)

	source, err := SourceWithError("openrouter", "")
	if err != nil || source != "keyring" {
		t.Fatalf("SourceWithError = %q, %v; want keyring", source, err)
	}
}

func TestCredentialErrorsContainNoSecrets(t *testing.T) {
	const (
		preferredSecret = "preferred-secret-sentinel"
		legacySecret    = "legacy-secret-sentinel"
		argumentSecret  = "argument-secret-sentinel"
		keyringSecret   = "keyring-error-secret-sentinel"
	)
	secrets := []string{preferredSecret, legacySecret, argumentSecret, keyringSecret}

	t.Run("resolve and source", func(t *testing.T) {
		preferred, legacy := useCredentialMigrationRoots(t, &fakeKeyring{getErr: errors.New(keyringSecret)})
		writeRawCredentialFixture(t, preferred, []byte(`{"openrouter":"`+preferredSecret+`"`))
		writeCredentialMapFixture(t, legacy, map[string]string{"openrouter": legacySecret})

		_, resolveErr := Resolve("openrouter", "")
		if resolveErr == nil {
			t.Fatal("Resolve unexpectedly succeeded")
		}
		assertSecretFree(t, resolveErr.Error(), secrets...)
		_, sourceErr := SourceWithError("openrouter", "")
		if sourceErr == nil {
			t.Fatal("SourceWithError unexpectedly succeeded")
		}
		assertSecretFree(t, sourceErr.Error(), secrets...)
		if source := Source("openrouter", ""); source != "none" {
			t.Fatalf("Source = %q, want none", source)
		}
		assertLegacyCredentialRemoved(t, legacy)
	})

	t.Run("store", func(t *testing.T) {
		preferred, legacy := useCredentialMigrationRoots(t, &fakeKeyring{setErr: errors.New(keyringSecret)})
		writeRawCredentialFixture(t, preferred, []byte(`{"openrouter":"`+preferredSecret+`"`))
		writeCredentialMapFixture(t, legacy, map[string]string{"openrouter": legacySecret})

		err := Store("openrouter", argumentSecret)
		if err == nil {
			t.Fatal("Store unexpectedly succeeded")
		}
		assertSecretFree(t, err.Error(), secrets...)
		assertLegacyCredentialRemoved(t, legacy)
	})

	t.Run("delete", func(t *testing.T) {
		preferred, legacy := useCredentialMigrationRoots(t, &fakeKeyring{deleteErr: errors.New(keyringSecret)})
		writeCredentialMapFixture(t, preferred, map[string]string{
			"openrouter": preferredSecret,
			"anthropic":  "preserved-provider",
		})
		writeCredentialMapFixture(t, legacy, map[string]string{"openrouter": legacySecret})

		err := Delete("openrouter")
		if err == nil {
			t.Fatal("Delete unexpectedly hid the keyring failure")
		}
		assertSecretFree(t, err.Error(), secrets...)
		credentials := readCredentialMapFixture(t, preferred)
		if _, exists := credentials["openrouter"]; exists {
			t.Fatal("Delete failure left the target provider in preferred credentials")
		}
		assertLegacyCredentialRemoved(t, legacy)
	})
}
