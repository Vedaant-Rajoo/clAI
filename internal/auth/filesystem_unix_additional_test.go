//go:build darwin || linux

package auth

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/sys/unix"
)

func TestIntermediateConfigComponentSymlinkCanonicalized(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realBase := filepath.Join(root, "real", "config")
	if err := os.MkdirAll(realBase, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(filepath.Join(root, "real"), link); err != nil {
		t.Fatal(err)
	}
	useTestBackendsAt(t, &fakeKeyring{setErr: errors.New("unavailable")}, filepath.Join(link, "config"))

	if err := Store("openrouter", "secret-value"); err != nil {
		t.Fatalf("Store through intermediate config symlink: %v", err)
	}
	if got := readCredentialMapFixture(t, realBase)["openrouter"]; got != "secret-value" {
		t.Fatalf("stored credential = %q, want secret-value", got)
	}
}

func TestIntermediateConfigComponentSubstitutionFailsClosed(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "parent")
	base := filepath.Join(parent, "config")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	useTestBackendsAt(t, &fakeKeyring{setErr: errors.New("unavailable")}, base)
	useFilesystemHooks(t, filesystemHooks{
		afterBaseValidationBeforeAppOpen: func(_, _ string) error {
			if err := os.Rename(parent, parent+".moved"); err != nil {
				return err
			}
			return os.MkdirAll(base, 0o700)
		},
	})

	if err := Store("openrouter", "secret-value"); err == nil || !strings.Contains(err.Error(), "user config directory path changed") {
		t.Fatalf("Store error = %v, want intermediate substitution rejection", err)
	}
	assertNoCredentialTemps(t, root)
}

func TestPostRenameDirectorySyncFailurePreservesCredential(t *testing.T) {
	syncErr := errors.New("injected directory sync failure")
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	base := useTestBackends(t, kr)
	useFilesystemHooks(t, filesystemHooks{
		syncDirectory: func(int) error { return syncErr },
	})

	err := Store("openrouter", "secret-value")
	if !errors.Is(err, syncErr) {
		t.Fatalf("Store error = %v, want sync failure", err)
	}
	if got := readCredentialMapFixture(t, base)["openrouter"]; got != "secret-value" {
		t.Fatalf("credential after fsync failure = %q, want installed value", got)
	}
}

func TestCleanupErrorAfterInstallPreservesCredential(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*filesystemHooks, error)
	}{
		{name: "credential close", apply: func(hooks *filesystemHooks, injected error) {
			hooks.closeCredential = func(file *os.File) error { return errors.Join(file.Close(), injected) }
		}},
		{name: "unlock", apply: func(hooks *filesystemHooks, injected error) {
			hooks.unlockLock = func(fd int) error { return errors.Join(unix.Flock(fd, unix.LOCK_UN), injected) }
		}},
		{name: "lock close", apply: func(hooks *filesystemHooks, injected error) {
			hooks.closeLock = func(file *os.File) error { return errors.Join(file.Close(), injected) }
		}},
		{name: "app directory close", apply: func(hooks *filesystemHooks, injected error) {
			hooks.closeAppDirectory = func(file *os.File) error { return errors.Join(file.Close(), injected) }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			injected := errors.New("injected " + test.name + " failure")
			kr := &fakeKeyring{setErr: errors.New("unavailable")}
			base := useTestBackends(t, kr)
			writeCredentialFixture(t, base, `{"openrouter":"old-value"}`)
			hooks := filesystemHooks{}
			test.apply(&hooks, injected)
			useFilesystemHooks(t, hooks)

			err := Store("openrouter", "secret-value")
			if !errors.Is(err, injected) {
				t.Fatalf("Store error = %v, want injected finalization failure", err)
			}
			if got := readCredentialMapFixture(t, base)["openrouter"]; got != "secret-value" {
				t.Fatalf("credential after cleanup failure = %q, want installed value", got)
			}
		})
	}
}

func TestIntegrityFailureErasesAndUnlinksCredential(t *testing.T) {
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	base := useTestBackends(t, kr)
	var retainedPath string
	useFilesystemHooks(t, filesystemHooks{
		afterRenameBeforeVerification: func(_ string, path string) error {
			retainedPath = path + ".retained"
			return os.Link(path, retainedPath)
		},
	})

	err := Store("openrouter", "secret-value")
	if err == nil || !strings.Contains(err.Error(), "link count") {
		t.Fatalf("Store error = %v, want installed link-count rejection", err)
	}
	_, path := credentialPaths(base)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installed credential remains after integrity failure: %v", err)
	}
	content, err := os.ReadFile(retainedPath)
	if err != nil {
		t.Fatalf("read retained credential inode: %v", err)
	}
	if len(content) != 0 {
		t.Fatalf("retained credential inode contains secret bytes: %q", content)
	}
}

func TestResolveAfterIntegrityEraseReportsNotConfigured(t *testing.T) {
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	base := useTestBackends(t, kr)
	useFilesystemHooks(t, filesystemHooks{
		afterRenameBeforeVerification: func(_ string, path string) error {
			return os.Link(path, path+".retained")
		},
	})

	if err := Store("openrouter", "secret-value"); err == nil {
		t.Fatal("Store unexpectedly accepted installed link-count change")
	}
	_, path := credentialPaths(base)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installed credential remains after integrity failure: %v", err)
	}
	key, err := Resolve("openrouter", "")
	if err != nil {
		t.Fatalf("Resolve after integrity erase: %v", err)
	}
	if key != "" {
		t.Fatalf("Resolve after integrity erase = %q, want not configured", key)
	}
}

func TestLockReleaseFailuresAreReturned(t *testing.T) {
	unlockErr := errors.New("injected unlock failure")
	closeErr := errors.New("injected lock close failure")
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	useTestBackends(t, kr)
	useFilesystemHooks(t, filesystemHooks{
		unlockLock: func(fd int) error {
			actual := unix.Flock(fd, unix.LOCK_UN)
			return errors.Join(actual, unlockErr)
		},
		closeLock: func(file *os.File) error {
			actual := file.Close()
			return errors.Join(actual, closeErr)
		},
	})

	err := Store("openrouter", "secret-value")
	if !errors.Is(err, unlockErr) || !errors.Is(err, closeErr) {
		t.Fatalf("Store error = %v, want unlock and close failures", err)
	}
}

func TestReadCredentialCloseFailureIsReturnedWithoutSecret(t *testing.T) {
	closeErr := errors.New("injected credential close failure")
	secret := "secret-must-not-leak"
	kr := &fakeKeyring{getErr: keyring.ErrNotFound}
	base := useTestBackends(t, kr)
	writeCredentialFixture(t, base, `{"openrouter":"`+secret+`"}`)
	useFilesystemHooks(t, filesystemHooks{
		closeCredential: func(file *os.File) error {
			actual := file.Close()
			return errors.Join(actual, closeErr)
		},
	})

	_, err := Resolve("openrouter", "")
	if !errors.Is(err, closeErr) {
		t.Fatalf("Resolve error = %v, want close failure", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("close error disclosed credential: %v", err)
	}
}

func TestLockFixturesRejected(t *testing.T) {
	fixtures := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "symlink", setup: func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "lock")
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, lockName)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "insecure mode", setup: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, lockName), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			kr := &fakeKeyring{setErr: errors.New("unavailable")}
			base := useTestBackends(t, kr)
			dir, _ := credentialPaths(base)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			fixture.setup(t, dir)
			if err := Store("openrouter", "secret-value"); err == nil {
				t.Fatal("Store unexpectedly accepted unsafe lock")
			}
		})
	}
}

func TestLockIdentitySubstitutionRejected(t *testing.T) {
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	base := useTestBackends(t, kr)
	useFilesystemHooks(t, filesystemHooks{
		afterLockAcquired: func(dir, _ string) error {
			path := filepath.Join(dir, lockName)
			if err := os.Rename(path, path+".moved"); err != nil {
				return err
			}
			return os.WriteFile(path, nil, 0o600)
		},
	})

	if err := Store("openrouter", "secret-value"); err == nil || !strings.Contains(err.Error(), "verify credential lock") {
		t.Fatalf("Store error = %v, want lock identity rejection", err)
	}
	assertNoCredentialTemps(t, base)
}

func TestSecondMutationCannotPassHeldLock(t *testing.T) {
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	base := useTestBackends(t, kr)
	writeCredentialFixture(t, base, `{"openrouter":"initial"}`)
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var entries atomic.Int32
	useFilesystemHooks(t, filesystemHooks{
		afterCredentialReadWhileLocked: func(string, string) error {
			if entries.Add(1) == 1 {
				close(firstEntered)
				<-releaseFirst
			}
			return nil
		},
	})

	firstDone := make(chan error, 1)
	go func() { firstDone <- Store("anthropic", "one") }()
	<-firstEntered
	secondDone := make(chan error, 1)
	go func() { secondDone <- Delete("openrouter") }()
	select {
	case err := <-secondDone:
		t.Fatalf("second mutation passed held lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if got := entries.Load(); got != 1 {
		t.Fatalf("lock checkpoint entries = %d, want 1 while first holds lock", got)
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Store: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}

func TestCredentialLockTimeoutIsBoundedAndReleasesDescriptors(t *testing.T) {
	kr := &fakeKeyring{setErr: errors.New("unavailable")}
	base := useTestBackends(t, kr)
	dir, _ := credentialPaths(base)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := openCredentialDirectory(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	lock, _, err := acquireCredentialLock(directory.app)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = Store("openrouter", "secret-value")
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "timed out acquiring credential lock") {
		t.Fatalf("Store error = %v, want lock timeout", err)
	}
	if elapsed < credentialLockTimeout || elapsed > credentialLockTimeout+time.Second {
		t.Fatalf("lock timeout elapsed %v, want [%v,%v]", elapsed, credentialLockTimeout, credentialLockTimeout+time.Second)
	}
	if err := releaseCredentialLock(lock); err != nil {
		t.Fatal(err)
	}
	if err := directory.close(); err != nil {
		t.Fatal(err)
	}
	if err := Store("openrouter", "secret-value"); err != nil {
		t.Fatalf("Store after timeout release: %v", err)
	}
}

func TestRepeatedCredentialOperationsDoNotLeakFileDescriptors(t *testing.T) {
	before := countOpenFileDescriptors()
	kr := &fakeKeyring{
		setErr:    errors.New("unavailable"),
		getErr:    keyring.ErrNotFound,
		deleteErr: keyring.ErrNotFound,
	}
	preferred, legacy := useCredentialMigrationRoots(t, kr)
	writeCredentialMapFixture(t, legacy, map[string]string{
		"openrouter": "legacy-secret",
		"anthropic":  "preserved-secret",
	})
	for i := 0; i < 100; i++ {
		if _, err := Resolve("openrouter", ""); err != nil {
			t.Fatal(err)
		}
		if err := Store("openrouter", "secret-value"); err != nil {
			t.Fatal(err)
		}
		if err := Delete("openrouter"); err != nil {
			t.Fatal(err)
		}
	}
	after := countOpenFileDescriptors()
	if delta := after - before; delta > 2 {
		t.Fatalf("file descriptor count grew by %d", delta)
	}
	credentials := readCredentialMapFixture(t, preferred)
	if len(credentials) != 1 || credentials["anthropic"] != "preserved-secret" {
		t.Fatalf("repeated migration lifecycle left unexpected map: %#v", credentials)
	}
	assertLegacyCredentialRemoved(t, legacy)
	assertNoCredentialTemps(t, preferred)
	assertNoCredentialTemps(t, legacy)
}

func countOpenFileDescriptors() int {
	count := 0
	for fd := 0; fd < 4096; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			count++
		}
	}
	return count
}

func TestCrossProcessHeldLockCheckpoints(t *testing.T) {
	tests := []struct {
		name           string
		secondAction   string
		secondProvider string
		assertFinal    func(*testing.T, string)
	}{
		{
			name:           "store store",
			secondAction:   "store",
			secondProvider: "anthropic",
			assertFinal: func(t *testing.T, base string) {
				useTestBackendsAt(t, &fakeKeyring{}, base)
				for provider, want := range map[string]string{"openrouter": "first", "anthropic": "second"} {
					if got, err := readFile(provider); err != nil || got != want {
						t.Fatalf("readFile(%s) = %q, %v; want %q", provider, got, err, want)
					}
				}
			},
		},
		{
			name:           "store delete",
			secondAction:   "delete",
			secondProvider: "openrouter",
			assertFinal: func(t *testing.T, base string) {
				useTestBackendsAt(t, &fakeKeyring{}, base)
				if got, err := readFile("openrouter"); err != nil || got != "" {
					t.Fatalf("deleted credential = %q, %v", got, err)
				}
				if got, err := readFile("anthropic"); err != nil || got != "first" {
					t.Fatalf("stored credential = %q, %v", got, err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			writeCredentialFixture(t, base, `{"openrouter":"initial"}`)
			signalA := filepath.Join(base, "signal-a")
			preLockB := filepath.Join(base, "pre-lock-b")
			signalB := filepath.Join(base, "signal-b")
			release := filepath.Join(base, "release")
			firstProvider := "openrouter"
			if test.secondAction == "delete" {
				firstProvider = "anthropic"
			}
			first := exec.Command(os.Args[0], "-test.run=^TestCredentialProcessHelper$")
			first.Env = append(os.Environ(),
				"CLAI_AUTH_HELPER_BASE="+base,
				"CLAI_AUTH_HELPER_ACTION=store",
				"CLAI_AUTH_HELPER_PROVIDER="+firstProvider,
				"CLAI_AUTH_HELPER_SECRET=first",
				"CLAI_AUTH_HELPER_SIGNAL="+signalA,
				"CLAI_AUTH_HELPER_RELEASE="+release,
			)
			if err := first.Start(); err != nil {
				t.Fatal(err)
			}
			firstDone := make(chan error, 1)
			go func() { firstDone <- first.Wait() }()
			waitForTestPath(t, signalA, 5*time.Second)

			second := exec.Command(os.Args[0], "-test.run=^TestCredentialProcessHelper$")
			second.Env = append(os.Environ(),
				"CLAI_AUTH_HELPER_BASE="+base,
				"CLAI_AUTH_HELPER_ACTION="+test.secondAction,
				"CLAI_AUTH_HELPER_PROVIDER="+test.secondProvider,
				"CLAI_AUTH_HELPER_SECRET=second",
				"CLAI_AUTH_HELPER_PRELOCK_SIGNAL="+preLockB,
				"CLAI_AUTH_HELPER_SIGNAL="+signalB,
				"CLAI_AUTH_HELPER_RELEASE="+release,
			)
			if err := second.Start(); err != nil {
				t.Fatal(err)
			}
			secondDone := make(chan error, 1)
			go func() { secondDone <- second.Wait() }()
			waitForTestPath(t, preLockB, 5*time.Second)
			select {
			case err := <-secondDone:
				t.Fatalf("second helper completed while first held lock: %v", err)
			case <-time.After(200 * time.Millisecond):
			}
			if _, err := os.Stat(signalB); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("second helper passed post-lock checkpoint while first held lock: %v", err)
			}
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			for name, done := range map[string]<-chan error{"first": firstDone, "second": secondDone} {
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("%s helper: %v", name, err)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("timed out waiting for %s helper", name)
				}
			}
			waitForTestPath(t, signalB, time.Second)
			test.assertFinal(t, base)
		})
	}
}

func waitForTestPath(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect checkpoint %s: %v", path, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for checkpoint %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCrossProcessStoreDeleteSerialized(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeCredentialFixture(t, base, `{"openrouter":"remove"}`)
	store := exec.Command(os.Args[0], "-test.run=^TestCredentialProcessHelper$")
	store.Env = append(os.Environ(), "CLAI_AUTH_HELPER_BASE="+base, "CLAI_AUTH_HELPER_ACTION=store", "CLAI_AUTH_HELPER_PROVIDER=anthropic", "CLAI_AUTH_HELPER_SECRET=keep")
	remove := exec.Command(os.Args[0], "-test.run=^TestCredentialProcessHelper$")
	remove.Env = append(os.Environ(), "CLAI_AUTH_HELPER_BASE="+base, "CLAI_AUTH_HELPER_ACTION=delete", "CLAI_AUTH_HELPER_PROVIDER=openrouter")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, command := range []*exec.Cmd{store, remove} {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; errs <- command.Run() }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("credential helper: %v", err)
		}
	}
	useTestBackendsAt(t, &fakeKeyring{}, base)
	if got, err := readFile("openrouter"); err != nil || got != "" {
		t.Fatalf("deleted credential = %q, %v", got, err)
	}
	if got, err := readFile("anthropic"); err != nil || got != "keep" {
		t.Fatalf("stored credential = %q, %v", got, err)
	}
}
