//go:build darwin

package config

import "golang.org/x/sys/unix"

func renameConfigNoReplace(dirfd int, oldName, newName string) error {
	return unix.RenameatxNp(dirfd, oldName, dirfd, newName, unix.RENAME_EXCL)
}

func exchangeConfigNames(dirfd int, leftName, rightName string) error {
	return unix.RenameatxNp(dirfd, leftName, dirfd, rightName, unix.RENAME_SWAP)
}
