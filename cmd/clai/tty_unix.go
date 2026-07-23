//go:build !windows

package main

import "os"

// openControllingTerminal opens the process's controlling terminal so the
// widget TUI can draw even when stdin/stdout are captured by the shell
// integration.
func openControllingTerminal() (*os.File, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
}
