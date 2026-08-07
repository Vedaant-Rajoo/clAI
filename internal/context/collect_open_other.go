//go:build !darwin && !linux

package machinecontext

import "os"

func openGitMetadataFile(path string) (*os.File, error) {
	// Portable os.Open has no no-follow flag. The caller still rejects symlinks
	// before open and verifies the opened descriptor's type and identity after it.
	return os.Open(path)
}
