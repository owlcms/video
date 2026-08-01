//go:build linux

package recording

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func findSystemFFmpegPath(currentPath string) string {
	currentPath = canonicalExecutablePath(currentPath)
	candidates := []string{"/usr/bin/ffmpeg", "/usr/local/bin/ffmpeg", "/bin/ffmpeg"}
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
	if !strings.ContainsRune(path, os.PathSeparator) {
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