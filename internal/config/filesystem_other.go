//go:build !darwin && !linux

package config

import "errors"

var errSecureFallbackUnsupported = errors.New("secure config write is unsupported on this platform")

// Non-Darwin/Linux builds fail closed instead of claiming descriptor-relative,
// no-follow, cross-process-lock behavior that has not been implemented or
// runtime-proven on those platforms, mirroring internal/auth's platform split.
// Load remains a plain os.ReadFile everywhere, so the CLI still works with a
// zero Config on these platforms — only persistence is unavailable (recorded
// decision, RESEARCH Open Question 1).
func writeConfigFile([]byte) error {
	return errSecureFallbackUnsupported
}
