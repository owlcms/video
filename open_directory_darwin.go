//go:build darwin

package main

import "os/exec"

func openConfigurationDirectory(path string) error {
	return exec.Command("open", path).Start()
}
