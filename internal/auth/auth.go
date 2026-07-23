// Package auth resolves and stores LLM provider credentials.
//
// Resolution precedence: explicit value (flag) > environment variable >
// OS keyring > config file fallback.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	serviceName = "clai"
	fileName    = "credentials.json"
	lockName    = ".credentials.lock"
)

type keyringStore interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

type systemKeyring struct{}

func (systemKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}
func (systemKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}
func (systemKeyring) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

// filesystemHooks are deterministic test checkpoints. Production leaves every
// hook nil. Hooks run while the cross-process credential lock is held.
type filesystemHooks struct {
	afterBaseValidationBeforeAppOpen func(base, app string) error
	beforeLockAcquire                func(dir, path string) error
	afterLockAcquired                func(dir, path string) error
	afterFileValidationBeforeRead    func(dir, path string) error
	afterCredentialReadWhileLocked   func(dir, path string) error
	beforeTempCreation               func(dir, path string) error
	afterTempCreation                func(dir, path, tempName string) error
	afterRenameBeforeVerification    func(dir, path string) error
	unlinkTemp                       func(dirfd int, name string) error
	unlockLock                       func(fd int) error
	closeLock                        func(file *os.File) error
	closeCredential                  func(file *os.File) error
	closeTemp                        func(file *os.File) error
	closeAppDirectory                func(file *os.File) error
	closeBaseDirectory               func(file *os.File) error
	syncDirectory                    func(fd int) error
}

var (
	credentialKeyring         keyringStore = systemKeyring{}
	userConfigDir                          = os.UserConfigDir
	credentialFilesystemHooks filesystemHooks
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

	key, keyringErr := credentialKeyring.Get(serviceName, provider)
	if keyringErr == nil && key != "" {
		return key, nil
	}
	if keyringErr == nil {
		keyringErr = keyring.ErrNotFound
	}

	key, fileErr := readFile(provider)
	if fileErr != nil {
		if errors.Is(keyringErr, keyring.ErrNotFound) {
			return "", fileErr
		}
		return "", errors.Join(
			fmt.Errorf("read credential from keyring: %w", keyringErr),
			fmt.Errorf("read credential from config file: %w", fileErr),
		)
	}
	if key != "" {
		return key, nil
	}
	if !errors.Is(keyringErr, keyring.ErrNotFound) {
		return "", fmt.Errorf("read credential from keyring: %w", keyringErr)
	}
	return "", nil
}

// Store persists an API key, preferring the OS keyring and falling back to a
// private config file when the keyring is unavailable.
func Store(provider, key string) error {
	if err := credentialKeyring.Set(serviceName, provider, key); err == nil {
		if err := deleteFileEntry(provider); err != nil {
			return fmt.Errorf("remove stale config credential: %w", err)
		}
		return nil
	}
	if err := writeFile(provider, key); err != nil {
		return fmt.Errorf("store credential in config file: %w", err)
	}
	return nil
}

// Delete removes the stored key from both keyring and file. A missing keyring
// entry is not an error, but operational errors are returned after config-file
// cleanup is attempted.
func Delete(provider string) error {
	keyringErr := credentialKeyring.Delete(serviceName, provider)
	if errors.Is(keyringErr, keyring.ErrNotFound) {
		keyringErr = nil
	} else if keyringErr != nil {
		keyringErr = fmt.Errorf("delete credential from keyring: %w", keyringErr)
	}
	fileErr := deleteFileEntry(provider)
	if fileErr != nil {
		fileErr = fmt.Errorf("delete credential from config file: %w", fileErr)
	}
	return errors.Join(keyringErr, fileErr)
}

// Source reports where the resolved credential came from, for compatibility
// with existing callers. It returns "none" when source inspection fails.
// Callers that must distinguish absence from unsafe storage or backend failure
// should use SourceWithError.
func Source(provider, explicit string) string {
	source, err := SourceWithError(provider, explicit)
	if err != nil {
		return "none"
	}
	return source
}

// SourceWithError reports where the resolved credential came from while
// preserving storage validation and keyring errors.
func SourceWithError(provider, explicit string) (string, error) {
	if explicit != "" {
		return "flag", nil
	}
	if env := EnvVarFor(provider); env != "" && os.Getenv(env) != "" {
		return "env (" + env + ")", nil
	}

	key, keyringErr := credentialKeyring.Get(serviceName, provider)
	if keyringErr == nil && key != "" {
		return "keyring", nil
	}
	if keyringErr == nil {
		keyringErr = keyring.ErrNotFound
	}

	key, fileErr := readFile(provider)
	if fileErr != nil {
		if errors.Is(keyringErr, keyring.ErrNotFound) {
			return "", fileErr
		}
		return "", errors.Join(
			fmt.Errorf("inspect credential keyring: %w", keyringErr),
			fmt.Errorf("inspect credential config file: %w", fileErr),
		)
	}
	if key != "" {
		return "config file", nil
	}
	if !errors.Is(keyringErr, keyring.ErrNotFound) {
		return "", fmt.Errorf("inspect credential keyring: %w", keyringErr)
	}
	return "none", nil
}

func configPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	if err := validateConfiguredBasePath(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, "clai", fileName), nil
}

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

func readFile(provider string) (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	data, exists, err := secureReadCredentialFile(path)
	if err != nil || !exists {
		return "", err
	}
	creds, err := decodeCredentials(path, data)
	if err != nil {
		return "", err
	}
	return creds[provider], nil
}

func writeFile(provider, key string) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	return secureMutateCredentialFile(path, true, func(creds map[string]string) bool {
		creds[provider] = key
		return true
	})
}

func deleteFileEntry(provider string) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	return secureMutateCredentialFile(path, false, func(creds map[string]string) bool {
		if _, ok := creds[provider]; !ok {
			return false
		}
		delete(creds, provider)
		return true
	})
}

func decodeCredentials(path string, data []byte) (map[string]string, error) {
	var creds map[string]string
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credential file %q: %w", path, err)
	}
	if creds == nil {
		creds = map[string]string{}
	}
	return creds, nil
}
