//go:build linux

package opendir

import "os/exec"

// Open reveals path in the desktop file manager.
func Open(path string) error {
	return exec.Command("xdg-open", path).Start()
}
