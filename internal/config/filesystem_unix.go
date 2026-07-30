//go:build darwin || linux

// Hardened config.json storage keeps root selection in configroot while all
// clai descendants remain descriptor-relative and no-follow. Migration and
// ordinary writes acquire every participating .config.lock in ascending
// canonical app-path order, install a verified 0600 preferred file atomically,
// and only then retire the legacy Darwin copy.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	lockName          = ".config.lock"
	configLockTimeout = 2 * time.Second
	configLockPoll    = 10 * time.Millisecond
)

type configDirectory struct {
	basePath string
	appPath  string
	base     *os.File
	app      *os.File
	baseInfo os.FileInfo
	appInfo  os.FileInfo
}

type configLock struct {
	directory *configDirectory
	file      *os.File
	info      os.FileInfo
}

type configFilesystemCheckpoint struct {
	AppPaths      []string
	ReleasePaths  []string
	PreferredPath string
	LegacyPath    string
	TempName      string
}

type configFilesystemHooks struct {
	afterOrderedLocksAcquired               func(configFilesystemCheckpoint) error
	afterPreferredInstallBeforeLegacyUnlink func(configFileLocations) error
}

var configHooks configFilesystemHooks

func migrateConfigFile(locations configFileLocations) (retErr error) {
	directories, preferred, legacy, err := openConfigDirectories(locations)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, closeConfigDirectories(directories)) }()

	locks, err := acquireOrderedConfigLocks(directories)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, releaseOrderedConfigLocks(locks)) }()

	if err := verifyLockedConfigState(directories, locks); err != nil {
		return err
	}
	preferredFile, preferredInfo, preferredExists, err := openConfigFile(preferred.app)
	if err != nil {
		return err
	}
	if preferredExists {
		if err := closeConfigFile(preferredFile); err != nil {
			return err
		}
		legacyInfo, legacyExists, err := inspectConfigFile(legacy)
		if err != nil {
			return err
		}
		if err := runOrderedLockCheckpoint(locks, locations, ""); err != nil {
			return err
		}
		if err := verifyLockedConfigState(directories, locks); err != nil {
			return err
		}
		if err := verifyNamedConfigFile(preferred.app, fileName, preferredInfo, false); err != nil {
			return fmt.Errorf("preferred config file changed during reconciliation: %w", err)
		}
		if !legacyExists {
			return nil
		}
		return removeConfigFile(legacy, legacyInfo, locks)
	}

	if legacy == nil {
		if err := runOrderedLockCheckpoint(locks, locations, ""); err != nil {
			return err
		}
		return ensureConfigFileAbsent(preferred.app)
	}
	legacyFile, legacyInfo, legacyExists, err := openConfigFile(legacy.app)
	if err != nil {
		return err
	}
	if !legacyExists {
		if err := runOrderedLockCheckpoint(locks, locations, ""); err != nil {
			return err
		}
		if err := verifyLockedConfigState(directories, locks); err != nil {
			return err
		}
		return ensureConfigFileAbsent(preferred.app)
	}
	if err := verifyLockedConfigState(directories, locks); err != nil {
		return errors.Join(err, closeConfigFile(legacyFile))
	}
	if err := verifyNamedConfigFile(legacy.app, fileName, legacyInfo, false); err != nil {
		return errors.Join(fmt.Errorf("legacy config file changed before read: %w", err), closeConfigFile(legacyFile))
	}
	data, readErr := io.ReadAll(legacyFile)
	closeErr := closeConfigFile(legacyFile)
	if readErr != nil {
		return errors.Join(fmt.Errorf("read legacy config file: %w", readErr), closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	cfg, err := decodeConfig(locations.legacy, data)
	if err != nil {
		return err
	}
	out, err := marshalConfig(cfg)
	if err != nil {
		return err
	}
	if err := runOrderedLockCheckpoint(locks, locations, ""); err != nil {
		return err
	}
	if err := verifyLockedConfigState(directories, locks); err != nil {
		return err
	}
	if err := ensureConfigFileAbsent(preferred.app); err != nil {
		return err
	}
	if err := verifyNamedConfigFile(legacy.app, fileName, legacyInfo, false); err != nil {
		return fmt.Errorf("legacy config file changed before migration: %w", err)
	}
	if err := installConfigFile(preferred, out, nil, false, locks, locations, false, nil); err != nil {
		return err
	}
	if hook := configHooks.afterPreferredInstallBeforeLegacyUnlink; hook != nil {
		if err := hook(locations); err != nil {
			return fmt.Errorf("run post-install config checkpoint: %w", err)
		}
	}
	return removeConfigFile(legacy, legacyInfo, locks)
}

func readConfigFile(locations configFileLocations) (data []byte, exists bool, retErr error) {
	basePath := filepath.Dir(filepath.Dir(locations.preferred))
	directory, err := openConfigDirectory(basePath, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer func() { retErr = errors.Join(retErr, directory.close()) }()

	lock, err := acquireConfigLock(directory)
	if err != nil {
		return nil, false, err
	}
	defer func() { retErr = errors.Join(retErr, releaseConfigLock(lock)) }()

	file, info, found, err := openConfigFile(directory.app)
	if err != nil || !found {
		return nil, found, err
	}
	if err := directory.verify(); err != nil {
		return nil, false, errors.Join(err, closeConfigFile(file))
	}
	if err := verifyConfigLock(lock); err != nil {
		return nil, false, errors.Join(err, closeConfigFile(file))
	}
	if err := verifyNamedConfigFile(directory.app, fileName, info, false); err != nil {
		return nil, false, errors.Join(fmt.Errorf("config file changed before read: %w", err), closeConfigFile(file))
	}
	data, readErr := io.ReadAll(file)
	closeErr := closeConfigFile(file)
	if readErr != nil {
		return nil, false, fmt.Errorf("read config file %q: %w", locations.preferred, readErr)
	}
	if closeErr != nil {
		return nil, false, closeErr
	}
	return data, true, nil
}

func writeConfigFile(locations configFileLocations, data []byte) (retErr error) {
	directories, preferred, legacy, err := openConfigDirectories(locations)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, closeConfigDirectories(directories)) }()

	locks, err := acquireOrderedConfigLocks(directories)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, releaseOrderedConfigLocks(locks)) }()

	if err := verifyLockedConfigState(directories, locks); err != nil {
		return err
	}
	currentInfo, currentExists, err := inspectConfigFile(preferred)
	if err != nil {
		return err
	}
	legacyInfo, legacyExists, err := inspectConfigFile(legacy)
	if err != nil {
		return err
	}
	verifyCapturedState := func() error {
		if err := verifyLockedConfigState(directories, locks); err != nil {
			return err
		}
		if legacyExists {
			if err := verifyNamedConfigFile(legacy.app, fileName, legacyInfo, false); err != nil {
				return fmt.Errorf("legacy config file changed before preferred replace: %w", err)
			}
		} else if legacy != nil {
			if err := ensureConfigFileAbsent(legacy.app); err != nil {
				return fmt.Errorf("legacy config file appeared before preferred replace: %w", err)
			}
		}
		return nil
	}

	if err := installConfigFile(preferred, data, currentInfo, currentExists, locks, locations, true, verifyCapturedState); err != nil {
		return err
	}
	if !legacyExists {
		return nil
	}
	if hook := configHooks.afterPreferredInstallBeforeLegacyUnlink; hook != nil {
		if err := hook(locations); err != nil {
			return fmt.Errorf("run post-install config checkpoint: %w", err)
		}
	}
	return removeConfigFile(legacy, legacyInfo, locks)
}

func openConfigDirectories(locations configFileLocations) ([]*configDirectory, *configDirectory, *configDirectory, error) {
	preferredBase := filepath.Dir(filepath.Dir(locations.preferred))
	preferred, err := openConfigDirectory(preferredBase, true)
	if err != nil {
		return nil, nil, nil, err
	}
	directories := []*configDirectory{preferred}

	var legacy *configDirectory
	if locations.legacy != "" {
		legacyBase := filepath.Dir(filepath.Dir(locations.legacy))
		legacyApp := filepath.Dir(locations.legacy)
		if legacyApp != preferred.appPath {
			legacy, err = openConfigDirectory(legacyBase, false)
			if errors.Is(err, os.ErrNotExist) {
				legacy = nil
			} else if err != nil {
				return nil, nil, nil, errors.Join(err, closeConfigDirectories(directories))
			} else {
				directories = append(directories, legacy)
			}
		}
	}

	sort.Slice(directories, func(i, j int) bool {
		return directories[i].appPath < directories[j].appPath
	})
	return directories, preferred, legacy, nil
}

func openConfigDirectory(basePath string, create bool) (*configDirectory, error) {
	base, baseInfo, err := openConfiguredDirectory(basePath)
	if err != nil {
		return nil, err
	}
	appPath := filepath.Join(basePath, "clai")
	result := &configDirectory{
		basePath: basePath,
		appPath:  appPath,
		base:     base,
		baseInfo: baseInfo,
	}

	created := false
	if create {
		err := unix.Mkdirat(int(base.Fd()), "clai", 0o700)
		if err == nil {
			created = true
		} else if !errors.Is(err, unix.EEXIST) {
			return nil, errors.Join(fmt.Errorf("create config directory: %w", err), result.close())
		}
	}
	fd, err := unix.Openat(int(base.Fd()), "clai", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, errors.Join(os.ErrNotExist, result.close())
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open config directory: %w", err), result.close())
	}
	result.app = os.NewFile(uintptr(fd), appPath)
	if created {
		if err := result.app.Chmod(0o700); err != nil {
			return nil, errors.Join(fmt.Errorf("set config directory permissions: %w", err), result.close())
		}
	}
	result.appInfo, err = result.app.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect config directory: %w", err), result.close())
	}
	if !result.appInfo.IsDir() || result.appInfo.Mode().Perm()&0o077 != 0 {
		return nil, errors.Join(errors.New("config directory must be a private non-symlink directory"), result.close())
	}
	if err := result.verify(); err != nil {
		return nil, errors.Join(err, result.close())
	}
	return result, nil
}

func openConfiguredDirectory(path string) (*os.File, os.FileInfo, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, nil, errors.New("config root must be absolute")
	}
	root := string(filepath.Separator)
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open filesystem root: %w", err)
	}
	current := os.NewFile(uintptr(fd), root)
	trimmed := strings.TrimPrefix(clean, root)
	if trimmed != "" {
		for _, component := range strings.Split(trimmed, string(filepath.Separator)) {
			if component == "" {
				continue
			}
			nextFD, openErr := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if openErr != nil {
				closeErr := current.Close()
				if errors.Is(openErr, unix.ENOENT) {
					return nil, nil, errors.Join(os.ErrNotExist, closeErr)
				}
				return nil, nil, errors.Join(fmt.Errorf("securely traverse config root: %w", openErr), closeErr)
			}
			next := os.NewFile(uintptr(nextFD), component)
			if closeErr := current.Close(); closeErr != nil {
				return nil, nil, errors.Join(fmt.Errorf("close traversed config root: %w", closeErr), next.Close())
			}
			current = next
		}
	}
	info, err := current.Stat()
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("inspect config root: %w", err), current.Close())
	}
	if !info.IsDir() {
		return nil, nil, errors.Join(errors.New("config root must be a non-symlink directory"), current.Close())
	}
	return current, info, nil
}

func (directory *configDirectory) verify() error {
	base, baseInfo, err := openConfiguredDirectory(directory.basePath)
	if err != nil {
		return fmt.Errorf("verify config root: %w", err)
	}
	if !os.SameFile(directory.baseInfo, baseInfo) {
		return errors.Join(errors.New("config root path changed during operation"), base.Close())
	}
	fd, err := unix.Openat(int(base.Fd()), "clai", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	baseCloseErr := base.Close()
	if err != nil {
		return fmt.Errorf("verify config directory path: %w", errors.Join(err, baseCloseErr))
	}
	app := os.NewFile(uintptr(fd), directory.appPath)
	info, statErr := app.Stat()
	closeErr := app.Close()
	if statErr != nil || closeErr != nil || baseCloseErr != nil {
		return fmt.Errorf("verify config directory: %w", errors.Join(statErr, closeErr, baseCloseErr))
	}
	if !info.IsDir() || !os.SameFile(directory.appInfo, info) {
		return errors.New("config directory path changed during operation")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("config directory permissions allow group or other access")
	}
	return nil
}

func (directory *configDirectory) close() error {
	var appErr, baseErr error
	if directory.app != nil {
		if err := directory.app.Close(); err != nil {
			appErr = fmt.Errorf("close config directory: %w", err)
		}
		directory.app = nil
	}
	if directory.base != nil {
		if err := directory.base.Close(); err != nil {
			baseErr = fmt.Errorf("close config root: %w", err)
		}
		directory.base = nil
	}
	return errors.Join(appErr, baseErr)
}

func closeConfigDirectories(directories []*configDirectory) error {
	var result error
	for i := len(directories) - 1; i >= 0; i-- {
		result = errors.Join(result, directories[i].close())
	}
	return result
}

func acquireOrderedConfigLocks(directories []*configDirectory) ([]*configLock, error) {
	locks := make([]*configLock, 0, len(directories))
	for _, directory := range directories {
		lock, err := acquireConfigLock(directory)
		if err != nil {
			return nil, errors.Join(err, releaseOrderedConfigLocks(locks))
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

func runOrderedLockCheckpoint(locks []*configLock, locations configFileLocations, tempName string) error {
	hook := configHooks.afterOrderedLocksAcquired
	if hook == nil {
		return nil
	}
	checkpoint := configFilesystemCheckpoint{
		AppPaths:      make([]string, 0, len(locks)),
		ReleasePaths:  make([]string, 0, len(locks)),
		PreferredPath: locations.preferred,
		LegacyPath:    locations.legacy,
		TempName:      tempName,
	}
	for _, lock := range locks {
		checkpoint.AppPaths = append(checkpoint.AppPaths, lock.directory.appPath)
	}
	for _, lock := range configLocksInReleaseOrder(locks) {
		checkpoint.ReleasePaths = append(checkpoint.ReleasePaths, lock.directory.appPath)
	}
	if err := hook(checkpoint); err != nil {
		return fmt.Errorf("run ordered config lock checkpoint: %w", err)
	}
	return nil
}

func acquireConfigLock(directory *configDirectory) (*configLock, error) {
	deadline := time.Now().Add(configLockTimeout)
	for {
		fd, err := unix.Openat(int(directory.app.Fd()), lockName, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, unix.ENOENT) {
			if time.Now().After(deadline) {
				return nil, errors.New("timed out acquiring config lock")
			}
			time.Sleep(configLockPoll)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("open config lock: %w", err)
		}
		file := os.NewFile(uintptr(fd), lockName)
		info, err := file.Stat()
		if err != nil {
			return nil, errors.Join(fmt.Errorf("inspect config lock: %w", err), file.Close())
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return nil, errors.Join(errors.New("config lock must be a private regular non-symlink file"), file.Close())
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			current, verifyErr := lockEntryMatches(directory.app, info)
			if verifyErr != nil {
				return nil, errors.Join(verifyErr, file.Close())
			}
			if current {
				lock := &configLock{directory: directory, file: file, info: info}
				if err := verifyConfigLock(lock); err != nil {
					return nil, errors.Join(err, unlockAndCloseConfigLock(file))
				}
				return lock, nil
			}
			if closeErr := file.Close(); closeErr != nil {
				return nil, fmt.Errorf("close stale config lock: %w", closeErr)
			}
		case errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN):
			if closeErr := file.Close(); closeErr != nil {
				return nil, fmt.Errorf("close config lock: %w", closeErr)
			}
		default:
			return nil, errors.Join(fmt.Errorf("lock config file: %w", err), file.Close())
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out acquiring config lock")
		}
		time.Sleep(configLockPoll)
	}
}

func lockEntryMatches(dir *os.File, expected os.FileInfo) (bool, error) {
	fd, err := unix.Openat(int(dir.Fd()), lockName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("verify config lock: %w", err)
	}
	entry := os.NewFile(uintptr(fd), lockName)
	info, statErr := entry.Stat()
	closeErr := entry.Close()
	if statErr != nil || closeErr != nil {
		return false, fmt.Errorf("inspect config lock entry: %w", errors.Join(statErr, closeErr))
	}
	return os.SameFile(expected, info), nil
}

func verifyConfigLock(lock *configLock) error {
	if err := lock.directory.verify(); err != nil {
		return err
	}
	if err := verifyNamedConfigFile(lock.directory.app, lockName, lock.info, true); err != nil {
		return fmt.Errorf("verify config lock: %w", err)
	}
	return nil
}

func verifyLockedConfigState(directories []*configDirectory, locks []*configLock) error {
	for _, directory := range directories {
		if err := directory.verify(); err != nil {
			return err
		}
	}
	for _, lock := range locks {
		if err := verifyConfigLock(lock); err != nil {
			return err
		}
	}
	return nil
}

func configLocksInReleaseOrder(locks []*configLock) []*configLock {
	reversed := make([]*configLock, len(locks))
	for i := range locks {
		reversed[i] = locks[len(locks)-1-i]
	}
	return reversed
}

func releaseOrderedConfigLocks(locks []*configLock) error {
	var result error
	for _, lock := range configLocksInReleaseOrder(locks) {
		result = errors.Join(result, releaseConfigLock(lock))
	}
	return result
}

func releaseConfigLock(lock *configLock) error {
	var verifyErr, unlinkErr, syncErr error
	if err := verifyConfigLock(lock); err != nil {
		verifyErr = err
	} else {
		if err := unix.Unlinkat(int(lock.directory.app.Fd()), lockName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			unlinkErr = fmt.Errorf("remove config lock: %w", err)
		}
		if unlinkErr == nil {
			if err := unix.Fsync(int(lock.directory.app.Fd())); err != nil {
				syncErr = fmt.Errorf("sync config directory after lock removal: %w", err)
			}
		}
	}
	return errors.Join(verifyErr, unlinkErr, syncErr, unlockAndCloseConfigLock(lock.file))
}

func unlockAndCloseConfigLock(file *os.File) error {
	var unlockErr, closeErr error
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		unlockErr = fmt.Errorf("unlock config file: %w", err)
	}
	if err := file.Close(); err != nil {
		closeErr = fmt.Errorf("close config lock: %w", err)
	}
	return errors.Join(unlockErr, closeErr)
}

func openConfigFile(dir *os.File) (*os.File, os.FileInfo, bool, error) {
	fd, err := unix.Openat(int(dir.Fd()), fileName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("open config file: %w", err)
	}
	file := os.NewFile(uintptr(fd), fileName)
	info, err := file.Stat()
	if err != nil {
		return nil, nil, false, errors.Join(fmt.Errorf("inspect config file: %w", err), file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, nil, false, errors.Join(errors.New("config file must be a regular non-symlink file"), file.Close())
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, nil, false, errors.Join(errors.New("config file permissions allow group or other access"), file.Close())
	}
	return file, info, true, nil
}

func closeConfigFile(file *os.File) error {
	if err := file.Close(); err != nil {
		return fmt.Errorf("close config file: %w", err)
	}
	return nil
}

func installConfigFile(
	directory *configDirectory,
	data []byte,
	expected os.FileInfo,
	existed bool,
	locks []*configLock,
	locations configFileLocations,
	runCheckpoint bool,
	verifyAdditional func() error,
) (retErr error) {
	if err := verifyLockedConfigState([]*configDirectory{directory}, locks); err != nil {
		return err
	}
	temp, tempName, tempInfo, err := createConfigTemp(directory.app)
	if err != nil {
		return err
	}
	tempExists := true
	defer func() {
		if tempExists {
			if unlinkErr := unix.Unlinkat(int(directory.app.Fd()), tempName, 0); unlinkErr != nil && !errors.Is(unlinkErr, unix.ENOENT) {
				retErr = errors.Join(retErr, fmt.Errorf("remove config temporary file: %w", unlinkErr))
			}
		}
		if closeErr := temp.Close(); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close config temporary file: %w", closeErr))
		}
	}()

	if runCheckpoint {
		if err := runOrderedLockCheckpoint(locks, locations, tempName); err != nil {
			return err
		}
	}
	if err := verifyConfigInstallState(directory, tempName, tempInfo, expected, existed, locks, verifyAdditional); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write config temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync config temporary file: %w", err)
	}
	if err := verifyConfigInstallState(directory, tempName, tempInfo, expected, existed, locks, verifyAdditional); err != nil {
		return err
	}
	if err := unix.Renameat(int(directory.app.Fd()), tempName, int(directory.app.Fd()), fileName); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}
	tempExists = false
	if err := verifyLockedConfigState([]*configDirectory{directory}, locks); err != nil {
		return err
	}
	if err := verifyNamedConfigFile(directory.app, fileName, tempInfo, false); err != nil {
		return fmt.Errorf("verify replaced config file: %w", err)
	}
	if err := unix.Fsync(int(directory.app.Fd())); err != nil {
		return fmt.Errorf("sync config directory: %w", err)
	}
	return nil
}

func verifyConfigInstallState(
	directory *configDirectory,
	tempName string,
	tempInfo os.FileInfo,
	expected os.FileInfo,
	existed bool,
	locks []*configLock,
	verifyAdditional func() error,
) error {
	if err := verifyLockedConfigState([]*configDirectory{directory}, locks); err != nil {
		return err
	}
	if err := verifyNamedConfigFile(directory.app, tempName, tempInfo, false); err != nil {
		return fmt.Errorf("config temporary file changed before replace: %w", err)
	}
	if existed {
		if err := verifyNamedConfigFile(directory.app, fileName, expected, false); err != nil {
			return fmt.Errorf("config file changed before replace: %w", err)
		}
	} else if err := ensureConfigFileAbsent(directory.app); err != nil {
		return err
	}
	if verifyAdditional != nil {
		if err := verifyAdditional(); err != nil {
			return err
		}
	}
	return nil
}

func inspectConfigFile(directory *configDirectory) (os.FileInfo, bool, error) {
	if directory == nil {
		return nil, false, nil
	}
	file, info, exists, err := openConfigFile(directory.app)
	if err != nil || !exists {
		return info, exists, err
	}
	if err := closeConfigFile(file); err != nil {
		return nil, false, err
	}
	return info, true, nil
}

func ensureConfigFileAbsent(dir *os.File) error {
	file, _, exists, err := openConfigFile(dir)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return errors.Join(errors.New("config file appeared during operation"), closeConfigFile(file))
}

func removeConfigFileIfPresent(directory *configDirectory, locks []*configLock) error {
	file, info, exists, err := openConfigFile(directory.app)
	if err != nil || !exists {
		return err
	}
	if err := closeConfigFile(file); err != nil {
		return err
	}
	return removeConfigFile(directory, info, locks)
}

func removeConfigFile(directory *configDirectory, expected os.FileInfo, locks []*configLock) error {
	if err := verifyLockedConfigState([]*configDirectory{directory}, locks); err != nil {
		return err
	}
	if err := verifyNamedConfigFile(directory.app, fileName, expected, false); err != nil {
		return fmt.Errorf("config file changed before removal: %w", err)
	}
	if err := unix.Unlinkat(int(directory.app.Fd()), fileName, 0); err != nil {
		return fmt.Errorf("remove legacy config file: %w", err)
	}
	if err := unix.Fsync(int(directory.app.Fd())); err != nil {
		return fmt.Errorf("sync legacy config directory: %w", err)
	}
	return nil
}

func verifyNamedConfigFile(dir *os.File, name string, expected os.FileInfo, lock bool) (retErr error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { retErr = errors.Join(retErr, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !os.SameFile(expected, info) {
		return errors.New("file identity changed")
	}
	if info.Mode().Perm()&0o077 != 0 {
		if lock {
			return errors.New("lock permissions allow group or other access")
		}
		return errors.New("file permissions allow group or other access")
	}
	return nil
}

func createConfigTemp(dir *os.File) (*os.File, string, os.FileInfo, error) {
	for range 32 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", nil, fmt.Errorf("generate config temporary name: %w", err)
		}
		name := ".config-" + hex.EncodeToString(random[:]) + ".tmp"
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", nil, fmt.Errorf("create config temporary file: %w", err)
		}
		file := os.NewFile(uintptr(fd), name)
		if err := file.Chmod(0o600); err != nil {
			return nil, "", nil, errors.Join(fmt.Errorf("set config temporary permissions: %w", err), cleanupConfigTemp(file, dir, name))
		}
		info, err := file.Stat()
		if err != nil {
			return nil, "", nil, errors.Join(fmt.Errorf("inspect config temporary file: %w", err), cleanupConfigTemp(file, dir, name))
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return nil, "", nil, errors.Join(errors.New("created config temporary is not a private regular file"), cleanupConfigTemp(file, dir, name))
		}
		return file, name, info, nil
	}
	return nil, "", nil, errors.New("could not allocate config temporary file")
}

func cleanupConfigTemp(file *os.File, dir *os.File, name string) error {
	closeErr := file.Close()
	unlinkErr := unix.Unlinkat(int(dir.Fd()), name, 0)
	if errors.Is(unlinkErr, unix.ENOENT) {
		unlinkErr = nil
	}
	return errors.Join(closeErr, unlinkErr)
}
