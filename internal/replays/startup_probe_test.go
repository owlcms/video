package replays

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/recording"
)

func TestRunFakeReplayProbeReportsFailedCameras(t *testing.T) {
	originalRunFakeReplayTest := runFakeReplayTest
	t.Cleanup(func() {
		runFakeReplayTest = originalRunFakeReplayTest
	})
	runFakeReplayTest = func(_ context.Context, cameras []config.CameraConfiguration, duration, timeout time.Duration) []recording.FakeReplayResult {
		return []recording.FakeReplayResult{
			{Camera: 1},
			{Camera: 2, Err: fmt.Errorf("no video")},
		}
	}
	cameras := []config.CameraConfiguration{
		{FfmpegCamera: "udp://127.0.0.1:9001"},
		{FfmpegCamera: "udp://127.0.0.1:9002"},
	}

	missing := runFakeReplayProbe(context.Background(), cameras)
	expected := "camera 2 (port 9002)"
	if len(missing) != 1 || missing[0] != expected {
		t.Fatalf("expected only %q missing, got %#v", expected, missing)
	}
}

func TestCombineStartupMessagesKeepsUnicastFirst(t *testing.T) {
	combined := combineStartupMessages(
		"Unicast mode: listening on 0.0.0.0.\nReplay receiver: localhost (127.0.0.1).",
		"Error: fake replay test failed on Platform Left [C1] (port 9002), Platform Right [C2] (port 9004).",
	)

	expected := "Unicast mode: listening on 0.0.0.0.\nReplay receiver: localhost (127.0.0.1).\nError: fake replay test failed on Platform Left [C1] (port 9002), Platform Right [C2] (port 9004)."
	if combined != expected {
		t.Fatalf("unexpected combined messages:\nexpected: %q\nactual:   %q", expected, combined)
	}
}

func TestOrderedStartupScanMessagesPlacesOwlcmsBeforeCameraWarning(t *testing.T) {
	combined := orderedStartupScanMessages(3, []startupScanResult{
		{order: 2, text: "Error: fake replay test failed on Platform Left [C1] (port 9002)."},
		{order: 0, text: "Unicast mode: listening on 0.0.0.0.\nReplay receiver: localhost (127.0.0.1)."},
		{order: 1, text: "Error: Could not find owlcms server - MQTT broker not found on the network"},
	})

	expected := "Unicast mode: listening on 0.0.0.0.\nReplay receiver: localhost (127.0.0.1).\nError: Could not find owlcms server - MQTT broker not found on the network\nError: fake replay test failed on Platform Left [C1] (port 9002)."
	if combined != expected {
		t.Fatalf("unexpected ordered startup messages:\nexpected: %q\nactual:   %q", expected, combined)
	}
}

func TestApplyStartupScanResultShowsCameraSuccessBeforeOwlcmsFinishes(t *testing.T) {
	messages := []string{
		"Unicast mode: listening on 0.0.0.0.\nReplay receiver: localhost (127.0.0.1).",
		"Scanning for owlcms server...",
		"Testing Cameras Module streams...",
	}

	combined := applyStartupScanResult(messages, startupScanResult{order: 2, text: startupCameraProbeSuccessText})

	expected := "Unicast mode: listening on 0.0.0.0.\nReplay receiver: localhost (127.0.0.1).\nScanning for owlcms server...\nCameras Module streams: fake replay test completed."
	if combined != expected {
		t.Fatalf("unexpected incremental startup messages:\nexpected: %q\nactual:   %q", expected, combined)
	}

	if messages[2] != startupCameraProbeSuccessText {
		t.Fatalf("expected camera scan message to be updated in place, got %q", messages[2])
	}
}

func TestApplyStartupScanResultShowsMQTTSuccessAddress(t *testing.T) {
	messages := []string{
		"Unicast mode: listening on 0.0.0.0.\nReplay receiver: localhost (127.0.0.1).",
		"Scanning for owlcms server...",
		"Testing Cameras Module streams...",
	}

	combined := applyStartupScanResult(messages, startupScanResult{order: 1, text: startupMQTTProbeSuccessText("192.168.1.174")})

	expected := "Unicast mode: listening on 0.0.0.0.\nReplay receiver: localhost (127.0.0.1).\nMQTT server found at 192.168.1.174:1883.\nTesting Cameras Module streams..."
	if combined != expected {
		t.Fatalf("unexpected incremental startup messages:\nexpected: %q\nactual:   %q", expected, combined)
	}

	if messages[1] != "MQTT server found at 192.168.1.174:1883." {
		t.Fatalf("expected mqtt scan message to be updated in place, got %q", messages[1])
	}
}

func TestFormatStartupCameraStreamLabelIncludesNameShortIDAndPort(t *testing.T) {
	label := formatStartupCameraStreamLabel(localCamerasStream{
		Name:       "Platform Left",
		ShortID:    "C1",
		OutputPort: 9002,
	})

	expected := "Platform Left [C1] (port 9002)"
	if label != expected {
		t.Fatalf("expected %q, got %q", expected, label)
	}
}

func TestCameraStreamProbeFailureTextListsMissingStreams(t *testing.T) {
	message := cameraStreamProbeFailureText([]string{
		"Platform Left [C1] (port 9002)",
		"Platform Right [C2] (port 9004)",
	})

	expected := "Error: fake replay test failed on Platform Left [C1] (port 9002), Platform Right [C2] (port 9004)."
	if message != expected {
		t.Fatalf("expected %q, got %q", expected, message)
	}
}
