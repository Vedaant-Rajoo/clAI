//go:build darwin || linux

package auth

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestCredentialMigrationIdempotent(t *testing.T) {
	preferred, legacy := useCredentialMigrationRoots(t, &fakeKeyring{
		getErr:    keyring.ErrNotFound,
		setErr:    errors.New("keyring unavailable"),
		deleteErr: keyring.ErrNotFound,
	})
	writeCredentialMapFixture(t, legacy, map[string]string{
		"openrouter": "legacy-target-secret",
		"anthropic":  "preserved-secret",
	})
	staleTemp := filepath.Join(legacy, "clai", ".credentials-00112233445566778899aabb.tmp")
	if err := os.WriteFile(staleTemp, []byte("stale-secret-temp"), 0o600); err != nil {
		t.Fatalf("write stale migration temp: %v", err)
	}

	if key, err := Resolve("openrouter", ""); err != nil || key != "legacy-target-secret" {
		t.Fatalf("Resolve initial migration = %q, %v", key, err)
	}
	for range 3 {
		if err := Store("openrouter", "updated-target-secret"); err != nil {
			t.Fatalf("Store repeated migration: %v", err)
		}
		if key, err := Resolve("openrouter", ""); err != nil || key != "updated-target-secret" {
			t.Fatalf("Resolve repeated Store = %q, %v", key, err)
		}
		if err := Delete("openrouter"); err != nil {
			t.Fatalf("Delete repeated migration: %v", err)
		}
		if key, err := Resolve("openrouter", ""); err != nil || key != "" {
			t.Fatalf("Resolve repeated Delete = %q, %v", key, err)
		}
	}

	credentials := readCredentialMapFixture(t, preferred)
	if len(credentials) != 1 || credentials["anthropic"] != "preserved-secret" {
		t.Fatalf("repeated lifecycle left unexpected preferred map: %#v", credentials)
	}
	assertLegacyCredentialRemoved(t, legacy)
	assertNoCredentialTemps(t, preferred)
	assertNoCredentialTemps(t, legacy)
	for _, root := range []string{preferred, legacy} {
		lock := filepath.Join(root, "clai", lockName)
		info, err := os.Lstat(lock)
		if err != nil {
			t.Fatalf("inspect persistent lock %q: %v", lock, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("persistent lock %q mode = %v, want private regular file", lock, info.Mode())
		}
		data, err := os.ReadFile(lock)
		if err != nil {
			t.Fatalf("read persistent lock %q: %v", lock, err)
		}
		if len(data) != 0 {
			t.Fatalf("persistent lock %q contains data", lock)
		}
	}
}

func TestCredentialMigrationInterruptedRecovery(t *testing.T) {
	preferred, legacy := useCredentialMigrationRoots(t, &fakeKeyring{getErr: keyring.ErrNotFound})
	original := map[string]string{
		"openrouter": "original-openrouter-secret",
		"anthropic":  "original-anthropic-secret",
	}
	writeCredentialMapFixture(t, legacy, original)
	interrupted := errors.New("injected post-install interruption")
	var calls atomic.Int32
	useFilesystemHooks(t, filesystemHooks{
		afterPreferredInstallBeforeLegacyUnlink: func(credentialFileLocations) error {
			if calls.Add(1) == 1 {
				return interrupted
			}
			return nil
		},
	})

	if _, err := Resolve("openrouter", ""); !errors.Is(err, interrupted) {
		t.Fatalf("Resolve interrupted migration error = %v, want injected interruption", err)
	}
	if got := readCredentialMapFixture(t, preferred); len(got) != 2 || got["openrouter"] != original["openrouter"] || got["anthropic"] != original["anthropic"] {
		t.Fatalf("interrupted migration installed partial preferred map: %#v", got)
	}
	if _, err := os.Stat(credentialFileAt(legacy)); err != nil {
		t.Fatalf("interrupted migration did not retain recoverable legacy source: %v", err)
	}
	assertNoCredentialTemps(t, preferred)
	assertNoCredentialTemps(t, legacy)

	writeCredentialMapFixture(t, legacy, map[string]string{
		"openrouter": "stale-openrouter-secret",
		"openai":     "stale-only-secret",
	})
	key, err := Resolve("openrouter", "")
	if err != nil {
		t.Fatalf("Resolve interrupted migration retry: %v", err)
	}
	if key != original["openrouter"] {
		t.Fatalf("retry returned %q, want installed preferred authority", key)
	}
	if got := readCredentialMapFixture(t, preferred); len(got) != 2 || got["openrouter"] != original["openrouter"] || got["anthropic"] != original["anthropic"] {
		t.Fatalf("retry merged stale legacy values: %#v", got)
	}
	assertLegacyCredentialRemoved(t, legacy)
	assertNoCredentialTemps(t, preferred)
	assertNoCredentialTemps(t, legacy)
}

func TestCredentialMigrationConcurrent(t *testing.T) {
	t.Run("goroutines acquire canonical order", func(t *testing.T) {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		firstRoot := filepath.Join(root, "z-preferred")
		secondRoot := filepath.Join(root, "a-legacy")
		for _, path := range []string{firstRoot, secondRoot} {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		writeCredentialMapFixture(t, secondRoot, map[string]string{"openrouter": "seed-secret"})
		firstLocations := credentialFileLocations{preferred: credentialFileAt(firstRoot), legacy: credentialFileAt(secondRoot)}
		secondLocations := credentialFileLocations{preferred: credentialFileAt(secondRoot), legacy: credentialFileAt(firstRoot)}
		wantAcquire := []string{filepath.Join(secondRoot, "clai"), filepath.Join(firstRoot, "clai")}
		wantRelease := []string{filepath.Join(firstRoot, "clai"), filepath.Join(secondRoot, "clai")}

		firstEntered := make(chan struct{})
		releaseFirst := make(chan struct{})
		var checkpoints atomic.Int32
		var ordersMu sync.Mutex
		var acquireOrders, releaseOrders [][]string
		useFilesystemHooks(t, filesystemHooks{
			afterOrderedLocksAcquired: func(checkpoint credentialFilesystemCheckpoint) error {
				if checkpoint.TempName != "" {
					return nil
				}
				ordersMu.Lock()
				acquireOrders = append(acquireOrders, append([]string(nil), checkpoint.AppPaths...))
				releaseOrders = append(releaseOrders, append([]string(nil), checkpoint.ReleasePaths...))
				ordersMu.Unlock()
				if checkpoints.Add(1) == 1 {
					close(firstEntered)
					<-releaseFirst
				}
				return nil
			},
		})

		firstDone := make(chan error, 1)
		go func() {
			firstDone <- secureMutateCredentialFiles(firstLocations, true, func(credentials map[string]string) bool {
				credentials["anthropic"] = "first-secret"
				return true
			})
		}()
		select {
		case <-firstEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for first ordered-lock checkpoint")
		}
		secondDone := make(chan error, 1)
		go func() {
			secondDone <- secureMutateCredentialFiles(secondLocations, true, func(credentials map[string]string) bool {
				credentials["openai"] = "second-secret"
				return true
			})
		}()
		select {
		case err := <-secondDone:
			t.Fatalf("opposite-order mutation passed held ordered locks: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		close(releaseFirst)
		for name, done := range map[string]<-chan error{"first": firstDone, "second": secondDone} {
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("%s mutation: %v", name, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out waiting for %s mutation", name)
			}
		}
		ordersMu.Lock()
		defer ordersMu.Unlock()
		if len(acquireOrders) != 2 || len(releaseOrders) != 2 {
			t.Fatalf("ordered-lock checkpoints = %d/%d, want 2/2", len(acquireOrders), len(releaseOrders))
		}
		for i := range acquireOrders {
			if !slices.Equal(acquireOrders[i], wantAcquire) || !slices.Equal(releaseOrders[i], wantRelease) {
				t.Fatalf("checkpoint %d order = acquire %#v release %#v, want %#v/%#v", i, acquireOrders[i], releaseOrders[i], wantAcquire, wantRelease)
			}
		}
		credentials := readCredentialMapFixture(t, secondRoot)
		if len(credentials) != 3 || credentials["openrouter"] != "seed-secret" || credentials["anthropic"] != "first-secret" || credentials["openai"] != "second-secret" {
			t.Fatalf("concurrent migration lost serialized updates: %#v", credentials)
		}
		assertLegacyCredentialRemoved(t, firstRoot)
		assertNoCredentialTemps(t, firstRoot)
		assertNoCredentialTemps(t, secondRoot)
	})

	t.Run("child processes acquire canonical order", func(t *testing.T) {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		firstRoot := filepath.Join(root, "z-preferred")
		secondRoot := filepath.Join(root, "a-legacy")
		for _, path := range []string{firstRoot, secondRoot} {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		writeCredentialMapFixture(t, secondRoot, map[string]string{"openrouter": "seed-secret"})
		firstSignal := filepath.Join(root, "first-locked")
		secondSignal := filepath.Join(root, "second-locked")
		release := filepath.Join(root, "release-first")

		first := credentialMigrationHelperCommand(firstRoot, secondRoot, "anthropic", "first-secret", firstSignal, release)
		if err := first.Start(); err != nil {
			t.Fatal(err)
		}
		firstDone := make(chan error, 1)
		go func() { firstDone <- first.Wait() }()
		waitForTestPath(t, firstSignal, 5*time.Second)

		second := credentialMigrationHelperCommand(secondRoot, firstRoot, "openai", "second-secret", secondSignal, "")
		if err := second.Start(); err != nil {
			t.Fatal(err)
		}
		secondDone := make(chan error, 1)
		go func() { secondDone <- second.Wait() }()
		select {
		case err := <-secondDone:
			t.Fatalf("opposite-order child passed held ordered locks: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		if _, err := os.Stat(secondSignal); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("second child reached ordered-lock checkpoint early: %v", err)
		}
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		for name, done := range map[string]<-chan error{"first": firstDone, "second": secondDone} {
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("%s migration helper: %v", name, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out waiting for %s migration helper", name)
			}
		}
		waitForTestPath(t, secondSignal, time.Second)
		credentials := readCredentialMapFixture(t, secondRoot)
		if len(credentials) != 3 || credentials["openrouter"] != "seed-secret" || credentials["anthropic"] != "first-secret" || credentials["openai"] != "second-secret" {
			t.Fatalf("cross-process migration lost serialized updates: %#v", credentials)
		}
		assertLegacyCredentialRemoved(t, firstRoot)
		assertNoCredentialTemps(t, firstRoot)
		assertNoCredentialTemps(t, secondRoot)
	})
}

func credentialMigrationHelperCommand(preferred, legacy, provider, secret, signal, release string) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestCredentialMigrationProcessHelper$")
	command.Env = append(os.Environ(),
		"CLAI_AUTH_MIGRATION_HELPER_PREFERRED="+preferred,
		"CLAI_AUTH_MIGRATION_HELPER_LEGACY="+legacy,
		"CLAI_AUTH_MIGRATION_HELPER_PROVIDER="+provider,
		"CLAI_AUTH_MIGRATION_HELPER_SECRET="+secret,
		"CLAI_AUTH_MIGRATION_HELPER_SIGNAL="+signal,
		"CLAI_AUTH_MIGRATION_HELPER_RELEASE="+release,
	)
	return command
}

func TestCredentialMigrationProcessHelper(t *testing.T) {
	preferred := os.Getenv("CLAI_AUTH_MIGRATION_HELPER_PREFERRED")
	if preferred == "" {
		return
	}
	legacy := os.Getenv("CLAI_AUTH_MIGRATION_HELPER_LEGACY")
	credentialKeyring = &fakeKeyring{setErr: errors.New("keyring unavailable")}
	resolveConfigRoots = func() (configroot.Roots, error) {
		return configroot.Roots{Preferred: preferred, Legacy: legacy}, nil
	}
	signal := os.Getenv("CLAI_AUTH_MIGRATION_HELPER_SIGNAL")
	release := os.Getenv("CLAI_AUTH_MIGRATION_HELPER_RELEASE")
	credentialFilesystemHooks.afterOrderedLocksAcquired = func(checkpoint credentialFilesystemCheckpoint) error {
		if checkpoint.TempName != "" {
			return nil
		}
		if !slices.IsSorted(checkpoint.AppPaths) {
			return errors.New("credential locks were not acquired in canonical order")
		}
		wantRelease := append([]string(nil), checkpoint.AppPaths...)
		slices.Reverse(wantRelease)
		if !slices.Equal(checkpoint.ReleasePaths, wantRelease) {
			return errors.New("credential locks were not released in reverse order")
		}
		if err := os.WriteFile(signal, nil, 0o600); err != nil {
			return err
		}
		if release == "" {
			return nil
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(release); err == nil {
				return nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if time.Now().After(deadline) {
				return errors.New("timed out waiting for migration helper release")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := Store(os.Getenv("CLAI_AUTH_MIGRATION_HELPER_PROVIDER"), os.Getenv("CLAI_AUTH_MIGRATION_HELPER_SECRET")); err != nil {
		t.Fatal(err)
	}
}
