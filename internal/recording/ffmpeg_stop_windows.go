//go:build windows

package recording

import (
	"io"
	"os/exec"
)

// RequestFFmpegStop asks ffmpeg to stop recording gracefully.
//
// On Windows we cannot reliably deliver SIGINT to a child process: Go's
// os.Interrupt is not implemented for Windows processes (cmd.Process.Signal
// returns "not supported by windows"), and CTRL_C_EVENT can only be sent to
// processes that share our console group. The portable mechanism is to write
// "q" to ffmpeg's stdin, which ffmpeg honours as a graceful stop request as
// long as it was started with stdin attached (no -nostdin flag).
func RequestFFmpegStop(cmd *exec.Cmd, stdin io.Writer) error {
	return RequestFFmpegQuit(stdin)
}
