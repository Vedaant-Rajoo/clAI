//go:build darwin || linux

package configroot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// canonicalizeAfterCreate is a test-only checkpoint reached after a newly
// created component has been opened but before its directory entry is
// identity-verified. Production leaves it nil.
var canonicalizeAfterCreate func(string) error

// Canonicalize resolves symlinks only within the already-approved root. When
// create is true, a missing suffix is created descriptor-relative with private
// permissions and every newly created name is re-opened no-follow and checked
// against the descriptor obtained immediately after creation.
func Canonicalize(path string, create bool) (string, error) {
	clean, err := validateBase("config root", path)
	if err != nil {
		return "", err
	}

	canonical, err := filepath.EvalSymlinks(clean)
	if err == nil {
		return validateAndOpenCanonicalRoot(canonical)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("canonicalize config root: %w", err)
	}
	if !create {
		return clean, nil
	}
	return createCanonicalRoot(clean)
}

func validateAndOpenCanonicalRoot(path string) (string, error) {
	canonical, err := validateBase("canonical config root", path)
	if err != nil {
		return "", err
	}
	dir, info, err := openCanonicalDirectory(canonical)
	if err != nil {
		return "", err
	}
	closeErr := dir.Close()
	if !info.IsDir() {
		return "", errors.Join(errors.New("canonical config root must be a directory"), closeErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close canonical config root: %w", closeErr)
	}
	return canonical, nil
}

func createCanonicalRoot(path string) (result string, retErr error) {
	prefix, suffix, err := longestExistingPrefix(path)
	if err != nil {
		return "", err
	}
	canonicalPrefix, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		return "", fmt.Errorf("canonicalize existing config root prefix: %w", err)
	}
	canonicalPrefix, err = validateBase("canonical config root prefix", canonicalPrefix)
	if err != nil {
		return "", err
	}

	current, info, err := openCanonicalDirectory(canonicalPrefix)
	if err != nil {
		return "", err
	}
	defer func() {
		if current != nil {
			retErr = errors.Join(retErr, closeRootDirectory(current))
		}
	}()
	if !info.IsDir() {
		return "", errors.New("existing config root prefix must be a directory")
	}

	currentPath := canonicalPrefix
	for _, component := range suffix {
		created := false
		if err := unix.Mkdirat(int(current.Fd()), component, 0o700); err == nil {
			created = true
		} else if !errors.Is(err, unix.EEXIST) {
			return "", fmt.Errorf("create config root component %q: %w", component, err)
		}

		nextFD, err := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return "", fmt.Errorf("open config root component %q without following symlinks: %w", component, err)
		}
		next := os.NewFile(uintptr(nextFD), component)
		if created {
			if err := next.Chmod(0o700); err != nil {
				return "", errors.Join(fmt.Errorf("set config root component %q permissions: %w", component, err), closeRootDirectory(next))
			}
		}
		nextInfo, err := next.Stat()
		if err != nil {
			return "", errors.Join(fmt.Errorf("inspect config root component %q: %w", component, err), closeRootDirectory(next))
		}
		if !nextInfo.IsDir() {
			return "", errors.Join(fmt.Errorf("config root component %q must be a directory", component), closeRootDirectory(next))
		}
		if nextInfo.Mode().Perm()&0o077 != 0 {
			return "", errors.Join(fmt.Errorf("config root component %q must be private", component), closeRootDirectory(next))
		}

		nextPath := filepath.Join(currentPath, component)
		if created && canonicalizeAfterCreate != nil {
			if err := canonicalizeAfterCreate(nextPath); err != nil {
				return "", errors.Join(fmt.Errorf("run config root creation checkpoint: %w", err), closeRootDirectory(next))
			}
		}
		if err := verifyCreatedRootEntry(current, component, nextInfo); err != nil {
			return "", errors.Join(err, closeRootDirectory(next))
		}
		if err := closeRootDirectory(current); err != nil {
			return "", errors.Join(err, closeRootDirectory(next))
		}
		current = next
		currentPath = nextPath
	}
	return currentPath, nil
}

func longestExistingPrefix(path string) (string, []string, error) {
	prefix := path
	var reversed []string
	for {
		if _, err := os.Lstat(prefix); err == nil {
			for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
				reversed[left], reversed[right] = reversed[right], reversed[left]
			}
			return prefix, reversed, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("inspect config root prefix %q: %w", prefix, err)
		}
		parent := filepath.Dir(prefix)
		if parent == prefix {
			return "", nil, errors.New("could not find an existing config root prefix")
		}
		reversed = append(reversed, filepath.Base(prefix))
		prefix = parent
	}
}

func openCanonicalDirectory(path string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open canonical config root %q: %w", path, err)
	}
	dir := os.NewFile(uintptr(fd), path)
	info, err := dir.Stat()
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("inspect canonical config root %q: %w", path, err), closeRootDirectory(dir))
	}
	return dir, info, nil
}

func verifyCreatedRootEntry(parent *os.File, name string, expected os.FileInfo) error {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("created config root component %q changed or became a symlink: %w", name, err)
	}
	entry := os.NewFile(uintptr(fd), name)
	info, statErr := entry.Stat()
	closeErr := closeRootDirectory(entry)
	if statErr != nil {
		return errors.Join(fmt.Errorf("inspect created config root component %q: %w", name, statErr), closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if !info.IsDir() || !os.SameFile(expected, info) {
		return fmt.Errorf("created config root component %q changed during creation", name)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("created config root component %q permissions changed during creation", name)
	}
	return nil
}

func closeRootDirectory(dir *os.File) error {
	if err := dir.Close(); err != nil {
		return fmt.Errorf("close config root directory: %w", err)
	}
	return nil
}
