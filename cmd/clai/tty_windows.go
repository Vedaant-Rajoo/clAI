//go:build windows

package main

import (
	"errors"
	"os"
)

// openControllingTerminal reports that widget mode is unavailable: Windows
// has no /dev/tty equivalent that Bubble Tea can drive through this path.
func openControllingTerminal() (*os.File, error) {
	return nil, errors.New("widget mode is not supported on Windows")
}
