//go:build !windows

package recording

import (
	"io"
	"os/exec"
	"syscall"
)

// RequestFFmpegStop asks ffmpeg to stop recording gracefully.
//
// On Unix systems ffmpeg installs signal handlers for SIGINT/SIGTERM that
// cause it to stop reading input, flush buffers, and write a valid trailer
// to the output container. Sending SIGINT is the canonical way to stop a
// long-running ffmpeg capture from a parent process and works regardless
// of whether stdin is attached. We fall back to writing "q" on stdin only
// if signal delivery fails.
func RequestFFmpegStop(cmd *exec.Cmd, stdin io.Writer) error {
	if cmd != nil && cmd.Process != nil {
		if err := cmd.Process.Signal(syscall.SIGINT); err == nil {
			return nil
		}
	}
	return RequestFFmpegQuit(stdin)
}
