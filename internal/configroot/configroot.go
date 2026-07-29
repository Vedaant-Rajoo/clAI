// Package configroot defines clai's shared configuration-root contract.
//
// Root discovery is centralized here so settings and file-backed credentials
// cannot drift to different platform locations. Filesystem authority stops at
// the selected base: callers may later canonicalize that approved root, but
// clai descendants remain the responsibility of descriptor-relative,
// no-follow storage code.
package configroot

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

var (
	currentGOOS   = runtime.GOOS
	lookupEnv     = os.LookupEnv
	userHomeDir   = os.UserHomeDir
	userConfigDir = os.UserConfigDir
)

// Roots names the preferred configuration base and an optional legacy base.
type Roots struct {
	Preferred string
	Legacy    string
}

// Resolve discovers clai's configuration roots for the current platform.
func Resolve() (Roots, error) {
	return Roots{}, errors.New("config root resolution is not implemented")
}

// resolve applies the platform root policy to already-collected inputs.
func resolve(goos, xdg, home, platformConfig string) (Roots, error) {
	return Roots{}, errors.New("config root resolution is not implemented")
}

// PreferredPath returns a named clai descendant below the preferred base.
func (r Roots) PreferredPath(name string) string {
	return filepath.Join(r.Preferred, "clai", name)
}

// LegacyPath returns a named clai descendant below the legacy base, or an
// empty path when this platform has no legacy base.
func (r Roots) LegacyPath(name string) string {
	if r.Legacy == "" {
		return ""
	}
	return filepath.Join(r.Legacy, "clai", name)
}
