package cameras

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/owlcms/video/internal/config"
	camerascfg "github.com/owlcms/video/internal/config/cameras"
	ffmpegcfg "github.com/owlcms/video/internal/config/ffmpeg"
	"github.com/owlcms/video/internal/recording"
)

func TestRTSPRetryDelay(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: 2 * time.Second},
		{attempt: 1, want: 4 * time.Second},
		{attempt: 2, want: 8 * time.Second},
		{attempt: 3, want: 8 * time.Second},
	}

	for _, tc := range tests {
		if got := rtspRetryDelay(tc.attempt); got != tc.want {
			t.Fatalf("rtspRetryDelay(%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

func TestRTSPRetryWindow(t *testing.T) {
	tests := []struct {
		name           string
		retriesStarted int
		wantDelay      time.Duration
		wantRetry      bool
	}{
		{name: "first retry uses 2 seconds", retriesStarted: 0, wantDelay: 2 * time.Second, wantRetry: true},
		{name: "second retry uses 4 seconds", retriesStarted: 1, wantDelay: 4 * time.Second, wantRetry: true},
		{name: "third retry uses 8 seconds", retriesStarted: 2, wantDelay: 8 * time.Second, wantRetry: true},
		{name: "fourth retry is blocked", retriesStarted: 3, wantDelay: 0, wantRetry: false},
	}

	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			gotDelay, gotRetry := rtspRetryWindow(tc.retriesStarted)
			if gotRetry != tc.wantRetry {
				t.Fatalf("rtspRetryWindow(%d) retry = %t, want %t", tc.retriesStarted, gotRetry, tc.wantRetry)
			}
			if gotDelay != tc.wantDelay {
				t.Fatalf("rtspRetryWindow(%d) delay = %s, want %s", tc.retriesStarted, gotDelay, tc.wantDelay)
			}
		})
	}
}

func TestDisableUnreachableUnicastDestinationsMarksFailuresDisabled(t *testing.T) {
	cfg := &camerascfg.Config{
		Unicast: camerascfg.UnicastConfig{
			Enabled: true,
			Destinations: []camerascfg.UnicastDestination{
				{Address: "127.0.0.1", Enabled: true},
				{Address: "192.0.2.44", Enabled: true},
				{Address: "   ", Enabled: true},
				{Address: "192.0.2.45", Enabled: false},
			},
		},
	}

	checked := make([]string, 0)
	checker := func(address string, port int) error {
		checked = append(checked, address)
		if address == "192.0.2.44" {
			return errors.New("network unreachable")
		}
		return nil
	}

	reachable, issues, changed := disableUnreachableUnicastDestinations(cfg, 9001, checker)

	if !changed {
		t.Fatal("expected changed=true when destinations are disabled")
	}
	if len(issues) != 2 {
		t.Fatalf("expected two disabled destination issues, got %d: %#v", len(issues), issues)
	}
	if len(reachable) != 1 || reachable[0].Address != "127.0.0.1" {
		t.Fatalf("expected only localhost to remain reachable, got %#v", reachable)
	}
	if !cfg.Unicast.Destinations[0].Enabled {
		t.Fatal("expected reachable destination to remain enabled")
	}
	if cfg.Unicast.Destinations[1].Enabled {
		t.Fatal("expected unreachable destination to be disabled")
	}
	if cfg.Unicast.Destinations[2].Enabled {
		t.Fatal("expected blank destination to be disabled")
	}
	if cfg.Unicast.Destinations[3].Enabled {
		t.Fatal("expected already disabled destination to stay disabled")
	}
	if strings.Join(checked, ",") != "127.0.0.1,192.0.2.44" {
		t.Fatalf("unexpected reachability checks: %v", checked)
	}
}

func TestFormatUnicastReachabilityWarning(t *testing.T) {
	issues := []unicastReachabilityIssue{
		{Address: "192.0.2.44", Err: errors.New("network unreachable")},
		{Address: "(blank)", Err: errors.New("empty destination address")},
	}

	got := formatUnicastReachabilityWarning(issues, 9001)
	want := "Disabled 2 unreachable unicast destinations: 192.0.2.44:9001 (network unreachable); (blank):9001 (empty destination address)"
	if got != want {
		t.Fatalf("formatUnicastReachabilityWarning() = %q, want %q", got, want)
	}
}

func TestTCPDialErrorMeansReachable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "connection refused means host reachable",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)},
			want: true,
		},
		{
			name: "connection reset means host reachable",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNRESET)},
			want: true,
		},
		{
			name: "network unreachable stays unreachable",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tcpDialErrorMeansReachable(tc.err); got != tc.want {
				t.Fatalf("tcpDialErrorMeansReachable(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

func TestCameraStreamAutoRestartReason(t *testing.T) {
	now := time.Date(2026, time.April, 14, 20, 0, 0, 0, time.UTC)

	tests := []struct {
		name           string
		stream         cameraStream
		startupDelay   time.Duration
		wantRestart    bool
		wantReasonPart string
	}{
		{
			name: "rtsp without fps after startup delay restarts",
			stream: cameraStream{
				sourceType: "rtsp",
				cmd:        &exec.Cmd{},
				running:    true,
				status:     "running",
				startTime:  now.Add(-3 * time.Second),
			},
			startupDelay:   2 * time.Second,
			wantRestart:    true,
			wantReasonPart: "no stream progress",
		},
		{
			name: "rtsp with recent progress before fps stays running",
			stream: cameraStream{
				sourceType:     "rtsp",
				cmd:            &exec.Cmd{},
				running:        true,
				status:         "running",
				startTime:      now.Add(-3 * time.Second),
				progressSeen:   true,
				progressSeenAt: now.Add(-500 * time.Millisecond),
			},
			startupDelay: 2 * time.Second,
			wantRestart:  false,
		},
		{
			name: "healthy rtsp stays running",
			stream: cameraStream{
				sourceType: "rtsp",
				cmd:        &exec.Cmd{},
				running:    true,
				status:     "running",
				fps:        "29.97",
				startTime:  now.Add(-10 * time.Second),
			},
			startupDelay: 2 * time.Second,
			wantRestart:  false,
		},
		{
			name: "unexpected rtsp exit triggers recovery",
			stream: cameraStream{
				sourceType: "rtsp",
				status:     "stopped: exit status 1",
				startTime:  now.Add(-5 * time.Second),
			},
			startupDelay:   4 * time.Second,
			wantRestart:    true,
			wantReasonPart: "ffmpeg exited",
		},
		{
			name: "rtsp waits until startup delay expires",
			stream: cameraStream{
				sourceType: "rtsp",
				cmd:        &exec.Cmd{},
				running:    true,
				status:     "running",
				startTime:  now.Add(-1500 * time.Millisecond),
			},
			startupDelay: 2 * time.Second,
			wantRestart:  false,
		},
		{
			name: "non rtsp stream is ignored",
			stream: cameraStream{
				sourceType: "usb",
				cmd:        &exec.Cmd{},
				running:    true,
				status:     "running",
				startTime:  now.Add(-30 * time.Second),
			},
			startupDelay: 2 * time.Second,
			wantRestart:  false,
		},
		{
			name: "intentional stop is ignored",
			stream: cameraStream{
				sourceType: "rtsp",
				status:     "stopped: exit status 1",
				startTime:  now.Add(-10 * time.Second),
				stopping:   true,
			},
			startupDelay: 2 * time.Second,
			wantRestart:  false,
		},
	}

	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := tc.stream.autoRestartReason(now, tc.startupDelay)
			if ok != tc.wantRestart {
				t.Fatalf("autoRestartReason() restart = %t, want %t (reason=%q)", ok, tc.wantRestart, reason)
			}
			if tc.wantReasonPart != "" && !strings.Contains(reason, tc.wantReasonPart) {
				t.Fatalf("autoRestartReason() reason = %q, want substring %q", reason, tc.wantReasonPart)
			}
		})
	}
}

func TestMonitoringSourceNeedsRestart(t *testing.T) {
	now := time.Date(2026, time.April, 14, 20, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		spec   sourceSpec
		stream *cameraStream
		want   bool
	}{
		{
			name: "config dirty highlights restart",
			spec: sourceSpec{
				SourceType:   "rtsp",
				Enabled:      true,
				OutputPort:   9005,
				DirtyReasons: []string{"restart"},
			},
			want: true,
		},
		{
			name: "enabled source without stream highlights restart",
			spec: sourceSpec{
				SourceType: "rtsp",
				Enabled:    true,
				OutputPort: 9005,
			},
			want: true,
		},
		{
			name: "rtsp without fps after grace highlights restart",
			spec: sourceSpec{
				SourceType: "rtsp",
				Enabled:    true,
				OutputPort: 9005,
			},
			stream: &cameraStream{
				sourceType: "rtsp",
				cmd:        &exec.Cmd{},
				running:    true,
				status:     "running",
				startTime:  now.Add(-3 * time.Second),
			},
			want: true,
		},
		{
			name: "rtsp with recent progress before fps does not highlight",
			spec: sourceSpec{
				SourceType: "rtsp",
				Enabled:    true,
				OutputPort: 9005,
			},
			stream: &cameraStream{
				sourceType:     "rtsp",
				cmd:            &exec.Cmd{},
				running:        true,
				status:         "running",
				startTime:      now.Add(-3 * time.Second),
				progressSeen:   true,
				progressSeenAt: now.Add(-500 * time.Millisecond),
			},
			want: false,
		},
		{
			name: "rtsp still in startup grace does not highlight",
			spec: sourceSpec{
				SourceType: "rtsp",
				Enabled:    true,
				OutputPort: 9005,
			},
			stream: &cameraStream{
				sourceType: "rtsp",
				cmd:        &exec.Cmd{},
				running:    true,
				status:     "running",
				startTime:  now.Add(-1500 * time.Millisecond),
			},
			want: false,
		},
		{
			name: "healthy rtsp does not highlight",
			spec: sourceSpec{
				SourceType: "rtsp",
				Enabled:    true,
				OutputPort: 9005,
			},
			stream: &cameraStream{
				sourceType: "rtsp",
				cmd:        &exec.Cmd{},
				running:    true,
				status:     "running",
				fps:        "30.00",
				startTime:  now.Add(-5 * time.Second),
			},
			want: false,
		},
		{
			name: "latched timeout stays highlighted during restart grace",
			spec: sourceSpec{
				SourceType: "rtsp",
				Enabled:    true,
				OutputPort: 9005,
			},
			stream: &cameraStream{
				sourceType: "rtsp",
				cmd:        &exec.Cmd{},
				running:    true,
				status:     "running",
				startTime:  now.Add(-1 * time.Second),
			},
			want: true,
		},
	}

	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			var recovery *rtspRecoveryState
			if tc.name == "latched timeout stays highlighted during restart grace" {
				recovery = &rtspRecoveryState{attention: true, reason: "no measured FPS after 2s"}
			}
			if got := monitoringSourceNeedsRestart(tc.spec, tc.stream, now, recovery); got != tc.want {
				t.Fatalf("monitoringSourceNeedsRestart() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestPreviewWindowSize(t *testing.T) {
	tests := []struct {
		name       string
		width      int
		height     int
		wantWidth  int
		wantHeight int
		wantOK     bool
	}{
		{name: "portrait 4k is capped", width: 2160, height: 3840, wantWidth: 540, wantHeight: 960, wantOK: true},
		{name: "landscape full hd is capped to half size", width: 1920, height: 1080, wantWidth: 960, wantHeight: 540, wantOK: true},
		{name: "small preview stays native", width: 640, height: 480, wantWidth: 640, wantHeight: 480, wantOK: true},
		{name: "invalid size is rejected", width: 0, height: 480, wantWidth: 0, wantHeight: 0, wantOK: false},
	}

	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			gotWidth, gotHeight, gotOK := previewWindowSize(tc.width, tc.height)
			if gotOK != tc.wantOK {
				t.Fatalf("previewWindowSize(%d, %d) ok = %t, want %t", tc.width, tc.height, gotOK, tc.wantOK)
			}
			if gotWidth != tc.wantWidth || gotHeight != tc.wantHeight {
				t.Fatalf("previewWindowSize(%d, %d) = %dx%d, want %dx%d", tc.width, tc.height, gotWidth, gotHeight, tc.wantWidth, tc.wantHeight)
			}
		})
	}
}

func TestShouldIgnoreFFplayStderrLine(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{name: "mpegts packet corrupt", message: "[mpegts @ 0xa2af05400] Packet corrupt (stream = 0, dts = 31627158).", want: true},
		{name: "packet corrupt with progress", message: "[mpegts @ 0xa2af05400] Packet corrupt (stream = 0, dts = 3611541). 39.92 M-V: 0.000", want: true},
		{name: "case insensitive", message: "PACKET CORRUPT", want: true},
		{name: "real error", message: "udp://127.0.0.1:9001: Input/output error", want: false},
		{name: "input header", message: "Input #0, mpegts, from 'udp://127.0.0.1:9001':", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldIgnoreFFplayStderrLine(tc.message); got != tc.want {
				t.Fatalf("shouldIgnoreFFplayStderrLine(%q) = %t, want %t", tc.message, got, tc.want)
			}
		})
	}
}

func TestPreviewArgsForSize(t *testing.T) {
	tests := []struct {
		name string
		size string
		want []string
	}{
		{
			name: "portrait size uses capped window",
			size: "2160x3840",
			want: []string{"-x", "540", "-y", "960"},
		},
		{
			name: "unknown size uses bounded scaling fallback",
			size: "-",
			want: []string{"-x", "960", "-y", "960", "-vf", "scale=960:960:force_original_aspect_ratio=decrease"},
		},
	}

	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			got := previewArgsForSize(tc.size)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("previewArgsForSize(%q) = %v, want %v", tc.size, got, tc.want)
			}
		})
	}
}

func TestEnabledUnicastDestinationsAlwaysIncludePreviewLoopback(t *testing.T) {
	destinations := []camerascfg.UnicastDestination{
		{Address: "192.0.2.44", Enabled: true},
		{Address: "localhost", Enabled: true},
		{Address: "   ", Enabled: true},
		{Address: "198.51.100.7", Enabled: false},
	}

	got := enabledUnicastDestinations(destinations)
	if len(got) != 2 {
		t.Fatalf("enabledUnicastDestinations() returned %d destinations, want 2: %#v", len(got), got)
	}
	if got[0].Address != camerascfg.PreviewLoopbackAddress {
		t.Fatalf("enabledUnicastDestinations()[0] = %q, want %q", got[0].Address, camerascfg.PreviewLoopbackAddress)
	}
	if got[1].Address != "192.0.2.44" {
		t.Fatalf("enabledUnicastDestinations()[1] = %q, want 192.0.2.44", got[1].Address)
	}
}

func TestCameraStreamListenURLUsesBroadcastMode(t *testing.T) {
	unicastStream := &cameraStream{port: 9001, udpDest: "ignored", unicastMode: true}
	if got := unicastStream.listenURL(); got != "udp://127.0.0.1:9001" {
		t.Fatalf("listenURL() in unicast mode = %q, want udp://127.0.0.1:9001", got)
	}

	multicastStream := &cameraStream{port: 9001, udpDest: "udp://239.255.0.1:9001?pkt_size=1316", unicastMode: false}
	if got := multicastStream.listenURL(); got != "udp://239.255.0.1:9001?pkt_size=1316" {
		t.Fatalf("listenURL() in multicast mode = %q, want multicast destination", got)
	}
}

func TestParseResolutionFromFFmpegLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
		ok   bool
	}{
		{
			name: "parses ffmpeg input video line",
			line: "  Stream #0:0: Video: h264 (High), yuv420p(progressive), 1920x1080, 30 fps, 30 tbr, 90k tbn",
			want: "1920x1080",
			ok:   true,
		},
		{
			name: "ignores non video lines",
			line: "Input #0, rtsp, from 'rtsp://camera':",
			want: "",
			ok:   false,
		},
		{
			name: "ignores video lines without dimensions",
			line: "Stream #0:0: Video: h264",
			want: "",
			ok:   false,
		},
	}

	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseResolutionFromFFmpegLine(tc.line)
			if ok != tc.ok {
				t.Fatalf("parseResolutionFromFFmpegLine() ok = %t, want %t", ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("parseResolutionFromFFmpegLine() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSimplifyDetectionProgressUsesSourceNameInActivity(t *testing.T) {
	payload := "Laptop Camera"
	update, ok := simplifyDetectionProgress(recording.ProgressMsg(recording.ProgLocalSource, payload))
	if !ok {
		t.Fatal("simplifyDetectionProgress() ok = false, want true")
	}
	expectedStage := renderDetectionProgressText(detectionProgressTemplates[recording.ProgLocalSource].Stage, payload)
	if update.stage != expectedStage {
		t.Fatalf("stage = %q, want %q", update.stage, expectedStage)
	}
	expectedDetail := renderDetectionProgressText(detectionProgressTemplates[recording.ProgLocalSource].Detail, payload)
	if update.detail != expectedDetail {
		t.Fatalf("detail = %q, want %q", update.detail, expectedDetail)
	}
}

func TestSimplifyDetectionProgressShowsStreamGrabActivity(t *testing.T) {
	payload := "CamON"
	update, ok := simplifyDetectionProgress(recording.ProgressMsg(recording.ProgStreamTest, payload))
	if !ok {
		t.Fatal("simplifyDetectionProgress() ok = false, want true")
	}
	expectedStage := renderDetectionProgressText(detectionProgressTemplates[recording.ProgStreamTest].Stage, payload)
	if update.stage != expectedStage {
		t.Fatalf("stage = %q, want %q", update.stage, expectedStage)
	}
	expectedDetail := renderDetectionProgressText(detectionProgressTemplates[recording.ProgStreamTest].Detail, payload)
	if update.detail != expectedDetail {
		t.Fatalf("detail = %q, want %q", update.detail, expectedDetail)
	}
}

func TestSimplifyDetectionProgressMarksSourceFailureAsError(t *testing.T) {
	payload := recording.ProgressDetailPayload("CamON", "timed out after 3s")
	update, ok := simplifyDetectionProgress(recording.ProgressMsg(recording.ProgValidateFailed, payload))
	if !ok {
		t.Fatal("simplifyDetectionProgress() ok = false, want true")
	}
	if !update.hasError {
		t.Fatal("hasError = false, want true")
	}
	expectedStatus := renderDetectionProgressText(detectionProgressTemplates[recording.ProgValidateFailed].StatusMessage, payload)
	if update.statusMessage != expectedStatus {
		t.Fatalf("statusMessage = %q, want %q", update.statusMessage, expectedStatus)
	}
}

func TestSimplifyDetectionProgressKeepsStreamFailureOutOfSourceStatus(t *testing.T) {
	update, ok := simplifyDetectionProgress(recording.ProgressMsg(recording.ProgStreamFailed, recording.ProgressDetailPayload("CamON", "stream validation failed: timed out after 3s")))
	if !ok {
		t.Fatal("simplifyDetectionProgress() ok = false, want true")
	}
	if !update.hasError {
		t.Fatal("hasError = false, want true")
	}
	if update.detail != "Startup failed for CamON: stream validation failed: timed out after 3s" {
		t.Fatalf("detail = %q, want detailed startup failure", update.detail)
	}
	if update.statusKey != "" {
		t.Fatalf("statusKey = %q, want empty so per-source failure is not overwritten", update.statusKey)
	}
	if update.statusMessage != "" {
		t.Fatalf("statusMessage = %q, want empty so generic startup failure stays out of source status", update.statusMessage)
	}
}

func TestRenderProgressActionTextDecodesStructuredStreamFailure(t *testing.T) {
	message := recording.ProgressMsg(recording.ProgStreamFailed, recording.ProgressDetailPayload("CamON", "server failed"))

	got := renderProgressActionText(message)
	if got != "Startup failed for CamON: server failed" {
		t.Fatalf("renderProgressActionText() = %q, want decoded stream failure", got)
	}
	if strings.Contains(got, recording.ProgressSep) {
		t.Fatalf("renderProgressActionText() = %q, should not contain progress separator", got)
	}
	if strings.Contains(got, recording.ProgressDetailSep) {
		t.Fatalf("renderProgressActionText() = %q, should not contain detail separator", got)
	}
}

func TestSimplifyDetectionProgressReportsUnconfiguredEncoder(t *testing.T) {
	payload := recording.ProgressDetailPayload("h264_amf", "no ffmpeg.toml encoder settings")
	update, ok := simplifyDetectionProgress(recording.ProgressMsg(recording.ProgEncoderUnconfigured, payload))
	if !ok {
		t.Fatal("simplifyDetectionProgress() ok = false, want true")
	}
	expectedDetail := renderDetectionProgressText(detectionProgressTemplates[recording.ProgEncoderUnconfigured].Detail, payload)
	if update.detail != expectedDetail {
		t.Fatalf("detail = %q, want unconfigured encoder detail", update.detail)
	}
	expectedStatus := renderDetectionProgressText(detectionProgressTemplates[recording.ProgEncoderUnconfigured].StatusMessage, payload)
	if update.statusMessage != expectedStatus {
		t.Fatalf("statusMessage = %q, want unconfigured encoder status", update.statusMessage)
	}
}

func TestRenderProgressStatusKeepsFailuresInline(t *testing.T) {
	order := []string{"cam1", "cam2", "cam3"}
	entries := map[string]progressStatusEntry{
		"cam1": {text: "✓ Camera 1"},
		"cam2": {text: "X Camera 2", hasError: true},
		"cam3": {text: "✓ Camera 3"},
	}

	got := renderProgressStatus(order, entries)
	want := "✓ Camera 1\nX Camera 2\n✓ Camera 3"
	if got != want {
		t.Fatalf("renderProgressStatus() = %q, want %q", got, want)
	}
}

func TestCameraStreamSnapshotUsesUpdatedResolution(t *testing.T) {
	stream := &cameraStream{camera: recording.DetectedCamera{Name: "CamON", Size: "-"}}
	if !stream.updateDetectedResolution("1920x1080") {
		t.Fatal("updateDetectedResolution() = false, want true for a valid new size")
	}
	if got := stream.snapshotRow()[2]; got != "1920x1080" {
		t.Fatalf("snapshotRow()[2] = %q, want 1920x1080", got)
	}
	if stream.updateDetectedResolution("invalid") {
		t.Fatal("updateDetectedResolution() = true, want false for an invalid size")
	}
}

func TestMonitorFFmpegErrorsUpdatesDetectedSourceResolution(t *testing.T) {
	stream := &cameraStream{
		camera:     recording.DetectedCamera{Name: "Laptop Cam", Size: "1280x720"},
		sourceType: "usb",
		shortID:    "C1",
	}

	monitorFFmpegErrors(stream, strings.NewReader("Stream #0:0: Video: mjpeg, yuvj422p(pc), 1920x1080, 30 fps\n"))

	if got := stream.snapshotRow()[2]; got != "1920x1080" {
		t.Fatalf("snapshotRow()[2] = %q, want 1920x1080", got)
	}
}

func TestUnreadyRTSPStatusUsesStartingLabel(t *testing.T) {
	stream := &cameraStream{
		sourceType: "rtsp",
		cmd:        &exec.Cmd{},
		running:    true,
		status:     "running",
		startTime:  time.Now().Add(-1 * time.Second),
	}
	spec := sourceSpec{SourceType: "rtsp"}
	recovery := &rtspRecoveryState{attention: true, reason: "no stream progress after 2s"}

	status := stream.snapshotRow()[8]
	if status != "running" {
		t.Fatalf("snapshot status = %q, want running before UI override", status)
	}

	uiStatus := monitoringSourceStatus(spec, stream, recovery)
	if uiStatus != "starting (1/3)" {
		t.Fatalf("faulted UI status = %q, want starting (1/3)", uiStatus)
	}
}

func TestExitedRTSPStatusUsesStartingLabelWhileRetryPending(t *testing.T) {
	stream := &cameraStream{sourceType: "rtsp", status: "stopped: exit status 1", startTime: time.Now().Add(-5 * time.Second)}
	spec := sourceSpec{SourceType: "rtsp"}
	recovery := &rtspRecoveryState{attention: true, reason: "ffmpeg exited"}

	if got := monitoringSourceStatus(spec, stream, recovery); got != "starting (1/3)" {
		t.Fatalf("monitoringSourceStatus() = %q, want starting (1/3)", got)
	}
}

func TestRetryBackoffRTSPStatusUsesStartingLabel(t *testing.T) {
	spec := sourceSpec{SourceType: "rtsp"}
	recovery := &rtspRecoveryState{attempts: 1, attention: true, nextRetry: time.Now().Add(4 * time.Second)}

	if got := monitoringSourceStatus(spec, nil, recovery); got != "starting (2/3)" {
		t.Fatalf("monitoringSourceStatus() = %q, want starting (2/3)", got)
	}
}

func TestCameraStreamInteractiveReadyAllowsRecentProgress(t *testing.T) {
	stream := &cameraStream{
		sourceType:     "rtsp",
		cmd:            &exec.Cmd{},
		running:        true,
		status:         "running",
		startTime:      time.Now().Add(-3 * time.Second),
		progressSeen:   true,
		progressSeenAt: time.Now().Add(-500 * time.Millisecond),
	}

	if !stream.isInteractiveReady() {
		t.Fatal("isInteractiveReady() = false, want true when recent progress is present")
	}

	stream.running = false
	if stream.isInteractiveReady() {
		t.Fatal("isInteractiveReady() = true, want false for stopped stream")
	}
}

func TestMacOSCaptureTransportFilter(t *testing.T) {
	tests := []struct {
		transportType string
		want          bool
	}{
		{transportType: "usb ", want: true},
		{transportType: "pci ", want: true},
		{transportType: "bltn", want: false},
		{transportType: "virt", want: false},
		{transportType: "netw", want: false},
		{transportType: "", want: false},
	}

	for _, tc := range tests {
		if got := isMacOSCaptureTransport(tc.transportType); got != tc.want {
			t.Errorf("isMacOSCaptureTransport(%q) = %t, want %t", tc.transportType, got, tc.want)
		}
	}
}

func TestMacOSCameraWithoutTransportMetadataIsNotFiltered(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS-specific camera classification")
	}

	cam := recording.DetectedCamera{
		Name:   "UVC Camera",
		Format: "avfoundation",
		PixFmt: "uyvy422",
	}
	if isIntegratedCamera(cam) {
		t.Fatal("AVFoundation camera without transport metadata should remain available")
	}
}

func TestBuildUSBSourcesFromDetectedUsesStableAssignmentState(t *testing.T) {
	previousConfig := camerasConfig
	defer func() {
		camerasConfig = previousConfig
	}()

	camerasConfig = &camerascfg.Config{
		DeviceAssignments: []camerascfg.DeviceAssignment{{
			AttachmentPath: "/stable/device/1",
			MatchKey:       "usb-stable-1",
			Name:           "Platform Left",
			ShortID:        "C1",
			OutputPort:     9001,
			Disabled:       true,
			On:             boolRef(false),
		}},
	}

	cam := recording.DetectedCamera{
		Name:           "USB Camera",
		AttachmentPath: "/stable/device/1",
		MatchKey:       "usb-stable-1",
		Identity:       "USB topology 1",
		PixFmt:         "mjpeg",
		Size:           "1920x1080",
		Fps:            30,
	}

	sources := buildUSBSourcesFromDetected([]recording.DetectedCamera{cam}, 9001, map[int]struct{}{}, map[string]struct{}{}, nil)
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	if sources[0].Enabled {
		t.Fatal("expected matched stable assignment to set Enabled=false from disabled=true")
	}
	if sources[0].MonitoringOn {
		t.Fatal("expected matched stable assignment to set MonitoringOn=false from on=false")
	}
	if sources[0].Name != "Platform Left" || sources[0].ShortID != "C1" {
		t.Fatalf("expected metadata from stable assignment, got name=%q shortID=%q", sources[0].Name, sources[0].ShortID)
	}
}

func TestBuildUSBSourcesWithProgressKeepsDisabledAssignmentsDisabledWhenDetected(t *testing.T) {
	previousConfig := camerasConfig
	previousFFmpegConfig := ffmpegConfig
	previousDetect := detectUSBCamerasWithConfigAndProgress
	defer func() {
		camerasConfig = previousConfig
		ffmpegConfig = previousFFmpegConfig
		detectUSBCamerasWithConfigAndProgress = previousDetect
	}()

	camerasConfig = &camerascfg.Config{
		Cameras: camerascfg.CamerasSettings{IncludeAll: true},
		DeviceAssignments: []camerascfg.DeviceAssignment{
			{
				AttachmentPath:   "/dev/v4l/by-path/disabled",
				MatchKey:         "usb-disabled",
				Name:             "Disabled Cam",
				ShortID:          "C1",
				OutputPort:       9001,
				Disabled:         true,
				On:               boolRef(false),
				ProbePixelFormat: "mjpeg",
				ProbeSize:        "1280x720",
				ProbeFPS:         30,
				ProbeFormats:     []string{"mjpeg", "yuyv422"},
			},
			{
				AttachmentPath: "/dev/v4l/by-path/enabled",
				MatchKey:       "usb-enabled",
				Name:           "Enabled Cam",
				ShortID:        "C2",
				OutputPort:     9002,
				On:             boolRef(true),
			},
		},
	}
	ffmpegConfig = &ffmpegcfg.Config{}

	detectUSBCamerasWithConfigAndProgress = func(cfg *ffmpegcfg.Config, progress recording.ProbeProgressFunc, skip func(name, matchKey, attachmentPath string) bool) []recording.DetectedCamera {
		if skip != nil {
			t.Fatal("USB discovery should probe disabled assignments so they remain visible when reconnected")
		}
		return []recording.DetectedCamera{{
			Name:             "Disabled Cam",
			Device:           "/dev/video8",
			Format:           "v4l2",
			PixFmt:           "mjpeg",
			Size:             "1280x720",
			Fps:              30,
			MatchKey:         "usb-disabled",
			AttachmentPath:   "/dev/v4l/by-path/disabled",
			Identity:         "/dev/v4l/by-path/disabled",
			SupportedFormats: []string{"mjpeg", "yuyv422"},
		}, {
			Name:             "Enabled Cam",
			Device:           "/dev/video9",
			Format:           "v4l2",
			PixFmt:           "mjpeg",
			Size:             "1280x720",
			Fps:              30,
			MatchKey:         "usb-enabled",
			AttachmentPath:   "/dev/v4l/by-path/enabled",
			Identity:         "/dev/v4l/by-path/enabled",
			SupportedFormats: []string{"mjpeg", "yuyv422"},
		}}
	}

	sources, changed := buildUSBSourcesWithProgress(9001, map[int]struct{}{}, map[string]struct{}{}, nil)
	if changed {
		t.Fatal("detected assignments should not be disabled as absent")
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}

	var disabled *sourceSpec
	var enabled *sourceSpec
	for i := range sources {
		switch sources[i].Key {
		case "usb-disabled":
			disabled = &sources[i]
		case "usb-enabled":
			enabled = &sources[i]
		}
	}
	if disabled == nil {
		t.Fatal("disabled assignment should remain visible in configuration inventory")
	}
	if disabled.Enabled {
		t.Fatal("disabled assignment should stay disabled in inventory")
	}
	if !disabled.Detected {
		t.Fatal("disabled assignment should be marked as detected when it reconnects")
	}
	if disabled.Camera.PixFmt != "mjpeg" || disabled.Camera.Size != "1280x720" {
		t.Fatalf("disabled assignment should keep stored probe metadata, got pixFmt=%q size=%q", disabled.Camera.PixFmt, disabled.Camera.Size)
	}
	if enabled == nil {
		t.Fatal("enabled detected source missing from inventory")
	}
	if !enabled.Enabled || !enabled.Detected {
		t.Fatalf("enabled source state = enabled:%t detected:%t, want both true", enabled.Enabled, enabled.Detected)
	}

	inv := assembleSourceInventory(sources, nil, nil)
	if len(inv.Active) != 1 || inv.Active[0].Key != "usb-enabled" {
		t.Fatalf("active inventory = %+v, want only enabled detected source", inv.Active)
	}
}

func TestBuildUSBSourcesWithProgressDisablesAbsentAssignment(t *testing.T) {
	previousConfig := camerasConfig
	previousFFmpegConfig := ffmpegConfig
	previousDetect := detectUSBCamerasWithConfigAndProgress
	defer func() {
		camerasConfig = previousConfig
		ffmpegConfig = previousFFmpegConfig
		detectUSBCamerasWithConfigAndProgress = previousDetect
	}()

	camerasConfig = &camerascfg.Config{DeviceAssignments: []camerascfg.DeviceAssignment{{
		AttachmentPath: "/dev/v4l/by-path/missing",
		MatchKey:       "usb-missing",
		Name:           "Missing Cam",
		OutputPort:     9001,
	}}}
	ffmpegConfig = &ffmpegcfg.Config{}
	detectUSBCamerasWithConfigAndProgress = func(*ffmpegcfg.Config, recording.ProbeProgressFunc, func(string, string, string) bool) []recording.DetectedCamera {
		return nil
	}

	sources, changed := buildUSBSourcesWithProgress(9001, map[int]struct{}{}, map[string]struct{}{}, nil)
	if !changed {
		t.Fatal("absence should mark the saved assignment disabled")
	}
	if !camerasConfig.DeviceAssignments[0].Disabled {
		t.Fatal("absent assignment should be disabled in configuration")
	}
	if len(sources) != 1 || sources[0].Detected || sources[0].Enabled {
		t.Fatalf("absent source = %+v, want stored, undetected, and disabled", sources)
	}
}

func TestRemoveDeviceAssignmentUsesAttachmentPathFirst(t *testing.T) {
	assignments := []camerascfg.DeviceAssignment{
		{AttachmentPath: "/dev/v4l/by-path/stale", MatchKey: "usb-stale", Name: "Stale Cam"},
		{AttachmentPath: "/dev/v4l/by-path/live", MatchKey: "usb-live", Name: "Live Cam"},
	}

	updated, removed, ok := removeDeviceAssignment(assignments, "/dev/v4l/by-path/stale", "usb-live")
	if !ok {
		t.Fatal("removeDeviceAssignment() = not found, want stale assignment removed by attachment path")
	}
	if removed.Name != "Stale Cam" {
		t.Fatalf("removed assignment = %+v, want Stale Cam", removed)
	}
	if len(updated) != 1 || updated[0].Name != "Live Cam" {
		t.Fatalf("updated assignments = %+v, want only Live Cam", updated)
	}
}

func TestStoredUSBSourceIsDisabledUntilDetected(t *testing.T) {
	assignment := &camerascfg.DeviceAssignment{
		AttachmentPath: "/dev/v4l/by-path/camera",
		MatchKey:       "usb-camera",
		Name:           "Platform Camera",
		ShortID:        "C1",
		OutputPort:     9001,
		Disabled:       false,
	}

	spec := storedUSBSourceSpec(assignment, 9001, map[int]struct{}{}, map[string]struct{}{})
	if spec.Detected {
		t.Fatal("stored USB source should not be marked detected")
	}
	if spec.Enabled {
		t.Fatal("stored USB source should be disabled until detected")
	}
	if spec.OutputPort != 9001 {
		t.Fatalf("stored USB source output port = %d, want 9001", spec.OutputPort)
	}
}

func TestRemoveDeviceAssignmentFallsBackToMatchKey(t *testing.T) {
	assignments := []camerascfg.DeviceAssignment{
		{MatchKey: "usb-stale", Name: "Stale Cam"},
		{MatchKey: "usb-live", Name: "Live Cam"},
	}

	updated, removed, ok := removeDeviceAssignment(assignments, "", "usb-live")
	if !ok {
		t.Fatal("removeDeviceAssignment() = not found, want live assignment removed by match key")
	}
	if removed.Name != "Live Cam" {
		t.Fatalf("removed assignment = %+v, want Live Cam", removed)
	}
	if len(updated) != 1 || updated[0].Name != "Stale Cam" {
		t.Fatalf("updated assignments = %+v, want only Stale Cam", updated)
	}
}

func TestCachedDetectedCamerasSkipsConfigOnlyRows(t *testing.T) {
	specs := []sourceSpec{
		{Key: "usb-config", Detected: false, Camera: recording.DetectedCamera{MatchKey: "usb-config"}},
		{Key: "usb-live", Detected: true, Camera: recording.DetectedCamera{MatchKey: "usb-live"}},
	}

	cameras := cachedDetectedCameras(specs)
	if len(cameras) != 1 {
		t.Fatalf("cachedDetectedCameras() returned %d cameras, want 1", len(cameras))
	}
	if cameras[0].MatchKey != "usb-live" {
		t.Fatalf("cachedDetectedCameras()[0] = %q, want usb-live", cameras[0].MatchKey)
	}
}

func TestResolveStreamFFmpegPath(t *testing.T) {
	tests := []struct {
		name          string
		defaultPath   string
		encoder       *recording.HwEncoder
		needsEncoding bool
		want          string
	}{
		{name: "copy stream keeps default runtime", defaultPath: "ffmpeg7", encoder: &recording.HwEncoder{FFmpegPath: "ffmpeg6"}, needsEncoding: false, want: "ffmpeg7"},
		{name: "encoded stream uses encoder override", defaultPath: "ffmpeg7", encoder: &recording.HwEncoder{FFmpegPath: "ffmpeg6"}, needsEncoding: true, want: "ffmpeg6"},
		{name: "encoded stream without override uses default", defaultPath: "ffmpeg7", encoder: &recording.HwEncoder{}, needsEncoding: true, want: "ffmpeg7"},
		{name: "empty default falls back to ffmpeg", defaultPath: "", encoder: nil, needsEncoding: false, want: "ffmpeg"},
	}

	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveStreamFFmpegPath(tc.defaultPath, tc.encoder, tc.needsEncoding); got != tc.want {
				t.Fatalf("resolveStreamFFmpegPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildStreamCommandSpecUsesNVENCFallbackOnlyWhenEncoding(t *testing.T) {
	previousCamerasConfig := camerasConfig
	previousFFmpegConfig := ffmpegConfig
	previousFFmpegPath := config.GetFFmpegPath()
	defer func() {
		camerasConfig = previousCamerasConfig
		ffmpegConfig = previousFFmpegConfig
		config.SetFFmpegPath(previousFFmpegPath)
	}()

	camerasConfig = &camerascfg.Config{}
	ffmpegConfig = &ffmpegcfg.Config{}
	config.SetFFmpegPath("ffmpeg7")

	nvenc := &recording.HwEncoder{
		Name:             "h264_nvenc",
		FFmpegPath:       "ffmpeg6",
		VideoFilter:      "format=yuv420p",
		OutputParameters: "-c:v h264_nvenc",
	}

	copyStream := &cameraStream{
		camera:  recording.DetectedCamera{Format: "rtsp", PixFmt: "h264", Device: "rtsp://copy"},
		encoder: nvenc,
		port:    9005,
	}
	copySpec, err := buildStreamCommandSpec(copyStream, streamOutputLive)
	if err != nil {
		t.Fatalf("buildStreamCommandSpec(copy) error = %v", err)
	}
	if copySpec.ffmpegPath != "ffmpeg7" {
		t.Fatalf("copy stream ffmpeg path = %q, want ffmpeg7", copySpec.ffmpegPath)
	}
	if strings.Contains(strings.Join(copySpec.args, " "), "format=yuv420p") {
		t.Fatalf("copy stream args = %q, did not expect encoder video filter", strings.Join(copySpec.args, " "))
	}

	encodedStream := &cameraStream{
		camera:  recording.DetectedCamera{Format: "rtsp", PixFmt: "mjpeg", Device: "rtsp://encode"},
		encoder: nvenc,
		port:    9006,
	}
	encodedSpec, err := buildStreamCommandSpec(encodedStream, streamOutputLive)
	if err != nil {
		t.Fatalf("buildStreamCommandSpec(encoded) error = %v", err)
	}
	if encodedSpec.ffmpegPath != "ffmpeg6" {
		t.Fatalf("encoded stream ffmpeg path = %q, want ffmpeg6", encodedSpec.ffmpegPath)
	}
	if !strings.Contains(strings.Join(encodedSpec.args, " "), "-vf format=yuv420p") {
		t.Fatalf("encoded stream args = %q, want encoder video filter from ffmpeg.toml", strings.Join(encodedSpec.args, " "))
	}
}

func TestBuildStreamCommandSpecUsesAVFoundationInput(t *testing.T) {
	previousCamerasConfig := camerasConfig
	previousFFmpegConfig := ffmpegConfig
	previousFFmpegPath := config.GetFFmpegPath()
	defer func() {
		camerasConfig = previousCamerasConfig
		ffmpegConfig = previousFFmpegConfig
		config.SetFFmpegPath(previousFFmpegPath)
	}()

	camerasConfig = &camerascfg.Config{
		Unicast: camerascfg.UnicastConfig{Enabled: true},
	}
	ffmpegConfig = &ffmpegcfg.Config{
		Software: ffmpegcfg.SoftwareEncoder{OutputParameters: "-c:v libx264"},
		Output:   ffmpegcfg.OutputConfig{ExtraFlags: "-f mpegts"},
	}
	config.SetFFmpegPath("ffmpeg")

	stream := &cameraStream{
		camera: recording.DetectedCamera{Format: "avfoundation", PixFmt: "uyvy422", Device: "0", Size: "1920x1080", Fps: 30},
		port:   9005,
	}
	spec, err := buildStreamCommandSpec(stream, streamOutputLive)
	if err != nil {
		t.Fatalf("buildStreamCommandSpec() error = %v", err)
	}
	joined := strings.Join(spec.args, " ")
	for _, want := range []string{"-f avfoundation", "-pixel_format uyvy422", "-video_size 1920x1080", "-framerate 30", "-i 0:none"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args = %q, want %q", joined, want)
		}
	}
	if strings.Contains(joined, "-use_wallclock_as_timestamps") {
		t.Fatalf("args = %q, do not want wall-clock timestamps for AVFoundation", joined)
	}
	if strings.Contains(joined, "-f tee") {
		t.Fatalf("args = %q, do not want tee for the single preview destination", joined)
	}
	if !strings.Contains(joined, "-f mpegts") || !strings.Contains(joined, "udp://127.0.0.1:9005?pkt_size=1316") {
		t.Fatalf("args = %q, want direct MPEG-TS UDP output", joined)
	}
}

// AVFoundation reassigns indices whenever a device is opened or released, so two
// cameras can exchange indices between detection and capture. Addressing the
// device by its stable name keeps each stream on the intended camera.
func TestBuildStreamCommandSpecOpensAVFoundationByStableName(t *testing.T) {
	previousCamerasConfig := camerasConfig
	previousFFmpegConfig := ffmpegConfig
	previousFFmpegPath := config.GetFFmpegPath()
	defer func() {
		camerasConfig = previousCamerasConfig
		ffmpegConfig = previousFFmpegConfig
		config.SetFFmpegPath(previousFFmpegPath)
	}()

	camerasConfig = &camerascfg.Config{
		Unicast: camerascfg.UnicastConfig{Enabled: true},
	}
	ffmpegConfig = &ffmpegcfg.Config{
		Software: ffmpegcfg.SoftwareEncoder{OutputParameters: "-c:v libx264"},
		Output:   ffmpegcfg.OutputConfig{ExtraFlags: "-f mpegts"},
	}
	config.SetFFmpegPath("ffmpeg")

	stream := &cameraStream{
		camera: recording.DetectedCamera{
			Name:       "Platform Left",
			DeviceName: "MacBook Air Camera",
			MatchKey:   "avfoundation:macbook air camera",
			Format:     "avfoundation",
			PixFmt:     "uyvy422",
			Device:     "1",
			Size:       "1920x1080",
			Fps:        30,
		},
		port: 9005,
	}
	spec, err := buildStreamCommandSpec(stream, streamOutputLive)
	if err != nil {
		t.Fatalf("buildStreamCommandSpec() error = %v", err)
	}
	joined := strings.Join(spec.args, " ")
	if !strings.Contains(joined, "-i MacBook Air Camera:none") {
		t.Fatalf("args = %q, want the stable device name as input", joined)
	}
	if strings.Contains(joined, "-i 1:none") {
		t.Fatalf("args = %q, do not want the volatile numeric index as input", joined)
	}
}

func TestStreamNeedsStartupProbeIncludesCopyStreams(t *testing.T) {
	tests := []struct {
		name   string
		stream *cameraStream
		want   bool
	}{
		{
			name:   "nil stream does not probe",
			stream: nil,
			want:   false,
		},
		{
			name: "h264 copy stream probes",
			stream: &cameraStream{
				camera: recording.DetectedCamera{PixFmt: "h264"},
			},
			want: true,
		},
		{
			name: "software stream probes",
			stream: &cameraStream{
				camera: recording.DetectedCamera{PixFmt: "mjpeg"},
			},
			want: true,
		},
		{
			name: "rtsp network stream probes",
			stream: &cameraStream{
				camera: recording.DetectedCamera{Format: "rtsp", PixFmt: "h264"},
			},
			want: true,
		},
		{
			name: "avfoundation capture device skips probe",
			stream: &cameraStream{
				camera: recording.DetectedCamera{Format: "avfoundation", PixFmt: "uyvy422"},
			},
			want: false,
		},
		{
			name: "v4l2 capture device skips probe",
			stream: &cameraStream{
				camera: recording.DetectedCamera{Format: "v4l2", PixFmt: "mjpeg"},
			},
			want: false,
		},
		{
			name: "dshow capture device skips probe",
			stream: &cameraStream{
				camera: recording.DetectedCamera{Format: "dshow", PixFmt: "mjpeg"},
			},
			want: false,
		},
	}

	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			if got := streamNeedsStartupProbe(tc.stream); got != tc.want {
				t.Fatalf("streamNeedsStartupProbe() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestBuildStreamCommandSpecProbeNullCopiesToNullOutput(t *testing.T) {
	previousCamerasConfig := camerasConfig
	previousFFmpegConfig := ffmpegConfig
	previousFFmpegPath := config.GetFFmpegPath()
	defer func() {
		camerasConfig = previousCamerasConfig
		ffmpegConfig = previousFFmpegConfig
		config.SetFFmpegPath(previousFFmpegPath)
	}()

	camerasConfig = &camerascfg.Config{}
	ffmpegConfig = &ffmpegcfg.Config{
		Software: ffmpegcfg.SoftwareEncoder{OutputParameters: "-c:v libx264"},
		Output:   ffmpegcfg.OutputConfig{ExtraFlags: "-f mpegts"},
	}
	config.SetFFmpegPath("ffmpeg7")

	stream := &cameraStream{
		camera: recording.DetectedCamera{Format: "rtsp", PixFmt: "h264", Device: "rtsp://copy"},
		port:   9005,
	}

	spec, err := buildStreamCommandSpec(stream, streamOutputProbeNull)
	if err != nil {
		t.Fatalf("buildStreamCommandSpec(probe) error = %v", err)
	}

	joined := strings.Join(spec.args, " ")
	if !strings.Contains(joined, "-c:v copy") {
		t.Fatalf("probe args = %q, want copy output", joined)
	}
	if !strings.Contains(joined, "-f null -") {
		t.Fatalf("probe args = %q, want null output", joined)
	}
	if !strings.Contains(joined, "-frames:v 1") {
		t.Fatalf("probe args = %q, want single-frame probe", joined)
	}
}

func TestRunStartupProbeRetriesTransientGrabFailure(t *testing.T) {
	previousCamerasConfig := camerasConfig
	previousFFmpegConfig := ffmpegConfig
	previousFFmpegPath := config.GetFFmpegPath()
	previousRunner := runStartupProbeCommandFunc
	previousBackoff := streamStartupProbeBackoff
	defer func() {
		camerasConfig = previousCamerasConfig
		ffmpegConfig = previousFFmpegConfig
		config.SetFFmpegPath(previousFFmpegPath)
		runStartupProbeCommandFunc = previousRunner
		streamStartupProbeBackoff = previousBackoff
	}()

	camerasConfig = &camerascfg.Config{}
	ffmpegConfig = &ffmpegcfg.Config{
		Software: ffmpegcfg.SoftwareEncoder{OutputParameters: "-c:v libx264"},
		Output:   ffmpegcfg.OutputConfig{ExtraFlags: "-f mpegts"},
	}
	config.SetFFmpegPath("ffmpeg7")
	streamStartupProbeBackoff = 0

	var logLevels []string
	var argLists []string
	var attemptCount int
	runStartupProbeCommandFunc = func(ffmpegPath string, args []string, logLevel string, timeout time.Duration) (string, error) {
		logLevels = append(logLevels, logLevel)
		argLists = append(argLists, strings.Join(append([]string(nil), args...), " "))
		attemptCount++
		if attemptCount < streamStartupProbeAttempts {
			return "framerate 60.000000 is not supported by the device", errors.New("grab failed")
		}
		return "", nil
	}

	stream := &cameraStream{
		camera:  recording.DetectedCamera{Name: "CamON", Format: "rtsp", PixFmt: "h264", Device: "rtsp://copy"},
		shortID: "R1",
		port:    9005,
	}

	err := runStartupProbe(stream, &streamStartupCallbacks{})
	if err != nil {
		t.Fatalf("runStartupProbe() error = %v, want nil after retry", err)
	}
	if attemptCount != streamStartupProbeAttempts {
		t.Fatalf("attemptCount = %d, want %d transient failures then success", attemptCount, streamStartupProbeAttempts)
	}
	for i, level := range logLevels {
		if level != "error" {
			t.Fatalf("probe log level[%d] = %q, want all error-level attempts before success", i, level)
		}
	}
	for i, joined := range argLists {
		if joined != argLists[0] {
			t.Fatalf("probe args[%d] = %q, want identical args on every retry", i, joined)
		}
		if !strings.Contains(joined, "-f null -") {
			t.Fatalf("probe args[%d] = %q, want null output", i, joined)
		}
	}
}

// TestUVCDeviceStartupProbeUsesCurrentAVFoundationIndex exercises the real
// root cause on actual hardware: AVFoundation numeric device indices are not
// stable, so a stored index can point at a different physical camera (for
// example the built-in FaceTime camera, which maxes out at 30 fps) and reject
// the configured 60 fps. The start path opens by the stable device name, so it
// targets the intended UVC camera even if the stored index is stale.
//
// This test is skipped entirely when no AVFoundation UVC camera capable of
// 60 fps is present, so it is a no-op on machines/CI without such hardware. It
// asserts the deterministic resolution logic and does not compete for an
// exclusive device open (which would contend with any running stream).
func TestUVCDeviceStartupProbeUsesStableAVFoundationName(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("AVFoundation stable-name test only applies to macOS")
	}
	if testing.Short() {
		t.Skip("skipping real-device UVC test in -short mode")
	}

	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found on PATH; skipping real-device UVC test")
	}

	fc, err := ffmpegcfg.LoadConfig()
	if err != nil || fc == nil {
		t.Skipf("cannot load ffmpeg config; skipping real-device UVC test: %v", err)
	}

	previousFFmpegPath := config.GetFFmpegPath()
	defer config.SetFFmpegPath(previousFFmpegPath)
	config.SetFFmpegPath(ffmpegPath)

	cameras := recording.DetectCamerasWithConfig(fc)
	var cam *recording.DetectedCamera
	for i := range cameras {
		if cameras[i].Format == "avfoundation" && cameras[i].Fps >= 60 {
			cam = &cameras[i]
			break
		}
	}
	if cam == nil {
		t.Skip("no AVFoundation UVC camera advertising >= 60 fps detected; skipping")
	}

	deviceName, ok := recording.AVFoundationDeviceName(*cam)
	if !ok {
		t.Skipf("camera %q has no AVFoundation match key (%q); skipping", cam.Name, cam.MatchKey)
	}

	// Simulate a stale/wrong stored index plus a user-assigned display name that
	// matches no device, and verify capture still uses the OS-reported name.
	stream := &cameraStream{
		camera:  *cam,
		shortID: "UVC",
		port:    9002,
	}
	stream.camera.Device = "99"
	stream.camera.Name = "Renamed By User"

	resolveAVFoundationStreamIndex(stream)

	if got, want := avfoundationInput(stream.camera), deviceName+":none"; got != want {
		t.Fatalf("avfoundationInput() = %q, want stable device input %q", got, want)
	}
}
