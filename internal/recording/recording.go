package recording

// Recording tracks whether a recording is currently in progress
var Recording bool

const FfmpegBuild = "ffmpeg-7.1-full_build"

// IsRecording returns the current recording state
func IsRecording() bool {
	return Recording
}
