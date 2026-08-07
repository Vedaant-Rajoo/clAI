//go:build darwin || linux

package machinecontext

import (
	"os"

	"golang.org/x/sys/unix"
)

func openGitMetadataFile(path string) (*os.File, error) {
	// O_NOFOLLOW rejects a final-component symlink. O_NONBLOCK also ensures a
	// raced-in FIFO or device cannot stall the open before descriptor type checks.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
