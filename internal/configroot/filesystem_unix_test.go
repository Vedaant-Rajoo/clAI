//go:build darwin || linux

package configroot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalizeRootSymlink(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "real-config-root")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	approved := filepath.Join(base, "approved-config-root")
	if err := os.Symlink(target, approved); err != nil {
		t.Fatal(err)
	}

	got, err := Canonicalize(approved, false)
	if err != nil {
		t.Fatalf("Canonicalize approved root symlink: %v", err)
	}
	if got != target {
		t.Fatalf("Canonicalize = %q, want canonical target %q", got, target)
	}
}

func TestCanonicalizeExistingSharedBaseMode0755(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(base, "xdg-config-home")
	if err := os.Mkdir(selected, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(selected, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Canonicalize(selected, false)
	if err != nil {
		t.Fatalf("Canonicalize existing 0755 shared base: %v", err)
	}
	if got != selected {
		t.Fatalf("Canonicalize = %q, want %q", got, selected)
	}
	info, err := os.Stat(selected)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Fatalf("shared base mode = %o, want unchanged 755", perm)
	}
}

func TestCanonicalizeCreatesMissingBase(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(base, "missing", "nested", "root")

	got, err := Canonicalize(selected, true)
	if err != nil {
		t.Fatalf("Canonicalize missing root: %v", err)
	}
	if got != selected {
		t.Fatalf("Canonicalize = %q, want %q", got, selected)
	}
	for path := filepath.Join(base, "missing"); ; path = filepath.Join(path, nextCreatedComponent(path, selected)) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat created component %q: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("created component %q is not a directory", path)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("created component %q mode = %o, want 700", path, perm)
		}
		if path == selected {
			break
		}
	}
}

func nextCreatedComponent(current, selected string) string {
	remainder := strings.TrimPrefix(selected, current+string(filepath.Separator))
	component, _, _ := strings.Cut(remainder, string(filepath.Separator))
	return component
}

func TestCanonicalizeMissingWithoutCreate(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(base, "missing", "root")

	got, err := Canonicalize(selected, false)
	if err != nil {
		t.Fatalf("Canonicalize missing root without create: %v", err)
	}
	if got != selected {
		t.Fatalf("Canonicalize = %q, want validated path %q", got, selected)
	}
	if _, err := os.Lstat(filepath.Join(base, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing root was created with create=false: %v", err)
	}
}

func TestCanonicalizeRejectsRelative(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	separator := string(filepath.Separator)
	paths := map[string]string{
		"empty":            "",
		"relative":         filepath.Join("relative", "root"),
		"dot component":    base + separator + "." + separator + "root",
		"parent component": base + separator + "child" + separator + ".." + separator + "root",
	}
	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			if got, err := Canonicalize(path, true); err == nil {
				t.Fatalf("Canonicalize(%q) = %q, nil; want validation error", path, got)
			}
		})
	}
}

func TestCanonicalizeRejectsNonDirectory(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if got, err := Canonicalize(file, true); err == nil {
		t.Fatalf("Canonicalize(non-directory) = %q, nil; want error", got)
	}
}

func TestCanonicalizeRejectsSuffixSubstitution(t *testing.T) {
	tests := []struct {
		name       string
		substitute func(string) error
	}{
		{
			name: "directory replacement",
			substitute: func(path string) error {
				if err := os.Remove(path); err != nil {
					return err
				}
				return os.Mkdir(path, 0o700)
			},
		},
		{
			name: "symlink replacement",
			substitute: func(path string) error {
				if err := os.Remove(path); err != nil {
					return err
				}
				target := filepath.Join(filepath.Dir(path), "attacker-target")
				if err := os.Mkdir(target, 0o700); err != nil {
					return err
				}
				return os.Symlink(target, path)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			selected := filepath.Join(base, "missing", "nested")
			firstCreated := filepath.Join(base, "missing")
			oldHook := canonicalizeAfterCreate
			called := false
			canonicalizeAfterCreate = func(path string) error {
				if path != firstCreated || called {
					return nil
				}
				called = true
				return tc.substitute(path)
			}
			t.Cleanup(func() { canonicalizeAfterCreate = oldHook })

			got, err := Canonicalize(selected, true)
			if err == nil {
				t.Fatalf("Canonicalize substituted suffix = %q, nil; want error", got)
			}
			if !called {
				t.Fatal("creation checkpoint was not reached")
			}
			if !strings.Contains(err.Error(), "changed") && !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("Canonicalize error = %v, want substitution rejection", err)
			}
		})
	}
}
