//go:build darwin || linux

package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

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
