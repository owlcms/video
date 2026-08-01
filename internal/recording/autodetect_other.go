//go:build !windows && !linux

package recording

func findSystemFFmpegPath(string) string {
	return ""
}