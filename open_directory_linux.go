//go:build linux

package main

import "os/exec"

func openConfigurationDirectory(path string) error {
	return exec.Command("xdg-open", path).Start()
}
