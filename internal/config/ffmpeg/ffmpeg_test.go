package ffmpeg

import (
	"runtime"
	"strings"
	"testing"
)

func TestApplyDefaultsUsesEmbeddedEncoderDefaults(t *testing.T) {
	var cfg Config
	cfg.applyDefaults()
	cfg.filterEncodersForPlatform()

	if len(cfg.Encoders) == 0 {
		t.Fatal("applyDefaults() left Encoders empty")
	}

	var nvenc *EncoderConfig
	for i := range cfg.Encoders {
		if cfg.Encoders[i].Name == "h264_nvenc" {
			nvenc = &cfg.Encoders[i]
			break
		}
	}
	if nvenc == nil {
		t.Fatal("applyDefaults() did not include h264_nvenc")
	}
	if nvenc.VideoFilter != "format=yuv420p" {
		t.Fatalf("h264_nvenc VideoFilter = %q, want format=yuv420p", nvenc.VideoFilter)
	}
	if !strings.Contains(nvenc.OutputParameters, "-forced-idr 1") {
		t.Fatalf("h264_nvenc OutputParameters = %q, want current embedded defaults", nvenc.OutputParameters)
	}
	if !strings.Contains(cfg.Software.OutputParameters, "-pix_fmt yuv420p") {
		t.Fatalf("software OutputParameters = %q, want yuv420p output", cfg.Software.OutputParameters)
	}
}

func TestEncoderPlatformMatchRecognizesCurrentPlatformAliases(t *testing.T) {
	tests := []struct {
		platform string
		want     bool
	}{
		{platform: "windows", want: runtime.GOOS == "windows"},
		{platform: "dshow", want: runtime.GOOS == "windows"},
		{platform: "linux", want: runtime.GOOS == "linux"},
		{platform: "v4l2", want: runtime.GOOS == "linux"},
		{platform: "darwin", want: runtime.GOOS == "darwin"},
		{platform: "macos", want: runtime.GOOS == "darwin"},
		{platform: "avfoundation", want: runtime.GOOS == "darwin"},
	}

	for _, tc := range tests {
		t.Run(tc.platform, func(t *testing.T) {
			if got := encoderPlatformMatch(tc.platform); got != tc.want {
				t.Fatalf("encoderPlatformMatch(%q) = %t, want %t", tc.platform, got, tc.want)
			}
		})
	}
}
