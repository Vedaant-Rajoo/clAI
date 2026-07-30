//go:build !darwin && !linux

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

var errSecureFallbackUnsupported = errors.New("secure config write is unsupported on this platform")

func migrateConfigFile(configFileLocations) error {
	return nil
}

func readConfigFile(locations configFileLocations) ([]byte, bool, error) {
	data, err := os.ReadFile(locations.preferred)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read config file %q: %w", locations.preferred, err)
	}
	return data, true, nil
}

// Non-Darwin/Linux builds fail closed instead of claiming descriptor-relative,
// no-follow, cross-process-lock behavior that has not been implemented or
// runtime-proven on those platforms, mirroring internal/auth's platform split.
func writeConfigFile(configFileLocations, []byte) error {
	return errSecureFallbackUnsupported
}
