package recording

import "io"

// RequestFFmpegQuit writes "q\n" to ffmpeg's stdin, which ffmpeg interprets
// as a request to stop recording and finalize the output file. Requires
// ffmpeg to have been started with stdin available (i.e., without -nostdin).
func RequestFFmpegQuit(stdin io.Writer) error {
	if stdin == nil {
		return nil
	}
	_, err := io.WriteString(stdin, "q\n")
	return err
}

// CloseFFmpegStdin closes ffmpeg's stdin pipe. ffmpeg treats EOF on stdin
// the same as receiving "q": it stops cleanly and finalizes the output.
func CloseFFmpegStdin(stdin io.Closer) error {
	if stdin == nil {
		return nil
	}
	return stdin.Close()
}
