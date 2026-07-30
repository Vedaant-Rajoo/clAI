//go:build !darwin && !linux

package auth

import (
	"errors"
	"os"
	"path/filepath"
)

var errSecureFallbackUnsupported = errors.New("secure credential config fallback is unsupported on this platform")

// Non-Darwin/Linux builds preserve the selected preferred platform location
// and fail closed instead of claiming descriptor-relative, no-follow,
// cross-process-lock, or migration behavior that has not been runtime-proven.
func secureReadCredentialFiles(locations credentialFileLocations, _ string) (string, error) {
	_, err := os.Lstat(filepath.Dir(locations.preferred))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return "", errSecureFallbackUnsupported
}

func secureMutateCredentialFiles(locations credentialFileLocations, create bool, _ func(map[string]string) bool) error {
	if !create {
		if _, err := os.Lstat(filepath.Dir(locations.preferred)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	return errSecureFallbackUnsupported
}
