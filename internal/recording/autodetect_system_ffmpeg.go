//go:build linux || windows

package recording

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/owlcms/video/internal/logging"
)

var ffmpegVersionPattern = regexp.MustCompile(`(?i)^ffmpeg version `)

func isSupportedSystemFFmpeg(path string) bool {
	cmd := CreateHiddenCmd(path, "-version")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		logging.InfoLogger.Printf("Skipping system ffmpeg candidate %s: failed to query version: %v", path, err)
		return false
	}

	versionLine := strings.TrimSpace(out.String())
	if idx := strings.Index(versionLine, "\n"); idx >= 0 {
		versionLine = strings.TrimSpace(versionLine[:idx])
	}
	if versionLine == "" {
		logging.InfoLogger.Printf("Skipping system ffmpeg candidate %s: empty version output", path)
		return false
	}
	if !ffmpegVersionPattern.MatchString(versionLine) {
		logging.InfoLogger.Printf("Skipping system ffmpeg candidate %s: unexpected version output %s", path, versionLine)
		return false
	}

	logging.InfoLogger.Printf("System ffmpeg candidate %s is supported: %s", path, versionLine)
	return true
}