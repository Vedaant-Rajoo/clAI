//go:build !darwin && !linux

package auth

import (
	"errors"
	"os"
	"path/filepath"
)

var errSecureFallbackUnsupported = errors.New("secure credential config fallback is unsupported on this platform")

// Non-Darwin/Linux builds fail closed instead of claiming descriptor-relative,
// no-follow, cross-process-lock behavior that has not been implemented or
// runtime-proven on those platforms.
func secureReadCredentialFile(path string) ([]byte, bool, error) {
	_, err := os.Lstat(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return nil, false, errSecureFallbackUnsupported
}

func secureMutateCredentialFile(path string, create bool, _ func(map[string]string) bool) error {
	if !create {
		if _, err := os.Lstat(filepath.Dir(path)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	return errSecureFallbackUnsupported
}
