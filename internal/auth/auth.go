// Package auth resolves and stores LLM provider credentials.
//
// Resolution precedence: explicit value (flag) > environment variable >
// OS keyring > config file fallback. Stored credentials are long-lived
// API keys; on 401 the caller should prompt the user to re-authenticate.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

const (
	serviceName = "clai"
	fileName    = "credentials.json"
)

// EnvVarFor returns the conventional environment variable for a provider.
func EnvVarFor(provider string) string {
	switch provider {
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	default:
		return ""
	}
}

// Resolve returns the API key for a provider using the precedence chain:
// explicit (flag) > env var > keyring > config file. Returns "" with nil
// error when no credential is configured anywhere.
func Resolve(provider, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	if env := EnvVarFor(provider); env != "" {
		if value := os.Getenv(env); value != "" {
			return value, nil
		}
	}

	key, err := keyring.Get(serviceName, provider)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		// Keyring exists but errored; fall through to file rather than fail.
		if key, ferr := readFile(provider); ferr == nil && key != "" {
			return key, nil
		}
		return "", fmt.Errorf("read credentials for %s: %w", provider, err)
	}

	return readFile(provider)
}

// Store persists an API key, preferring the OS keyring and falling back to
// a 0600 config file when the keyring is unavailable (headless Linux, CI).
func Store(provider, key string) error {
	if err := keyring.Set(serviceName, provider, key); err == nil {
		// Clear any stale file copy so there is a single source of truth.
		_ = deleteFileEntry(provider)
		return nil
	}

	return writeFile(provider, key)
}

// Delete removes the stored key from both keyring and file.
func Delete(provider string) error {
	err := keyring.Delete(serviceName, provider)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		err = nil // keyring unavailable; still try the file
	}
	if ferr := deleteFileEntry(provider); ferr != nil {
		return ferr
	}
	return err
}

// Source reports where the resolved credential came from, for `auth status`.
func Source(provider, explicit string) string {
	switch {
	case explicit != "":
		return "flag"
	case EnvVarFor(provider) != "" && os.Getenv(EnvVarFor(provider)) != "":
		return "env (" + EnvVarFor(provider) + ")"
	}
	if _, err := keyring.Get(serviceName, provider); err == nil {
		return "keyring"
	}
	if key, err := readFile(provider); err == nil && key != "" {
		return "config file"
	}
	return "none"
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "clai", fileName), nil
}

func readFile(provider string) (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var creds map[string]string
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	return creds[provider], nil
}

func writeFile(provider, key string) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	creds := map[string]string{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &creds)
	}
	creds[provider] = key
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func deleteFileEntry(provider string) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var creds map[string]string
	if err := json.Unmarshal(data, &creds); err != nil {
		return err
	}
	if _, ok := creds[provider]; !ok {
		return nil
	}
	delete(creds, provider)
	out, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}
