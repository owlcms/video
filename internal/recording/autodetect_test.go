package recording

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ffmpegcfg "github.com/owlcms/video/internal/config/ffmpeg"
)

func testFFmpegConfigWithEncoder(name string) *ffmpegcfg.Config {
	return &ffmpegcfg.Config{
		Encoders: []ffmpegcfg.EncoderConfig{{Name: name}},
	}
}

func TestParseAvailableH264EncodersOnlyUsesEncoderRows(t *testing.T) {
	output := []byte(`Encoders:
 V..... h264_nvenc           NVIDIA NVENC H.264 encoder (codec h264)
 V....D libx264              H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (codec h264)
 configuration: --enable-h264_amf
 V..... wrapped_description  mentions h264_amf but is not that encoder
 V....D h264_amf             AMD AMF H.264 Encoder (codec h264)
 A..... h264_audio_name      not a video encoder
 V..... h264_qsv             H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (Intel Quick Sync Video acceleration)
`)

	available := parseAvailableH264Encoders(output)
	for _, name := range []string{"h264_nvenc", "h264_amf", "h264_qsv"} {
		if !available[name] {
			t.Fatalf("available[%q] = false, want true", name)
		}
	}
	for _, name := range []string{"libx264", "h264_audio_name", "wrapped_description"} {
		if available[name] {
			t.Fatalf("available[%q] = true, want false", name)
		}
	}
}

func TestEncoderGPUVendorMatches(t *testing.T) {
	tests := []struct {
		name     string
		required []string
		detected map[string]bool
		want     bool
	}{
		{
			name:     "matches detected vendor",
			required: []string{" AMD "},
			detected: map[string]bool{"amd": true},
			want:     true,
		},
		{
			name:     "skips when detected vendors do not overlap",
			required: []string{"amd"},
			detected: map[string]bool{"intel": true, "nvidia": true},
			want:     false,
		},
		{
			name:     "skips vendor encoder when GPU detection found nothing",
			required: []string{"amd"},
			detected: map[string]bool{},
			want:     false,
		},
		{
			name:     "allows encoder without vendor requirements when GPU detection found nothing",
			required: nil,
			detected: map[string]bool{},
			want:     true,
		},
		{
			name:     "allows encoders without vendor requirements",
			required: nil,
			detected: map[string]bool{"intel": true},
			want:     true,
		},
	}

	for i := range tests {
		tc := tests[i]
		t.Run(tc.name, func(t *testing.T) {
			if got := encoderGPUVendorMatches(tc.required, tc.detected); got != tc.want {
				t.Fatalf("encoderGPUVendorMatches(%v, %v) = %t, want %t", tc.required, tc.detected, got, tc.want)
			}
		})
	}
}

func TestEncoderIsProbeCandidateRequiresAvailableEncoderAndMatchingGPU(t *testing.T) {
	available := map[string]bool{"h264_nvenc": true, "h264_amf": true}
	detected := map[string]bool{"intel": true, "nvidia": true}

	if !encoderIsProbeCandidate("h264_nvenc", []string{"nvidia"}, available, detected) {
		t.Fatal("h264_nvenc should be a candidate when ffmpeg reports it and NVIDIA is detected")
	}
	if encoderIsProbeCandidate("h264_amf", []string{"amd"}, available, detected) {
		t.Fatal("h264_amf should not be a candidate when only Intel/NVIDIA GPUs are detected")
	}
	if encoderIsProbeCandidate("h264_qsv", []string{"intel"}, available, detected) {
		t.Fatal("h264_qsv should not be a candidate when ffmpeg did not report it")
	}
}

func TestReportUnconfiguredEncodersDoesNotUseProgress(t *testing.T) {
	available := map[string]bool{"h264_amf": true, "h264_nvenc": true, "h264_qsv": true}
	cfg := testFFmpegConfigWithEncoder("h264_qsv")
	var messages []string

	reportUnconfiguredEncoders(available, cfg, func(message string) {
		messages = append(messages, message)
	})

	if len(messages) != 0 {
		t.Fatalf("reported %d messages, want 0: %v", len(messages), messages)
	}
}

func TestParseAVFoundationVideoDevices(t *testing.T) {
	output := `[AVFoundation indev @ 0x1] AVFoundation video devices:
[AVFoundation indev @ 0x1] [0] MacBook Air Camera
[AVFoundation indev @ 0x1] [1] MacBook Air Desk View Camera
[AVFoundation indev @ 0x1] [2] JFL Camera
[AVFoundation indev @ 0x1] [3] Capture screen 0
[AVFoundation indev @ 0x1] AVFoundation audio devices:
[AVFoundation indev @ 0x1] [0] MacBook Air Microphone`

	got := parseAVFoundationVideoDevices(output)
	if len(got) != 2 {
		t.Fatalf("parseAVFoundationVideoDevices() returned %d devices, want 2: %#v", len(got), got)
	}
	if got[0] != (avfoundationDevice{index: "0", name: "MacBook Air Camera"}) {
		t.Fatalf("first device = %#v", got[0])
	}
	if got[1] != (avfoundationDevice{index: "2", name: "JFL Camera"}) {
		t.Fatalf("second device = %#v", got[1])
	}
}

func TestAVFoundationNameHelpersIgnoreUserAssignedName(t *testing.T) {
	cam := DetectedCamera{
		Name:       "Platform Left",
		DeviceName: "MacBook Air Camera",
		MatchKey:   "avfoundation:macbook air camera",
		Format:     "avfoundation",
	}

	if got, ok := AVFoundationDeviceName(cam); !ok || got != "MacBook Air Camera" {
		t.Fatalf("AVFoundationDeviceName() = %q, %t; want %q, true", got, ok, "MacBook Air Camera")
	}
	if got, ok := AVFoundationInputName(cam); !ok || got != "MacBook Air Camera" {
		t.Fatalf("AVFoundationInputName() = %q, %t; want %q, true", got, ok, "MacBook Air Camera")
	}

	// Configs written before DeviceName existed still resolve an index lookup
	// name from the match key, but cannot be opened by name because the key is
	// lower-cased and ffmpeg matches device names case-sensitively.
	legacy := DetectedCamera{Name: "Platform Left", MatchKey: "avfoundation:macbook air camera", Format: "avfoundation"}
	if got, ok := AVFoundationDeviceName(legacy); !ok || got != "macbook air camera" {
		t.Fatalf("AVFoundationDeviceName(legacy) = %q, %t; want %q, true", got, ok, "macbook air camera")
	}
	if got, ok := AVFoundationInputName(legacy); ok {
		t.Fatalf("AVFoundationInputName(legacy) = %q, true; want fallback to index", got)
	}

	// A colon would be parsed as the video/audio device separator.
	colon := DetectedCamera{DeviceName: "Weird:Cam", Format: "avfoundation"}
	if got, ok := AVFoundationInputName(colon); ok {
		t.Fatalf("AVFoundationInputName(colon) = %q, true; want fallback to index", got)
	}
}

func TestParseAVFoundationModesAndPixelFormat(t *testing.T) {
	modeOutput := `[avfoundation @ 0x1] Supported modes:
[avfoundation @ 0x1]   640x480@[15.000000 30.000000]fps
[avfoundation @ 0x1]   1920x1080@[15.000000 30.000000]fps`
	modes := parseAVFoundationModes(modeOutput)
	if len(modes) != 2 {
		t.Fatalf("parseAVFoundationModes() returned %d modes, want 2: %#v", len(modes), modes)
	}
	if modes[1] != (cameraMode{pixFmt: "raw", width: 1920, height: 1080, fps: 30}) {
		t.Fatalf("second mode = %#v", modes[1])
	}

	pixelOutput := `[avfoundation @ 0x1] Supported pixel formats:
[avfoundation @ 0x1]   uyvy422
[avfoundation @ 0x1]   yuyv422`
	if got := parseAVFoundationPixelFormat(pixelOutput); got != "uyvy422" {
		t.Fatalf("parseAVFoundationPixelFormat() = %q, want uyvy422", got)
	}
}

func TestWriteAutoConfigUsesAVFoundationInputSyntax(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "auto.toml")
	config := &ffmpegcfg.Config{
		Software: ffmpegcfg.SoftwareEncoder{OutputParameters: "-c:v libx264"},
		Encoders: []ffmpegcfg.EncoderConfig{{
			Name:             "h264_videotoolbox",
			Platform:         "avfoundation",
			OutputParameters: "-c:v h264_videotoolbox",
			VideoFilter:      "format=nv12",
		}},
	}
	cameras := []DetectedCamera{{
		Name:   "MacBook Air Camera",
		Device: "0",
		Format: "avfoundation",
		PixFmt: "uyvy422",
		Size:   "1920x1080",
		Fps:    30,
	}}

	if err := writeAutoConfig(outputPath, cameras, nil, config); err != nil {
		t.Fatalf("writeAutoConfig() error = %v", err)
	}
	contents, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	generated := string(contents)
	for _, want := range []string{"camera = '0:none'", "format = 'avfoundation'", "-pixel_format uyvy422"} {
		if !strings.Contains(generated, want) {
			t.Fatalf("generated config missing %q:\n%s", want, generated)
		}
	}
	if strings.Contains(generated, "-input_format uyvy422") {
		t.Fatalf("generated config used V4L2 syntax:\n%s", generated)
	}
}
