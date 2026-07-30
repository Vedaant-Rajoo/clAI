//go:build darwin || linux

package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Vedaant-Rajoo/clai/internal/configroot"
	"github.com/zalando/go-keyring"
)

func TestInvalidConfiguredPathsRejectedAcrossPublicOperations(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		"relative":                  filepath.Join("relative", "config"),
		"absolute parent component": filepath.Join(root, "child") + string(filepath.Separator) + ".." + string(filepath.Separator) + "real",
		"dot component":             root + string(filepath.Separator) + "." + string(filepath.Separator) + "real",
		"removed symlink component": link + string(filepath.Separator) + ".." + string(filepath.Separator) + "real",
	}
	operations := []struct {
		name string
		run  func() error
	}{
		{name: "resolve", run: func() error { _, err := Resolve("openrouter", ""); return err }},
		{name: "store", run: func() error { return Store("openrouter", "secret-value") }},
		{name: "source", run: func() error { _, err := SourceWithError("openrouter", ""); return err }},
		{name: "delete", run: func() error { return Delete("openrouter") }},
	}
	for name, path := range paths {
		for _, operation := range operations {
			t.Run(name+"/"+operation.name, func(t *testing.T) {
				useTestBackendsAt(t, &fakeKeyring{
					getErr:    keyring.ErrNotFound,
					setErr:    errors.New("unavailable"),
					deleteErr: keyring.ErrNotFound,
				}, path)
				if err := operation.run(); err == nil {
					t.Fatal("operation unexpectedly accepted invalid configured path")
				}
			})
		}
	}
}

func TestCredentialRootSymlinkPolicy(t *testing.T) {
	operations := []struct {
		name    string
		fixture map[string]string
		run     func() error
		assert  func(*testing.T, string)
	}{
		{
			name:    "resolve",
			fixture: map[string]string{"openrouter": "root-link-secret"},
			run: func() error {
				key, err := Resolve("openrouter", "")
				if err == nil && key != "root-link-secret" {
					return errors.New("Resolve did not read through approved root symlink")
				}
				return err
			},
		},
		{
			name: "store",
			run:  func() error { return Store("openrouter", "root-link-secret") },
			assert: func(t *testing.T, target string) {
				if got := readCredentialMapFixture(t, target)["openrouter"]; got != "root-link-secret" {
					t.Fatalf("stored credential = %q, want root-link-secret", got)
				}
			},
		},
		{
			name:    "source",
			fixture: map[string]string{"openrouter": "root-link-secret"},
			run: func() error {
				source, err := SourceWithError("openrouter", "")
				if err == nil && source != "config file" {
					return errors.New("SourceWithError did not report config file through approved root symlink")
				}
				return err
			},
		},
		{
			name:    "delete",
			fixture: map[string]string{"openrouter": "root-link-secret", "anthropic": "preserved-secret"},
			run:     func() error { return Delete("openrouter") },
			assert: func(t *testing.T, target string) {
				credentials := readCredentialMapFixture(t, target)
				if _, exists := credentials["openrouter"]; exists || credentials["anthropic"] != "preserved-secret" {
					t.Fatalf("Delete through approved root symlink left unexpected map: %#v", credentials)
				}
			},
		},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(parent, "canonical-root")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			selected := filepath.Join(parent, "approved-root")
			if err := os.Symlink(target, selected); err != nil {
				t.Fatal(err)
			}
			if operation.fixture != nil {
				writeCredentialMapFixture(t, target, operation.fixture)
			}

			oldKeyring, oldRoots := credentialKeyring, resolveConfigRoots
			credentialKeyring = &fakeKeyring{
				getErr:    keyring.ErrNotFound,
				setErr:    errors.New("keyring unavailable"),
				deleteErr: keyring.ErrNotFound,
			}
			resolveConfigRoots = func() (configroot.Roots, error) {
				return configroot.Roots{Preferred: selected}, nil
			}
			t.Cleanup(func() {
				credentialKeyring = oldKeyring
				resolveConfigRoots = oldRoots
			})

			if err := operation.run(); err != nil {
				t.Fatalf("%s through approved root symlink: %v", operation.name, err)
			}
			if operation.assert != nil {
				operation.assert(t, target)
			}
		})
	}
}

func TestCredentialBelowRootSymlinksRejectedAfterCreation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	useTestBackendsAt(t, &fakeKeyring{setErr: errors.New("keyring unavailable")}, root)
	attackerTarget := filepath.Join(t.TempDir(), "attacker-temp-target")
	if err := os.WriteFile(attackerTarget, []byte("attacker-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	var moved string
	useFilesystemHooks(t, filesystemHooks{
		afterTempCreation: func(dir, _ string, tempName string) error {
			original := filepath.Join(dir, tempName)
			moved = original + ".moved"
			if err := os.Rename(original, moved); err != nil {
				return err
			}
			return os.Symlink(attackerTarget, original)
		},
	})

	if err := Store("openrouter", "secret-value"); err == nil {
		t.Fatal("Store unexpectedly accepted credential temp substitution")
	}
	data, err := os.ReadFile(moved)
	if err != nil {
		t.Fatalf("read moved credential temp: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("moved credential temp retained secret bytes: %q", data)
	}
	assertNoCredentialTemps(t, root)
}

func TestCredentialBelowRootSymlinksRejected(t *testing.T) {
	operations := []struct {
		name string
		run  func() error
	}{
		{name: "resolve", run: func() error { _, err := Resolve("openrouter", ""); return err }},
		{name: "store", run: func() error { return Store("openrouter", "secret-value") }},
		{name: "source", run: func() error { _, err := SourceWithError("openrouter", ""); return err }},
		{name: "delete", run: func() error { return Delete("openrouter") }},
	}
	fixtures := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "clai symlink",
			setup: func(t *testing.T, root string) {
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "clai")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "credential symlink",
			setup: func(t *testing.T, root string) {
				dir := filepath.Join(root, "clai")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "credential-target")
				if err := os.WriteFile(target, []byte(`{"openrouter":"attacker-secret"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, fileName)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "lock symlink",
			setup: func(t *testing.T, root string) {
				writeCredentialMapFixture(t, root, map[string]string{"openrouter": "existing-secret"})
				target := filepath.Join(t.TempDir(), "lock-target")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, "clai", lockName)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "temporary symlink",
			setup: func(t *testing.T, root string) {
				dir := filepath.Join(root, "clai")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "temp-target")
				if err := os.WriteFile(target, []byte("attacker-secret"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, ".credentials-00112233445566778899aabb.tmp")); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, fixture := range fixtures {
		for _, operation := range operations {
			t.Run(fixture.name+"/"+operation.name, func(t *testing.T) {
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				useTestBackendsAt(t, &fakeKeyring{
					getErr:    keyring.ErrNotFound,
					setErr:    errors.New("keyring unavailable"),
					deleteErr: keyring.ErrNotFound,
				}, root)
				fixture.setup(t, root)
				if err := operation.run(); err == nil {
					t.Fatal("operation unexpectedly accepted below-root symlink")
				}
			})
		}
	}
}
