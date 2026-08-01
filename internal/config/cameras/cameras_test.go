package cameras

import (
	"strings"
	"testing"
)

func TestEnsureSourceIDsKeepsRTSPSourcesUnique(t *testing.T) {
	cfg := &Config{
		RTSPSources: []RTSPSource{
			{RTSPURL: "rtsp://192.168.1.10:8554/live", Transport: "tcp"},
			{RTSPURL: "rtsp://192.168.1.10:8554/live", Transport: "udp"},
			{SourceID: "custom-source", RTSPURL: "rtsp://192.168.1.11:8554/live", Transport: "tcp"},
			{SourceID: "custom-source", RTSPURL: "rtsp://192.168.1.12:8554/live", Transport: "tcp"},
		},
	}

	cfg.ensureSourceIDs()

	seen := make(map[string]struct{}, len(cfg.RTSPSources))
	for i, src := range cfg.RTSPSources {
		if src.SourceID == "" {
			t.Fatalf("source %d has empty source ID", i)
		}
		if _, exists := seen[src.SourceID]; exists {
			t.Fatalf("source %d reused duplicate source ID %q", i, src.SourceID)
		}
		seen[src.SourceID] = struct{}{}
	}

	if cfg.RTSPSources[2].SourceID != "custom-source" {
		t.Fatalf("expected first explicit source ID to be preserved, got %q", cfg.RTSPSources[2].SourceID)
	}
	if cfg.RTSPSources[3].SourceID == "custom-source" {
		t.Fatalf("expected duplicate explicit source ID to be reassigned")
	}
	if cfg.RTSPSources[0].SourceID == cfg.RTSPSources[1].SourceID {
		t.Fatalf("expected duplicate RTSP URLs to receive unique source IDs")
	}
}

func TestApplyDefaultsMarksUnprobedRTSPSourceDirty(t *testing.T) {
	cfg := &Config{
		RTSPSources: []RTSPSource{
			{RTSPURL: "rtsp://192.168.1.10:8554/live"},
			{RTSPURL: "rtsp://192.168.1.11:8554/live", Codec: "h264", ProbeSize: "1920x1080", ProbeFPS: 30},
		},
	}

	cfg.applyDefaults()

	if !cfg.RTSPSources[0].ProbeDirty {
		t.Fatalf("expected unprobed RTSP source to be marked dirty")
	}
	if cfg.RTSPSources[1].ProbeDirty {
		t.Fatalf("expected previously probed RTSP source to stay clean")
	}
}

func TestApplyDefaultsMarksUnprobedDeviceAssignmentDirty(t *testing.T) {
	cfg := &Config{
		DeviceAssignments: []DeviceAssignment{
			{MatchKey: "usb-1"},
			{MatchKey: "usb-2", ProbePixelFormat: "mjpeg", ProbeSize: "1920x1080", ProbeFPS: 30},
		},
	}

	cfg.applyDefaults()

	if len(cfg.DeviceAssignments[0].DirtyReasons) == 0 || cfg.DeviceAssignments[0].DirtyReasons[0] != "probe" {
		t.Fatalf("expected unprobed device assignment to be marked dirty, got %v", cfg.DeviceAssignments[0].DirtyReasons)
	}
	if len(cfg.DeviceAssignments[1].DirtyReasons) != 0 {
		t.Fatalf("expected previously probed device assignment to stay clean, got %v", cfg.DeviceAssignments[1].DirtyReasons)
	}
}

func TestApplyDefaultsSetsMonitoringOnForLegacyConfig(t *testing.T) {
	cfg := &Config{
		DeviceAssignments: []DeviceAssignment{{MatchKey: "usb-1"}},
		RTSPSources:       []RTSPSource{{RTSPURL: "rtsp://192.168.1.10:8554/live"}},
	}

	cfg.applyDefaults()

	if cfg.DeviceAssignments[0].On == nil || !*cfg.DeviceAssignments[0].On {
		t.Fatalf("expected legacy USB assignment to default on=true, got %#v", cfg.DeviceAssignments[0].On)
	}
	if cfg.RTSPSources[0].On == nil || !*cfg.RTSPSources[0].On {
		t.Fatalf("expected legacy RTSP source to default on=true, got %#v", cfg.RTSPSources[0].On)
	}
}

func TestSerializeIncludesMonitoringOnFlag(t *testing.T) {
	cfg := &Config{
		Multicast: MulticastConfig{IP: "239.255.0.1", StartPort: 9001},
		Unicast:   UnicastConfig{StartPort: 9001},
		DeviceAssignments: []DeviceAssignment{{
			MatchKey:   "usb-1",
			Name:       "USB",
			ShortID:    "C1",
			OutputPort: 9001,
			On:         boolPtr(false),
		}},
		RTSPSources: []RTSPSource{{
			SourceID:   "rtsp-1",
			Name:       "RTSP",
			Enabled:    true,
			On:         boolPtr(false),
			RTSPURL:    "rtsp://192.168.1.11:8554/live",
			OutputPort: 9005,
			Transport:  "tcp",
		}},
	}

	serialized := cfg.serialize()

	if !strings.Contains(serialized, "    on = false") {
		t.Fatalf("expected serialized config to contain on = false, got:\n%s", serialized)
	}
	if !strings.Contains(serialized, "    enabled = true\n    on = false") {
		t.Fatalf("expected RTSP enabled/on semantics in serialized config, got:\n%s", serialized)
	}
}

func TestUnicastTeeOutputSkipsDisabledAndBlankDestinations(t *testing.T) {
	cfg := &UnicastConfig{
		Destinations: []UnicastDestination{
			{Address: " 127.0.0.1 ", Enabled: true},
			{Address: "192.0.2.44", Enabled: false},
			{Address: "   ", Enabled: true},
		},
	}

	got := cfg.TeeOutput(9001)
	want := "[f=mpegts:onfail=ignore]udp://127.0.0.1:9001?pkt_size=1316"
	if got != want {
		t.Fatalf("TeeOutput() = %q, want %q", got, want)
	}
}

func TestUnicastTeeOutputAlwaysIncludesPreviewLoopback(t *testing.T) {
	cfg := &UnicastConfig{
		Destinations: []UnicastDestination{
			{Address: "192.0.2.44", Enabled: true},
			{Address: "localhost", Enabled: true},
		},
	}

	got := cfg.TeeOutput(9001)
	want := "[f=mpegts:onfail=ignore]udp://127.0.0.1:9001?pkt_size=1316|[f=mpegts:onfail=ignore]udp://192.0.2.44:9001?pkt_size=1316"
	if got != want {
		t.Fatalf("TeeOutput() = %q, want %q", got, want)
	}
}

func TestClearRestartDirtyReasonsRemovesRestartOnly(t *testing.T) {
	cfg := &Config{
		DeviceAssignments: []DeviceAssignment{{
			MatchKey:     "usb-1",
			DirtyReasons: []string{"probe", "restart"},
		}},
		RTSPSources: []RTSPSource{{
			SourceID:     "rtsp-1",
			DirtyReasons: []string{"restart"},
		}},
	}

	changed := cfg.ClearRestartDirtyReasons()
	if !changed {
		t.Fatalf("expected restart dirty reasons to be cleared")
	}
	if got := cfg.DeviceAssignments[0].DirtyReasons; len(got) != 1 || got[0] != "probe" {
		t.Fatalf("expected usb dirty reasons to preserve probe only, got %v", got)
	}
	if got := cfg.RTSPSources[0].DirtyReasons; len(got) != 0 {
		t.Fatalf("expected rtsp dirty reasons to be empty, got %v", got)
	}

	if cfg.ClearRestartDirtyReasons() {
		t.Fatalf("expected second clear to report no changes")
	}
}
