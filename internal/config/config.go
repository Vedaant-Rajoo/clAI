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
	"errors"
	"os"
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

// Load reads config.json leniently. A missing file is a first-class "not
// configured yet" outcome: (Config{}, nil) with no diagnostic.
func Load() (Config, error) {
	return Config{}, errors.New("config: Load not implemented")
}
