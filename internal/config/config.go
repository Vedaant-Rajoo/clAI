// Package config persists clai's behavioral settings to
// <UserConfigDir>/clai/config.json under the config-file/v1 contract.
//
// The persisted struct carries user choices only: provider, model, delivery,
// and init_completed. It structurally cannot hold credentials — API keys
// resolve exclusively through internal/auth (CONF-04). Reads are lenient:
// unknown fields are ignored so future-version files never brick the CLI. A
// readable but unparseable file classifies as ErrCorrupt so callers can warn
// and continue with defaults rather than hard-fail (CONF-05).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ConfigContract identifies the config-file schema version (CONF-01).
const ConfigContract = "config-file/v1"

const fileName = "config.json"

// userConfigDir is a swappable seam mirroring internal/auth so tests can
// point the package at a temporary base directory.
var userConfigDir = os.UserConfigDir

// ErrCorrupt classifies a readable but unparseable config file. Callers match
// it with errors.Is to distinguish parse failures from I/O failures; either
// way the CLI warns and continues with defaults (CONF-05).
var ErrCorrupt = errors.New("config file is corrupt")

// Config is the persisted config-file/v1 payload. It carries user choices
// only; no field can hold key material (CONF-04 is structural).
type Config struct {
	Contract      string `json:"contract"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	Delivery      string `json:"delivery,omitempty"`
	InitCompleted bool   `json:"init_completed"`
}

// Path returns the config file location <UserConfigDir>/clai/config.json,
// mirroring internal/auth's derivation so config.json and credentials.json
// provably share a base directory (CONF-01).
func Path() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	if err := validateConfiguredBasePath(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, "clai", fileName), nil
}

// validateConfiguredBasePath rejects relative bases and dot path components,
// duplicated verbatim from internal/auth (which this phase must not modify).
func validateConfiguredBasePath(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("user config directory must be absolute")
	}
	volume := filepath.VolumeName(path)
	remainder := strings.TrimPrefix(path, volume)
	for _, component := range strings.FieldsFunc(remainder, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if component == "." || component == ".." {
			return errors.New("user config directory must not contain dot path components")
		}
	}
	return nil
}

// Save atomically persists cfg to <UserConfigDir>/clai/config.json via the
// platform writer (flock + temp + fsync + rename, 0600/0700 on unix;
// fail-closed elsewhere). The config-file/v1 contract is stamped
// unconditionally — every written file identifies its schema version even
// when the caller passes a zero Config (CONF-01). init_completed persists as
// a plain JSON field of the same file; no separate marker file exists
// (CONF-02).
func Save(cfg Config) error {
	cfg.Contract = ConfigContract
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	out = append(out, '\n')
	return writeConfigFile(out)
}

// Load reads config.json leniently. A missing file is a first-class "not
// configured yet" outcome: (Config{}, nil) with no diagnostic. Any readable
// but unparseable content — including an empty file — classifies as
// ErrCorrupt; other read failures wrap the underlying I/O error. The read
// side holds no secrets, so a plain os.ReadFile works on every platform (the
// hardened writer arrives with Save in a later plan).
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config file %q: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		// The wrapped json error carries only offset/type detail, never raw
		// file bytes — a user may have pasted a key into the file.
		return Config{}, fmt.Errorf("parse config file %q: %w (%v)", path, ErrCorrupt, err)
	}
	return cfg, nil
}
