package replays

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/owlcms/video/internal/config"
)

func TestProbeConfiguredCameraStreamsReportsMissingStreams(t *testing.T) {
	activePort := reserveUDPPort(t)
	inactivePort := reserveUDPPort(t)

	cameras := []config.CameraConfiguration{
		{FfmpegCamera: fmt.Sprintf("udp://127.0.0.1:%d", activePort)},
		{FfmpegCamera: fmt.Sprintf("udp://127.0.0.1:%d", inactivePort)},
	}

	senderErr := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		conn, err := net.Dial("udp4", fmt.Sprintf("127.0.0.1:%d", activePort))
		if err != nil {
			senderErr <- err
			return
		}
		defer conn.Close()
		_, err = conn.Write([]byte{0x01})
		senderErr <- err
	}()

	missing := probeConfiguredCameraStreams(cameras, 500*time.Millisecond)
	if err := <-senderErr; err != nil {
		t.Fatalf("send packet to active port: %v", err)
	}

	expected := fmt.Sprintf("camera 2 (port %d)", inactivePort)
	if len(missing) != 1 || missing[0] != expected {
		t.Fatalf("expected only %q missing, got %#v", expected, missing)
	}
}

func TestCombineStartupMessagesKeepsUnicastFirst(t *testing.T) {
	combined := combineStartupMessages(
		"Unicast mode: listening on 0.0.0.0. The sending Cameras Module must list this machine in its destinations.",
		"Error: no camera stream packets detected at startup on Platform Left [C1] (port 9002), Platform Right [C2] (port 9004).",
	)

	expected := "Unicast mode: listening on 0.0.0.0. The sending Cameras Module must list this machine in its destinations.\nError: no camera stream packets detected at startup on Platform Left [C1] (port 9002), Platform Right [C2] (port 9004)."
	if combined != expected {
		t.Fatalf("unexpected combined messages:\nexpected: %q\nactual:   %q", expected, combined)
	}
}

func TestOrderedStartupScanMessagesPlacesOwlcmsBeforeCameraWarning(t *testing.T) {
	combined := orderedStartupScanMessages(3, []startupScanResult{
		{order: 2, text: "Error: no camera stream packets detected at startup on Platform Left [C1] (port 9002)."},
		{order: 0, text: "Unicast mode: listening on 0.0.0.0. The sending Cameras Module must list this machine in its destinations."},
		{order: 1, text: "Error: Could not find owlcms server - MQTT broker not found on the network"},
	})

	expected := "Unicast mode: listening on 0.0.0.0. The sending Cameras Module must list this machine in its destinations.\nError: Could not find owlcms server - MQTT broker not found on the network\nError: no camera stream packets detected at startup on Platform Left [C1] (port 9002)."
	if combined != expected {
		t.Fatalf("unexpected ordered startup messages:\nexpected: %q\nactual:   %q", expected, combined)
	}
}

func TestApplyStartupScanResultShowsCameraSuccessBeforeOwlcmsFinishes(t *testing.T) {
	messages := []string{
		"Unicast mode: listening on 0.0.0.0. The sending Cameras Module must list this machine in its destinations.",
		"Scanning for owlcms server...",
		"Scanning Cameras Module streams...",
	}

	combined := applyStartupScanResult(messages, startupScanResult{order: 2, text: startupCameraProbeSuccessText})

	expected := "Unicast mode: listening on 0.0.0.0. The sending Cameras Module must list this machine in its destinations.\nScanning for owlcms server...\nCameras Module streams: all streams are producing data."
	if combined != expected {
		t.Fatalf("unexpected incremental startup messages:\nexpected: %q\nactual:   %q", expected, combined)
	}

	if messages[2] != startupCameraProbeSuccessText {
		t.Fatalf("expected camera scan message to be updated in place, got %q", messages[2])
	}
}

func TestApplyStartupScanResultShowsMQTTSuccessAddress(t *testing.T) {
	messages := []string{
		"Unicast mode: listening on 0.0.0.0. The sending Cameras Module must list this machine in its destinations.",
		"Scanning for owlcms server...",
		"Scanning Cameras Module streams...",
	}

	combined := applyStartupScanResult(messages, startupScanResult{order: 1, text: startupMQTTProbeSuccessText("192.168.1.174")})

	expected := "Unicast mode: listening on 0.0.0.0. The sending Cameras Module must list this machine in its destinations.\nMQTT server found at 192.168.1.174:1883.\nScanning Cameras Module streams..."
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

	expected := "Error: no camera stream packets detected at startup on Platform Left [C1] (port 9002), Platform Right [C2] (port 9004)."
	if message != expected {
		t.Fatalf("expected %q, got %q", expected, message)
	}
}

func reserveUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	defer conn.Close()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("expected UDP address, got %T", conn.LocalAddr())
	}
	return addr.Port
}
