//go:build darwin || linux

// Hardened atomic write path for config.json, mirroring the shape of
// internal/auth/filesystem_unix.go (which this phase must not modify or
// import) scoped down for a secretless file: cross-process flock on
// .config.lock, O_NOFOLLOW descriptor-relative operations, random temp file,
// write -> fsync -> renameat -> directory fsync, and 0600/0700 permission
// discipline (CONF-01, threats T-01-06/T-01-07/T-01-08). The credential-grade
// TOCTOU re-verification battery and erase-on-finalize logic are intentionally
// omitted — config.json holds no key material by construction (CONF-04).
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const (
	lockName          = ".config.lock"
	configLockTimeout = 2 * time.Second
	configLockPoll    = 10 * time.Millisecond
)

// writeConfigFile atomically replaces <UserConfigDir>/clai/config.json with
// data while holding the .config.lock flock. Readers never observe a partial
// file: replacement is a single rename of a fully-synced temp file (CONF-02).
func writeConfigFile(data []byte) (retErr error) {
	path, err := Path()
	if err != nil {
		return err
	}
	dir, err := openConfigDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, closeConfigDirectory(dir)) }()

	lock, err := acquireConfigLock(dir)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, releaseConfigLock(dir, lock)) }()

	temp, tempName, err := createConfigTemp(dir)
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			if unlinkErr := unix.Unlinkat(int(dir.Fd()), tempName, 0); unlinkErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("remove config temporary file: %w", unlinkErr))
			}
		}
		if closeErr := temp.Close(); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close config temporary file: %w", closeErr))
		}
	}()

	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write config temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync config temporary file: %w", err)
	}
	if err := unix.Renameat(int(dir.Fd()), tempName, int(dir.Fd()), fileName); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}
	renamed = true
	if err := unix.Fsync(int(dir.Fd())); err != nil {
		return fmt.Errorf("sync config directory: %w", err)
	}
	return nil
}

// openConfigDirectory opens (creating if absent, mode 0700) the clai config
// directory as a descriptor via O_NOFOLLOW so a symlink planted at the
// directory name is rejected rather than followed (T-01-06). An existing
// directory whose permissions grant group or other access is rejected.
func openConfigDirectory(appPath string) (retFile *os.File, retErr error) {
	basePath := filepath.Dir(appPath)
	baseFd, err := unix.Open(basePath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open user config directory: %w", err)
	}
	base := os.NewFile(uintptr(baseFd), basePath)
	defer func() {
		if closeErr := base.Close(); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close user config directory: %w", closeErr))
		}
	}()

	created := false
	if err := unix.Mkdirat(int(base.Fd()), filepath.Base(appPath), 0o700); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return nil, fmt.Errorf("create config directory: %w", err)
	}
	fd, err := unix.Openat(int(base.Fd()), filepath.Base(appPath), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open config directory: %w", err)
	}
	dir := os.NewFile(uintptr(fd), appPath)
	if created {
		if err := dir.Chmod(0o700); err != nil {
			return nil, errors.Join(fmt.Errorf("set config directory permissions: %w", err), dir.Close())
		}
	}
	info, err := dir.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect config directory: %w", err), dir.Close())
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.Join(errors.New("config directory must be a private non-symlink directory"), dir.Close())
	}
	return dir, nil
}

func closeConfigDirectory(dir *os.File) error {
	if err := dir.Close(); err != nil {
		return fmt.Errorf("close config directory: %w", err)
	}
	return nil
}

// acquireConfigLock takes an exclusive flock on .config.lock inside dir,
// polling every configLockPoll up to configLockTimeout. Because
// releaseConfigLock unlinks the lock file, a successful flock is only valid
// if the locked descriptor still matches the current directory entry — a
// stale inode (unlinked by a finishing writer) is discarded and the acquire
// retried on the fresh entry.
func acquireConfigLock(dir *os.File) (*os.File, error) {
	deadline := time.Now().Add(configLockTimeout)
	for {
		fd, err := unix.Openat(int(dir.Fd()), lockName, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, unix.ENOENT) {
			// A finishing writer unlinked the entry mid-open (create vs.
			// unlink race); retry on the next poll tick.
			if time.Now().After(deadline) {
				return nil, errors.New("timed out acquiring config lock")
			}
			time.Sleep(configLockPoll)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("open config lock: %w", err)
		}
		lock := os.NewFile(uintptr(fd), lockName)
		info, err := lock.Stat()
		if err != nil {
			return nil, errors.Join(fmt.Errorf("inspect config lock: %w", err), lock.Close())
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return nil, errors.Join(errors.New("config lock must be a private regular non-symlink file"), lock.Close())
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			current, verifyErr := lockEntryMatches(dir, info)
			if verifyErr != nil {
				return nil, errors.Join(verifyErr, lock.Close())
			}
			if current {
				return lock, nil
			}
			// A finishing writer unlinked this inode after we opened it;
			// close the stale descriptor and retry on the fresh entry.
			if closeErr := lock.Close(); closeErr != nil {
				return nil, fmt.Errorf("close stale config lock: %w", closeErr)
			}
		case errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN):
			// Held by another writer; close so a holder's unlink+recreate is
			// picked up on the next open.
			if closeErr := lock.Close(); closeErr != nil {
				return nil, fmt.Errorf("close config lock: %w", closeErr)
			}
		default:
			return nil, errors.Join(fmt.Errorf("lock config file: %w", err), lock.Close())
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out acquiring config lock")
		}
		time.Sleep(configLockPoll)
	}
}

// lockEntryMatches reports whether info (the locked descriptor's identity)
// still names the current lockName entry in dir. A missing entry means the
// lock file was already unlinked.
func lockEntryMatches(dir *os.File, info os.FileInfo) (bool, error) {
	fd, err := unix.Openat(int(dir.Fd()), lockName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("verify config lock: %w", err)
	}
	entry := os.NewFile(uintptr(fd), lockName)
	entryInfo, err := entry.Stat()
	closeErr := entry.Close()
	if err != nil {
		return false, errors.Join(fmt.Errorf("inspect config lock entry: %w", err), closeErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close config lock entry: %w", closeErr)
	}
	return os.SameFile(info, entryInfo), nil
}

// releaseConfigLock unlinks the lock file while the flock is still held (so
// the lock file is transient on disk), then unlocks and closes the
// descriptor. Independent failures are joined.
func releaseConfigLock(dir *os.File, lock *os.File) error {
	var unlinkErr error
	if err := unix.Unlinkat(int(dir.Fd()), lockName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		unlinkErr = fmt.Errorf("remove config lock: %w", err)
	}
	var unlockErr error
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		unlockErr = fmt.Errorf("unlock config file: %w", err)
	}
	var closeErr error
	if err := lock.Close(); err != nil {
		closeErr = fmt.Errorf("close config lock: %w", err)
	}
	return errors.Join(unlinkErr, unlockErr, closeErr)
}

// createConfigTemp creates a private random ".config-<hex>.tmp" file inside
// dir via O_EXCL|O_NOFOLLOW, retrying on name collisions.
func createConfigTemp(dir *os.File) (*os.File, string, error) {
	for range 32 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", fmt.Errorf("generate config temporary name: %w", err)
		}
		name := ".config-" + hex.EncodeToString(random[:]) + ".tmp"
		fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("create config temporary file: %w", err)
		}
		file := os.NewFile(uintptr(fd), name)
		if err := file.Chmod(0o600); err != nil {
			return nil, "", errors.Join(fmt.Errorf("set config temporary permissions: %w", err), cleanupConfigTemp(file, dir, name))
		}
		info, err := file.Stat()
		if err != nil {
			return nil, "", errors.Join(fmt.Errorf("inspect config temporary file: %w", err), cleanupConfigTemp(file, dir, name))
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return nil, "", errors.Join(errors.New("created config temporary is not a private regular file"), cleanupConfigTemp(file, dir, name))
		}
		return file, name, nil
	}
	return nil, "", errors.New("could not allocate config temporary file")
}

func cleanupConfigTemp(file *os.File, dir *os.File, name string) error {
	closeErr := file.Close()
	unlinkErr := unix.Unlinkat(int(dir.Fd()), name, 0)
	return errors.Join(closeErr, unlinkErr)
}
