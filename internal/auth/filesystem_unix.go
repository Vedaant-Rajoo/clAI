//go:build darwin || linux

package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	credentialLockTimeout = 2 * time.Second
	credentialLockPoll    = 10 * time.Millisecond
)

type credentialDirectory struct {
	basePath string
	appPath  string
	base     *os.File
	app      *os.File
	baseInfo os.FileInfo
	appInfo  os.FileInfo
}

type credentialLock struct {
	directory *credentialDirectory
	file      *os.File
	info      os.FileInfo
}

type credentialFileSnapshot struct {
	directory *credentialDirectory
	path      string
	file      *os.File
	info      os.FileInfo
	exists    bool
}

type credentialOperation struct {
	directories                  []*credentialDirectory
	locks                        []*credentialLock
	snapshots                    []*credentialFileSnapshot
	tempDirectory                *credentialDirectory
	temp                         *os.File
	tempName                     string
	tempExists                   bool
	installed                    bool
	preserveInstalledOnBodyError bool
}

func secureReadCredentialFiles(locations credentialFileLocations, provider string) (string, error) {
	credentials, err := secureCredentialFileOperation(locations, false, nil)
	if err != nil {
		return "", err
	}
	return credentials[provider], nil
}

func secureMutateCredentialFiles(locations credentialFileLocations, create bool, mutate func(map[string]string) bool) error {
	_, err := secureCredentialFileOperation(locations, create, mutate)
	return err
}

func prepareCredentialFileOperation(locations credentialFileLocations, create bool) (credentialFileLocations, bool, error) {
	if create {
		canonical, err := canonicalizeCredentialFileLocations(locations, true)
		return canonical, err == nil, err
	}
	exists, err := credentialStorageStateExists(locations)
	if err != nil || !exists {
		return locations, exists, err
	}
	canonical, err := canonicalizeCredentialFileLocations(locations, true)
	if err != nil {
		return credentialFileLocations{}, false, err
	}
	return canonical, true, nil
}

func credentialStorageStateExists(locations credentialFileLocations) (bool, error) {
	seen := map[string]bool{}
	for _, path := range []string{locations.preferred, locations.legacy} {
		if path == "" {
			continue
		}
		appPath := filepath.Dir(path)
		if seen[appPath] {
			continue
		}
		seen[appPath] = true
		directory, err := openCredentialDirectory(appPath, false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		names, listErr := readCredentialDirectoryNames(directory.app)
		closeErr := directory.close()
		if listErr != nil || closeErr != nil {
			return false, errors.Join(listErr, closeErr)
		}
		for _, name := range names {
			if name == fileName || isCredentialTransientName(name) {
				return true, nil
			}
		}
	}
	return false, nil
}

func readCredentialDirectoryNames(dir *os.File) ([]string, error) {
	fd, err := unix.Dup(int(dir.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate credential directory descriptor: %w", err)
	}
	duplicate := os.NewFile(uintptr(fd), "credential-directory-listing")
	names, readErr := duplicate.Readdirnames(-1)
	closeErr := duplicate.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("list credential directory: %w", errors.Join(readErr, closeErr))
	}
	return names, nil
}

func isCredentialTransientName(name string) bool {
	if !strings.HasPrefix(name, ".credentials-") || !strings.HasSuffix(name, ".tmp") {
		return false
	}
	hexPart := strings.TrimSuffix(strings.TrimPrefix(name, ".credentials-"), ".tmp")
	if len(hexPart) != 24 {
		return false
	}
	_, err := hex.DecodeString(hexPart)
	return err == nil
}

func secureCredentialFileOperation(
	locations credentialFileLocations,
	create bool,
	mutate func(map[string]string) bool,
) (credentials map[string]string, retErr error) {
	locations, active, err := prepareCredentialFileOperation(locations, create)
	if err != nil {
		return nil, err
	}
	if !active {
		return map[string]string{}, nil
	}

	operation := &credentialOperation{}
	defer func() { retErr = operation.finalize(retErr) }()

	var preferred, legacy *credentialDirectory
	operation.directories, preferred, legacy, err = openCredentialDirectories(locations)
	if err != nil {
		return nil, err
	}
	operation.locks, err = acquireOrderedCredentialLocks(operation.directories)
	if err != nil {
		return nil, err
	}
	if err := runOrderedCredentialLockCheckpoint(operation.locks, locations, ""); err != nil {
		return nil, err
	}
	if err := verifyLockedCredentialState(operation.directories, operation.locks); err != nil {
		return nil, err
	}

	preferredSnapshot, err := operation.snapshotCredentialFile(preferred, locations.preferred)
	if err != nil {
		return nil, err
	}
	legacySnapshot, err := operation.snapshotCredentialFile(legacy, locations.legacy)
	if err != nil {
		return nil, err
	}

	if preferredSnapshot.exists {
		if legacySnapshot.exists {
			if err := removeCredentialSnapshot(legacySnapshot, operation.locks); err != nil {
				return nil, err
			}
		} else if legacy != nil {
			if err := ensureCredentialFileAbsent(legacy.app); err != nil {
				return nil, err
			}
		}
		credentials, err = readCredentialSnapshot(preferredSnapshot, operation.directories, operation.locks)
		if err != nil {
			return nil, err
		}
		if err := runCredentialReadCheckpoint(preferredSnapshot); err != nil {
			return nil, err
		}
		if mutate == nil || !mutate(credentials) {
			return credentials, nil
		}
		if err := installCredentialFile(operation, preferred, locations, credentials, preferredSnapshot.info, true, legacySnapshot); err != nil {
			return nil, err
		}
		return credentials, nil
	}

	credentials = map[string]string{}
	if legacySnapshot.exists {
		credentials, err = readCredentialSnapshot(legacySnapshot, operation.directories, operation.locks)
		if err != nil {
			return nil, err
		}
	}
	checkpointSnapshot := legacySnapshot
	if !legacySnapshot.exists {
		checkpointSnapshot = preferredSnapshot
	}
	if err := runCredentialReadCheckpoint(checkpointSnapshot); err != nil {
		return nil, err
	}
	changed := false
	if mutate != nil {
		changed = mutate(credentials)
	}
	if !legacySnapshot.exists && !changed {
		return credentials, nil
	}
	if err := installCredentialFile(operation, preferred, locations, credentials, nil, false, legacySnapshot); err != nil {
		return nil, err
	}
	if !legacySnapshot.exists {
		return credentials, nil
	}

	// At this point the preferred inode has been verified and its directory
	// synced. A deterministic interruption now leaves a valid preferred file and
	// the legacy source; retry observes preferred authority and finishes cleanup.
	operation.preserveInstalledOnBodyError = true
	if hook := credentialFilesystemHooks.afterPreferredInstallBeforeLegacyUnlink; hook != nil {
		if err := hook(locations); err != nil {
			return nil, fmt.Errorf("run post-install credential checkpoint: %w", err)
		}
	}
	if err := removeCredentialSnapshot(legacySnapshot, operation.locks); err != nil {
		return nil, err
	}
	return credentials, nil
}

func openCredentialDirectories(locations credentialFileLocations) ([]*credentialDirectory, *credentialDirectory, *credentialDirectory, error) {
	preferred, err := openCredentialDirectory(filepath.Dir(locations.preferred), true)
	if err != nil {
		return nil, nil, nil, err
	}
	directories := []*credentialDirectory{preferred}

	var legacy *credentialDirectory
	if locations.legacy != "" {
		legacyApp := filepath.Dir(locations.legacy)
		if legacyApp != preferred.appPath {
			legacy, err = openCredentialDirectory(legacyApp, false)
			if errors.Is(err, os.ErrNotExist) {
				legacy = nil
			} else if err != nil {
				return nil, nil, nil, errors.Join(err, closeCredentialDirectories(directories))
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

func closeCredentialDirectories(directories []*credentialDirectory) error {
	var result error
	for i := len(directories) - 1; i >= 0; i-- {
		result = errors.Join(result, directories[i].close())
	}
	return result
}

func acquireOrderedCredentialLocks(directories []*credentialDirectory) ([]*credentialLock, error) {
	locks := make([]*credentialLock, 0, len(directories))
	for _, directory := range directories {
		path := filepath.Join(directory.appPath, fileName)
		if hook := credentialFilesystemHooks.beforeLockAcquire; hook != nil {
			if err := hook(directory.appPath, path); err != nil {
				return nil, errors.Join(err, releaseOrderedCredentialLocks(locks))
			}
		}
		file, info, err := acquireCredentialLock(directory.app)
		if err != nil {
			return nil, errors.Join(err, releaseOrderedCredentialLocks(locks))
		}
		lock := &credentialLock{directory: directory, file: file, info: info}
		locks = append(locks, lock)
		if hook := credentialFilesystemHooks.afterLockAcquired; hook != nil {
			if err := hook(directory.appPath, path); err != nil {
				return nil, errors.Join(err, releaseOrderedCredentialLocks(locks))
			}
		}
		if err := verifyCredentialLock(lock); err != nil {
			return nil, errors.Join(err, releaseOrderedCredentialLocks(locks))
		}
	}
	return locks, nil
}

func releaseOrderedCredentialLocks(locks []*credentialLock) error {
	var result error
	for i := len(locks) - 1; i >= 0; i-- {
		if locks[i].file != nil {
			result = errors.Join(result, releaseCredentialLock(locks[i].file))
			locks[i].file = nil
		}
	}
	return result
}

func runOrderedCredentialLockCheckpoint(locks []*credentialLock, locations credentialFileLocations, tempName string) error {
	hook := credentialFilesystemHooks.afterOrderedLocksAcquired
	if hook == nil {
		return nil
	}
	checkpoint := credentialFilesystemCheckpoint{
		AppPaths:      make([]string, 0, len(locks)),
		ReleasePaths:  make([]string, 0, len(locks)),
		PreferredPath: locations.preferred,
		LegacyPath:    locations.legacy,
		TempName:      tempName,
	}
	for _, lock := range locks {
		checkpoint.AppPaths = append(checkpoint.AppPaths, lock.directory.appPath)
	}
	for i := len(locks) - 1; i >= 0; i-- {
		checkpoint.ReleasePaths = append(checkpoint.ReleasePaths, locks[i].directory.appPath)
	}
	if err := hook(checkpoint); err != nil {
		return fmt.Errorf("run ordered credential lock checkpoint: %w", err)
	}
	return nil
}

func verifyCredentialLock(lock *credentialLock) error {
	if err := lock.directory.verify(); err != nil {
		return err
	}
	if err := verifyNamedFile(lock.directory.app, lockName, lock.info, true); err != nil {
		return fmt.Errorf("verify credential lock: %w", err)
	}
	return nil
}

func verifyLockedCredentialState(directories []*credentialDirectory, locks []*credentialLock) error {
	for _, directory := range directories {
		if err := directory.verify(); err != nil {
			return err
		}
	}
	for _, lock := range locks {
		if err := verifyCredentialLock(lock); err != nil {
			return err
		}
	}
	return nil
}

func (operation *credentialOperation) snapshotCredentialFile(directory *credentialDirectory, path string) (*credentialFileSnapshot, error) {
	snapshot := &credentialFileSnapshot{directory: directory, path: path}
	operation.snapshots = append(operation.snapshots, snapshot)
	if directory == nil {
		return snapshot, nil
	}
	file, info, exists, err := openCredentialFile(directory.app)
	if err != nil {
		return nil, err
	}
	snapshot.file = file
	snapshot.info = info
	snapshot.exists = exists
	if exists {
		if err := verifyCredentialLinkCount(file, 1); err != nil {
			return nil, errors.Join(errors.New("credential file must have exactly one link"), err)
		}
	}
	return snapshot, nil
}

func readCredentialSnapshot(
	snapshot *credentialFileSnapshot,
	directories []*credentialDirectory,
	locks []*credentialLock,
) (map[string]string, error) {
	if !snapshot.exists {
		return map[string]string{}, nil
	}
	if hook := credentialFilesystemHooks.afterFileValidationBeforeRead; hook != nil {
		if err := hook(snapshot.directory.appPath, snapshot.path); err != nil {
			return nil, err
		}
	}
	if err := verifyLockedCredentialState(directories, locks); err != nil {
		return nil, err
	}
	if err := verifyNamedFile(snapshot.directory.app, fileName, snapshot.info, false); err != nil {
		return nil, fmt.Errorf("credential file changed before read: %w", err)
	}
	if _, err := snapshot.file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek credential file: %w", err)
	}
	data, err := io.ReadAll(snapshot.file)
	if err != nil {
		return nil, fmt.Errorf("read credential file: %w", err)
	}
	return decodeCredentials(snapshot.path, data)
}

func runCredentialReadCheckpoint(snapshot *credentialFileSnapshot) error {
	hook := credentialFilesystemHooks.afterCredentialReadWhileLocked
	if hook == nil {
		return nil
	}
	dir := filepath.Dir(snapshot.path)
	if snapshot.directory != nil {
		dir = snapshot.directory.appPath
	}
	if err := hook(dir, snapshot.path); err != nil {
		return err
	}
	return nil
}

func installCredentialFile(
	operation *credentialOperation,
	directory *credentialDirectory,
	locations credentialFileLocations,
	credentials map[string]string,
	currentInfo os.FileInfo,
	currentExists bool,
	legacySnapshot *credentialFileSnapshot,
) error {
	out, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credential config: %w", err)
	}
	if hook := credentialFilesystemHooks.beforeTempCreation; hook != nil {
		if err := hook(directory.appPath, locations.preferred); err != nil {
			return err
		}
	}
	if err := verifyCredentialInstallState(directory, "", nil, currentInfo, currentExists, operation.locks, legacySnapshot); err != nil {
		return err
	}

	operation.tempDirectory = directory
	var tempInfo os.FileInfo
	operation.temp, operation.tempName, tempInfo, err = createCredentialTemp(directory.app)
	if err != nil {
		return err
	}
	operation.tempExists = true
	if _, err := operation.temp.Write(out); err != nil {
		return fmt.Errorf("write credential temporary file: %w", err)
	}
	if err := operation.temp.Sync(); err != nil {
		return fmt.Errorf("sync credential temporary file: %w", err)
	}
	if hook := credentialFilesystemHooks.afterTempCreation; hook != nil {
		if err := hook(directory.appPath, locations.preferred, operation.tempName); err != nil {
			return err
		}
	}
	if err := verifyCredentialInstallState(directory, operation.tempName, tempInfo, currentInfo, currentExists, operation.locks, legacySnapshot); err != nil {
		return err
	}
	if err := runOrderedCredentialLockCheckpoint(operation.locks, locations, operation.tempName); err != nil {
		return err
	}
	if err := verifyCredentialInstallState(directory, operation.tempName, tempInfo, currentInfo, currentExists, operation.locks, legacySnapshot); err != nil {
		return err
	}

	if err := unix.Renameat(int(directory.app.Fd()), operation.tempName, int(directory.app.Fd()), fileName); err != nil {
		return fmt.Errorf("replace credential file: %w", err)
	}
	operation.tempExists = false
	operation.installed = true
	if hook := credentialFilesystemHooks.afterRenameBeforeVerification; hook != nil {
		if err := hook(directory.appPath, locations.preferred); err != nil {
			return err
		}
	}
	if err := verifyLockedCredentialState([]*credentialDirectory{directory}, operation.locks); err != nil {
		return err
	}
	if err := verifyNamedFile(directory.app, fileName, tempInfo, false); err != nil {
		return fmt.Errorf("verify replaced credential file: %w", err)
	}
	if err := verifyCredentialLinkCount(operation.temp, 1); err != nil {
		return fmt.Errorf("verify replaced credential link count: %w", err)
	}
	if err := syncCredentialDirectory(int(directory.app.Fd())); err != nil {
		return fmt.Errorf("sync credential directory: %w", err)
	}
	return nil
}

func verifyCredentialInstallState(
	directory *credentialDirectory,
	tempName string,
	tempInfo os.FileInfo,
	currentInfo os.FileInfo,
	currentExists bool,
	locks []*credentialLock,
	legacySnapshot *credentialFileSnapshot,
) error {
	if err := verifyLockedCredentialState([]*credentialDirectory{directory}, locks); err != nil {
		return err
	}
	if tempName != "" {
		if err := verifyNamedFile(directory.app, tempName, tempInfo, false); err != nil {
			return fmt.Errorf("credential temporary file changed before replace: %w", err)
		}
	}
	if currentExists {
		if err := verifyNamedFile(directory.app, fileName, currentInfo, false); err != nil {
			return fmt.Errorf("credential file changed before replace: %w", err)
		}
	} else if err := ensureCredentialFileAbsent(directory.app); err != nil {
		return err
	}
	if legacySnapshot != nil && legacySnapshot.directory != nil {
		if legacySnapshot.exists {
			if err := verifyNamedFile(legacySnapshot.directory.app, fileName, legacySnapshot.info, false); err != nil {
				return fmt.Errorf("legacy credential file changed before preferred replace: %w", err)
			}
		} else if err := ensureCredentialFileAbsent(legacySnapshot.directory.app); err != nil {
			return fmt.Errorf("legacy credential file appeared before preferred replace: %w", err)
		}
	}
	return nil
}

func ensureCredentialFileAbsent(dir *os.File) error {
	file, _, exists, err := openCredentialFile(dir)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return errors.Join(errors.New("credential file appeared during operation"), closeCredentialFile(file))
}

func removeCredentialSnapshot(snapshot *credentialFileSnapshot, locks []*credentialLock) error {
	if snapshot == nil || !snapshot.exists {
		return nil
	}
	if err := verifyLockedCredentialState([]*credentialDirectory{snapshot.directory}, locks); err != nil {
		return err
	}
	if err := verifyNamedFile(snapshot.directory.app, fileName, snapshot.info, false); err != nil {
		return fmt.Errorf("legacy credential file changed before removal: %w", err)
	}
	if err := verifyCredentialLinkCount(snapshot.file, 1); err != nil {
		return fmt.Errorf("legacy credential link count is unsafe: %w", err)
	}
	if err := unix.Unlinkat(int(snapshot.directory.app.Fd()), fileName, 0); err != nil {
		return fmt.Errorf("remove legacy credential file: %w", err)
	}
	if err := verifyCredentialLinkCount(snapshot.file, 0); err != nil {
		return fmt.Errorf("verify legacy credential removal: %w", err)
	}
	if err := syncCredentialDirectory(int(snapshot.directory.app.Fd())); err != nil {
		return fmt.Errorf("sync legacy credential directory: %w", err)
	}
	snapshot.exists = false
	return nil
}

func verifyCredentialLinkCount(file *os.File, want uint64) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fmt.Errorf("inspect credential link count: %w", err)
	}
	if uint64(stat.Nlink) != want {
		return fmt.Errorf("credential link count = %d, want %d", stat.Nlink, want)
	}
	return nil
}

func (operation *credentialOperation) finalize(bodyErr error) error {
	var cleanupErr error
	for i := len(operation.snapshots) - 1; i >= 0; i-- {
		snapshot := operation.snapshots[i]
		if snapshot != nil && snapshot.file != nil {
			cleanupErr = errors.Join(cleanupErr, closeCredentialFile(snapshot.file))
			snapshot.file = nil
		}
	}

	if operation.temp == nil {
		cleanupErr = errors.Join(cleanupErr, releaseOrderedCredentialLocks(operation.locks))
		cleanupErr = errors.Join(cleanupErr, closeCredentialDirectories(operation.directories))
		return errors.Join(bodyErr, cleanupErr)
	}
	if operation.tempExists {
		if err := unlinkTempFile(int(operation.tempDirectory.app.Fd()), operation.tempName); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove credential temporary file: %w", err))
			cleanupErr = errors.Join(cleanupErr, eraseCredentialFile(operation.temp))
		}
		cleanupErr = errors.Join(cleanupErr, closeTempFile(operation.temp))
		operation.temp = nil
		cleanupErr = errors.Join(cleanupErr, releaseOrderedCredentialLocks(operation.locks))
		cleanupErr = errors.Join(cleanupErr, closeCredentialDirectories(operation.directories))
		return errors.Join(bodyErr, cleanupErr)
	}

	var stable *os.File
	fd, duplicateErr := unix.Dup(int(operation.temp.Fd()))
	if duplicateErr != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("duplicate replaced credential descriptor: %w", duplicateErr))
		stable = operation.temp
	} else {
		stable = os.NewFile(uintptr(fd), "replaced-credential")
		cleanupErr = errors.Join(cleanupErr, closeTempFile(operation.temp))
		operation.temp = nil
	}
	cleanupErr = errors.Join(cleanupErr, releaseOrderedCredentialLocks(operation.locks))
	cleanupErr = errors.Join(cleanupErr, closeCredentialDirectories(operation.directories))

	shouldErase := operation.installed && (cleanupErr != nil || (bodyErr != nil && !operation.preserveInstalledOnBodyError))
	if shouldErase {
		cleanupErr = errors.Join(cleanupErr, eraseCredentialFile(stable))
	}
	if stable != nil {
		if stable == operation.temp {
			cleanupErr = errors.Join(cleanupErr, closeTempFile(stable))
			operation.temp = nil
		} else {
			cleanupErr = errors.Join(cleanupErr, stable.Close())
		}
	}
	return errors.Join(bodyErr, cleanupErr)
}

func openCredentialDirectory(appPath string, create bool) (*credentialDirectory, error) {
	basePath := filepath.Dir(appPath)
	base, baseInfo, err := openConfiguredDirectory(basePath)
	if err != nil {
		return nil, err
	}
	result := &credentialDirectory{basePath: basePath, appPath: appPath, base: base, baseInfo: baseInfo}
	if hook := credentialFilesystemHooks.afterBaseValidationBeforeAppOpen; hook != nil {
		if err := hook(basePath, appPath); err != nil {
			return nil, errors.Join(err, result.close())
		}
	}

	created := false
	if create {
		err := unix.Mkdirat(int(base.Fd()), filepath.Base(appPath), 0o700)
		if err == nil {
			created = true
		} else if !errors.Is(err, unix.EEXIST) {
			return nil, errors.Join(fmt.Errorf("create credential directory: %w", err), result.close())
		}
	}
	fd, err := unix.Openat(int(base.Fd()), filepath.Base(appPath), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, errors.Join(os.ErrNotExist, result.close())
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open credential directory: %w", err), result.close())
	}
	result.app = os.NewFile(uintptr(fd), appPath)
	if created {
		if err := result.app.Chmod(0o700); err != nil {
			return nil, errors.Join(fmt.Errorf("set credential directory permissions: %w", err), result.close())
		}
	}
	result.appInfo, err = result.app.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect credential directory: %w", err), result.close())
	}
	if !result.appInfo.IsDir() || result.appInfo.Mode().Perm()&0o077 != 0 {
		return nil, errors.Join(errors.New("credential directory must be a private non-symlink directory"), result.close())
	}
	if err := result.verify(); err != nil {
		return nil, errors.Join(err, result.close())
	}
	return result, nil
}

func openConfiguredDirectory(path string) (*os.File, os.FileInfo, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, nil, errors.New("user config directory must be absolute")
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
			if component == "" || component == "." {
				continue
			}
			nextFD, openErr := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if openErr != nil {
				closeErr := current.Close()
				if errors.Is(openErr, unix.ENOENT) {
					return nil, nil, errors.Join(os.ErrNotExist, closeErr)
				}
				return nil, nil, errors.Join(fmt.Errorf("securely traverse user config directory: %w", openErr), closeErr)
			}
			next := os.NewFile(uintptr(nextFD), component)
			if closeErr := current.Close(); closeErr != nil {
				return nil, nil, errors.Join(fmt.Errorf("close traversed config directory: %w", closeErr), next.Close())
			}
			current = next
		}
	}
	info, err := current.Stat()
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("inspect user config directory: %w", err), current.Close())
	}
	if !info.IsDir() {
		return nil, nil, errors.Join(errors.New("user config path must be a non-symlink directory"), current.Close())
	}
	return current, info, nil
}

func (directory *credentialDirectory) verify() error {
	base, baseInfo, err := openConfiguredDirectory(directory.basePath)
	if err != nil {
		return fmt.Errorf("verify user config directory: %w", err)
	}
	if !os.SameFile(directory.baseInfo, baseInfo) {
		return errors.Join(errors.New("user config directory path changed during operation"), base.Close())
	}
	fd, err := unix.Openat(int(base.Fd()), filepath.Base(directory.appPath), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	baseCloseErr := base.Close()
	if err != nil {
		return fmt.Errorf("verify credential directory path: %w", errors.Join(err, baseCloseErr))
	}
	app := os.NewFile(uintptr(fd), directory.appPath)
	info, statErr := app.Stat()
	closeErr := app.Close()
	if statErr != nil || closeErr != nil {
		return fmt.Errorf("verify credential directory: %w", errors.Join(statErr, closeErr, baseCloseErr))
	}
	if baseCloseErr != nil {
		return fmt.Errorf("close verified config directory: %w", baseCloseErr)
	}
	if !info.IsDir() || !os.SameFile(directory.appInfo, info) {
		return errors.New("credential directory path changed during operation")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("credential directory permissions allow group or other access")
	}
	return nil
}

func (directory *credentialDirectory) close() error {
	var appErr, baseErr error
	if directory.app != nil {
		if hook := credentialFilesystemHooks.closeAppDirectory; hook != nil {
			appErr = hook(directory.app)
		} else {
			appErr = directory.app.Close()
		}
		directory.app = nil
	}
	if directory.base != nil {
		if hook := credentialFilesystemHooks.closeBaseDirectory; hook != nil {
			baseErr = hook(directory.base)
		} else {
			baseErr = directory.base.Close()
		}
		directory.base = nil
	}
	if appErr != nil {
		appErr = fmt.Errorf("close credential directory: %w", appErr)
	}
	if baseErr != nil {
		baseErr = fmt.Errorf("close user config directory: %w", baseErr)
	}
	return errors.Join(appErr, baseErr)
}

func acquireCredentialLock(dir *os.File) (*os.File, os.FileInfo, error) {
	fd, err := unix.Openat(int(dir.Fd()), lockName, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(dir.Fd()), lockName, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open credential lock: %w", err)
	}
	lock := os.NewFile(uintptr(fd), lockName)
	if created {
		if err := lock.Chmod(0o600); err != nil {
			return nil, nil, errors.Join(fmt.Errorf("set credential lock permissions: %w", err), lock.Close())
		}
	}
	info, err := lock.Stat()
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("inspect credential lock: %w", err), lock.Close())
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, nil, errors.Join(errors.New("credential lock must be a private regular non-symlink file"), lock.Close())
	}
	deadline := time.Now().Add(credentialLockTimeout)
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return lock, info, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return nil, nil, errors.Join(fmt.Errorf("lock credential file: %w", err), lock.Close())
		}
		if time.Now().After(deadline) {
			return nil, nil, errors.Join(errors.New("timed out acquiring credential lock"), lock.Close())
		}
		time.Sleep(credentialLockPoll)
	}
}

func releaseCredentialLock(lock *os.File) error {
	var unlockErr error
	if hook := credentialFilesystemHooks.unlockLock; hook != nil {
		unlockErr = hook(int(lock.Fd()))
	} else {
		unlockErr = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	}
	var closeErr error
	if hook := credentialFilesystemHooks.closeLock; hook != nil {
		closeErr = hook(lock)
	} else {
		closeErr = lock.Close()
	}
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock credential file: %w", unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close credential lock: %w", closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}

func openCredentialFile(dir *os.File) (*os.File, os.FileInfo, bool, error) {
	fd, err := unix.Openat(int(dir.Fd()), fileName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("open credential file: %w", err)
	}
	file := os.NewFile(uintptr(fd), fileName)
	info, err := file.Stat()
	if err != nil {
		return nil, nil, false, errors.Join(fmt.Errorf("inspect credential file: %w", err), file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, nil, false, errors.Join(errors.New("credential file must be a regular non-symlink file"), file.Close())
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, nil, false, errors.Join(errors.New("credential file permissions allow group or other access"), file.Close())
	}
	return file, info, true, nil
}

func closeCredentialFile(file *os.File) error {
	if hook := credentialFilesystemHooks.closeCredential; hook != nil {
		return hook(file)
	}
	return file.Close()
}

func closeTempFile(file *os.File) error {
	if hook := credentialFilesystemHooks.closeTemp; hook != nil {
		return hook(file)
	}
	return file.Close()
}

func createCredentialTemp(dir *os.File) (*os.File, string, os.FileInfo, error) {
	for range 32 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", nil, fmt.Errorf("generate credential temporary name: %w", err)
		}
		name := ".credentials-" + hex.EncodeToString(random[:]) + ".tmp"
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", nil, fmt.Errorf("create credential temporary file: %w", err)
		}
		file := os.NewFile(uintptr(fd), name)
		if err := file.Chmod(0o600); err != nil {
			return nil, "", nil, errors.Join(fmt.Errorf("set credential temporary permissions: %w", err), cleanupEmptyTemp(file, int(dir.Fd()), name))
		}
		info, err := file.Stat()
		if err != nil {
			return nil, "", nil, errors.Join(fmt.Errorf("inspect credential temporary file: %w", err), cleanupEmptyTemp(file, int(dir.Fd()), name))
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return nil, "", nil, errors.Join(errors.New("created credential file is not a private regular file"), cleanupEmptyTemp(file, int(dir.Fd()), name))
		}
		return file, name, info, nil
	}
	return nil, "", nil, errors.New("could not allocate credential temporary file")
}

func cleanupEmptyTemp(file *os.File, dirfd int, name string) error {
	closeErr := file.Close()
	unlinkErr := unix.Unlinkat(dirfd, name, 0)
	return errors.Join(closeErr, unlinkErr)
}

func eraseCredentialFile(file *os.File) error {
	truncateErr := file.Truncate(0)
	var syncErr error
	if truncateErr == nil {
		syncErr = file.Sync()
	}
	return errors.Join(truncateErr, syncErr)
}

func verifyNamedFile(dir *os.File, name string, expected os.FileInfo, lock bool) (retErr error) {
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

func unlinkTempFile(dirfd int, name string) error {
	if hook := credentialFilesystemHooks.unlinkTemp; hook != nil {
		return hook(dirfd, name)
	}
	return unix.Unlinkat(dirfd, name, 0)
}

func syncCredentialDirectory(fd int) error {
	if hook := credentialFilesystemHooks.syncDirectory; hook != nil {
		return hook(fd)
	}
	return unix.Fsync(fd)
}
