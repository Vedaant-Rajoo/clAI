// Package auth resolves and stores LLM provider credentials.
//
// Resolution precedence: explicit value (flag) > environment variable >
// OS keyring > preferred private credential file. On Darwin, an old-only
// Application Support file is migrated under the shared config-root contract;
// an existing preferred file is globally authoritative.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Vedaant-Rajoo/clai/internal/configroot"
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

type credentialFilesystemCheckpoint struct {
	AppPaths      []string
	ReleasePaths  []string
	PreferredPath string
	LegacyPath    string
	TempName      string
}

// filesystemHooks are deterministic test checkpoints. Production leaves every
// hook nil. Hooks run while the participating cross-process credential locks
// are held.
type filesystemHooks struct {
	afterBaseValidationBeforeAppOpen        func(base, app string) error
	beforeLockAcquire                       func(dir, path string) error
	afterLockAcquired                       func(dir, path string) error
	afterOrderedLocksAcquired               func(credentialFilesystemCheckpoint) error
	afterFileValidationBeforeRead           func(dir, path string) error
	afterCredentialReadWhileLocked          func(dir, path string) error
	beforeTempCreation                      func(dir, path string) error
	afterTempCreation                       func(dir, path, tempName string) error
	afterRenameBeforeVerification           func(dir, path string) error
	afterPreferredInstallBeforeLegacyUnlink func(credentialFileLocations) error
	unlinkTemp                              func(dirfd int, name string) error
	unlockLock                              func(fd int) error
	closeLock                               func(file *os.File) error
	closeCredential                         func(file *os.File) error
	closeTemp                               func(file *os.File) error
	closeAppDirectory                       func(file *os.File) error
	closeBaseDirectory                      func(file *os.File) error
	syncDirectory                           func(fd int) error
}

var (
	credentialKeyring         keyringStore = systemKeyring{}
	resolveConfigRoots                     = configroot.Resolve
	credentialFilesystemHooks filesystemHooks
)

type credentialFileLocations struct {
	preferred string
	legacy    string
}

// backendError keeps backend failures classifiable with errors.Is while
// preventing an untrusted keyring implementation from reflecting credential
// material through its error string.
type backendError struct {
	operation string
	cause     error
}

func (e backendError) Error() string { return e.operation }
func (e backendError) Unwrap() error { return e.cause }

func secretFreeBackendError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return backendError{operation: operation, cause: err}
}

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
// explicit (flag) > env var > keyring > preferred config file. Returns "" with
// nil error when no credential is configured anywhere.
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
			secretFreeBackendError("read credential from keyring", keyringErr),
			fmt.Errorf("read credential from config file: %w", fileErr),
		)
	}
	if key != "" {
		return key, nil
	}
	if !errors.Is(keyringErr, keyring.ErrNotFound) {
		return "", secretFreeBackendError("read credential from keyring", keyringErr)
	}
	return "", nil
}

// Store persists an API key, preferring the OS keyring and falling back to a
// private config file when the keyring is unavailable. A successful keyring
// store removes the provider from both file locations before returning.
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

// Delete removes the stored key from both keyring and reconciled file storage.
// A missing keyring entry is not an error, but operational errors are returned
// after file cleanup is attempted independently.
func Delete(provider string) error {
	keyringErr := credentialKeyring.Delete(serviceName, provider)
	if errors.Is(keyringErr, keyring.ErrNotFound) {
		keyringErr = nil
	} else if keyringErr != nil {
		keyringErr = secretFreeBackendError("delete credential from keyring", keyringErr)
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
// preserving storage validation and classifiable keyring errors.
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
			secretFreeBackendError("inspect credential keyring", keyringErr),
			fmt.Errorf("inspect credential config file: %w", fileErr),
		)
	}
	if key != "" {
		return "config file", nil
	}
	if !errors.Is(keyringErr, keyring.ErrNotFound) {
		return "", secretFreeBackendError("inspect credential keyring", keyringErr)
	}
	return "none", nil
}

func resolveCredentialFileLocations(createPreferred bool) (credentialFileLocations, error) {
	roots, err := resolveConfigRoots()
	if err != nil {
		return credentialFileLocations{}, fmt.Errorf("resolve config roots: %w", err)
	}
	preferred, err := canonicalizeCredentialRoot(roots.Preferred, createPreferred)
	if err != nil {
		return credentialFileLocations{}, fmt.Errorf("canonicalize preferred credential root: %w", err)
	}
	legacy := ""
	if roots.Legacy != "" {
		legacy, err = canonicalizeCredentialRoot(roots.Legacy, false)
		if err != nil {
			return credentialFileLocations{}, fmt.Errorf("canonicalize legacy credential root: %w", err)
		}
	}
	canonical := configroot.Roots{Preferred: preferred, Legacy: legacy}
	return credentialFileLocations{
		preferred: canonical.PreferredPath(fileName),
		legacy:    canonical.LegacyPath(fileName),
	}, nil
}

func canonicalizeCredentialFileLocations(selected credentialFileLocations, createPreferred bool) (credentialFileLocations, error) {
	preferredRoot := credentialRoot(selected.preferred)
	preferred, err := canonicalizeCredentialRoot(preferredRoot, createPreferred)
	if err != nil {
		return credentialFileLocations{}, fmt.Errorf("canonicalize preferred credential root: %w", err)
	}
	legacy := ""
	if selected.legacy != "" {
		legacyRoot := credentialRoot(selected.legacy)
		legacy, err = canonicalizeCredentialRoot(legacyRoot, false)
		if err != nil {
			return credentialFileLocations{}, fmt.Errorf("canonicalize legacy credential root: %w", err)
		}
	}
	canonical := configroot.Roots{Preferred: preferred, Legacy: legacy}
	return credentialFileLocations{
		preferred: canonical.PreferredPath(fileName),
		legacy:    canonical.LegacyPath(fileName),
	}, nil
}

// canonicalizeCredentialRoot permits a symlink only at the selected root
// itself. Parent components remain outside that one-time authority grant so an
// intermediate link cannot silently redirect credential storage.
func canonicalizeCredentialRoot(path string, create bool) (string, error) {
	if err := validateCredentialRootParents(path); err != nil {
		return "", err
	}
	return configroot.Canonicalize(path, create)
}

func validateCredentialRootParents(path string) error {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		// configroot.Canonicalize owns the syntax diagnostic for relative roots.
		return nil
	}

	current := filepath.Dir(clean)
	for {
		_, err := os.Lstat(current)
		if err == nil {
			canonical, err := filepath.EvalSymlinks(current)
			if err != nil {
				return fmt.Errorf("securely traverse credential root parent: %w", err)
			}
			if filepath.Clean(canonical) != current {
				return errors.New("securely traverse credential root parent: intermediate symlinks are not allowed")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("securely traverse credential root parent: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func credentialRoot(path string) string {
	return filepath.Dir(filepath.Dir(path))
}

func readFile(provider string) (string, error) {
	locations, err := resolveCredentialFileLocations(false)
	if err != nil {
		return "", err
	}
	return secureReadCredentialFiles(locations, provider)
}

func writeFile(provider, key string) error {
	locations, err := resolveCredentialFileLocations(true)
	if err != nil {
		return err
	}
	return secureMutateCredentialFiles(locations, true, func(creds map[string]string) bool {
		creds[provider] = key
		return true
	})
}

func deleteFileEntry(provider string) error {
	locations, err := resolveCredentialFileLocations(false)
	if err != nil {
		return err
	}
	return secureMutateCredentialFiles(locations, false, func(creds map[string]string) bool {
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
