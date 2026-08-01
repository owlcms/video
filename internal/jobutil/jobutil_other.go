//go:build !windows

package jobutil

import "os/exec"

// Init is a no-op on non-Windows platforms.
func Init() error {
	return nil
}

// Assign is a no-op on non-Windows platforms.
func Assign(cmd *exec.Cmd) error {
	return nil
}
