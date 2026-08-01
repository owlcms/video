//go:build windows

package recording

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func findSystemFFmpegPath(currentPath string) string {
	currentPath = canonicalExecutablePath(currentPath)
	candidates := make([]string, 0, 2)
	if lookPath, err := exec.LookPath("ffmpeg.exe"); err == nil {
		candidates = append(candidates, lookPath)
	}
	if lookPath, err := exec.LookPath("ffmpeg"); err == nil {
		candidates = append(candidates, lookPath)
	}

	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		canonical := canonicalExecutablePath(candidate)
		if canonical == "" || canonical == currentPath {
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		if _, err := os.Stat(canonical); err == nil && isSupportedSystemFFmpeg(canonical) {
			return canonical
		}
	}

	return ""
}

func canonicalExecutablePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if !strings.ContainsRune(path, os.PathSeparator) && !strings.ContainsRune(path, '/') {
		if lookPath, err := exec.LookPath(path); err == nil {
			path = lookPath
		}
	}
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
	}
	if resolvedPath, err := filepath.EvalSymlinks(path); err == nil {
		path = resolvedPath
	}
	return filepath.Clean(path)
}