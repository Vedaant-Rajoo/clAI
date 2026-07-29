// Package config persists clai's behavioral settings to the shared preferred
// configuration root under the config-file/v1 contract.
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

	"github.com/Vedaant-Rajoo/clai/internal/configroot"
)

// ConfigContract identifies the config-file schema version (CONF-01).
const ConfigContract = "config-file/v1"

const fileName = "config.json"

// resolveConfigRoots is the shared root-policy seam used by deterministic tests.
var resolveConfigRoots = configroot.Resolve

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

// Path returns config.json below the shared preferred clai root (CONF-01).
func Path() (string, error) {
	roots, err := resolveConfigRoots()
	if err != nil {
		return "", fmt.Errorf("resolve config roots: %w", err)
	}
	return roots.PreferredPath(fileName), nil
}

// Save atomically persists cfg to <preferred-base>/clai/config.json via the
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
