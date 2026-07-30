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
	"path/filepath"

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

type configFileLocations struct {
	preferred string
	legacy    string
}

// Path returns config.json below the selected preferred clai root (CONF-01).
// Filesystem operations canonicalize that approved root internally, but Path
// preserves the resolver's selected spelling for diagnostics and callers.
func Path() (string, error) {
	locations, err := selectedConfigFileLocations()
	if err != nil {
		return "", err
	}
	return locations.preferred, nil
}

func selectedConfigFileLocations() (configFileLocations, error) {
	roots, err := resolveConfigRoots()
	if err != nil {
		return configFileLocations{}, fmt.Errorf("resolve config roots: %w", err)
	}
	return configFileLocations{
		preferred: roots.PreferredPath(fileName),
		legacy:    roots.LegacyPath(fileName),
	}, nil
}

func resolveConfigFileLocations(createPreferred bool) (configFileLocations, error) {
	selected, err := selectedConfigFileLocations()
	if err != nil {
		return configFileLocations{}, err
	}
	return canonicalizeConfigFileLocations(selected, createPreferred)
}

func canonicalizeConfigFileLocations(selected configFileLocations, createPreferred bool) (configFileLocations, error) {
	preferredRoot := filepathRoot(selected.preferred)
	preferred, err := configroot.Canonicalize(preferredRoot, createPreferred)
	if err != nil {
		return configFileLocations{}, fmt.Errorf("canonicalize preferred config root: %w", err)
	}
	legacy := ""
	if selected.legacy != "" {
		legacyRoot := filepathRoot(selected.legacy)
		legacy, err = configroot.Canonicalize(legacyRoot, false)
		if err != nil {
			return configFileLocations{}, fmt.Errorf("canonicalize legacy config root: %w", err)
		}
	}
	canonical := configroot.Roots{Preferred: preferred, Legacy: legacy}
	return configFileLocations{
		preferred: canonical.PreferredPath(fileName),
		legacy:    canonical.LegacyPath(fileName),
	}, nil
}

func filepathRoot(configPath string) string {
	return filepath.Dir(filepath.Dir(configPath))
}

// Save atomically persists cfg to <preferred-base>/clai/config.json via the
// platform writer. Every write stamps config-file/v1 and reconciles any legacy
// Darwin copy under the same ordered lock set (CONF-01, REQ-CONFIG-003).
func Save(cfg Config) error {
	locations, err := resolveConfigFileLocations(true)
	if err != nil {
		return err
	}
	out, err := marshalConfig(cfg)
	if err != nil {
		return err
	}
	return writeConfigFile(locations, out)
}

func marshalConfig(cfg Config) ([]byte, error) {
	cfg.Contract = ConfigContract
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return append(out, '\n'), nil
}

// Load reconciles the Darwin legacy location before decoding the preferred
// config. A preferred file is authoritative whenever it exists, including when
// its bytes are corrupt, so stale legacy settings can never be revived.
func Load() (Config, error) {
	selected, err := selectedConfigFileLocations()
	if err != nil {
		return Config{}, err
	}
	exists, err := configStateExists(selected)
	if err != nil {
		return Config{}, err
	}
	if !exists {
		return Config{}, nil
	}
	locations, err := canonicalizeConfigFileLocations(selected, true)
	if err != nil {
		return Config{}, err
	}
	if err := migrateConfigFile(locations); err != nil {
		return Config{}, err
	}
	data, exists, err := readConfigFile(locations)
	if err != nil {
		return Config{}, err
	}
	if !exists {
		return Config{}, nil
	}
	return decodeConfig(locations.preferred, data)
}

func configStateExists(locations configFileLocations) (bool, error) {
	for _, path := range []string{locations.preferred, locations.legacy} {
		if path == "" {
			continue
		}
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("inspect config file %q: %w", path, err)
		}
	}
	return false, nil
}

func decodeConfig(path string, data []byte) (Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		// The wrapped json error carries only offset/type detail, never raw
		// file bytes — a user may have pasted a key into the file.
		return Config{}, fmt.Errorf("parse config file %q: %w (%v)", path, ErrCorrupt, err)
	}
	return cfg, nil
}
