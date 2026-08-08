//go:build darwin || linux

package auth

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vedaant-Rajoo/clai/internal/configroot"
	"github.com/zalando/go-keyring"
	"golang.org/x/sys/unix"
)

func useFilesystemHooks(t *testing.T, hooks filesystemHooks) {
	t.Helper()
	old := credentialFilesystemHooks
	credentialFilesystemHooks = hooks
	t.Cleanup(func() { credentialFilesystemHooks = old })
}

func assertNoCredentialTemps(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".credentials-") && strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("secret-bearing temporary file remains: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk temporary files: %v", err)
	}
}

func TestFileSubstitutionBetweenValidationAndReadFailsClosed(t *testing.T) {
	kr := &fakeKeyring{getErr: keyring.ErrNotFound}
	base := useTestBackends(t, kr)
	writeCredentialFixture(t, base, `{"openrouter":"original-secret"}`)
	useFilesystemHooks(t, filesystemHooks{
		afterFileValidationBeforeRead: func(_ string, path string) error {
			if err := os.Rename(path, path+".moved"); err != nil {
				return err
			}
			return os.WriteFile(path, []byte(`{"openrouter":"substitute-secret"}`), 0o600)
		},
	})

	if _, err := Resolve("openrouter", ""); err == nil || !strings.Contains(err.Error(), "changed before read") {
		t.Fatalf("Resolve error = %v, want substitution rejection", err)
	}
}

func TestDirectorySubstitutionBeforeTempCreationFailsClosed(t *testing.T) {
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	base := useTestBackends(t, kr)
	useFilesystemHooks(t, filesystemHooks{
		beforeTempCreation: func(dir, _ string) error {
			if err := os.Rename(dir, dir+".moved"); err != nil {
				return err
			}
			return os.Mkdir(dir, 0o700)
		},
	})

	if err := Store("openrouter", "secret-value"); err == nil || !strings.Contains(err.Error(), "directory path changed") {
		t.Fatalf("Store error = %v, want directory substitution rejection", err)
	}
	assertNoCredentialTemps(t, base)
}

func TestDirectorySubstitutionAfterTempCreationCleansTemp(t *testing.T) {
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	base := useTestBackends(t, kr)
	useFilesystemHooks(t, filesystemHooks{
		afterTempCreation: func(dir, _ string, _ string) error {
			if err := os.Rename(dir, dir+".moved"); err != nil {
				return err
			}
			return os.Mkdir(dir, 0o700)
		},
	})

	if err := Store("openrouter", "secret-value"); err == nil || !strings.Contains(err.Error(), "directory path changed") {
		t.Fatalf("Store error = %v, want directory substitution rejection", err)
	}
	assertNoCredentialTemps(t, base)
}

func TestForeignCredentialTransientIsErasedAndRemovedWithoutBlockingRead(t *testing.T) {
	kr := &fakeKeyring{getErr: keyring.ErrNotFound}
	base := useTestBackends(t, kr)
	dir, _ := credentialPaths(base)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".credentials-foreign.tmp")
	if err := os.WriteFile(path, []byte("stale-secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	if key, err := Resolve("openrouter", ""); err != nil || key != "" {
		t.Fatalf("Resolve with foreign transient = %q, %v", key, err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreign credential transient remains: %v", err)
	}
	if _, err := held.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(held)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("held stale transient still contains %d bytes", len(data))
	}
}

func TestReadOnlyCredentialDirectoryFallsBackToLocklessPreferredRead(t *testing.T) {
	kr := &fakeKeyring{getErr: keyring.ErrNotFound}
	base := useTestBackends(t, kr)
	path := writeCredentialFixture(t, base, `{"openrouter":"stored-secret"}`)
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	key, err := Resolve("openrouter", "")
	if err != nil {
		t.Fatalf("Resolve from read-only credential directory: %v", err)
	}
	if key != "stored-secret" {
		t.Fatalf("Resolve = %q, want stored-secret", key)
	}
	if _, err := os.Lstat(filepath.Join(dir, lockName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock was created in read-only directory: %v", err)
	}
}

func TestDestinationSubstitutionAfterRenameFailsVerification(t *testing.T) {
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	base := useTestBackends(t, kr)
	useFilesystemHooks(t, filesystemHooks{
		afterRenameBeforeVerification: func(_ string, path string) error {
			if err := os.Rename(path, path+".moved"); err != nil {
				return err
			}
			return os.WriteFile(path, []byte(`{"openrouter":"substitute"}`), 0o600)
		},
	})

	if err := Store("openrouter", "secret-value"); err == nil || !strings.Contains(err.Error(), "verify replaced credential file") {
		t.Fatalf("Store error = %v, want destination substitution rejection", err)
	}
	_, path := credentialPaths(base)
	moved, err := os.ReadFile(path + ".moved")
	if err != nil {
		t.Fatalf("read moved credential inode: %v", err)
	}
	if len(moved) != 0 {
		t.Fatalf("moved credential inode still contains secret bytes: %q", moved)
	}
	assertNoCredentialTemps(t, base)
}

func TestSecretTempCleanupFailureIsSurfacedAfterRemoval(t *testing.T) {
	triggerErr := errors.New("injected operation failure")
	cleanupErr := errors.New("injected cleanup failure")
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	base := useTestBackends(t, kr)
	useFilesystemHooks(t, filesystemHooks{
		afterTempCreation: func(string, string, string) error { return triggerErr },
		unlinkTemp: func(dirfd int, name string) error {
			if err := unix.Unlinkat(dirfd, name, 0); err != nil {
				return err
			}
			return cleanupErr
		},
	})

	err := Store("openrouter", "secret-value")
	if !errors.Is(err, triggerErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Store error = %v, want operation and cleanup failures", err)
	}
	assertNoCredentialTemps(t, base)
}

func TestConcurrentFallbackStoresPreserveBothEntries(t *testing.T) {
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	useTestBackends(t, kr)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for provider, secret := range map[string]string{"openrouter": "one", "anthropic": "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- Store(provider, secret)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Store: %v", err)
		}
	}
	for provider, want := range map[string]string{"openrouter": "one", "anthropic": "two"} {
		got, err := readFile(provider)
		if err != nil || got != want {
			t.Fatalf("readFile(%s) = %q, %v; want %q", provider, got, err, want)
		}
	}
}

func TestConcurrentStoreDeleteHasSerializedOutcome(t *testing.T) {
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	base := useTestBackends(t, kr)
	writeCredentialFixture(t, base, `{"openrouter":"remove"}`)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; errs <- Store("anthropic", "keep") }()
	go func() { defer wg.Done(); <-start; errs <- Delete("openrouter") }()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent mutation: %v", err)
		}
	}
	if got, err := readFile("openrouter"); err != nil || got != "" {
		t.Fatalf("deleted credential = %q, %v", got, err)
	}
	if got, err := readFile("anthropic"); err != nil || got != "keep" {
		t.Fatalf("stored credential = %q, %v", got, err)
	}
}

func TestCrossProcessFallbackStoresPreserveBothEntries(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve test config directory: %v", err)
	}
	commands := []*exec.Cmd{
		exec.Command(os.Args[0], "-test.run=^TestCredentialProcessHelper$"),
		exec.Command(os.Args[0], "-test.run=^TestCredentialProcessHelper$"),
	}
	commands[0].Env = append(os.Environ(), "CLAI_AUTH_HELPER_BASE="+base, "CLAI_AUTH_HELPER_PROVIDER=openrouter", "CLAI_AUTH_HELPER_SECRET=one")
	commands[1].Env = append(os.Environ(), "CLAI_AUTH_HELPER_BASE="+base, "CLAI_AUTH_HELPER_PROVIDER=anthropic", "CLAI_AUTH_HELPER_SECRET=two")
	start := make(chan struct{})
	errs := make(chan error, len(commands))
	var wg sync.WaitGroup
	for _, command := range commands {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- command.Run()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("credential helper: %v", err)
		}
	}

	kr := &fakeKeyring{}
	oldKeyring, oldRoots := credentialKeyring, resolveConfigRoots
	credentialKeyring = kr
	resolveConfigRoots = func() (configroot.Roots, error) {
		return configroot.Roots{Preferred: base}, nil
	}
	t.Cleanup(func() { credentialKeyring, resolveConfigRoots = oldKeyring, oldRoots })
	for provider, want := range map[string]string{"openrouter": "one", "anthropic": "two"} {
		got, err := readFile(provider)
		if err != nil || got != want {
			t.Fatalf("cross-process readFile(%s) = %q, %v; want %q", provider, got, err, want)
		}
	}
}

func TestCredentialProcessHelper(t *testing.T) {
	base := os.Getenv("CLAI_AUTH_HELPER_BASE")
	if base == "" {
		return
	}
	credentialKeyring = &fakeKeyring{setErr: errors.New("unavailable")}
	resolveConfigRoots = func() (configroot.Roots, error) {
		return configroot.Roots{Preferred: base}, nil
	}
	if preLock := os.Getenv("CLAI_AUTH_HELPER_PRELOCK_SIGNAL"); preLock != "" {
		credentialFilesystemHooks.beforeLockAcquire = func(string, string) error {
			return os.WriteFile(preLock, nil, 0o600)
		}
	}
	if signal := os.Getenv("CLAI_AUTH_HELPER_SIGNAL"); signal != "" {
		release := os.Getenv("CLAI_AUTH_HELPER_RELEASE")
		credentialFilesystemHooks.afterCredentialReadWhileLocked = func(string, string) error {
			if err := os.WriteFile(signal, nil, 0o600); err != nil {
				return err
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(release); err == nil {
					return nil
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if time.Now().After(deadline) {
					return errors.New("timed out waiting for helper release")
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
	var err error
	if os.Getenv("CLAI_AUTH_HELPER_ACTION") == "delete" {
		err = Delete(os.Getenv("CLAI_AUTH_HELPER_PROVIDER"))
	} else {
		err = Store(os.Getenv("CLAI_AUTH_HELPER_PROVIDER"), os.Getenv("CLAI_AUTH_HELPER_SECRET"))
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestSpecialPathsRejectedAcrossPublicOperations(t *testing.T) {
	operations := []struct {
		name string
		run  func() error
	}{
		{name: "resolve", run: func() error { _, err := Resolve("openrouter", ""); return err }},
		{name: "store", run: func() error { return Store("openrouter", "new-secret") }},
		{name: "source", run: func() error { _, err := SourceWithError("openrouter", ""); return err }},
		{name: "delete", run: func() error { return Delete("openrouter") }},
	}
	fixtures := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "directory symlink", setup: func(t *testing.T, base string) {
			dir, _ := credentialPaths(base)
			if err := os.Symlink(t.TempDir(), dir); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "file symlink", setup: func(t *testing.T, base string) {
			dir, path := credentialPaths(base)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "file directory", setup: func(t *testing.T, base string) {
			dir, path := credentialPaths(base)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
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
