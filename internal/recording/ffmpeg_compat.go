package recording

import (
	"strings"

	configpkg "github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/logging"
)

// EnsureCompatibleFFmpegForRecording switches to a system ffmpeg 6 build when
// the current ffmpeg cannot initialize NVENC for configured recording cameras.
func EnsureCompatibleFFmpegForRecording(cameras []configpkg.CameraConfiguration) error {
	if !recordingConfigsRequireNVENC(cameras) {
		return nil
	}

	vendors := detectGPUVendors()
	if !vendors["nvidia"] {
		return nil
	}

	path := configpkg.GetFFmpegPath()
	if path == "" {
		path = "ffmpeg"
	}

	nvencProbe := HwEncoder{Name: "h264_nvenc", Description: "NVIDIA GPU (NVENC)"}
	if testEncoderWithInit(path, nvencProbe) {
		return nil
	}

	fallbackPath := findSystemFFmpegPath(path)
	if fallbackPath == "" {
		logging.WarningLogger.Printf("Configured recording uses NVENC, but %s could not initialize it and no system ffmpeg 6 fallback was found", path)
		return nil
	}

	if !testEncoderWithInit(fallbackPath, nvencProbe) {
		logging.WarningLogger.Printf("Configured recording uses NVENC, but system ffmpeg fallback %s also failed NVENC initialization", fallbackPath)
		return nil
	}

	if err := applyFFmpegPath(fallbackPath); err != nil {
		return err
	}

	logging.InfoLogger.Printf("Using system ffmpeg for recording because bundled ffmpeg could not initialize NVENC: %s", fallbackPath)
	return nil
}

func recordingConfigsRequireNVENC(cameras []configpkg.CameraConfiguration) bool {
	for _, camera := range cameras {
		if strings.Contains(strings.ToLower(camera.OutputParameters), "h264_nvenc") ||
			strings.Contains(strings.ToLower(camera.Params), "h264_nvenc") {
			return true
		}
	}
	return false
}