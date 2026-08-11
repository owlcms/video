//go:build darwin

package opendir

import "os/exec"

// Open reveals path in the desktop file manager.
func Open(path string) error {
	return exec.Command("open", path).Start()
}
