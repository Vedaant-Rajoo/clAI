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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	currentGOOS   = runtime.GOOS
	lookupEnv     = os.LookupEnv
	userHomeDir   = os.UserHomeDir
	userConfigDir = os.UserConfigDir

	relativeXDGWarningOnce sync.Once
	warnRelativeXDG        = func(value string) {
		fmt.Fprintf(os.Stderr, "clai: warning: ignoring relative XDG_CONFIG_HOME %q; using the platform default config directory\n", value)
	}
)

// Roots names the preferred configuration base and an optional legacy base.
type Roots struct {
	Preferred string
	Legacy    string
}

// Resolve discovers clai's configuration roots for the current platform.
func Resolve() (Roots, error) {
	xdg, _ := lookupEnv("XDG_CONFIG_HOME")
	if xdg != "" && !filepath.IsAbs(xdg) {
		relativeXDGWarningOnce.Do(func() { warnRelativeXDG(xdg) })
		xdg = ""
	}

	switch currentGOOS {
	case "darwin":
		home, err := userHomeDir()
		if err != nil {
			return Roots{}, fmt.Errorf("locate user home directory: %w", err)
		}
		return resolve(currentGOOS, xdg, home, "")
	case "linux":
		home := ""
		if xdg == "" {
			var err error
			home, err = userHomeDir()
			if err != nil {
				return Roots{}, fmt.Errorf("locate user home directory: %w", err)
			}
		}
		return resolve(currentGOOS, xdg, home, "")
	default:
		platformConfig, err := userConfigDir()
		if err != nil {
			return Roots{}, fmt.Errorf("locate user config directory: %w", err)
		}
		return resolve(currentGOOS, xdg, "", platformConfig)
	}
}

// resolve applies the platform root policy to already-collected inputs.
func resolve(goos, xdg, home, platformConfig string) (Roots, error) {
	switch goos {
	case "darwin":
		home, err := validateBase("HOME", home)
		if err != nil {
			return Roots{}, err
		}
		preferred := filepath.Join(home, ".config")
		if xdg != "" {
			preferred, err = validateBase("XDG_CONFIG_HOME", xdg)
			if err != nil {
				return Roots{}, err
			}
		}
		return Roots{
			Preferred: preferred,
			Legacy:    filepath.Join(home, "Library", "Application Support"),
		}, nil
	case "linux":
		if xdg != "" {
			preferred, err := validateBase("XDG_CONFIG_HOME", xdg)
			if err != nil {
				return Roots{}, err
			}
			return Roots{Preferred: preferred}, nil
		}
		home, err := validateBase("HOME", home)
		if err != nil {
			return Roots{}, err
		}
		return Roots{Preferred: filepath.Join(home, ".config")}, nil
	default:
		preferred, err := validateBase("user config directory", platformConfig)
		if err != nil {
			return Roots{}, err
		}
		return Roots{Preferred: preferred}, nil
	}
}

// validateBase rejects ambiguous roots before cleaning them. In particular,
// relative and dot-component inputs are never normalized into an approved
// absolute path.
func validateBase(name, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be absolute", name)
	}
	volume := filepath.VolumeName(path)
	remainder := strings.TrimPrefix(path, volume)
	for _, component := range strings.FieldsFunc(remainder, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if component == "." || component == ".." {
			return "", fmt.Errorf("%s must not contain dot path components", name)
		}
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", errors.New("cleaned config root must remain absolute")
	}
	return clean, nil
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
