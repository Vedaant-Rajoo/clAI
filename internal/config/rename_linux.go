//go:build linux

package config

import "golang.org/x/sys/unix"

func renameConfigNoReplace(dirfd int, oldName, newName string) error {
	return unix.Renameat2(dirfd, oldName, dirfd, newName, unix.RENAME_NOREPLACE)
}

func exchangeConfigNames(dirfd int, leftName, rightName string) error {
	return unix.Renameat2(dirfd, leftName, dirfd, rightName, unix.RENAME_EXCHANGE)
}
