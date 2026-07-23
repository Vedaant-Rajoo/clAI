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

func secureReadCredentialFile(path string) (data []byte, exists bool, retErr error) {
	directory, err := openCredentialDirectory(filepath.Dir(path), false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer func() { retErr = errors.Join(retErr, directory.close()) }()

	if hook := credentialFilesystemHooks.beforeLockAcquire; hook != nil {
		if err := hook(directory.appPath, path); err != nil {
			return nil, false, err
		}
	}
	lock, lockInfo, err := acquireCredentialLock(directory.app)
	if err != nil {
		return nil, false, err
	}
	defer func() { retErr = errors.Join(retErr, releaseCredentialLock(lock)) }()
	if hook := credentialFilesystemHooks.afterLockAcquired; hook != nil {
		if err := hook(directory.appPath, path); err != nil {
			return nil, false, err
		}
	}
	if err := directory.verify(); err != nil {
		return nil, false, err
	}
	if err := verifyNamedFile(directory.app, lockName, lockInfo, true); err != nil {
		return nil, false, fmt.Errorf("verify credential lock: %w", err)
	}

	file, info, found, err := openCredentialFile(directory.app)
	if err != nil || !found {
		return nil, found, err
	}
	if hook := credentialFilesystemHooks.afterFileValidationBeforeRead; hook != nil {
		if err := hook(directory.appPath, path); err != nil {
			return nil, false, errors.Join(err, closeCredentialFile(file))
		}
	}
	if err := directory.verify(); err != nil {
		return nil, false, errors.Join(err, closeCredentialFile(file))
	}
	if err := verifyNamedFile(directory.app, fileName, info, false); err != nil {
		return nil, false, errors.Join(fmt.Errorf("credential file changed before read: %w", err), closeCredentialFile(file))
	}
	data, readErr := io.ReadAll(file)
	closeErr := closeCredentialFile(file)
	if readErr != nil {
		return nil, false, fmt.Errorf("read credential file: %w", readErr)
	}
	if closeErr != nil {
		return nil, false, fmt.Errorf("close credential file: %w", closeErr)
	}
	return data, true, nil
}

func secureMutateCredentialFile(path string, create bool, mutate func(map[string]string) bool) (retErr error) {
	var directory *credentialDirectory
	var lock *os.File
	var temp *os.File
	var tempName string
	tempExists := false
	renamed := false
	defer func() {
		retErr = finalizeCredentialMutation(retErr, directory, lock, temp, tempName, tempExists, renamed)
	}()

	var err error
	directory, err = openCredentialDirectory(filepath.Dir(path), create)
	if !create && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	if hook := credentialFilesystemHooks.beforeLockAcquire; hook != nil {
		if err := hook(directory.appPath, path); err != nil {
			return err
		}
	}
	var lockInfo os.FileInfo
	lock, lockInfo, err = acquireCredentialLock(directory.app)
	if err != nil {
		return err
	}
	if hook := credentialFilesystemHooks.afterLockAcquired; hook != nil {
		if err := hook(directory.appPath, path); err != nil {
			return err
		}
	}
	if err := directory.verify(); err != nil {
		return err
	}
	if err := verifyNamedFile(directory.app, lockName, lockInfo, true); err != nil {
		return fmt.Errorf("verify credential lock: %w", err)
	}

	creds := map[string]string{}
	current, currentInfo, exists, err := openCredentialFile(directory.app)
	if err != nil {
		return err
	}
	if exists {
		if hook := credentialFilesystemHooks.afterFileValidationBeforeRead; hook != nil {
			if err := hook(directory.appPath, path); err != nil {
				return errors.Join(err, closeCredentialFile(current))
			}
		}
		if err := directory.verify(); err != nil {
			return errors.Join(err, closeCredentialFile(current))
		}
		if err := verifyNamedFile(directory.app, fileName, currentInfo, false); err != nil {
			return errors.Join(fmt.Errorf("credential file changed before read: %w", err), closeCredentialFile(current))
		}
		content, readErr := io.ReadAll(current)
		closeErr := closeCredentialFile(current)
		if readErr != nil {
			return fmt.Errorf("read credential file: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close credential file: %w", closeErr)
		}
		creds, err = decodeCredentials(path, content)
		if err != nil {
			return err
		}
	}
	if hook := credentialFilesystemHooks.afterCredentialReadWhileLocked; hook != nil {
		if err := hook(directory.appPath, path); err != nil {
			return err
		}
	}
	if !mutate(creds) {
		return nil
	}
	out, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credential config: %w", err)
	}

	if hook := credentialFilesystemHooks.beforeTempCreation; hook != nil {
		if err := hook(directory.appPath, path); err != nil {
			return err
		}
	}
	if err := directory.verify(); err != nil {
		return err
	}
	var tempInfo os.FileInfo
	temp, tempName, tempInfo, err = createCredentialTemp(directory.app)
	if err != nil {
		return err
	}
	tempExists = true
	if _, err := temp.Write(out); err != nil {
		return fmt.Errorf("write credential temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync credential temporary file: %w", err)
	}
	if hook := credentialFilesystemHooks.afterTempCreation; hook != nil {
		if err := hook(directory.appPath, path, tempName); err != nil {
			return err
		}
	}
	if err := directory.verify(); err != nil {
		return err
	}
	if err := verifyNamedFile(directory.app, lockName, lockInfo, true); err != nil {
		return fmt.Errorf("credential lock changed before replace: %w", err)
	}
	if exists {
		if err := verifyNamedFile(directory.app, fileName, currentInfo, false); err != nil {
			return fmt.Errorf("credential file changed before replace: %w", err)
		}
	} else {
		appeared, _, nowExists, err := openCredentialFile(directory.app)
		if err != nil {
			return err
		}
		if nowExists {
			return errors.Join(errors.New("credential file appeared before replace"), closeCredentialFile(appeared))
		}
	}

	if err := unix.Renameat(int(directory.app.Fd()), tempName, int(directory.app.Fd()), fileName); err != nil {
		return fmt.Errorf("replace credential file: %w", err)
	}
	tempExists = false
	renamed = true
	if hook := credentialFilesystemHooks.afterRenameBeforeVerification; hook != nil {
		if err := hook(directory.appPath, path); err != nil {
			return err
		}
	}
	if err := directory.verify(); err != nil {
		return err
	}
	if err := verifyNamedFile(directory.app, fileName, tempInfo, false); err != nil {
		return fmt.Errorf("verify replaced credential file: %w", err)
	}
	if err := syncCredentialDirectory(int(directory.app.Fd())); err != nil {
		return fmt.Errorf("sync credential directory: %w", err)
	}
	return nil
}

func finalizeCredentialMutation(retErr error, directory *credentialDirectory, lock, temp *os.File, tempName string, tempExists, renamed bool) error {
	if temp == nil {
		if lock != nil {
			retErr = errors.Join(retErr, releaseCredentialLock(lock))
		}
		if directory != nil {
			retErr = errors.Join(retErr, directory.close())
		}
		return retErr
	}
	if tempExists {
		if err := unlinkTempFile(int(directory.app.Fd()), tempName); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("remove credential temporary file: %w", err))
			retErr = errors.Join(retErr, eraseCredentialFile(temp))
		}
		retErr = errors.Join(retErr, closeTempFile(temp))
		if lock != nil {
			retErr = errors.Join(retErr, releaseCredentialLock(lock))
		}
		if directory != nil {
			retErr = errors.Join(retErr, directory.close())
		}
		return retErr
	}

	var stable *os.File
	if renamed {
		fd, err := unix.Dup(int(temp.Fd()))
		if err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("duplicate replaced credential descriptor: %w", err))
			stable = temp
		} else {
			stable = os.NewFile(uintptr(fd), "replaced-credential")
			retErr = errors.Join(retErr, closeTempFile(temp))
		}
	} else {
		retErr = errors.Join(retErr, closeTempFile(temp))
	}
	if lock != nil {
		retErr = errors.Join(retErr, releaseCredentialLock(lock))
	}
	if directory != nil {
		retErr = errors.Join(retErr, directory.close())
	}
	if renamed && retErr != nil {
		retErr = errors.Join(retErr, eraseCredentialFile(stable))
	}
	if stable != nil {
		if stable == temp {
			retErr = errors.Join(retErr, closeTempFile(stable))
		} else {
			retErr = errors.Join(retErr, stable.Close())
		}
	}
	return retErr
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
