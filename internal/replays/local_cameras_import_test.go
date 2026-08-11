package replays

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/owlcms/video/internal/config"
	camerascfg "github.com/owlcms/video/internal/config/cameras"
)

func TestLoadLocalCamerasImportPreviewUsesPassiveListenerForUnicast(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "cameras.toml")
	content := `
[multicast]
ip = "239.255.0.1"
startPort = 9001

[unicast]
enabled = true
startPort = 9001

[[unicast.destinations]]
address = "127.0.0.1"
enabled = true

[[deviceAssignment]]
matchKey = "usb-1"
name = "Platform Right"
shortId = "C2"
outputPort = 9002
on = true

[[deviceAssignment]]
matchKey = "usb-2"
name = "Platform Left"
shortId = "C1"
outputPort = 9001
on = true

[[rtsp]]
sourceId = "rtsp-a"
name = "Side Angle"
shortId = "R1"
enabled = true
on = true
rtspUrl = "rtsp://127.0.0.1:8554/side"
outputPort = 9005
transport = "tcp"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write cameras.toml: %v", err)
	}

	preview, err := loadLocalCamerasImportPreview(configPath)
	if err != nil {
		t.Fatalf("load import preview: %v", err)
	}

	if preview.Mode != "unicast" {
		t.Fatalf("expected unicast mode, got %q", preview.Mode)
	}
	if preview.ListenIP != "0.0.0.0" {
		t.Fatalf("expected passive listener IP 0.0.0.0, got %q", preview.ListenIP)
	}
	if !preview.CompatibilityAllowed {
		t.Fatalf("expected unicast preview to be compatible, got message %q", preview.CompatibilityMessage)
	}
	if preview.CamerasAddressLabel != "Cameras unicast address" {
		t.Fatalf("unexpected cameras address label: %q", preview.CamerasAddressLabel)
	}
	if preview.CamerasAddressValue != "127.0.0.1" {
		t.Fatalf("expected matched local unicast address 127.0.0.1, got %q", preview.CamerasAddressValue)
	}
	if preview.ReplaysAddressValue != "0.0.0.0" {
		t.Fatalf("expected replays listening value 0.0.0.0, got %q", preview.ReplaysAddressValue)
	}
	if len(preview.ImportedStreams) != 3 {
		t.Fatalf("expected 3 imported streams, got %d", len(preview.ImportedStreams))
	}
	if preview.ImportedStreams[0].ShortID != "C1" || preview.ImportedStreams[1].ShortID != "C2" || preview.ImportedStreams[2].ShortID != "R1" {
		t.Fatalf("unexpected stream order: %#v", preview.ImportedStreams)
	}
	if len(preview.EnabledDestinations) != 1 || preview.EnabledDestinations[0] != "127.0.0.1" {
		t.Fatalf("unexpected enabled destinations: %#v", preview.EnabledDestinations)
	}
}

func TestLoadLocalCamerasImportPreviewAddsLoopbackUnicastDestination(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "cameras.toml")
	content := `
[multicast]
ip = "239.255.0.1"
startPort = 9001

[unicast]
enabled = true
startPort = 9001

[[unicast.destinations]]
address = "203.0.113.10"
enabled = true

[[rtsp]]
sourceId = "rtsp-a"
name = "Remote Angle"
shortId = "R1"
enabled = true
on = true
rtspUrl = "rtsp://203.0.113.10:8554/remote"
outputPort = 9005
transport = "tcp"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write cameras.toml: %v", err)
	}

	preview, err := loadLocalCamerasImportPreview(configPath)
	if err != nil {
		t.Fatalf("load import preview: %v", err)
	}

	if !preview.CompatibilityAllowed {
		t.Fatalf("expected unicast preview to be compatible, got message %q", preview.CompatibilityMessage)
	}
	if preview.CamerasAddressValue != "127.0.0.1" {
		t.Fatalf("expected loopback cameras address, got %q", preview.CamerasAddressValue)
	}
	if len(preview.EnabledDestinations) != 2 || preview.EnabledDestinations[0] != "127.0.0.1" || preview.EnabledDestinations[1] != "203.0.113.10" {
		t.Fatalf("expected loopback destination to be added, got %#v", preview.EnabledDestinations)
	}
}

func TestCollectLocalCamerasStreamsSkipsDisabledSources(t *testing.T) {
	cfg := &camerascfg.Config{
		DeviceAssignments: []camerascfg.DeviceAssignment{
			{Name: "Enabled USB", ShortID: "C1", OutputPort: 9001},
			{Name: "Disabled USB", ShortID: "C2", OutputPort: 9002, Disabled: true},
		},
		RTSPSources: []camerascfg.RTSPSource{
			{Name: "Enabled RTSP", ShortID: "R1", OutputPort: 9005, Enabled: true},
			{Name: "Disabled RTSP", ShortID: "R2", OutputPort: 9006, Enabled: false},
		},
	}

	streams := collectLocalCamerasStreams(cfg)
	if len(streams) != 2 {
		t.Fatalf("expected 2 streams, got %d", len(streams))
	}
	if streams[0].ShortID != "C1" || streams[1].ShortID != "R1" {
		t.Fatalf("unexpected collected streams: %#v", streams)
	}
}

func TestLoadRemoteCamerasImportPreviewFetchesCamerasEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/cameras/config" {
			t.Errorf("expected cameras config endpoint, got %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/toml")
		_, _ = writer.Write([]byte(`
[multicast]
ip = "239.44.0.1"

[[deviceAssignment]]
name = "Remote Camera"
shortId = "C1"
outputPort = 9011
on = true
`))
	}))
	defer server.Close()

	preview, err := loadRemoteCamerasImportPreview(server.URL)
	if err != nil {
		t.Fatalf("load remote config: %v", err)
	}
	if preview.ListenIP != "239.44.0.1" {
		t.Fatalf("expected remote multicast IP, got %q", preview.ListenIP)
	}
	if len(preview.ImportedStreams) != 1 || preview.ImportedStreams[0].OutputPort != 9011 {
		t.Fatalf("unexpected imported streams: %#v", preview.ImportedStreams)
	}
}

func TestImportedMpegTSSettingsReplacePriorPorts(t *testing.T) {
	preview := buildCamerasImportPreview(&camerascfg.Config{
		Multicast: camerascfg.MulticastConfig{IP: "239.44.0.1"},
		DeviceAssignments: []camerascfg.DeviceAssignment{
			{Name: "Camera 2", ShortID: "C2", OutputPort: 9012, On: boolPointer(true)},
			{Name: "Camera 1", ShortID: "C1", OutputPort: 9011, On: boolPointer(true)},
		},
	}, "remote")

	settings, err := importedMpegTSSettings(config.MulticastSettings{
		IP:          "239.255.0.1",
		Camera1Port: 8001,
		Camera2Port: 8002,
		Camera3Port: 8003,
		Camera4Port: 8004,
	}, preview, preview.ImportedStreams)
	if err != nil {
		t.Fatalf("build imported settings: %v", err)
	}
	if settings.IP != "239.44.0.1" {
		t.Fatalf("expected fetched IP, got %q", settings.IP)
	}
	if settings.Camera1Port != 9011 || settings.Camera2Port != 9012 || settings.Camera3Port != 0 || settings.Camera4Port != 0 {
		t.Fatalf("expected fetched ports to replace prior ports, got %#v", settings)
	}
}

func TestSelectedCamerasFirstPreservesConfiguredReplayOrder(t *testing.T) {
	preview := buildCamerasImportPreview(&camerascfg.Config{
		Multicast: camerascfg.MulticastConfig{IP: "239.44.0.1"},
		DeviceAssignments: []camerascfg.DeviceAssignment{
			{Name: "Camera 1", ShortID: "C1", OutputPort: 9011, On: boolPointer(true)},
			{Name: "Camera 2", ShortID: "C2", OutputPort: 9012, On: boolPointer(true)},
			{Name: "Camera 3", ShortID: "C3", OutputPort: 9013, On: boolPointer(true)},
		},
	}, "local")

	selected, available := selectedCamerasFirst(preview, config.MulticastSettings{
		Camera1Port: 9013,
		Camera2Port: 9011,
	})
	if len(selected) != 2 || selected[0].OutputPort != 9013 || selected[1].OutputPort != 9011 {
		t.Fatalf("expected configured order at the top, got %#v", selected)
	}
	if len(available) != 1 || available[0].OutputPort != 9012 {
		t.Fatalf("expected remaining camera below selected cameras, got %#v", available)
	}
}

func boolPointer(value bool) *bool {
	return &value
}
