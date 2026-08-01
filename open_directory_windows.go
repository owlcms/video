//go:build windows

package main

import "os/exec"

func openConfigurationDirectory(path string) error {
	return exec.Command("explorer", path).Start()
}
