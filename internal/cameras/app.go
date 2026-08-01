package cameras

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/owlcms/video/internal/assets"
	"github.com/owlcms/video/internal/config"
	camerascfg "github.com/owlcms/video/internal/config/cameras"
	ffmpegcfg "github.com/owlcms/video/internal/config/ffmpeg"
	"github.com/owlcms/video/internal/jobutil"
	"github.com/owlcms/video/internal/logging"
	"github.com/owlcms/video/internal/recording"
)

var (
	includeAll bool
	startPort  int

	previewMu   sync.Mutex
	previewCmds []*exec.Cmd

	// camerasConfig holds the per-instance cameras configuration (multicast, includeAll).
	camerasConfig *camerascfg.Config
	// configFilePath is the cameras.toml chosen by the host.
	configFilePath string
	// ffmpegConfig holds the machine-specific encoder configuration (from ffmpeg.toml).
	ffmpegConfig *ffmpegcfg.Config
)

const (
	previewMaxLongSide  = 960
	previewMaxShortSide = 540
	defaultWindowWidth  = 1368
	defaultWindowHeight = 880
)

var ffmpegVideoResolutionPattern = regexp.MustCompile(`\b(\d{2,5})x(\d{2,5})\b`)

func setAppIcon(myApp fyne.App) {
	if assets.IconResource != nil && len(assets.IconResource.Content()) > 0 {
		myApp.SetIcon(assets.IconResource)
		return
	}

	iconCandidates := make([]string, 0, 2)

	if exePath, err := os.Executable(); err == nil {
		iconCandidates = append(iconCandidates, filepath.Join(filepath.Dir(exePath), "Icon.png"))
	}
	if wd, err := os.Getwd(); err == nil {
		iconCandidates = append(iconCandidates, filepath.Join(wd, "Icon.png"))
	}

	for _, iconPath := range iconCandidates {
		res, err := fyne.LoadResourceFromPath(iconPath)
		if err == nil {
			myApp.SetIcon(res)
			return
		}
	}
}

func newSectionTitle(text string) fyne.CanvasObject {
	title := canvas.NewText(text, theme.Color(theme.ColorNameForeground))
	title.TextSize = 16
	title.TextStyle = fyne.TextStyle{Bold: true}
	return title
}

func newProgressDetailRow(label string, value fyne.CanvasObject) fyne.CanvasObject {
	return container.NewBorder(nil, nil, widget.NewLabel(label), nil, value)
}

type detectionProgressUpdate struct {
	stage          string
	detail         string
	statusKey      string
	statusMessage  string
	replaceStatus  bool
	hasError       bool
	statusHasError bool
}

type progressStatusEntry struct {
	text     string
	hasError bool
}

func newDetectionProgressDialog(window fyne.Window, title string) (dialog.Dialog, func(string), func() bool) {
	stageLabel := widget.NewLabel(detectionProgressUIStrings.InitialStage)
	stageLabel.Wrapping = fyne.TextWrapWord
	detailLabel := widget.NewLabel("")
	detailLabel.Wrapping = fyne.TextWrapWord
	statusList := widget.NewLabel("")
	statusList.Wrapping = fyne.TextWrapWord
	failureHeader := widget.NewLabel(detectionProgressUIStrings.FailureStatusLabel)
	failureHeader.Hide()
	failureList := widget.NewLabel("")
	failureList.Wrapping = fyne.TextWrapWord
	failureList.Hide()
	historyScroll := container.NewVScroll(statusList)
	historyScroll.SetMinSize(fyne.NewSize(520, 280))
	var progressDialog dialog.Dialog
	closeButton := widget.NewButton(detectionProgressUIStrings.CloseButtonLabel, func() {
		if progressDialog != nil {
			progressDialog.Hide()
		}
	})
	closeButton.Hide()

	var mu sync.Mutex
	statusOrder := make([]string, 0, 32)
	statusByKey := make(map[string]progressStatusEntry)
	nonSourceFailures := make([]string, 0, 8)
	lastStage := ""
	lastDetail := ""
	hasError := false
	report := func(message string) {
		trimmed := strings.TrimSpace(message)
		if trimmed == "" {
			return
		}

		update, ok := simplifyDetectionProgress(trimmed)
		if !ok {
			return
		}

		mu.Lock()
		if update.hasError {
			hasError = true
		}
		if update.hasError && update.statusKey == "" {
			if text := strings.TrimSpace(update.detail); text != "" {
				alreadyPresent := false
				for _, existing := range nonSourceFailures {
					if existing == text {
						alreadyPresent = true
						break
					}
				}
				if !alreadyPresent {
					nonSourceFailures = append(nonSourceFailures, text)
				}
			}
		}
		if update.statusKey != "" {
			if _, exists := statusByKey[update.statusKey]; !exists {
				statusOrder = append(statusOrder, update.statusKey)
			}
			statusByKey[update.statusKey] = progressStatusEntry{text: update.statusMessage, hasError: update.statusHasError}
		}
		if len(statusOrder) > 25 {
			trimmedOrder := statusOrder[len(statusOrder)-25:]
			keep := make(map[string]struct{}, len(trimmedOrder))
			for _, key := range trimmedOrder {
				keep[key] = struct{}{}
			}
			for key := range statusByKey {
				if _, ok := keep[key]; !ok {
					delete(statusByKey, key)
				}
			}
			statusOrder = trimmedOrder
		}
		content := renderProgressStatus(statusOrder, statusByKey)
		failureContent := strings.Join(nonSourceFailures, "\n")
		if update.stage != "" {
			lastStage = update.stage
		}
		if update.detail != "" || update.replaceStatus {
			lastDetail = update.detail
		}
		stageValue := lastStage
		detailValue := lastDetail
		showFailureSection := strings.TrimSpace(failureContent) != ""
		showClose := hasError
		mu.Unlock()

		fyne.Do(func() {
			if stageValue != "" {
				stageLabel.SetText(stageValue)
			}
			if detailValue != "" {
				detailLabel.SetText(detailValue)
			}
			statusList.SetText(content)
			statusList.Refresh()
			failureList.SetText(failureContent)
			failureList.Refresh()
			historyScroll.Refresh()
			if showFailureSection {
				failureHeader.Show()
				failureHeader.Refresh()
				failureList.Show()
				failureList.Refresh()
			} else {
				failureHeader.Hide()
				failureList.Hide()
			}
			if showClose {
				closeButton.Show()
				closeButton.Refresh()
			}
		})
	}

	body := container.NewVBox(
		newProgressDetailRow(detectionProgressUIStrings.CurrentStageLabel, stageLabel),
		newProgressDetailRow(detectionProgressUIStrings.CurrentActivityLabel, detailLabel),
		widget.NewSeparator(),
		widget.NewLabel(detectionProgressUIStrings.SourceStatusLabel),
		historyScroll,
		failureHeader,
		failureList,
		closeButton,
	)
	progressDialog = dialog.NewCustomWithoutButtons(title, body, window)
	return progressDialog, report, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return hasError
	}
}

func renderProgressStatus(statusOrder []string, statusByKey map[string]progressStatusEntry) string {
	lines := make([]string, 0, len(statusOrder))
	for _, key := range statusOrder {
		entry, ok := statusByKey[key]
		if !ok {
			continue
		}
		text := strings.TrimSpace(entry.text)
		if text == "" {
			continue
		}
		lines = append(lines, text)
	}
	return strings.Join(lines, "\n")
}

func simplifyDetectionProgress(message string) (detectionProgressUpdate, bool) {
	tag, payload, ok := recording.ProgressParse(strings.TrimSpace(message))
	if !ok {
		return detectionProgressUpdate{}, false
	}

	return detectionProgressUpdateForTag(tag, payload)
}

func newVerticalGap(height float32) fyne.CanvasObject {
	rect := canvas.NewRectangle(color.Transparent)
	rect.SetMinSize(fyne.NewSize(1, height))
	return rect
}

type cameraStream struct {
	camera      recording.DetectedCamera
	port        int
	udpDest     string
	unicastMode bool
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	encoder     *recording.HwEncoder
	shortID     string
	summary     string
	sourceType  string
	transport   string
	commandLine string

	mu                 sync.RWMutex
	running            bool
	status             string
	fps                string
	metricName         string
	frame              string
	bitrate            string
	speed              string
	progressFrame      int64
	hasProgressFrame   bool
	progressOutTimeUS  int64
	hasProgressOutTime bool
	progressSeen       bool
	progressSeenAt     time.Time
	lastProgress       Progress
	hasLastProgress    bool
	fpsEMA             float64
	hasFPSEMA          bool
	driftEMA           float64
	hasDriftEMA        bool
	lastStderr         string
	startTime          time.Time
	lastUpdate         time.Time
	stopping           bool
	stopReason         string
	diagnosticRetried  bool
}

type Progress struct {
	Frame     int64
	OutTimeUS int64 // microseconds (ffmpeg out_time_ms is actually µs)
	WallTime  time.Time
}

type Metrics struct {
	FPS   float64
	Drift float64
}

type rtspRecoveryState struct {
	attempts  int
	attention bool
	reason    string
	nextRetry time.Time
	exhausted bool
}

const maxRTSPRetryAttempts = 3

func ComputeMetrics(prev, curr Progress) Metrics {
	deltaFrame := curr.Frame - prev.Frame
	deltaOutSeconds := float64(curr.OutTimeUS-prev.OutTimeUS) / 1_000_000.0
	deltaWallSeconds := curr.WallTime.Sub(prev.WallTime).Seconds()

	var fps float64
	if deltaOutSeconds > 0 {
		fps = float64(deltaFrame) / deltaOutSeconds
	}

	var drift float64
	if deltaWallSeconds > 0 {
		drift = deltaOutSeconds / deltaWallSeconds
	}

	return Metrics{
		FPS:   fps,
		Drift: drift,
	}
}

// Options carries the host-supplied startup overrides for the Cameras module.
type Options struct {
	CamerasConfigPath string
	FFmpegConfigPath  string
	IncludeAll        bool
	StartPort         int
}

// Init loads the Cameras module configuration and prepares the ffmpeg runtime.
// It must be called before BuildUI.
func Init(opts Options) error {
	includeAll = opts.IncludeAll
	startPort = opts.StartPort
	configFilePath = opts.CamerasConfigPath

	// Create a job object so child processes die with us
	if err := jobutil.Init(); err != nil {
		logging.WarningLogger.Printf("Failed to create job object: %v", err)
	}

	fc, err := ffmpegcfg.LoadConfigFromFile(opts.FFmpegConfigPath)
	if err != nil {
		return fmt.Errorf("loading ffmpeg config %s: %w", opts.FFmpegConfigPath, err)
	}
	ffmpegConfig = fc

	cfg, err := camerascfg.LoadConfigFromFile(opts.CamerasConfigPath)
	if err != nil {
		return fmt.Errorf("loading cameras config %s: %w", opts.CamerasConfigPath, err)
	}
	// Every in-app edit must be written back to the file the host chose.
	camerascfg.SetConfigSourcePath(opts.CamerasConfigPath)
	if cfg.ClearRestartDirtyReasons() {
		if saveErr := camerascfg.SaveConfig(cfg); saveErr != nil {
			logging.WarningLogger.Printf("Failed to clear persisted restart dirty flags: %v", saveErr)
		} else {
			logging.InfoLogger.Printf("Cleared persisted restart dirty flags from cameras config on startup")
		}
	}
	camerasConfig = cfg

	if includeAll {
		camerasConfig.Cameras.IncludeAll = true
	}
	if startPort > 0 {
		camerasConfig.Multicast.StartPort = startPort
	}

	if err := recording.InitializeFFmpeg(); err != nil {
		logging.WarningLogger.Printf("ffmpeg initialization warning: %v", err)
	}
	return nil
}

// Cleanup performs the final ffmpeg teardown after the host window closes.
func Cleanup() {
	logging.InfoLogger.Printf("Camera UI exited; performing final ffmpeg cleanup")
	jobutil.KillAllFFmpeg()
}

// Theme returns the Cameras table/row theme wrapped around the supplied base theme.
func Theme(base fyne.Theme) fyne.Theme {
	return appTheme{base}
}

// SetAppIcon applies the Cameras icon to the host application.
func SetAppIcon(myApp fyne.App) {
	setAppIcon(myApp)
}

// WindowSize returns the preferred window size for the Cameras interface.
func WindowSize() fyne.Size {
	return fyne.NewSize(defaultWindowWidth, defaultWindowHeight)
}

// startAllStreams starts streams for all configured or autodetected sources.
// Returns only the streams that started successfully and any unicast warning generated during setup.
func startAllStreams(sources []sourceSpec, encoder *recording.HwEncoder, callbacks *streamStartupCallbacks) ([]*cameraStream, string) {
	unicastMode := camerasConfig.Unicast.Enabled
	var streams []*cameraStream
	unicastWarning := ""

	if unicastMode {
		fmt.Println("\nStarting camera streams (unicast tee):")
		fmt.Println("=======================================")
	} else {
		fmt.Println("\nStarting camera streams (multicast):")
		fmt.Println("=====================================")
	}
	callbacks.report(recording.ProgressMsg(recording.ProgStreamsAll, fmt.Sprintf("%d", len(sources))))
	if unicastMode && len(sources) > 0 {
		probePort := camerasConfig.Unicast.StartPort
		if sources[0].OutputPort > 0 {
			probePort = sources[0].OutputPort
		}
		warning, err := prepareReachableUnicastDestinations(probePort)
		if err != nil {
			fmt.Printf("  ERROR: Failed to prepare unicast destinations: %v\n", err)
			callbacks.report(recording.ProgressMsg(recording.ProgStreamFailed, recording.ProgressDetailPayload("Unicast", err.Error())))
			return streams, ""
		}
		if warning != "" {
			unicastWarning = warning
			fmt.Printf("  WARNING: %s\n", warning)
			callbacks.report(recording.ProgressMsg(recording.ProgError, warning))
		}
	}

	for _, source := range sources {
		cam := source.Camera
		port := source.OutputPort
		var udpDest string
		if unicastMode {
			udpDest = camerasConfig.Unicast.TeeOutput(port)
		} else {
			udpDest = fmt.Sprintf("udp://%s:%d", camerasConfig.Multicast.IP, port)
		}
		stream := &cameraStream{
			camera:      cam,
			port:        port,
			encoder:     encoder,
			udpDest:     udpDest,
			unicastMode: unicastMode,
			status:      "starting",
			running:     false,
			fps:         "-",
			frame:       "-",
			bitrate:     "-",
			speed:       "-",
			shortID:     source.ShortID,
			summary:     source.Summary,
			sourceType:  source.SourceType,
			transport:   source.Transport,
		}

		fmt.Printf("\n[%s] %s (%s, %s @ %d fps)\n", cam.PixFmt, cam.Name, cam.Size, cam.PixFmt, cam.Fps)
		if unicastMode {
			for _, dest := range enabledUnicastDestinations(camerasConfig.Unicast.Destinations) {
				fmt.Printf("  -> udp://%s:%d\n", dest.Address, port)
			}
		} else {
			fmt.Printf("  -> %s\n", udpDest)
		}

		callbacks.report(recording.ProgressMsg(recording.ProgStreamPrep, cam.Name))
		cmd, err := startStream(stream, callbacks)
		if err != nil {
			fmt.Printf("  ERROR: Failed to start stream: %v\n", err)
			stream.setStopped(fmt.Sprintf("failed: %v", err))
			callbacks.report(recording.ProgressMsg(recording.ProgStreamFailed, recording.ProgressDetailPayload(cam.Name, err.Error())))
		} else {
			stream.cmd = cmd
			stream.setRunning()
			streams = append(streams, stream)
		}

	}

	return streams, unicastWarning
}

func combineStopError(current error, next error) error {
	if next == nil {
		return current
	}
	if current == nil {
		return next
	}
	return fmt.Errorf("%v; %w", current, next)
}

func multicastOutputURL(multicast camerascfg.MulticastConfig, port int) string {
	url := fmt.Sprintf("udp://%s:%d?pkt_size=%d", multicast.IP, port, camerascfg.PktSize)
	if multicast.LocalOnly {
		url += "&ttl=0"
	}
	return url
}

func enabledUnicastDestinations(destinations []camerascfg.UnicastDestination) []camerascfg.UnicastDestination {
	enabled := make([]camerascfg.UnicastDestination, 0, len(destinations)+1)
	seen := make(map[string]struct{}, len(destinations)+1)
	appendDestination := func(address string) {
		normalized := camerascfg.NormalizeUnicastDestinationAddress(address)
		if normalized == "" {
			return
		}
		key := strings.ToLower(normalized)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		enabled = append(enabled, camerascfg.UnicastDestination{Address: normalized, Enabled: true})
	}

	appendDestination(camerascfg.PreviewLoopbackAddress)
	for _, destination := range destinations {
		if !destination.Enabled {
			continue
		}
		appendDestination(destination.Address)
	}
	return enabled
}

func probeUnicastDestinationReachable(address string, port int) error {
	trimmedAddress := strings.TrimSpace(address)
	if trimmedAddress == "" {
		return fmt.Errorf("empty destination address")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid destination port %d", port)
	}

	target := net.JoinHostPort(trimmedAddress, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", target, unicastReachabilityTimeout)
	if err != nil {
		if tcpDialErrorMeansReachable(err) {
			return nil
		}
		return err
	}
	return conn.Close()
}

func tcpDialErrorMeansReachable(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET)
}

func disableUnreachableUnicastDestinations(cfg *camerascfg.Config, port int, checker func(string, int) error) ([]camerascfg.UnicastDestination, []unicastReachabilityIssue, bool) {
	if cfg == nil || !cfg.Unicast.Enabled {
		return nil, nil, false
	}
	if checker == nil {
		checker = checkUnicastDestinationReachable
	}

	issues := make([]unicastReachabilityIssue, 0)
	changed := false
	for index := range cfg.Unicast.Destinations {
		destination := &cfg.Unicast.Destinations[index]
		if !destination.Enabled {
			continue
		}

		trimmedAddress := strings.TrimSpace(destination.Address)
		if trimmedAddress == "" {
			destination.Enabled = false
			issues = append(issues, unicastReachabilityIssue{Address: "(blank)", Err: fmt.Errorf("empty destination address")})
			changed = true
			continue
		}

		if err := checker(trimmedAddress, port); err != nil {
			destination.Enabled = false
			issues = append(issues, unicastReachabilityIssue{Address: trimmedAddress, Err: err})
			changed = true
		}
	}

	return enabledUnicastDestinations(cfg.Unicast.Destinations), issues, changed
}

func formatUnicastReachabilityWarning(issues []unicastReachabilityIssue, port int) string {
	if len(issues) == 0 {
		return ""
	}

	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, fmt.Sprintf("%s:%d (%v)", issue.Address, port, issue.Err))
	}

	if len(parts) == 1 {
		return fmt.Sprintf("Disabled unreachable unicast destination %s", parts[0])
	}
	return fmt.Sprintf("Disabled %d unreachable unicast destinations: %s", len(parts), strings.Join(parts, "; "))
}

func prepareReachableUnicastDestinations(port int) (string, error) {
	if camerasConfig == nil || !camerasConfig.Unicast.Enabled {
		return "", nil
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid unicast destination port %d", port)
	}

	reachable, issues, changed := disableUnreachableUnicastDestinations(camerasConfig, port, nil)
	warning := formatUnicastReachabilityWarning(issues, port)
	for _, issue := range issues {
		logging.WarningLogger.Printf("Disabling unreachable unicast destination %s:%d: %v", issue.Address, port, issue.Err)
	}

	if changed {
		if err := camerascfg.SaveConfig(camerasConfig); err != nil {
			logging.ErrorLogger.Printf("Failed to save disabled unicast destinations: %v", err)
		} else {
			logging.InfoLogger.Printf("Saved cameras config after disabling %d unreachable unicast destination(s)", len(issues))
		}
	}

	if len(reachable) == 0 {
		if warning != "" {
			return "", fmt.Errorf("%s; no reachable unicast destinations remain enabled", warning)
		}
		return "", fmt.Errorf("no reachable unicast destinations remain enabled")
	}
	return warning, nil
}

func broadcastConfigSignature(unicastEnabled bool, multicast camerascfg.MulticastConfig, destinations []camerascfg.UnicastDestination) string {
	if unicastEnabled {
		enabledDestinations := make([]string, 0, len(destinations))
		for _, dest := range destinations {
			if !dest.Enabled {
				continue
			}
			trimmed := strings.TrimSpace(dest.Address)
			if trimmed == "" {
				continue
			}
			enabledDestinations = append(enabledDestinations, trimmed)
		}
		sort.Strings(enabledDestinations)
		return "unicast|" + strings.Join(enabledDestinations, "|")
	}
	return fmt.Sprintf("multicast|%s|local=%t", strings.TrimSpace(multicast.IP), multicast.LocalOnly)
}

// isIntegratedCamera checks if a camera should be excluded when includeAll is false.
// macOS uses AVFoundation transport metadata because raw pixel formats do not distinguish
// built-in and Continuity cameras from USB capture devices.
func isIntegratedCamera(cam recording.DetectedCamera) bool {
	if runtime.GOOS == "darwin" && cam.Format == "avfoundation" {
		if strings.TrimSpace(cam.TransportType) == "" {
			return false
		}
		return !isMacOSCaptureTransport(cam.TransportType)
	}

	// Raw pixel formats are the primary indicator of integrated cameras
	switch cam.PixFmt {
	case "yuyv422", "nv12", "rgb24", "bgr24", "uyvy422":
		// Raw format - likely integrated camera
		return true
	case "h264", "mjpeg":
		// Compressed format - external camera, check name just in case
		lower := strings.ToLower(cam.Name)
		keywords := []string{
			"integrated",
			"internal",
			"built-in",
			"builtin",
			"ir camera",     // IR cameras often on laptops
			"windows hello", // Windows Hello cameras
			"front camera",  // Tablet/laptop front cameras
			"face",          // Face recognition cameras
		}
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				return true
			}
		}
		return false
	default:
		// Unknown format - assume integrated if name matches
		lower := strings.ToLower(cam.Name)
		keywords := []string{
			"integrated",
			"internal",
			"built-in",
			"builtin",
			"ir camera",
			"windows hello",
			"front camera",
			"face",
		}
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				return true
			}
		}
		return true // Unknown raw format - assume integrated
	}
}

func isMacOSCaptureTransport(transportType string) bool {
	switch transportType {
	case "usb ", "pci ":
		return true
	default:
		return false
	}
}

func describeEncodingPlan(cam recording.DetectedCamera, encoder *recording.HwEncoder, fc *ffmpegcfg.Config) string {
	pixFmt := strings.ToLower(strings.TrimSpace(cam.PixFmt))
	switch pixFmt {
	case "h264":
		return "copy input h264"
	case "hevc", "h265":
		return "copy input hevc"
	case "mjpeg":
		if encoder != nil {
			return fmt.Sprintf("encode mjpeg -> h264 via %s (%s)", encoder.Name, encoder.Description)
		}
		if fc != nil && strings.TrimSpace(fc.Software.OutputParameters) != "" {
			return fmt.Sprintf("encode mjpeg -> h264 via software (%s)", strings.TrimSpace(fc.Software.OutputParameters))
		}
		return "encode mjpeg -> h264 via software"
	default:
		if encoder != nil {
			return fmt.Sprintf("encode raw %s -> h264 via %s (%s)", cam.PixFmt, encoder.Name, encoder.Description)
		}
		if fc != nil && strings.TrimSpace(fc.Software.OutputParameters) != "" {
			return fmt.Sprintf("encode raw %s -> h264 via software (%s)", cam.PixFmt, strings.TrimSpace(fc.Software.OutputParameters))
		}
		return fmt.Sprintf("encode raw %s -> h264 via software", cam.PixFmt)
	}
}

type streamCommandSpec struct {
	ffmpegPath string
	args       []string
	udpDest    string
}

type streamOutputMode int

const (
	streamOutputLive streamOutputMode = iota
	streamOutputProbeNull
)

const streamStartupProbeTimeout = 5 * time.Second

const streamStartupProbeAttempts = 2

// streamStartupProbeBackoff is the settle delay between preflight attempts. It
// covers the brief window where a camera the app just released is not yet
// reopenable. Declared as a var so tests can disable the delay.
var streamStartupProbeBackoff = 500 * time.Millisecond

const unicastReachabilityTimeout = 250 * time.Millisecond

type unicastReachabilityIssue struct {
	Address string
	Err     error
}

var checkUnicastDestinationReachable = probeUnicastDestinationReachable

type streamStartupCallbacks struct {
	progress func(string)
	action   func(string)
}

func (c *streamStartupCallbacks) report(message string) {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" || c == nil {
		return
	}
	if c.progress != nil {
		c.progress(trimmed)
	}
	if c.action != nil {
		if actionText := renderProgressActionText(trimmed); actionText != "" {
			c.action(actionText)
		}
	}
}

func resolveStreamFFmpegPath(defaultPath string, encoder *recording.HwEncoder, needsEncoding bool) string {
	defaultPath = strings.TrimSpace(defaultPath)
	if defaultPath == "" {
		defaultPath = "ffmpeg"
	}
	if !needsEncoding || encoder == nil {
		return defaultPath
	}
	if encoderPath := strings.TrimSpace(encoder.FFmpegPath); encoderPath != "" {
		return encoderPath
	}
	return defaultPath
}

func buildStreamCommandSpec(stream *cameraStream, mode streamOutputMode) (streamCommandSpec, error) {
	// cameras app behavior
	// - input
	//   - input format is obtained by probing cameras
	//   - format preference is defined in [cameras] priorities
	// - output
	//   - H.264 input is copied (no re-encode)
	//   - MJPEG and raw inputs are encoded to H.264 using encoder block settings
	cam := stream.camera
	encoder := stream.encoder
	port := stream.port
	camCfg := camerasConfig
	fc := ffmpegConfig
	if camCfg == nil {
		return streamCommandSpec{}, fmt.Errorf("cameras config is not initialized")
	}
	if fc == nil {
		return streamCommandSpec{}, fmt.Errorf("ffmpeg config is not initialized")
	}

	ffmpegPath := config.GetFFmpegPath()

	var udpDest string
	unicastMode := camCfg.Unicast.Enabled
	if unicastMode {
		udpDest = camCfg.Unicast.TeeOutput(port)
	} else {
		udpDest = multicastOutputURL(camCfg.Multicast, port)
	}

	var args []string

	// AVFoundation timestamps can conflict with a wall-clock override when
	// muxed into MPEG-TS, so retain the capture device's native timestamps.
	if cam.Format != "avfoundation" {
		args = append(args, "-use_wallclock_as_timestamps", "1")
	}

	needsEncoding := strings.ToLower(strings.TrimSpace(cam.PixFmt)) != "h264"
	ffmpegPath = resolveStreamFFmpegPath(ffmpegPath, encoder, needsEncoding)

	if needsEncoding && encoder != nil && encoder.InputParameters != "" {
		args = append(args, strings.Fields(encoder.InputParameters)...)
	}

	switch cam.Format {
	case "dshow":
		args = append(args, "-f", "dshow")
		switch cam.PixFmt {
		case "mjpeg":
			args = append(args, "-vcodec", "mjpeg")
		case "h264":
			// dshow cannot reliably negotiate H.264 input on many UVC cameras;
			// omitting -vcodec lets the device deliver its native stream.
		default:
			args = append(args, "-pixel_format", cam.PixFmt)
		}
		args = append(args, "-video_size", cam.Size)
		args = append(args, "-framerate", fmt.Sprintf("%d", cam.Fps))
		if encoder == nil || !strings.Contains(encoder.InputParameters, "rtbufsize") {
			args = append(args, "-rtbufsize", "512M")
		}
		args = append(args, "-i", fmt.Sprintf("video=%s", cam.Device))
		if runtime.GOOS == "windows" {
			args = append(args, "-map", "0:v:0", "-dn")
		}

	case "v4l2":
		args = append(args, "-f", "v4l2")
		switch cam.PixFmt {
		case "mjpeg":
			args = append(args, "-input_format", "mjpeg")
		case "h264":
			args = append(args, "-input_format", "h264")
		default:
			args = append(args, "-input_format", cam.PixFmt)
		}
		args = append(args, "-video_size", cam.Size)
		args = append(args, "-framerate", fmt.Sprintf("%d", cam.Fps))
		args = append(args, "-i", cam.Device)

	case "avfoundation":
		args = append(args, "-f", "avfoundation")
		args = append(args, "-pixel_format", cam.PixFmt)
		args = append(args, "-video_size", cam.Size)
		args = append(args, "-framerate", fmt.Sprintf("%d", cam.Fps))
		args = append(args, "-i", avfoundationInput(cam))

	case "rtsp":
		transport := strings.ToLower(strings.TrimSpace(stream.transport))
		if sourceTransport := strings.ToLower(strings.TrimSpace(transport)); sourceTransport == "udp" || sourceTransport == "tcp" {
			args = append(args, "-rtsp_transport", sourceTransport)
		}
		args = append(args, "-i", cam.Device)
	}

	gopFPS := cam.Fps
	if gopFPS <= 0 {
		gopFPS = 60
	}
	gopSize := gopFPS * fc.Output.GopMultiplier

	switch cam.PixFmt {
	case "h264":
		args = append(args, "-c:v", "copy")
		if cam.Format == "rtsp" {
			args = append(args, "-bsf:v", "h264_mp4toannexb")
		}
	case "hevc", "h265":
		args = append(args, "-c:v", "copy")
		if cam.Format == "rtsp" {
			args = append(args, "-bsf:v", "hevc_mp4toannexb")
		}
	case "mjpeg":
		if encoder != nil {
			if strings.TrimSpace(encoder.VideoFilter) != "" {
				args = append(args, "-vf", strings.TrimSpace(encoder.VideoFilter))
			}
			args = append(args, strings.Fields(encoder.OutputParameters)...)
		} else {
			args = append(args, strings.Fields(fc.Software.OutputParameters)...)
		}
		args = append(args, "-g", fmt.Sprintf("%d", gopSize))
		args = append(args, "-keyint_min", fmt.Sprintf("%d", gopSize))
	default:
		if encoder != nil {
			if strings.TrimSpace(encoder.VideoFilter) != "" {
				args = append(args, "-vf", strings.TrimSpace(encoder.VideoFilter))
			}
			args = append(args, strings.Fields(encoder.OutputParameters)...)
		} else {
			args = append(args, strings.Fields(fc.Software.OutputParameters)...)
		}
		args = append(args, "-g", fmt.Sprintf("%d", gopSize))
		args = append(args, "-keyint_min", fmt.Sprintf("%d", gopSize))
	}

	switch mode {
	case streamOutputProbeNull:
		extra := strings.TrimSpace(strings.ReplaceAll(fc.Output.ExtraFlags, "-f mpegts", ""))
		if extra != "" {
			args = append(args, strings.Fields(extra)...)
		}
		args = append(args, "-frames:v", "1", "-nostats", "-f", "null", "-")
	case streamOutputLive:
		if unicastMode {
			if strings.TrimSpace(udpDest) == "" {
				return streamCommandSpec{}, fmt.Errorf("no enabled unicast destinations")
			}
			extra := fc.Output.ExtraFlags
			usesTee := strings.Contains(udpDest, "|")
			if usesTee {
				extra = strings.ReplaceAll(extra, "-f mpegts", "")
			}
			extra = strings.TrimSpace(extra)
			if extra != "" {
				args = append(args, strings.Fields(extra)...)
			}
			args = append(args, "-map", "0:v")
			args = append(args, "-nostats", "-progress", "pipe:1")
			if usesTee {
				args = append(args, "-f", "tee", udpDest)
			} else {
				const teeMPEGTSLegPrefix = "[f=mpegts:onfail=ignore]"
				args = append(args, strings.TrimPrefix(udpDest, teeMPEGTSLegPrefix))
			}
		} else {
			args = append(args, strings.Fields(fc.Output.ExtraFlags)...)
			args = append(args, "-nostats", "-progress", "pipe:1")
			args = append(args, udpDest)
		}
	default:
		return streamCommandSpec{}, fmt.Errorf("unsupported stream output mode %d", mode)
	}

	return streamCommandSpec{
		ffmpegPath: ffmpegPath,
		args:       args,
		udpDest:    udpDest,
	}, nil
}

// avfoundationInput returns the ffmpeg "-i" value for an AVFoundation camera,
// preferring the stable device name over the volatile numeric index.
func avfoundationInput(cam recording.DetectedCamera) string {
	if name, ok := recording.AVFoundationInputName(cam); ok {
		return name + ":none"
	}
	return cam.Device + ":none"
}

func formatCommandLine(path string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, strconv.Quote(path))
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

func streamNeedsStartupProbe(stream *cameraStream) bool {
	if stream == nil {
		return false
	}
	// Physical capture devices (USB/integrated cameras) can be opened by only one
	// process at a time, and macOS/AVFoundation (and v4l2/dshow) release them
	// asynchronously. A separate null-sink preflight opens then closes the device,
	// and the live stream reopening it immediately races that release, causing
	// exit 251 (I/O error). For these sources the live command is the single
	// open, so no separate preflight. Network sources (RTSP) have no such device
	// exclusivity, so keep the preflight to catch bad URLs/transports early.
	switch stream.camera.Format {
	case "avfoundation", "v4l2", "dshow":
		return false
	}
	return true
}

func summarizeStartupProbeOutput(output string, runErr error) string {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "deprecated pixel format") {
			continue
		}
		const maxLen = 180
		if len(trimmed) > maxLen {
			return trimmed[:maxLen] + "..."
		}
		return trimmed
	}
	if runErr != nil {
		return runErr.Error()
	}
	return "no ffmpeg error details"
}

func formatProbeDiagnostics(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "(no debug output)"
	}
	const maxLen = 6000
	if len(trimmed) > maxLen {
		trimmed = trimmed[:maxLen] + "\n..."
	}
	return strings.ReplaceAll(trimmed, "\n", "\n    ")
}

func runStartupProbeCommand(ffmpegPath string, args []string, logLevel string, timeout time.Duration) (string, error) {
	probeArgs := append([]string{"-hide_banner", "-loglevel", logLevel}, args...)
	cmd := recording.CreateHiddenCmd(ffmpegPath, probeArgs...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return strings.TrimSpace(stderr.String()), err
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var waitErr error
	timedOut := false
	select {
	case waitErr = <-done:
	case <-timer.C:
		timedOut = true
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitErr = <-done
	}

	parts := make([]string, 0, 2)
	if text := strings.TrimSpace(stdout.String()); text != "" {
		parts = append(parts, text)
	}
	if text := strings.TrimSpace(stderr.String()); text != "" {
		parts = append(parts, text)
	}
	output := strings.Join(parts, "\n")
	if timedOut {
		return output, fmt.Errorf("timed out after %s", timeout.Round(time.Second))
	}
	return output, waitErr
}

var runStartupProbeCommandFunc = runStartupProbeCommand

// resolveAVFoundationStreamIndex refreshes the stored AVFoundation index from
// the camera's stable device name. It only matters when avfoundationInput falls
// back to index-based input, since indices are reassigned whenever a device is
// opened or released and a stored one can point at a different camera.
//
// The lookup never uses stream.camera.Name: that field holds the user-assigned
// display name, which either matches no device or matches a different one.
func resolveAVFoundationStreamIndex(stream *cameraStream) {
	if stream == nil || stream.camera.Format != "avfoundation" {
		return
	}
	if _, ok := recording.AVFoundationInputName(stream.camera); ok {
		return
	}
	deviceName, ok := recording.AVFoundationDeviceName(stream.camera)
	if !ok {
		logging.WarningLogger.Printf("No stable AVFoundation identity for %q (matchKey=%q); using stored index %q", stream.camera.Name, stream.camera.MatchKey, stream.camera.Device)
		return
	}
	current, ok := recording.ResolveAVFoundationDeviceIndex(deviceName)
	if !ok {
		logging.WarningLogger.Printf("AVFoundation device %q not found in current device list; using stored index %q", deviceName, stream.camera.Device)
		return
	}
	if current != stream.camera.Device {
		logging.WarningLogger.Printf("AVFoundation index for %q changed from %q to %q; using current index", deviceName, stream.camera.Device, current)
		stream.camera.Device = current
	}
}

// settleAVFoundationDevice waits briefly before opening an AVFoundation capture
// device so an asynchronous release from a prior open (restart, preview,
// detection) can complete, avoiding exit 251 (I/O error) on reopen. It is a
// no-op off macOS and for non-avfoundation sources.
func settleAVFoundationDevice(stream *cameraStream) {
	if runtime.GOOS != "darwin" || stream == nil || stream.camera.Format != "avfoundation" {
		return
	}
	if avfoundationOpenSettle > 0 {
		time.Sleep(avfoundationOpenSettle)
	}
}

func runStartupProbe(stream *cameraStream, callbacks *streamStartupCallbacks) error {
	if !streamNeedsStartupProbe(stream) {
		return nil
	}

	spec, err := buildStreamCommandSpec(stream, streamOutputProbeNull)
	if err != nil {
		return err
	}

	quickArgs := append([]string{"-hide_banner", "-loglevel", "error"}, spec.args...)
	logging.InfoLogger.Printf("Startup stream preflight for %s [%s]: %s", stream.camera.Name, stream.shortID, formatCommandLine(spec.ffmpegPath, quickArgs))
	callbacks.report(recording.ProgressMsg(recording.ProgStreamTest, stream.camera.Name))

	var output string
	for attempt := 1; attempt <= streamStartupProbeAttempts; attempt++ {
		output, err = runStartupProbeCommandFunc(spec.ffmpegPath, spec.args, "error", streamStartupProbeTimeout)
		if err == nil {
			logging.InfoLogger.Printf("Startup stream preflight passed for %s [%s]: %s", stream.camera.Name, stream.shortID, describeEncodingPlan(stream.camera, stream.encoder, ffmpegConfig))
			callbacks.report(recording.ProgressMsg(recording.ProgValidatePassed, stream.camera.Name))
			return nil
		}
		if attempt < streamStartupProbeAttempts {
			logging.InfoLogger.Printf("Startup stream preflight retry %d/%d for %s [%s] after transient failure: %s", attempt, streamStartupProbeAttempts-1, stream.camera.Name, stream.shortID, summarizeStartupProbeOutput(output, err))
			if streamStartupProbeBackoff > 0 {
				time.Sleep(streamStartupProbeBackoff)
			}
		}
	}

	reason := summarizeStartupProbeOutput(output, err)
	logging.ErrorLogger.Printf("Startup stream preflight failed for %s [%s]: %s", stream.camera.Name, stream.shortID, reason)
	callbacks.report(recording.ProgressMsg(recording.ProgValidateFailed, recording.ProgressDetailPayload(stream.camera.Name, reason)))

	debugOutput, debugErr := runStartupProbeCommandFunc(spec.ffmpegPath, spec.args, "debug", streamStartupProbeTimeout)
	if debugErr != nil {
		logging.ErrorLogger.Printf("Startup encoder diagnostics exited for %s [%s]: %v", stream.camera.Name, stream.shortID, debugErr)
	}
	logging.ErrorLogger.Printf("Startup encoder diagnostics for %s [%s]:\n    %s", stream.camera.Name, stream.shortID, formatProbeDiagnostics(debugOutput))

	return fmt.Errorf("stream validation failed: %s", reason)
}

// avfoundationOpenSettle is a short delay before opening an AVFoundation
// capture device. macOS releases a camera asynchronously after a previous
// process (a just-stopped stream on restart, a preview, or detection) closes
// it, and reopening too soon fails with exit 251 (I/O error). A brief wait lets
// the release complete. Declared as a var so tests can disable it; only applied
// on macOS for avfoundation sources.
var avfoundationOpenSettle = 300 * time.Millisecond

// startStream starts ffmpeg to stream a camera to multicast UDP
func startStream(stream *cameraStream, callbacks *streamStartupCallbacks) (*exec.Cmd, error) {
	if err := runStartupProbe(stream, callbacks); err != nil {
		return nil, err
	}
	callbacks.report(recording.ProgressMsg(recording.ProgStreamStart, stream.camera.Name))

	// Re-resolve the AVFoundation index by name right before opening, since
	// numeric indices are not stable and a stored one can point at a different
	// physical camera.
	resolveAVFoundationStreamIndex(stream)
	settleAVFoundationDevice(stream)

	spec, err := buildStreamCommandSpec(stream, streamOutputLive)
	if err != nil {
		return nil, err
	}
	stream.udpDest = spec.udpDest
	stream.commandLine = formatCommandLine(spec.ffmpegPath, spec.args)

	cmd := recording.CreateHiddenCmd(spec.ffmpegPath, spec.args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stream.stdin = stdin

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	logging.InfoLogger.Printf(
		"Stream plan for %s: source=%s format=%s %s@%dfps, %s, destination=%s",
		stream.camera.Name,
		stream.camera.Device,
		stream.camera.Format,
		stream.camera.Size,
		stream.camera.Fps,
		describeEncodingPlan(stream.camera, stream.encoder, ffmpegConfig),
		stream.udpDest,
	)
	logging.InfoLogger.Printf("Starting ffmpeg: %s", stream.commandLine)

	if err := cmd.Start(); err != nil {
		logging.ErrorLogger.Printf("Failed to start ffmpeg for %s [%s]: %v | command=%s", stream.camera.Name, stream.shortID, err, stream.commandLine)
		return nil, err
	}
	if cmd.Process != nil {
		logging.InfoLogger.Printf("ffmpeg started for %s (%s) with pid=%d", stream.camera.Name, stream.udpDest, cmd.Process.Pid)
	}

	if err := jobutil.Assign(cmd); err != nil {
		logging.ErrorLogger.Printf("Failed to assign ffmpeg to job object: %v", err)
	}

	go monitorFFmpegProgress(stream, stdout)
	go monitorFFmpegErrors(stream, stderr)
	go func() {
		err := cmd.Wait()
		wasStopping := stream.isStopping()
		stopReason := stream.getStopReason()
		stream.clearProcessHandles(cmd)
		if err != nil {
			lastErr := stream.getLastStderr()
			if wasStopping {
				if stopReason != "" {
					logging.InfoLogger.Printf("ffmpeg stop completed for %s (%s); reason=%s", stream.camera.Name, stream.udpDest, stopReason)
				}
				if lastErr != "" {
					logging.InfoLogger.Printf("ffmpeg stopped for %s (%s): %v | last stderr: %s", stream.camera.Name, stream.udpDest, err, lastErr)
				} else {
					logging.InfoLogger.Printf("ffmpeg stopped for %s (%s): %v", stream.camera.Name, stream.udpDest, err)
				}
			} else {
				if lastErr != "" {
					logging.ErrorLogger.Printf("ffmpeg exited for %s (%s): %v | last stderr: %s", stream.camera.Name, stream.udpDest, err, lastErr)
				} else {
					logging.ErrorLogger.Printf("ffmpeg exited for %s (%s): %v", stream.camera.Name, stream.udpDest, err)
				}
				if stream.shouldRunActivationDiagnostic() {
					go runActivationDiagnosticRetry(stream)
				}
			}
			stream.setStopped(fmt.Sprintf("stopped: %v", err))
			return
		}
		if wasStopping && stopReason != "" {
			logging.InfoLogger.Printf("ffmpeg stopped cleanly for %s (%s); reason=%s", stream.camera.Name, stream.udpDest, stopReason)
		}
		stream.setStopped("stopped")
	}()

	return cmd, nil
}

// stopProcess stops a camera-related process and verifies the stream port is free afterwards.
func stopProcess(stream *cameraStream, reason string) error {
	if stream == nil {
		return nil
	}

	stream.markStopping(reason)

	stream.mu.Lock()
	cmd := stream.cmd
	port := stream.port
	stream.stdin = nil
	stream.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	if reason != "" {
		logging.InfoLogger.Printf("Stopping ffmpeg for %s (%s); reason=%s", stream.camera.Name, stream.udpDest, reason)
	} else {
		logging.InfoLogger.Printf("Stopping ffmpeg for %s (%s)", stream.camera.Name, stream.udpDest)
	}

	// Live preview / multicast streams produce no file that needs a clean
	// trailer, so we skip any graceful-stop signalling (closing stdin would
	// actually trigger ffmpeg's clean-shutdown path, the opposite of what we
	// want here) and tear the process tree down directly. The stdin pipe is
	// released as part of the process teardown.

	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		if port > 0 {
			return jobutil.WaitForUDPPortFree(port, 500*time.Millisecond)
		}
		return nil
	}

	pid := cmd.Process.Pid

	// Kill the ffmpeg process directly via the OS handle Go already holds.
	// This uses TerminateProcess() on Windows / SIGKILL on Unix and does not
	// depend on PID lookups via taskkill/tasklist subprocesses, so it is both
	// faster and more reliable than the polite taskkill escalation in
	// StopProcessTree. We then still call StopProcessTree to reap any child
	// processes ffmpeg may have spawned (e.g. helpers for some input/output
	// filters), and to wait until the parent is observed gone.
	if err := cmd.Process.Kill(); err != nil {
		logging.InfoLogger.Printf("cmd.Process.Kill() returned for %s (pid=%d): %v", stream.camera.Name, pid, err)
	} else {
		logging.InfoLogger.Printf("cmd.Process.Kill() issued for %s (pid=%d)", stream.camera.Name, pid)
	}

	stopErr := jobutil.StopProcessTree(pid, 2*time.Second)
	if port > 0 {
		stopErr = combineStopError(stopErr, jobutil.StopUDPPortOwners(port, 2*time.Second))
		stopErr = combineStopError(stopErr, jobutil.WaitForUDPPortFree(port, 500*time.Millisecond))
	}
	if stopErr != nil {
		logging.ErrorLogger.Printf("Failed to fully stop %s (%s): %v", stream.camera.Name, stream.udpDest, stopErr)
	} else {
		logging.InfoLogger.Printf("Stopped ffmpeg for %s (%s) pid=%d", stream.camera.Name, stream.udpDest, pid)
	}
	return stopErr
}

func splitCRLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// monitorFFmpegProgress reads structured key=value progress from stdout (-progress pipe:1)
func monitorFFmpegProgress(stream *cameraStream, stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			stream.updateProgress(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}
	if err := scanner.Err(); err != nil {
		logging.WarningLogger.Printf("ffmpeg progress read failed for %s: %v", stream.camera.Name, err)
	}
}

// monitorFFmpegErrors reads stderr for error logging (skip noisy H.264 sync messages)
func monitorFFmpegErrors(stream *cameraStream, stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	scanner.Split(splitCRLF)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		stream.setLastStderr(line)
		if size, ok := parseResolutionFromFFmpegLine(line); ok {
			if stream.updateDetectedResolution(size) {
				logging.InfoLogger.Printf("Observed input resolution for %s [%s]: %s", stream.camera.Name, stream.shortID, size)
			}
		}

		lower := strings.ToLower(line)
		if shouldIgnoreFFmpegStderrLine(lower) {
			continue
		}
		if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "unable") || strings.Contains(lower, "invalid") || strings.Contains(lower, "permission denied") || strings.Contains(lower, "device or resource busy") {
			logging.ErrorLogger.Printf("ffmpeg stderr [%s]: %s", stream.camera.Name, line)
		}
	}
	if err := scanner.Err(); err != nil {
		logging.WarningLogger.Printf("ffmpeg stderr read failed for %s: %v", stream.camera.Name, err)
	}
}

func shouldIgnoreFFmpegStderrLine(lower string) bool {
	if strings.Contains(lower, "decode_slice_header") ||
		strings.Contains(lower, "non-existing pps") ||
		strings.Contains(lower, "no frame") ||
		strings.Contains(lower, "corrupted") {
		return true
	}

	if strings.Contains(lower, "unable to decode app fields") {
		return true
	}

	if strings.Contains(lower, "invalid dts:") && strings.Contains(lower, "replacing by guess") {
		return true
	}

	return false
}

func (s *cameraStream) shouldRunActivationDiagnostic() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping || s.diagnosticRetried || s.progressSeen {
		return false
	}
	s.diagnosticRetried = true
	return true
}

func runActivationDiagnosticRetry(stream *cameraStream) {
	spec, err := buildStreamCommandSpec(stream, streamOutputLive)
	if err != nil {
		logging.ErrorLogger.Printf("Diagnostic retry setup failed for %s [%s]: %v", stream.camera.Name, stream.shortID, err)
		return
	}

	diagnosticArgs := append([]string{"-hide_banner", "-loglevel", "debug"}, spec.args...)
	commandLine := formatCommandLine(spec.ffmpegPath, diagnosticArgs)
	logging.ErrorLogger.Printf("Activation diagnostic retry for %s [%s]: %s", stream.camera.Name, stream.shortID, commandLine)

	cmd := recording.CreateHiddenCmd(spec.ffmpegPath, diagnosticArgs...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		logging.ErrorLogger.Printf("Diagnostic retry failed to start for %s [%s]: %v", stream.camera.Name, stream.shortID, err)
		if text := strings.TrimSpace(stderr.String()); text != "" {
			logging.ErrorLogger.Printf("Diagnostic retry stderr for %s [%s]: %s", stream.camera.Name, stream.shortID, text)
		}
		return
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()

	var waitErr error
	timedOut := false
	select {
	case waitErr = <-done:
	case <-timer.C:
		timedOut = true
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitErr = <-done
	}

	stdoutText := strings.TrimSpace(stdout.String())
	stderrText := strings.TrimSpace(stderr.String())
	if timedOut {
		logging.ErrorLogger.Printf("Diagnostic retry for %s [%s] ran for 3s without reproducing an immediate startup failure", stream.camera.Name, stream.shortID)
	} else if waitErr != nil {
		logging.ErrorLogger.Printf("Diagnostic retry for %s [%s] exited: %v", stream.camera.Name, stream.shortID, waitErr)
	} else {
		logging.ErrorLogger.Printf("Diagnostic retry for %s [%s] exited cleanly", stream.camera.Name, stream.shortID)
	}
	if stdoutText != "" {
		logging.ErrorLogger.Printf("Diagnostic retry stdout for %s [%s]: %s", stream.camera.Name, stream.shortID, stdoutText)
	}
	if stderrText != "" {
		logging.ErrorLogger.Printf("Diagnostic retry stderr for %s [%s]: %s", stream.camera.Name, stream.shortID, stderrText)
	}
}

func (s *cameraStream) setRunning() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.running = true
	s.stopping = false
	s.stopReason = ""
	s.status = "running"
	s.startTime = now
	s.lastUpdate = now
}

func (s *cameraStream) setStopped(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	s.status = status
	s.lastUpdate = time.Now()
}

func (s *cameraStream) markStopping(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopping = true
	s.stopReason = strings.TrimSpace(reason)
}

func (s *cameraStream) isStopping() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stopping
}

func (s *cameraStream) isRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running && !s.stopping
}

func (s *cameraStream) getStopReason() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stopReason
}

func rtspRetryDelay(attempt int) time.Duration {
	switch {
	case attempt <= 0:
		return 2 * time.Second
	case attempt == 1:
		return 4 * time.Second
	default:
		return 8 * time.Second
	}
}

func rtspRetryWindow(retriesStarted int) (time.Duration, bool) {
	if retriesStarted < 0 || retriesStarted >= maxRTSPRetryAttempts {
		return 0, false
	}
	return rtspRetryDelay(retriesStarted), true
}

func (s *cameraStream) autoRestartReason(now time.Time, startupDelay time.Duration) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !strings.EqualFold(strings.TrimSpace(s.sourceType), "rtsp") || s.stopping {
		return "", false
	}

	if s.cmd == nil && !s.running {
		status := strings.TrimSpace(s.status)
		if !s.startTime.IsZero() && now.Sub(s.startTime) < startupDelay {
			return "", false
		}
		if status == "" || status == "stopped" || strings.HasPrefix(status, "stopped:") || strings.HasPrefix(status, "failed:") {
			return "ffmpeg exited", true
		}
		return "", false
	}

	if isUsableFPSValue(s.fps) || s.hasRecentProgressLocked(now, startupDelay) {
		return "", false
	}

	if s.startTime.IsZero() || now.Sub(s.startTime) < startupDelay {
		return "", false
	}

	return fmt.Sprintf("no stream progress after %s", startupDelay.Round(time.Second)), true
}

func (s *cameraStream) isInteractiveReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.running || s.status != "running" {
		return false
	}
	return isUsableFPSValue(s.fps) || s.hasRecentProgressLocked(time.Now(), rtspRetryDelay(0))
}

func (s *cameraStream) hasRecentProgressLocked(now time.Time, maxIdle time.Duration) bool {
	if !s.progressSeen || s.progressSeenAt.IsZero() {
		return false
	}
	if maxIdle <= 0 {
		return true
	}
	if now.Before(s.progressSeenAt) {
		return true
	}
	return now.Sub(s.progressSeenAt) <= maxIdle
}

func (s *cameraStream) clearProcessHandles(cmd *exec.Cmd) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == cmd {
		s.cmd = nil
	}
	s.stdin = nil
}

func (s *cameraStream) setLastStderr(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastStderr = line
}

func (s *cameraStream) getLastStderr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastStderr
}

func (s *cameraStream) updateDetectedResolution(size string) bool {
	size = strings.TrimSpace(size)
	if _, _, ok := parseResolution(size); !ok {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.camera.Size == size {
		return false
	}
	s.camera.Size = size
	return true
}

func parseResolutionFromFFmpegLine(line string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(line))
	if !strings.Contains(lower, "video:") {
		return "", false
	}

	match := ffmpegVideoResolutionPattern.FindStringSubmatch(line)
	if len(match) != 3 {
		return "", false
	}

	size := fmt.Sprintf("%sx%s", match[1], match[2])
	if _, _, ok := parseResolution(size); !ok {
		return "", false
	}
	return size, true
}

func (s *cameraStream) updateProgress(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch key {
	case "frame":
		s.frame = value
		if frameNumber, err := strconv.ParseInt(value, 10, 64); err == nil {
			s.progressFrame = frameNumber
			s.hasProgressFrame = true
		}
	case "out_time":
		// out_time is always HH:MM:SS.ffffff — unambiguous across ffmpeg versions
		if us, ok := parseOutTime(value); ok {
			s.progressOutTimeUS = us
			s.hasProgressOutTime = true
		}
	case "out_time_ms", "out_time_us":
		// Fallback: if out_time wasn't seen yet, try the numeric fields.
		// out_time_ms is µs despite the name in most ffmpeg versions.
		if !s.hasProgressOutTime {
			if micros, err := strconv.ParseInt(value, 10, 64); err == nil {
				s.progressOutTimeUS = micros
				s.hasProgressOutTime = true
			}
		}
	case "fps":
		// Keep ffmpeg-reported fps as fallback, prefer computed progress-delta FPS.
		if isUsableFPSValue(value) {
			s.fps = value
			s.metricName = "FPS"
		}
	case "bitrate":
		s.bitrate = value
	case "speed":
		// Keep ffmpeg speed as fallback, prefer computed drift ratio.
		if normalizedSpeed, ok := normalizeSpeedValue(value); ok {
			s.speed = normalizedSpeed
		}
	case "progress":
		progressWallTime := time.Now()
		s.progressSeen = true
		s.progressSeenAt = progressWallTime
		if s.hasProgressFrame && s.hasProgressOutTime {
			currentProgress := Progress{
				Frame:     s.progressFrame,
				OutTimeUS: s.progressOutTimeUS,
				WallTime:  progressWallTime,
			}

			if s.hasLastProgress {
				metrics := ComputeMetrics(s.lastProgress, currentProgress)

				if metrics.FPS > 0 {
					if !s.hasFPSEMA {
						s.fpsEMA = metrics.FPS
						s.hasFPSEMA = true
					} else {
						s.fpsEMA = (0.8 * s.fpsEMA) + (0.2 * metrics.FPS)
					}
					s.fps = fmt.Sprintf("%.2f", s.fpsEMA)
					s.metricName = "FPS"
				}

				if metrics.Drift > 0 {
					if !s.hasDriftEMA {
						s.driftEMA = metrics.Drift
						s.hasDriftEMA = true
					} else {
						s.driftEMA = (0.8 * s.driftEMA) + (0.2 * metrics.Drift)
					}
					if normalizedDrift, ok := formatRatioValue(s.driftEMA); ok {
						s.speed = normalizedDrift
						s.metricName = "speed"
					}
				}
			}

			s.lastProgress = currentProgress
			s.hasLastProgress = true
		}
		// Reset per-block flags so out_time takes priority in next block
		s.hasProgressOutTime = false
		s.hasProgressFrame = false
		s.running = true
		s.status = "running"
		s.lastUpdate = progressWallTime
	}
}

// parseOutTime parses ffmpeg's out_time field "HH:MM:SS.ffffff" to microseconds.
func parseOutTime(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "N/A" {
		return 0, false
	}

	// Split "HH:MM:SS.ffffff" into time part and fractional seconds
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 {
		return 0, false
	}
	hours, err1 := strconv.Atoi(parts[0])
	minutes, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, false
	}

	// parts[2] is "SS.ffffff" or "SS"
	secParts := strings.SplitN(parts[2], ".", 2)
	seconds, err3 := strconv.Atoi(secParts[0])
	if err3 != nil {
		return 0, false
	}

	totalUS := int64(hours)*3_600_000_000 + int64(minutes)*60_000_000 + int64(seconds)*1_000_000

	if len(secParts) == 2 {
		frac := secParts[1]
		// Pad or truncate to 6 digits (microseconds)
		for len(frac) < 6 {
			frac += "0"
		}
		frac = frac[:6]
		fracUS, err := strconv.Atoi(frac)
		if err != nil {
			return 0, false
		}
		totalUS += int64(fracUS)
	}

	return totalUS, true
}

func isUsableFPSValue(value string) bool {
	if value == "" || value == "-" || value == "0" || value == "0.0" || value == "0.00" || value == "N/A" {
		return false
	}
	if strings.HasSuffix(value, "x") {
		return false
	}
	return true
}

func formatRatioValue(ratio float64) (string, bool) {
	if !(ratio > 0 && ratio <= 10.0) {
		return "", false
	}
	return fmt.Sprintf("%.2fx", ratio), true
}

func normalizeSpeedValue(value string) (string, bool) {
	if value == "" || value == "-" || value == "N/A" {
		return "", false
	}
	if !strings.HasSuffix(value, "x") {
		return "", false
	}
	raw := strings.TrimSuffix(value, "x")
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return "", false
	}
	return formatRatioValue(n)
}

func formatMeasuredFPSValue(raw string) string {
	if !isUsableFPSValue(raw) {
		return "-"
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	return fmt.Sprintf("%.2f", value)
}

func formatExpectedFPSValue(expected int) string {
	if expected <= 0 {
		return "-"
	}
	return strconv.Itoa(expected)
}

func multicastPort(port int) string {
	if port <= 0 {
		return "-"
	}
	return strconv.Itoa(port)
}

func monitoringSourceNeedsRestart(spec sourceSpec, stream *cameraStream, now time.Time, recovery *rtspRecoveryState) bool {
	if hasDirtyReason(spec.DirtyReasons, "restart") {
		return true
	}
	if !spec.Enabled || spec.OutputPort <= 0 {
		return false
	}
	if recovery != nil && recovery.attention {
		return true
	}
	if stream == nil {
		return true
	}
	if !strings.EqualFold(spec.SourceType, "rtsp") {
		return false
	}
	_, shouldRestart := stream.autoRestartReason(now, rtspRetryDelay(0))
	return shouldRestart
}

func rtspStartingAttemptLabel(recovery *rtspRecoveryState) string {
	attempt := 1
	if recovery != nil {
		attempt += recovery.attempts
	}
	if attempt < 1 {
		attempt = 1
	}
	if attempt > maxRTSPRetryAttempts {
		attempt = maxRTSPRetryAttempts
	}
	return fmt.Sprintf("starting (%d/%d)", attempt, maxRTSPRetryAttempts)
}

func monitoringSourceStatus(spec sourceSpec, stream *cameraStream, recovery *rtspRecoveryState) string {
	if stream == nil {
		if !spec.Detected && strings.EqualFold(spec.SourceType, "usb") {
			return "not connected"
		}
	}

	if strings.EqualFold(spec.SourceType, "rtsp") && recovery != nil && !recovery.exhausted {
		if stream == nil {
			if recovery.attention || !recovery.nextRetry.IsZero() {
				return rtspStartingAttemptLabel(recovery)
			}
		}
	}

	if stream == nil {
		return "stopped"
	}

	status := stream.snapshotRow()[8]
	if !strings.EqualFold(spec.SourceType, "rtsp") {
		return status
	}
	if stream.isInteractiveReady() {
		return status
	}

	normalized := strings.ToLower(strings.TrimSpace(status))
	if strings.HasPrefix(normalized, "stopped") || strings.HasPrefix(normalized, "failed") {
		if recovery != nil && !recovery.exhausted {
			return rtspStartingAttemptLabel(recovery)
		}
		return "stopped"
	}
	if recovery != nil && recovery.attention {
		return rtspStartingAttemptLabel(recovery)
	}
	if normalized == "running" || normalized == "starting" {
		return rtspStartingAttemptLabel(recovery)
	}
	return status
}

func (s *cameraStream) snapshotRow() [9]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	name := s.camera.Name
	if strings.TrimSpace(s.shortID) != "" {
		name = fmt.Sprintf("%s [%s]", name, s.shortID)
	}

	return [9]string{
		name,
		s.camera.PixFmt,
		s.camera.Size,
		formatExpectedFPSValue(s.camera.Fps),
		formatMeasuredFPSValue(s.fps),
		multicastPort(s.port),
		"Preview",
		"Record 10s",
		s.status,
	}
}

func parseResolution(size string) (int, int, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(size)), "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	width, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func previewWindowSize(width, height int) (int, int, bool) {
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}

	maxWidth := previewMaxLongSide
	maxHeight := previewMaxShortSide
	if height > width {
		maxWidth = previewMaxShortSide
		maxHeight = previewMaxLongSide
	}

	if width <= maxWidth && height <= maxHeight {
		return width, height, true
	}

	scale := math.Min(float64(maxWidth)/float64(width), float64(maxHeight)/float64(height))
	if !(scale > 0) {
		return 0, 0, false
	}

	scaledWidth := int(math.Round(float64(width) * scale))
	scaledHeight := int(math.Round(float64(height) * scale))
	if scaledWidth < 1 {
		scaledWidth = 1
	}
	if scaledHeight < 1 {
		scaledHeight = 1
	}
	return scaledWidth, scaledHeight, true
}

func previewArgsForSize(size string) []string {
	if width, height, ok := parseResolution(size); ok {
		if previewWidth, previewHeight, ok := previewWindowSize(width, height); ok {
			return []string{"-x", strconv.Itoa(previewWidth), "-y", strconv.Itoa(previewHeight)}
		}
	}

	box := strconv.Itoa(previewMaxLongSide)
	filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", previewMaxLongSide, previewMaxLongSide)
	return []string{"-x", box, "-y", box, "-vf", filter}
}

func encoderSummaryText(encoder *recording.HwEncoder, fc *ffmpegcfg.Config) string {
	if encoder != nil {
		summary := fmt.Sprintf("Encoder: %s", encoder.Name)
		if desc := strings.TrimSpace(encoder.Description); desc != "" {
			summary += fmt.Sprintf(" (%s)", desc)
		}
		return summary
	}
	if fc == nil {
		return "Encoder: software"
	}
	params := strings.TrimSpace(fc.Software.OutputParameters)
	if params == "" {
		return "Encoder: software"
	}
	return fmt.Sprintf("Encoder: software (%s)", params)
}

func resolveFFplayPath() string {
	if envPath := strings.TrimSpace(os.Getenv("VIDEO_FFPLAY_PATH")); envPath != "" {
		return envPath
	}

	ffmpegPath := config.GetFFmpegPath()
	if ffmpegPath == "" {
		ffplayName := "ffplay"
		if runtime.GOOS == "windows" {
			ffplayName = "ffplay.exe"
		}
		if sharedPath := config.FindSharedFFmpegExecutable(ffplayName); sharedPath != "" {
			return sharedPath
		}
		return ffplayName
	}

	ffplayName := "ffplay"
	if runtime.GOOS == "windows" {
		ffplayName = "ffplay.exe"
	}

	candidate := filepath.Join(filepath.Dir(ffmpegPath), ffplayName)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}

	if sharedPath := config.FindSharedFFmpegExecutable(ffplayName); sharedPath != "" {
		return sharedPath
	}

	return ffplayName
}

func registerPreviewCmd(cmd *exec.Cmd) {
	previewMu.Lock()
	previewCmds = append(previewCmds, cmd)
	previewMu.Unlock()
}

func unregisterPreviewCmd(cmd *exec.Cmd) {
	previewMu.Lock()
	defer previewMu.Unlock()

	for i, existing := range previewCmds {
		if existing == cmd {
			previewCmds = append(previewCmds[:i], previewCmds[i+1:]...)
			return
		}
	}
}

func stopAllPreviews() {
	previewMu.Lock()
	cmds := append([]*exec.Cmd(nil), previewCmds...)
	previewMu.Unlock()

	for _, cmd := range cmds {
		stopProcess(&cameraStream{cmd: cmd, camera: recording.DetectedCamera{Name: "preview"}, udpDest: "preview"}, "stop preview")
	}
}

// listenURL returns a UDP URL suitable for receiving (listening to) the stream.
// In multicast mode it returns the multicast group address; in unicast mode
// it returns udp://127.0.0.1:<port> so that ffplay / ffmpeg can listen on the
// localhost copy that the tee muxer sends.
func (s *cameraStream) listenURL() string {
	if s.unicastMode {
		return fmt.Sprintf("udp://127.0.0.1:%d", s.port)
	}
	return s.udpDest
}

func (s *cameraStream) previewListenURL() string {
	raw := s.listenURL()
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	if query.Get("overrun_nonfatal") == "" {
		query.Set("overrun_nonfatal", "1")
	}
	if query.Get("fifo_size") == "" {
		query.Set("fifo_size", "50000")
	}
	if query.Get("buffer_size") == "" {
		query.Set("buffer_size", "65535")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func activatePreviewWindow(pid int) {
	if runtime.GOOS != "darwin" || pid <= 0 {
		return
	}

	go func() {
		var lastErr error
		var lastOutput string
		activated := false
		for attempt := 1; attempt <= 12; attempt++ {
			time.Sleep(250 * time.Millisecond)
			script := fmt.Sprintf(`ObjC.import('AppKit'); var application = $.NSRunningApplication.runningApplicationWithProcessIdentifier(%d); if (!application || !application.activateWithOptions($.NSApplicationActivateIgnoringOtherApps)) { throw new Error('Preview window is not ready'); }`, pid)
			output, err := exec.Command("osascript", "-l", "JavaScript", "-e", script).CombinedOutput()
			if err == nil {
				activated = true
				continue
			}
			lastErr = err
			lastOutput = strings.TrimSpace(string(output))
		}
		if activated {
			logging.InfoLogger.Printf("Preview window activated for pid=%d", pid)
			return
		}
		logging.WarningLogger.Printf("Could not activate Preview window for pid=%d: %v: %s", pid, lastErr, lastOutput)
	}()
}

func launchPreview(stream *cameraStream, onDone func()) error {
	startTime := time.Now()
	args := []string{
		"-fflags", "nobuffer",
		"-flags", "low_delay",
		"-sync", "video",
		"-framedrop",
	}
	if runtime.GOOS == "darwin" {
		args = append(args, "-alwaysontop")
	}
	args = append(args, previewArgsForSize(stream.camera.Size)...)
	listenURL := stream.previewListenURL()
	args = append(args, listenURL)

	ffplayPath := resolveFFplayPath()
	logging.InfoLogger.Printf("Preview launch requested for %s [%s]: ffplay=%s listenURL=%s size=%s port=%d", stream.camera.Name, stream.shortID, ffplayPath, listenURL, stream.camera.Size, stream.port)
	cmd := recording.CreateHiddenCmd(ffplayPath, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("create ffplay stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		logging.ErrorLogger.Printf("Preview start failed for %s [%s] after %s: %v", stream.camera.Name, stream.shortID, time.Since(startTime), err)
		return err
	}
	logging.InfoLogger.Printf("Preview process started for %s [%s] after %s with pid=%d", stream.camera.Name, stream.shortID, time.Since(startTime), cmd.Process.Pid)
	activatePreviewWindow(cmd.Process.Pid)

	if err := jobutil.Assign(cmd); err != nil {
		logging.ErrorLogger.Printf("Failed to assign ffplay to job object: %v", err)
	}

	registerPreviewCmd(cmd)
	go logPreviewErrors(stream, stderr, cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		if err != nil {
			logging.InfoLogger.Printf("Preview process exited for %s [%s] with error after %s: %v", stream.camera.Name, stream.shortID, time.Since(startTime), err)
		} else {
			logging.InfoLogger.Printf("Preview process exited normally for %s [%s] after %s", stream.camera.Name, stream.shortID, time.Since(startTime))
		}
		unregisterPreviewCmd(cmd)
		if onDone != nil {
			onDone()
		}
	}()

	return nil
}

func logPreviewErrors(stream *cameraStream, stderr io.ReadCloser, previewPID int) {
	scanner := bufio.NewScanner(stderr)
	previewWindowReady := false
	for scanner.Scan() {
		message := strings.TrimSpace(scanner.Text())
		if message != "" {
			if !previewWindowReady && strings.HasPrefix(message, "Input #") {
				previewWindowReady = true
				activatePreviewWindow(previewPID)
			}
			if shouldIgnoreFFplayStderrLine(message) {
				continue
			}
			logging.WarningLogger.Printf("ffplay stderr [%s]: %s", stream.camera.Name, message)
		}
	}
	if err := scanner.Err(); err != nil {
		logging.WarningLogger.Printf("ffplay stderr read failed for %s: %v", stream.camera.Name, err)
	}
}

func shouldIgnoreFFplayStderrLine(message string) bool {
	return strings.Contains(strings.ToLower(message), "packet corrupt")
}

func sanitizeFilePart(value string) string {
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_", ";", "_", "|", "_", "?", "_", "*", "_")
	cleaned := replacer.Replace(strings.TrimSpace(value))
	if cleaned == "" {
		return "camera"
	}
	return cleaned
}

func buildClipPath(stream *cameraStream) string {
	timestamp := time.Now().Format("20060102-150405")
	cameraName := sanitizeFilePart(stream.camera.Name)
	return filepath.Join(os.TempDir(), fmt.Sprintf("%s_%s.mp4", cameraName, timestamp))
}

// openFile opens a file with the OS default application and calls onDone when the viewer exits.
func openFile(path string, onDone func()) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		logging.ErrorLogger.Printf("Failed to open file %s: %v", path, err)
		return
	}
	go func() {
		_ = cmd.Wait()
		if onDone != nil {
			onDone()
		}
	}()
}

func recordClip(stream *cameraStream) (string, error) {
	clipInput := stream.listenURL()
	if runtime.GOOS == "windows" {
		parsed, err := url.Parse(clipInput)
		if err == nil {
			query := parsed.Query()
			query.Del("pkt_size")
			if query.Get("overrun_nonfatal") == "" {
				query.Set("overrun_nonfatal", "1")
			}
			if query.Get("fifo_size") == "" {
				query.Set("fifo_size", "50000")
			}
			parsed.RawQuery = query.Encode()
			clipInput = parsed.String()
		}
	}

	outputPath := buildClipPath(stream)
	args := []string{
		"-fflags", "nobuffer",
		"-flags", "low_delay",
		"-y",
		"-t", "10",
		"-i", clipInput,
		"-c", "copy",
		"-movflags", "+faststart",
		outputPath,
	}

	cmd := recording.CreateHiddenCmd(config.GetFFmpegPath(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stderrText := strings.TrimSpace(stderr.String())
		if stderrText != "" {
			return "", fmt.Errorf("%w: %s", err, stderrText)
		}
		return "", err
	}
	return outputPath, nil
}

type appTheme struct{ fyne.Theme }

var (
	borderedCheckButtonIcon = fyne.NewStaticResource("checkbutton-bordered.svg", []byte(`<svg width="24" height="24" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
	<rect x="4.25" y="4.25" width="15.5" height="15.5" rx="2" fill="none" stroke="#4f4f4f" stroke-width="1.8"/>
</svg>`))
	borderedCheckButtonCheckedIcon = fyne.NewStaticResource("checkbutton-bordered-checked.svg", []byte(`<svg width="24" height="24" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
	<rect x="4.25" y="4.25" width="15.5" height="15.5" rx="2" fill="none" stroke="#4f4f4f" stroke-width="1.8"/>
  <path d="M8 12.5L10.7 15.2L16.5 9.4" fill="none" stroke="#1f6feb" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`))
	borderedCheckButtonFillIcon = fyne.NewStaticResource("checkbutton-bordered-fill.svg", []byte(`<svg width="24" height="24" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
	<rect x="4.25" y="4.25" width="15.5" height="15.5" rx="2" fill="#dbeafe" stroke="#4f4f4f" stroke-width="1.8"/>
  <path d="M8 12.5L10.7 15.2L16.5 9.4" fill="none" stroke="#1f6feb" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`))
)

func (t appTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if name == theme.ColorNameButton {
		// Visible steel-blue for medium-importance buttons
		return color.RGBA{R: 185, G: 205, B: 225, A: 255}
	}
	if name == theme.ColorNameError {
		return color.RGBA{R: 122, G: 24, B: 24, A: 255}
	}
	return t.Theme.Color(name, variant)
}

func (t appTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	switch name {
	case theme.IconNameCheckButton:
		return borderedCheckButtonIcon
	case theme.IconNameCheckButtonChecked:
		return borderedCheckButtonCheckedIcon
	case theme.IconNameCheckButtonFill:
		return borderedCheckButtonFillIcon
	default:
		return t.Theme.Icon(name)
	}
}

type portTableEntry struct {
	widget.Entry
	onFocusGained func()
	onFocusLost   func(string)
}

func newPortTableEntry() *portTableEntry {
	entry := &portTableEntry{}
	entry.ExtendBaseWidget(entry)
	return entry
}

func (e *portTableEntry) FocusGained() {
	e.Entry.FocusGained()
	if e.onFocusGained != nil {
		e.onFocusGained()
	}
}

func (e *portTableEntry) FocusLost() {
	e.Entry.FocusLost()
	if e.onFocusLost != nil {
		e.onFocusLost(e.Text)
	}
}

// UI exposes the Cameras module tabs, menus and lifecycle hooks to the host application.
type UI struct {
	Monitoring    fyne.CanvasObject
	Configuration fyne.CanvasObject
	Menus         []*fyne.Menu
	Start         func()
	Stop          func()
}

// BuildUI constructs the Cameras module interface inside the host window.
func BuildUI(window fyne.Window) *UI {
	headers := []string{"Name", "Short ID", "Port", "Format", "Encoder", "Resolution", "Expected FPS", "Measured FPS", "Start", "Stop", "Status", "Preview", "Record"}
	cameraStatusLabel := widget.NewLabel(detectionProgressUIStrings.DetectingSourcesStatus)
	cameraStatusLabel.TextStyle = fyne.TextStyle{Bold: true}
	actionStatus := widget.NewLabel("")
	clipLink := widget.NewHyperlink("", nil)
	clipLink.Hide()
	var currentEncoder *recording.HwEncoder
	encoderStatusLabel := widget.NewLabel(encoderSummaryText(nil, ffmpegConfig))
	updateEncoderStatus := func() {
		encoderStatusLabel.SetText(encoderSummaryText(currentEncoder, ffmpegConfig))
	}
	var monitoringSources []sourceSpec

	var clipLinkTimer *time.Timer
	scheduleClipHide := func(d time.Duration) {
		if clipLinkTimer != nil {
			clipLinkTimer.Stop()
		}
		clipLinkTimer = time.AfterFunc(d, func() {
			fyne.Do(func() {
				clipLink.Hide()
				actionStatus.SetText("Preview/Record: ready")
			})
		})
	}

	ipEntry := newPortTableEntry()
	ipEntry.SetText(camerasConfig.Multicast.IP)
	ipEntryField := container.NewGridWrap(fyne.NewSize(170, ipEntry.MinSize().Height), ipEntry)
	modeSelect := widget.NewSelect([]string{"Multicast", "Unicast"}, nil)
	if camerasConfig.Unicast.Enabled {
		modeSelect.SetSelected("Unicast")
	} else {
		modeSelect.SetSelected("Multicast")
	}
	unicastDestinations := append([]camerascfg.UnicastDestination(nil), camerasConfig.Unicast.Destinations...)
	unicastContainer := container.NewVBox()

	var applyBroadcastUIToConfig func() error
	var syncBroadcastUIFromConfig func(bool)
	var setBroadcastRestartDirty func()
	var renderUnicastRows func()
	renderUnicastRows = func() {
		unicastContainer.Objects = nil
		unicastContainer.Add(container.NewHBox(
			container.NewGridWrap(fyne.NewSize(70, widget.NewLabel("Enabled").MinSize().Height), widget.NewLabel("Enabled")),
			container.NewGridWrap(fyne.NewSize(320, widget.NewLabel("Destination").MinSize().Height), widget.NewLabel("Destination")),
			container.NewGridWrap(fyne.NewSize(80, widget.NewLabel("").MinSize().Height), widget.NewLabel("")),
		))
		for i := range unicastDestinations {
			idx := i
			check := widget.NewCheck("", nil)
			check.SetChecked(unicastDestinations[idx].Enabled)
			check.OnChanged = func(v bool) {
				unicastDestinations[idx].Enabled = v
				setBroadcastRestartDirty()
			}
			entry := newPortTableEntry()
			entry.SetText(unicastDestinations[idx].Address)
			entry.OnChanged = func(v string) {
				unicastDestinations[idx].Address = v
				setBroadcastRestartDirty()
			}
			entry.onFocusLost = nil
			entry.OnSubmitted = nil
			removeBtn := widget.NewButton("Remove", func() {
				unicastDestinations = append(unicastDestinations[:idx], unicastDestinations[idx+1:]...)
				renderUnicastRows()
				setBroadcastRestartDirty()
			})
			unicastContainer.Add(container.NewHBox(
				check,
				container.NewGridWrap(fyne.NewSize(320, entry.MinSize().Height), entry),
				container.NewGridWrap(fyne.NewSize(80, removeBtn.MinSize().Height), removeBtn),
			))
		}
		// Blank add row at the bottom
		addEntry := widget.NewEntry()
		addEntry.SetPlaceHolder("Enter destination IP or host...")
		addBtn := widget.NewButton("Add", func() {
			val := strings.TrimSpace(addEntry.Text)
			if val == "" {
				return
			}
			unicastDestinations = append(unicastDestinations, camerascfg.UnicastDestination{Address: val, Enabled: true})
			renderUnicastRows()
			setBroadcastRestartDirty()
		})
		checkSpacer := widget.NewCheck("", nil)
		checkSpacer.Disable()
		unicastContainer.Add(container.NewHBox(
			checkSpacer,
			container.NewGridWrap(fyne.NewSize(320, addEntry.MinSize().Height), addEntry),
			container.NewGridWrap(fyne.NewSize(80, addBtn.MinSize().Height), addBtn),
		))
		unicastContainer.Refresh()
	}
	renderUnicastRows()
	localOnlyCheck := widget.NewCheck("Local only", nil)
	localOnlyCheck.SetChecked(camerasConfig.Multicast.LocalOnly)

	var streams []*cameraStream
	currentStreams := &streams
	var currentInventory sourceInventory
	rtspRecoveryStates := make(map[string]*rtspRecoveryState)
	currentStreamingCount := func() int {
		count := 0
		for _, stream := range *currentStreams {
			if stream == nil {
				continue
			}
			stream.mu.RLock()
			running := stream.running && strings.EqualFold(strings.TrimSpace(stream.status), "running") && !stream.stopping
			stream.mu.RUnlock()
			if running {
				count++
			}
		}
		return count
	}
	updateCameraStatusLabel := func(fallback string) {
		status := strings.TrimSpace(fallback)
		if len(currentInventory.Errors) > 0 {
			status = strings.Join(currentInventory.Errors, " | ")
		}
		if running := currentStreamingCount(); running > 0 {
			status = fmt.Sprintf("%d source(s) streaming.", running)
		}
		if status == "" {
			status = currentInventory.Status
		}
		cameraStatusLabel.SetText(status)
	}
	restartBtn := widget.NewButton("Restart Streams", nil)
	restartBtn.Importance = widget.HighImportance
	stopAllBtn := widget.NewButton("Stop All Streams", nil)
	stopAllBtn.Importance = widget.HighImportance
	broadcastRestartBtn := widget.NewButton("Restart", nil)
	broadcastRestartBtn.Importance = widget.MediumImportance
	rescanBtn := widget.NewButton(detectionProgressUIStrings.RescanButtonLabel, nil)
	currentBroadcastUISignature := func() string {
		if strings.TrimSpace(modeSelect.Selected) == "Unicast" {
			return broadcastConfigSignature(true, camerascfg.MulticastConfig{}, unicastDestinations)
		}
		return broadcastConfigSignature(false, camerascfg.MulticastConfig{
			IP:        strings.TrimSpace(ipEntry.Text),
			LocalOnly: localOnlyCheck.Checked,
		}, nil)
	}
	savedBroadcastSignature := currentBroadcastUISignature()
	setBroadcastRestartDirty = func() {
		applyRestartButtonStyle(broadcastRestartBtn, currentBroadcastUISignature() != savedBroadcastSignature)
	}
	syncBroadcastUIFromConfig = func(force bool) {
		if !force && currentBroadcastUISignature() != savedBroadcastSignature {
			return
		}

		if camerasConfig.Unicast.Enabled {
			modeSelect.SetSelected("Unicast")
			unicastDestinations = append([]camerascfg.UnicastDestination(nil), camerasConfig.Unicast.Destinations...)
			renderUnicastRows()
		} else {
			modeSelect.SetSelected("Multicast")
			ipEntry.SetText(camerasConfig.Multicast.IP)
			localOnlyCheck.SetChecked(camerasConfig.Multicast.LocalOnly)
		}

		savedBroadcastSignature = currentBroadcastUISignature()
		setBroadcastRestartDirty()
	}

	usbRowsContainer := container.NewVBox()
	rtspRowsContainer := container.NewVBox()
	var usbRows []*usbSourceRow
	var rtspRows []*rtspSourceRow
	var table *widget.Table
	var applyUIToConfig func() error
	var restartStreams func()
	var renderMonitoringSourceToggles func(sourceInventory)
	var restartWithInventory func(sourceInventory, string)
	var refreshInventoryFromConfig func(bool)
	var restartSource func(sourceSpec) error
	var findInventorySource func(string) (*sourceSpec, bool)
	var sortUSBRows func()
	var sortRTSPRows func()
	var syncUSBRowFromSpec func(*usbSourceRow, sourceSpec)
	var syncRTSPRowFromSpec func(*rtspSourceRow, sourceSpec)
	var saveUSBRow func(*usbSourceRow) error
	var removeUSBRow func(*usbSourceRow) error
	var saveRTSPRow func(*rtspSourceRow) error
	var removeRTSPRow func(*rtspSourceRow) error
	var clearRestartDirtyForSource func(sourceSpec) error
	var toggleSingleSource func(sourceSpec, bool) error
	var validateSourceStart func(*sourceSpec) error
	var promptToFreeBusyPort func(*sourceSpec, func())
	var startSourceFromUI func(sourceSpec)
	var stopSourceFromUI func(sourceSpec)
	var stopSingleSource func(string, string) error
	normalizeRTSPRows := func() {
		var normalized []*rtspSourceRow
		for _, row := range rtspRows {
			if row == nil {
				continue
			}
			if row.isAddRow && row.hasContent() {
				row.isAddRow = false
			}
			if row.isAddRow {
				continue
			}
			normalized = append(normalized, row)
		}
		rtspRows = normalized
		rtspRows = append(rtspRows, newBlankRTSPSourceRow())
	}

	renderUSBRows := func() {
		if sortUSBRows != nil {
			sortUSBRows()
		}
		objects := []fyne.CanvasObject{
			container.NewHBox(
				fixedWidth(usbEnabledWidth, widget.NewLabel("Enabled")),
				fixedWidth(usbIdentityWidth, widget.NewLabel("Stable Identity")),
				fixedWidth(usbNameWidth, widget.NewLabel("Name")),
				fixedWidth(usbShortIDWidth, widget.NewLabel("Short ID")),
				fixedWidth(usbPortWidth, widget.NewLabel("Port")),
				fixedWidth(usbFormatWidth, widget.NewLabel("Format")),
				fixedWidth(usbRestartWidth, widget.NewLabel("")),
				fixedWidth(usbProbeWidth, widget.NewLabel("")),
				fixedWidth(usbRemoveWidth, widget.NewLabel("")),
			),
		}
		for i, row := range usbRows {
			index := i
			objects = append(objects, row.object(func() {
				// Probe: re-detect the camera on demand
				actionStatus.SetText("Probing USB camera...")
				probeUSBSource(usbRows[index].matchKey, func(cam *recording.DetectedCamera) {
					if cam == nil {
						dialog.ShowInformation("USB Probe", "Camera not found or not connected.", window)
						actionStatus.SetText("")
						return
					}
					usbRows[index].detectedPixFmt = cam.PixFmt
					usbRows[index].detectedSize = cam.Size
					usbRows[index].detectedFPS = cam.Fps
					usbRows[index].detectedFormats = append([]string(nil), cam.SupportedFormats...)
					usbRows[index].dirtyReasons = removeReason(usbRows[index].dirtyReasons, "probe")
					for i := range currentInventory.USB {
						if currentInventory.USB[i].Key != usbRows[index].matchKey {
							continue
						}
						currentInventory.USB[i].Detected = true
						currentInventory.USB[i].Camera = *cam
						currentInventory.USB[i].SupportedFormats = append([]string(nil), cam.SupportedFormats...)
						break
					}
					usbRows[index].detected = true
					usbRows[index].enabledCheck.Enable()
					if err := saveUSBRow(usbRows[index]); err != nil {
						dialog.ShowError(err, window)
						actionStatus.SetText(fmt.Sprintf("Save failed: %v", err))
						return
					}
					msg := fmt.Sprintf("%s  %s  %d fps", cam.PixFmt, cam.Size, cam.Fps)
					dialog.ShowInformation("USB Camera Detected", msg, window)
					actionStatus.SetText(msg)
				})
			}, func() bool {
				if err := saveUSBRow(usbRows[index]); err != nil {
					dialog.ShowError(err, window)
					actionStatus.SetText(fmt.Sprintf("Save failed: %v", err))
					return false
				}
				return true
			}, func() {
				if err := removeUSBRow(usbRows[index]); err != nil {
					dialog.ShowError(err, window)
					actionStatus.SetText(fmt.Sprintf("Remove failed: %v", err))
				}
			}, func() {
				row := usbRows[index]
				wasRunning := false
				for _, stream := range *currentStreams {
					if stream != nil && stream.camera.MatchKey == row.matchKey {
						wasRunning = true
						break
					}
				}
				name := strings.TrimSpace(row.nameEntry.Text)
				if name == "" {
					name = row.identity
				}
				if wasRunning && !row.enabledCheck.Checked {
					dialog.ShowConfirm("Stop Stream?",
						fmt.Sprintf("This will stop the camera stream for %s.", name),
						func(ok bool) {
							if !ok {
								return
							}
							if err := saveUSBRow(row); err != nil {
								dialog.ShowError(err, window)
								actionStatus.SetText(fmt.Sprintf("Apply failed: %v", err))
								return
							}
							if err := stopSingleSource(row.matchKey, "disabled by user"); err != nil {
								dialog.ShowError(err, window)
								actionStatus.SetText(fmt.Sprintf("Stop failed: %v", err))
								return
							}
							actionStatus.SetText(fmt.Sprintf("Stopped %s.", name))
						}, window)
					return
				}
				if err := saveUSBRow(row); err != nil {
					dialog.ShowError(err, window)
					actionStatus.SetText(fmt.Sprintf("Apply failed: %v", err))
					return
				}
				if !wasRunning {
					actionStatus.SetText(fmt.Sprintf("Applied %s.", name))
					return
				}
				item, ok := findInventorySource(row.matchKey)
				if !ok {
					dialog.ShowError(fmt.Errorf("source %s not found", name), window)
					actionStatus.SetText(fmt.Sprintf("Apply failed: source %s not found", name))
					return
				}
				dialog.ShowConfirm("Restart?", fmt.Sprintf("%s is running. Restart?", name), func(restartNow bool) {
					if !restartNow {
						actionStatus.SetText(fmt.Sprintf("Applied %s. Restart required.", name))
						return
					}
					actionStatus.SetText(fmt.Sprintf("Restarting %s...", name))
					if err := restartSource(*item); err != nil {
						dialog.ShowError(err, window)
						actionStatus.SetText(fmt.Sprintf("Restart failed: %v", err))
						return
					}
					if updated, ok := findInventorySource(row.matchKey); ok {
						syncUSBRowFromSpec(row, *updated)
					}
					usbRowsContainer.Refresh()
					if current := strings.TrimSpace(row.nameEntry.Text); current != "" {
						actionStatus.SetText(fmt.Sprintf("Restarted %s.", current))
					}
				}, window)
			}))
		}
		usbRowsContainer.Objects = objects
		usbRowsContainer.Refresh()
	}

	var renderRTSPRows func()
	renderRTSPRows = func() {
		if sortRTSPRows != nil {
			sortRTSPRows()
		} else {
			normalizeRTSPRows()
		}
		objects := []fyne.CanvasObject{
			container.NewHBox(
				fixedWidth(rtspEnabledWidth, widget.NewLabel("Enabled")),
				fixedWidth(rtspNameWidth, widget.NewLabel("Name")),
				fixedWidth(rtspShortIDWidth, widget.NewLabel("Short ID")),
				fixedWidth(rtspURLWidth, widget.NewLabel("RTSP URL")),
				fixedWidth(rtspPortWidth, widget.NewLabel("Port")),
				fixedWidth(rtspTransportWidth, widget.NewLabel("Transport")),
				fixedWidth(rtspRestartWidth, widget.NewLabel("")),
				fixedWidth(rtspProbeWidth, widget.NewLabel("")),
				fixedWidth(rtspRemoveWidth, widget.NewLabel("")),
			),
		}
		for i, row := range rtspRows {
			index := i
			objects = append(objects, row.object(func() {
				// Add handler
				if !rtspRows[index].hasContent() {
					return
				}
				if !rtspRows[index].enabledCheck.Checked {
					rtspRows[index].enabledCheck.SetChecked(true)
				}
				if err := saveRTSPRow(rtspRows[index]); err != nil {
					dialog.ShowError(err, window)
					actionStatus.SetText(fmt.Sprintf("Save failed: %v", err))
				}
			}, func() bool {
				// Save handler for this row
				if err := saveRTSPRow(rtspRows[index]); err != nil {
					dialog.ShowError(err, window)
					actionStatus.SetText(fmt.Sprintf("Save failed: %v", err))
					return false
				}
				return true
			}, func() {
				row := rtspRows[index]
				wasRunning := false
				for _, stream := range *currentStreams {
					if stream != nil && stream.camera.MatchKey == row.sourceID {
						wasRunning = true
						break
					}
				}
				name := strings.TrimSpace(row.nameEntry.Text)
				if name == "" {
					name = summarizeRTSPURL(row.urlEntry.Text)
				}
				if wasRunning && !row.enabledCheck.Checked {
					dialog.ShowConfirm("Stop Stream?",
						fmt.Sprintf("This will stop the camera stream for %s.", name),
						func(ok bool) {
							if !ok {
								return
							}
							if err := saveRTSPRow(row); err != nil {
								dialog.ShowError(err, window)
								actionStatus.SetText(fmt.Sprintf("Apply failed: %v", err))
								return
							}
							if err := stopSingleSource(row.sourceID, "disabled by user"); err != nil {
								dialog.ShowError(err, window)
								actionStatus.SetText(fmt.Sprintf("Stop failed: %v", err))
								return
							}
							actionStatus.SetText(fmt.Sprintf("Stopped %s.", name))
						}, window)
					return
				}
				if err := saveRTSPRow(row); err != nil {
					dialog.ShowError(err, window)
					actionStatus.SetText(fmt.Sprintf("Apply failed: %v", err))
					return
				}
				if !wasRunning {
					actionStatus.SetText(fmt.Sprintf("Applied %s.", name))
					return
				}
				item, ok := findInventorySource(row.sourceID)
				if !ok {
					dialog.ShowError(fmt.Errorf("source %s not found", name), window)
					actionStatus.SetText(fmt.Sprintf("Apply failed: source %s not found", name))
					return
				}
				dialog.ShowConfirm("Restart?", fmt.Sprintf("%s is running. Restart?", name), func(restartNow bool) {
					if !restartNow {
						actionStatus.SetText(fmt.Sprintf("Applied %s. Restart required.", name))
						return
					}
					actionStatus.SetText(fmt.Sprintf("Restarting %s...", name))
					if err := restartSource(*item); err != nil {
						dialog.ShowError(err, window)
						actionStatus.SetText(fmt.Sprintf("Restart failed: %v", err))
						return
					}
					if updated, ok := findInventorySource(row.sourceID); ok {
						syncRTSPRowFromSpec(row, *updated)
					}
					renderRTSPRows()
					if current := strings.TrimSpace(row.nameEntry.Text); current != "" {
						actionStatus.SetText(fmt.Sprintf("Restarted %s.", current))
					}
				}, window)
			}, func() {
				// Probe handler for this row
				row := rtspRows[index]
				src, skip, err := row.source()
				if skip || err != nil || strings.TrimSpace(src.RTSPURL) == "" {
					actionStatus.SetText("Enter a URL before probing")
					return
				}
				actionStatus.SetText(fmt.Sprintf("Probing %s...", summarizeRTSPURL(src.RTSPURL)))
				probeAndFillRTSPRow(src, func(codec, size string, fps int, probeErr error) {
					if probeErr != nil {
						dialog.ShowInformation("Probe Failed", probeErr.Error(), window)
						actionStatus.SetText(fmt.Sprintf("Probe failed: %v", probeErr))
						return
					}
					msg := fmt.Sprintf("%s  %s", codec, size)
					if fps > 0 {
						msg = fmt.Sprintf("%s  %s  %d fps", codec, size, fps)
					}
					dialog.ShowCustomConfirm("RTSP Probe Result", "Apply", "Cancel",
						widget.NewLabel(msg),
						func(apply bool) {
							if apply {
								row.detectedCodec = codec
								row.detectedSize = size
								row.detectedFPS = fps
								row.probeDirty = false
								row.dirtyReasons = removeReason(row.dirtyReasons, "probe")
								if err := saveRTSPRow(row); err != nil {
									dialog.ShowError(err, window)
									actionStatus.SetText(fmt.Sprintf("Save failed: %v", err))
								}
							}
						}, window)
					actionStatus.SetText(msg)
				})
			}, func() {
				// Remove handler
				if err := removeRTSPRow(rtspRows[index]); err != nil {
					dialog.ShowError(err, window)
					actionStatus.SetText(fmt.Sprintf("Remove failed: %v", err))
				}
			}))
		}
		rtspRowsContainer.Objects = objects
		rtspRowsContainer.Refresh()
	}

	sortedSpecsByPort := func(specs []sourceSpec) []sourceSpec {
		sorted := append([]sourceSpec(nil), specs...)
		sort.Slice(sorted, func(i, j int) bool {
			leftPort := sorted[i].OutputPort
			rightPort := sorted[j].OutputPort
			if leftPort <= 0 {
				leftPort = int(^uint(0) >> 1)
			}
			if rightPort <= 0 {
				rightPort = int(^uint(0) >> 1)
			}
			if leftPort != rightPort {
				return leftPort < rightPort
			}
			leftName := strings.TrimSpace(sorted[i].Name)
			if leftName == "" {
				leftName = strings.TrimSpace(sorted[i].Summary)
			}
			rightName := strings.TrimSpace(sorted[j].Name)
			if rightName == "" {
				rightName = strings.TrimSpace(sorted[j].Summary)
			}
			if leftName != rightName {
				return leftName < rightName
			}
			return sorted[i].Key < sorted[j].Key
		})
		return sorted
	}

	loadInventoryIntoRows := func(inv sourceInventory) {
		usbRows = usbRows[:0]
		for _, spec := range sortedSpecsByPort(inv.USB) {
			usbRows = append(usbRows, newUSBSourceRow(spec))
		}
		renderUSBRows()

		rtspRows = rtspRows[:0]
		for _, spec := range sortedSpecsByPort(inv.RTSP) {
			row := newRTSPSourceRow(spec)
			row.isAddRow = false
			rtspRows = append(rtspRows, row)
		}
		renderRTSPRows()
	}

	findInventorySource = func(key string) (*sourceSpec, bool) {
		for i := range currentInventory.USB {
			if currentInventory.USB[i].Key == key {
				return &currentInventory.USB[i], true
			}
		}
		for i := range currentInventory.RTSP {
			if currentInventory.RTSP[i].Key == key {
				return &currentInventory.RTSP[i], true
			}
		}
		return nil, false
	}

	findStreamIndex := func(key string) int {
		for i, stream := range *currentStreams {
			if stream != nil && stream.camera.MatchKey == key {
				return i
			}
		}
		return -1
	}

	refreshMonitoringStatus := func(message string) {
		fyne.Do(func() {
			table.Refresh()
			updateCameraStatusLabel(currentInventory.Status)
			actionStatus.SetText(message)
		})
	}

	recoveryStateFor := func(key string) *rtspRecoveryState {
		state, ok := rtspRecoveryStates[key]
		if !ok {
			state = &rtspRecoveryState{}
			rtspRecoveryStates[key] = state
		}
		return state
	}

	resetRTSPRecovery := func(key string) {
		delete(rtspRecoveryStates, key)
	}

	startSingleSource := func(spec sourceSpec, resetRecovery bool) (string, error) {
		cam := spec.Camera
		port := spec.OutputPort
		var udpDest string
		warning := ""
		if camerasConfig.Unicast.Enabled {
			var err error
			warning, err = prepareReachableUnicastDestinations(port)
			if err != nil {
				return "", err
			}
			if warning != "" {
				syncBroadcastUIFromConfig(false)
			}
			udpDest = camerasConfig.Unicast.TeeOutput(port)
		} else {
			udpDest = multicastOutputURL(camerasConfig.Multicast, port)
		}
		stream := &cameraStream{
			camera:      cam,
			port:        port,
			encoder:     currentEncoder,
			udpDest:     udpDest,
			unicastMode: camerasConfig.Unicast.Enabled,
			status:      "starting",
			running:     false,
			fps:         "-",
			frame:       "-",
			bitrate:     "-",
			speed:       "-",
			shortID:     spec.ShortID,
			summary:     spec.Summary,
			sourceType:  spec.SourceType,
			transport:   spec.Transport,
		}

		callbacks := &streamStartupCallbacks{action: func(message string) {
			fyne.Do(func() { actionStatus.SetText(message) })
		}}
		cmd, err := startStream(stream, callbacks)
		if err != nil {
			stream.setStopped(fmt.Sprintf("failed: %v", err))
			return warning, err
		}
		stream.cmd = cmd
		stream.setRunning()
		if strings.EqualFold(spec.SourceType, "rtsp") {
			state := recoveryStateFor(spec.Key)
			if resetRecovery {
				state.attempts = 0
			}
			state.nextRetry = time.Time{}
			state.exhausted = false
		} else {
			resetRTSPRecovery(spec.Key)
		}
		logging.InfoLogger.Printf("Source stream started for %s on port %d", spec.Name, spec.OutputPort)
		stale := make([]*cameraStream, 0, len(*currentStreams))
		for _, existing := range *currentStreams {
			if existing == nil || (existing.camera.MatchKey == spec.Key && !existing.isRunning()) {
				continue
			}
			stale = append(stale, existing)
		}
		*currentStreams = stale
		*currentStreams = append(*currentStreams, stream)
		sort.Slice(*currentStreams, func(i, j int) bool {
			if (*currentStreams)[i].port != (*currentStreams)[j].port {
				return (*currentStreams)[i].port < (*currentStreams)[j].port
			}
			return (*currentStreams)[i].camera.Name < (*currentStreams)[j].camera.Name
		})
		return warning, nil
	}

	stopSingleSource = func(key, reason string) error {
		idx := findStreamIndex(key)
		if idx < 0 {
			return nil
		}
		stream := (*currentStreams)[idx]
		stopErr := stopProcess(stream, reason)
		updated := append([]*cameraStream(nil), (*currentStreams)[:idx]...)
		updated = append(updated, (*currentStreams)[idx+1:]...)
		*currentStreams = updated
		return stopErr
	}

	ensurePortFreeForRestart := func(item *sourceSpec) error {
		if item == nil || item.OutputPort <= 0 {
			return nil
		}
		owners, err := jobutil.FindUDPPortOwners(item.OutputPort)
		if err != nil {
			return err
		}
		if len(owners) == 0 {
			return nil
		}
		logging.InfoLogger.Printf("Port %d busy before start/restart of %s; stopping %s", item.OutputPort, item.Name, jobutil.DescribePortProcesses(owners))
		if err := jobutil.StopUDPPortOwners(item.OutputPort, 2*time.Second); err != nil {
			return err
		}
		return jobutil.WaitForUDPPortFree(item.OutputPort, 500*time.Millisecond)
	}

	promptToFreeBusyPort = func(item *sourceSpec, onReady func()) {
		if item == nil {
			return
		}
		owners, err := jobutil.FindUDPPortOwners(item.OutputPort)
		if err != nil {
			dialog.ShowError(err, window)
			actionStatus.SetText(fmt.Sprintf("Start failed: %v", err))
			return
		}
		if len(owners) == 0 {
			if onReady != nil {
				onReady()
			}
			return
		}

		heading := widget.NewLabel("Port is busy. Kill the existing process?")
		details := widget.NewLabel(fmt.Sprintf("Port %d is in use by %s.", item.OutputPort, jobutil.DescribePortProcesses(owners)))
		status := widget.NewLabel("")
		cancelBtn := widget.NewButton("Cancel", nil)
		killBtn := widget.NewButton("Kill", nil)
		buttons := container.NewHBox(cancelBtn, killBtn)
		body := container.NewVBox(heading, details, status, buttons)
		prompt := dialog.NewCustomWithoutButtons("Port Busy", body, window)

		cancelBtn.OnTapped = func() {
			prompt.Hide()
			actionStatus.SetText(fmt.Sprintf("Start canceled for %s.", item.Name))
		}
		killBtn.OnTapped = func() {
			killBtn.Disable()
			cancelBtn.Disable()
			status.SetText(fmt.Sprintf("Stopping process on port %d...", item.OutputPort))
			go func(port int, name string) {
				killErr := jobutil.StopUDPPortOwners(port, 3*time.Second)
				if killErr == nil {
					killErr = jobutil.WaitForUDPPortFree(port, 500*time.Millisecond)
				}
				fyne.Do(func() {
					if killErr != nil {
						status.SetText(fmt.Sprintf("Failed to kill existing process: %v", killErr))
						cancelBtn.SetText("Close")
						cancelBtn.Enable()
						killBtn.Enable()
						actionStatus.SetText(fmt.Sprintf("Start failed: %v", killErr))
						return
					}
					prompt.Hide()
					if onReady != nil {
						onReady()
					}
				})
			}(item.OutputPort, item.Name)
		}
		prompt.Show()
	}

	startSourceFromUI = func(spec sourceSpec) {
		item, ok := findInventorySource(spec.Key)
		if !ok {
			err := fmt.Errorf("source %s not found", spec.Name)
			dialog.ShowError(err, window)
			actionStatus.SetText(fmt.Sprintf("Start failed: %v", err))
			return
		}
		if err := validateSourceStart(item); err != nil {
			dialog.ShowError(err, window)
			actionStatus.SetText(fmt.Sprintf("Start failed: %v", err))
			return
		}
		promptToFreeBusyPort(item, func() {
			actionStatus.SetText(fmt.Sprintf("Starting %s...", item.Name))
			if err := toggleSingleSource(spec, true); err != nil {
				dialog.ShowError(err, window)
				actionStatus.SetText(fmt.Sprintf("Start failed: %v", err))
			}
		})
	}

	setSourceMonitoringOn := func(spec sourceSpec, on bool) error {
		item, ok := findInventorySource(spec.Key)
		if !ok {
			return fmt.Errorf("source %s not found", spec.Name)
		}
		switch spec.SourceType {
		case "usb":
			updated := false
			for i := range camerasConfig.DeviceAssignments {
				if strings.TrimSpace(item.AttachmentPath) != "" && camerasConfig.DeviceAssignments[i].AttachmentPath == item.AttachmentPath {
					camerasConfig.DeviceAssignments[i].On = boolRef(on)
					updated = true
					break
				}
				if camerasConfig.DeviceAssignments[i].MatchKey == spec.Key {
					camerasConfig.DeviceAssignments[i].On = boolRef(on)
					updated = true
					break
				}
			}
			if !updated {
				camerasConfig.DeviceAssignments = append(camerasConfig.DeviceAssignments, camerascfg.DeviceAssignment{
					AttachmentPath: item.AttachmentPath,
					MatchKey:       spec.Key,
					Name:           item.Name,
					ShortID:        item.ShortID,
					OutputPort:     item.OutputPort,
					Disabled:       !item.Enabled,
					On:             boolRef(on),
				})
			}
		case "rtsp":
			for i := range camerasConfig.RTSPSources {
				if camerasConfig.RTSPSources[i].SourceID == spec.Key {
					camerasConfig.RTSPSources[i].On = boolRef(on)
					break
				}
			}
		default:
			return fmt.Errorf("unsupported source type %s", spec.SourceType)
		}
		if err := camerascfg.SaveConfig(camerasConfig); err != nil {
			return fmt.Errorf("save failed: %w", err)
		}
		refreshInventoryFromConfig(false)
		return nil
	}

	toggleSingleSource = func(spec sourceSpec, enabled bool) error {
		item, ok := findInventorySource(spec.Key)
		if !ok {
			return fmt.Errorf("source %s not found", spec.Name)
		}
		if enabled {
			if item.OutputPort <= 0 {
				return fmt.Errorf("%s has no output port configured", item.Name)
			}
			for _, src := range monitoringSources {
				if src.Key != item.Key && src.OutputPort > 0 && src.OutputPort == item.OutputPort {
					return fmt.Errorf("port %d is already used by %s", item.OutputPort, src.Name)
				}
			}
			if err := setSourceMonitoringOn(*item, true); err != nil {
				return err
			}
			item, _ = findInventorySource(spec.Key)
			streamIndex := findStreamIndex(spec.Key)
			if streamIndex < 0 || !(*currentStreams)[streamIndex].isRunning() {
				warning, err := startSingleSource(*item, true)
				if err != nil {
					return fmt.Errorf("failed to start %s: %w", item.Name, err)
				}
				if warning != "" {
					refreshMonitoringStatus(fmt.Sprintf("Started %s. %s", item.Name, warning))
				} else {
					refreshMonitoringStatus(fmt.Sprintf("Started %s", item.Name))
				}
				if err := clearRestartDirtyForSource(*item); err != nil {
					return err
				}
				return nil
			}
			refreshMonitoringStatus(fmt.Sprintf("Started %s", item.Name))
			return nil
		}
		if err := setSourceMonitoringOn(*item, false); err != nil {
			return err
		}
		if err := stopSingleSource(spec.Key, "toggle source off"); err != nil {
			return err
		}
		resetRTSPRecovery(spec.Key)
		if err := clearRestartDirtyForSource(*item); err != nil {
			return err
		}
		refreshMonitoringStatus(fmt.Sprintf("Stopped %s", item.Name))
		return nil
	}

	stopSourceFromUI = func(spec sourceSpec) {
		item, ok := findInventorySource(spec.Key)
		if !ok {
			err := fmt.Errorf("source %s not found", spec.Name)
			dialog.ShowError(err, window)
			actionStatus.SetText(fmt.Sprintf("Stop failed: %v", err))
			return
		}
		if err := setSourceMonitoringOn(*item, false); err != nil {
			dialog.ShowError(err, window)
			actionStatus.SetText(fmt.Sprintf("Stop failed: %v", err))
			return
		}

		streamIndex := findStreamIndex(spec.Key)
		if streamIndex < 0 {
			resetRTSPRecovery(spec.Key)
			refreshMonitoringStatus(fmt.Sprintf("Stopped %s", item.Name))
			return
		}
		stream := (*currentStreams)[streamIndex]
		stream.markStopping("toggle source off")
		table.Refresh()
		actionStatus.SetText(fmt.Sprintf("Stopping %s...", item.Name))

		go func(stream *cameraStream, key, name string) {
			stopErr := stopProcess(stream, "toggle source off")
			fyne.Do(func() {
				if index := findStreamIndex(key); index >= 0 && (*currentStreams)[index] == stream {
					*currentStreams = append((*currentStreams)[:index], (*currentStreams)[index+1:]...)
				}
				resetRTSPRecovery(key)
				if stopErr != nil {
					dialog.ShowError(stopErr, window)
					actionStatus.SetText(fmt.Sprintf("Stop failed: %v", stopErr))
					table.Refresh()
					return
				}
				if err := clearRestartDirtyForSource(*item); err != nil {
					dialog.ShowError(err, window)
					actionStatus.SetText(fmt.Sprintf("Stop failed: %v", err))
					return
				}
				refreshMonitoringStatus(fmt.Sprintf("Stopped %s", name))
			})
		}(stream, spec.Key, item.Name)
	}

	validateSourceStart = func(item *sourceSpec) error {
		if item.OutputPort <= 0 {
			return fmt.Errorf("%s has no output port configured", item.Name)
		}
		for _, src := range monitoringSources {
			if src.Key != item.Key && src.OutputPort > 0 && src.OutputPort == item.OutputPort {
				return fmt.Errorf("port %d is already used by %s", item.OutputPort, src.Name)
			}
		}
		return nil
	}

	restartSourceWithRecovery := func(spec sourceSpec, resetRecovery bool) error {
		item, ok := findInventorySource(spec.Key)
		if !ok {
			return fmt.Errorf("source %s not found", spec.Name)
		}
		logging.InfoLogger.Printf("Restart requested for %s on port %d", item.Name, item.OutputPort)
		if err := validateSourceStart(item); err != nil {
			return err
		}
		if findStreamIndex(spec.Key) >= 0 {
			if err := stopSingleSource(spec.Key, "restart source"); err != nil {
				return err
			}
		}
		if err := ensurePortFreeForRestart(item); err != nil {
			return err
		}
		warning, err := startSingleSource(*item, resetRecovery)
		if err != nil {
			return fmt.Errorf("failed to restart %s: %w", item.Name, err)
		}
		if err := clearRestartDirtyForSource(*item); err != nil {
			return err
		}
		logging.InfoLogger.Printf("Restart completed for %s on port %d", item.Name, item.OutputPort)
		if warning != "" {
			refreshMonitoringStatus(fmt.Sprintf("Restarted %s. %s", item.Name, warning))
		} else {
			refreshMonitoringStatus(fmt.Sprintf("Restarted %s", item.Name))
		}
		return nil
	}

	restartSource = func(spec sourceSpec) error {
		resetRTSPRecovery(spec.Key)
		return restartSourceWithRecovery(spec, true)
	}

	shouldAutoRestartRTSP := func(stream *cameraStream, now time.Time) (string, bool) {
		if stream == nil {
			return "", false
		}
		state := recoveryStateFor(stream.camera.MatchKey)
		if stream.isInteractiveReady() {
			state.attempts = 0
			state.attention = false
			state.reason = ""
			state.nextRetry = time.Time{}
			state.exhausted = false
			return "", false
		}
		if state.nextRetry.After(now) || state.exhausted {
			return "", false
		}

		reason, shouldRestart := stream.autoRestartReason(now, rtspRetryDelay(state.attempts))
		if !shouldRestart {
			return "", false
		}
		state.attention = true
		state.reason = reason

		return reason, true
	}

	autoRecoverRTSPStreams := func() {
		now := time.Now()
		streams := append([]*cameraStream(nil), (*currentStreams)...)
		for _, stream := range streams {
			reason, shouldRestart := shouldAutoRestartRTSP(stream, now)
			if !shouldRestart {
				continue
			}

			item, ok := findInventorySource(stream.camera.MatchKey)
			if !ok {
				continue
			}

			state := recoveryStateFor(stream.camera.MatchKey)
			if err := stopSingleSource(stream.camera.MatchKey, fmt.Sprintf("rtsp watchdog timeout: %s", reason)); err != nil {
				logging.ErrorLogger.Printf("RTSP WATCHDOG STOP FAILED: source=%s port=%d error=%v", item.Name, item.OutputPort, err)
			}

			delay, retryAllowed := rtspRetryWindow(state.attempts)
			if !retryAllowed {
				state.nextRetry = time.Time{}
				state.exhausted = true
				logging.ErrorLogger.Printf("RTSP WATCHDOG STOPPED: source=%s port=%d url=%s reason=%s retries=%d", item.Name, item.OutputPort, item.Camera.Device, reason, state.attempts)
				actionStatus.SetText(fmt.Sprintf("Stopped RTSP source %s after %d retries", item.Name, state.attempts))
				updateCameraStatusLabel(currentInventory.Status)
				continue
			}

			retryNumber := state.attempts + 1
			state.attempts = retryNumber
			state.nextRetry = now.Add(delay)
			state.exhausted = false
			logging.ErrorLogger.Printf("RTSP WATCHDOG TIMEOUT: source=%s port=%d url=%s reason=%s retry=%d backoff=%s next_retry_at=%s", item.Name, item.OutputPort, item.Camera.Device, reason, retryNumber, delay, state.nextRetry.Format(time.RFC3339))
			actionStatus.SetText(fmt.Sprintf("RTSP source %s stopped; retry %d in %s", item.Name, retryNumber, delay))
			updateCameraStatusLabel(currentInventory.Status)
		}

		for _, spec := range currentInventory.RTSP {
			state, ok := rtspRecoveryStates[spec.Key]
			if !ok || state == nil {
				continue
			}
			if state.exhausted || state.nextRetry.IsZero() || now.Before(state.nextRetry) {
				continue
			}
			if !spec.Enabled || !spec.MonitoringOn || spec.OutputPort <= 0 {
				state.nextRetry = time.Time{}
				continue
			}
			if findStreamIndex(spec.Key) >= 0 {
				state.nextRetry = time.Time{}
				continue
			}

			state.nextRetry = time.Time{}
			logging.InfoLogger.Printf("RTSP WATCHDOG RETRY STARTING: source=%s port=%d retry=%d", spec.Name, spec.OutputPort, state.attempts)
			warning, err := startSingleSource(spec, false)
			if err != nil {
				state.reason = fmt.Sprintf("restart failed: %v", err)
				if delay, retryAllowed := rtspRetryWindow(state.attempts); retryAllowed {
					state.attempts++
					state.nextRetry = now.Add(delay)
					logging.ErrorLogger.Printf("RTSP WATCHDOG RETRY FAILED: source=%s port=%d error=%v retry=%d backoff=%s", spec.Name, spec.OutputPort, err, state.attempts, delay)
				} else {
					state.exhausted = true
					logging.ErrorLogger.Printf("RTSP WATCHDOG STOPPED: source=%s port=%d error=%v retries=%d", spec.Name, spec.OutputPort, err, state.attempts)
				}
				continue
			}
			if warning != "" {
				actionStatus.SetText(fmt.Sprintf("Restarted %s. %s", spec.Name, warning))
			}
			logging.InfoLogger.Printf("RTSP WATCHDOG RETRY STARTED: source=%s port=%d retry=%d", spec.Name, spec.OutputPort, state.attempts)
		}
	}

	renderMonitoringSourceToggles = func(inv sourceInventory) {
		sources := make([]sourceSpec, 0, len(inv.USB)+len(inv.RTSP))
		for _, src := range inv.USB {
			if src.Enabled {
				sources = append(sources, src)
			}
		}
		for _, src := range inv.RTSP {
			if src.Enabled && strings.TrimSpace(src.RTSP.RTSPURL) != "" {
				sources = append(sources, src)
			}
		}
		monitoringSources = sortedSpecsByPort(sources)
		table.Refresh()
	}

	refreshInventoryFromConfig = func(reloadRows bool) {
		inv := buildCachedSourceInventory(currentInventory, currentEncoder)
		currentInventory = inv
		if inv.Encoder != nil {
			currentEncoder = inv.Encoder
		}
		fyne.Do(func() {
			updateEncoderStatus()
			if reloadRows {
				loadInventoryIntoRows(inv)
			}
			renderMonitoringSourceToggles(inv)
			table.Refresh()
			updateCameraStatusLabel(inv.Status)
		})
	}

	verificationMessage := func(prefix string) string {
		if len(currentInventory.PendingVerification) == 0 {
			return prefix
		}
		return fmt.Sprintf("%s Config review needed: %s", prefix, strings.Join(currentInventory.PendingVerification, ", "))
	}

	applyBroadcastUIToConfig = func() error {
		selectedMode := strings.TrimSpace(modeSelect.Selected)
		if selectedMode == "Unicast" {
			destinations := make([]camerascfg.UnicastDestination, 0, len(unicastDestinations))
			for _, d := range unicastDestinations {
				if strings.TrimSpace(d.Address) != "" {
					destinations = append(destinations, d)
				}
			}
			if len(destinations) == 0 {
				return fmt.Errorf("at least one unicast destination is required")
			}
			camerasConfig.Unicast.Enabled = true
			camerasConfig.Unicast.Destinations = destinations
			return nil
		}

		newIP := strings.TrimSpace(ipEntry.Text)
		parsedIP := net.ParseIP(newIP)
		if parsedIP == nil || !parsedIP.IsMulticast() {
			return fmt.Errorf("invalid multicast IP")
		}
		camerasConfig.Unicast.Enabled = false
		camerasConfig.Multicast.IP = newIP
		camerasConfig.Multicast.LocalOnly = localOnlyCheck.Checked
		return nil
	}

	applyUIToConfig = func() error {
		if err := applyBroadcastUIToConfig(); err != nil {
			return err
		}

		// Preserve runtime On (monitoring) state from camerasConfig so that
		// toggling Start/Stop in the monitoring page is not clobbered by stale
		// row.monitoringOn values when the user clicks "Restart Streams".
		existingUSBOn := make(map[string]*bool, len(camerasConfig.DeviceAssignments))
		for i := range camerasConfig.DeviceAssignments {
			a := &camerasConfig.DeviceAssignments[i]
			if a.On != nil {
				v := *a.On
				if strings.TrimSpace(a.AttachmentPath) != "" {
					existingUSBOn[a.AttachmentPath] = &v
				}
				if strings.TrimSpace(a.MatchKey) != "" {
					existingUSBOn[a.MatchKey] = &v
				}
			}
		}
		existingRTSPOn := make(map[string]*bool, len(camerasConfig.RTSPSources))
		for i := range camerasConfig.RTSPSources {
			s := &camerasConfig.RTSPSources[i]
			if s.On != nil {
				v := *s.On
				existingRTSPOn[s.SourceID] = &v
			}
		}

		assignments := make([]camerascfg.DeviceAssignment, 0, len(usbRows))
		portOwners := make(map[int]string)
		for _, row := range usbRows {
			assignment, err := row.assignment()
			if err != nil {
				return err
			}
			if on, ok := existingUSBOn[assignment.AttachmentPath]; ok && on != nil {
				v := *on
				assignment.On = &v
			} else if on, ok := existingUSBOn[assignment.MatchKey]; ok && on != nil {
				v := *on
				assignment.On = &v
			}
			if assignment.OutputPort > 0 {
				if owner, exists := portOwners[assignment.OutputPort]; exists {
					return fmt.Errorf("duplicate output port %d for %s and %s", assignment.OutputPort, owner, assignment.Name)
				}
				portOwners[assignment.OutputPort] = assignment.Name
			}
			assignments = append(assignments, assignment)
		}

		normalizeRTSPRows()
		rtspSources := make([]camerascfg.RTSPSource, 0, len(rtspRows))
		for _, row := range rtspRows {
			source, skip, err := row.source()
			if err != nil {
				return err
			}
			if skip {
				continue
			}
			if on, ok := existingRTSPOn[source.SourceID]; ok && on != nil {
				v := *on
				source.On = &v
			}
			if source.OutputPort > 0 {
				if owner, exists := portOwners[source.OutputPort]; exists {
					return fmt.Errorf("duplicate output port %d for %s and %s", source.OutputPort, owner, source.Name)
				}
				portOwners[source.OutputPort] = source.Name
			}
			rtspSources = append(rtspSources, source)
		}

		camerasConfig.DeviceAssignments = assignments
		camerasConfig.RTSPSources = rtspSources
		return nil
	}

	findStreamForSource := func(key string) *cameraStream {
		for _, stream := range *currentStreams {
			if stream != nil && stream.camera.MatchKey == key {
				return stream
			}
		}
		return nil
	}

	ensureDeviceAssignment := func(spec sourceSpec) *camerascfg.DeviceAssignment {
		for i := range camerasConfig.DeviceAssignments {
			if strings.TrimSpace(spec.AttachmentPath) != "" && camerasConfig.DeviceAssignments[i].AttachmentPath == spec.AttachmentPath {
				return &camerasConfig.DeviceAssignments[i]
			}
			if camerasConfig.DeviceAssignments[i].MatchKey == spec.Key {
				return &camerasConfig.DeviceAssignments[i]
			}
		}
		camerasConfig.DeviceAssignments = append(camerasConfig.DeviceAssignments, camerascfg.DeviceAssignment{
			AttachmentPath: spec.AttachmentPath,
			MatchKey:       spec.Key,
			Name:           spec.Name,
			ShortID:        spec.ShortID,
		})
		return &camerasConfig.DeviceAssignments[len(camerasConfig.DeviceAssignments)-1]
	}

	configRowSortPort := func(raw string) int {
		port, err := parseOptionalPort(raw)
		if err != nil || port <= 0 {
			return int(^uint(0) >> 1)
		}
		return port
	}

	configRowName := func(name, fallback string) string {
		name = strings.TrimSpace(name)
		if name != "" {
			return name
		}
		return strings.TrimSpace(fallback)
	}

	validateConfigPort := func(sourceType, key string, port int, name string) error {
		if port <= 0 {
			return nil
		}
		for _, src := range currentInventory.USB {
			if sourceType == "usb" && src.Key == key {
				continue
			}
			if src.OutputPort == port {
				return fmt.Errorf("duplicate output port %d for %s and %s", port, name, src.Name)
			}
		}
		for _, src := range currentInventory.RTSP {
			if sourceType == "rtsp" && src.Key == key {
				continue
			}
			if src.OutputPort == port {
				return fmt.Errorf("duplicate output port %d for %s and %s", port, name, src.Name)
			}
		}
		return nil
	}

	clearRestartDirtyForSource = func(spec sourceSpec) error {
		switch spec.SourceType {
		case "usb":
			for i := range camerasConfig.DeviceAssignments {
				if camerasConfig.DeviceAssignments[i].MatchKey == spec.Key {
					camerasConfig.DeviceAssignments[i].DirtyReasons = removeReason(camerasConfig.DeviceAssignments[i].DirtyReasons, "restart")
					break
				}
			}
		case "rtsp":
			for i := range camerasConfig.RTSPSources {
				if camerasConfig.RTSPSources[i].SourceID == spec.Key {
					camerasConfig.RTSPSources[i].DirtyReasons = removeReason(camerasConfig.RTSPSources[i].DirtyReasons, "restart")
					break
				}
			}
		}
		if err := camerascfg.SaveConfig(camerasConfig); err != nil {
			return fmt.Errorf("save failed: %w", err)
		}
		refreshInventoryFromConfig(false)
		return nil
	}

	clearRestartDirtyForStreams := func(streams []*cameraStream) {
		changed := false
		for _, stream := range streams {
			if stream == nil {
				continue
			}
			key := stream.camera.MatchKey
			for i := range camerasConfig.DeviceAssignments {
				if camerasConfig.DeviceAssignments[i].MatchKey == key && hasDirtyReason(camerasConfig.DeviceAssignments[i].DirtyReasons, "restart") {
					camerasConfig.DeviceAssignments[i].DirtyReasons = removeReason(camerasConfig.DeviceAssignments[i].DirtyReasons, "restart")
					changed = true
				}
			}
			for i := range camerasConfig.RTSPSources {
				if camerasConfig.RTSPSources[i].SourceID == key && hasDirtyReason(camerasConfig.RTSPSources[i].DirtyReasons, "restart") {
					camerasConfig.RTSPSources[i].DirtyReasons = removeReason(camerasConfig.RTSPSources[i].DirtyReasons, "restart")
					changed = true
				}
			}
		}
		if !changed {
			return
		}
		if err := camerascfg.SaveConfig(camerasConfig); err != nil {
			logging.ErrorLogger.Printf("Failed to clear restart-dirty state: %v", err)
			return
		}
		refreshInventoryFromConfig(false)
	}

	sortUSBRows = func() {
		sort.SliceStable(usbRows, func(i, j int) bool {
			leftPort := configRowSortPort(usbRows[i].portEntry.Text)
			rightPort := configRowSortPort(usbRows[j].portEntry.Text)
			if leftPort != rightPort {
				return leftPort < rightPort
			}
			leftName := configRowName(usbRows[i].nameEntry.Text, usbRows[i].identity)
			rightName := configRowName(usbRows[j].nameEntry.Text, usbRows[j].identity)
			if leftName != rightName {
				return leftName < rightName
			}
			return usbRows[i].matchKey < usbRows[j].matchKey
		})
	}

	sortRTSPRows = func() {
		normalizeRTSPRows()
		if len(rtspRows) <= 1 {
			return
		}
		configured := append([]*rtspSourceRow(nil), rtspRows[:len(rtspRows)-1]...)
		addRow := rtspRows[len(rtspRows)-1]
		sort.SliceStable(configured, func(i, j int) bool {
			leftPort := configRowSortPort(configured[i].portEntry.Text)
			rightPort := configRowSortPort(configured[j].portEntry.Text)
			if leftPort != rightPort {
				return leftPort < rightPort
			}
			leftName := configRowName(configured[i].nameEntry.Text, summarizeRTSPURL(configured[i].urlEntry.Text))
			rightName := configRowName(configured[j].nameEntry.Text, summarizeRTSPURL(configured[j].urlEntry.Text))
			if leftName != rightName {
				return leftName < rightName
			}
			return configured[i].sourceID < configured[j].sourceID
		})
		rtspRows = append(configured, addRow)
	}

	syncUSBRowFromSpec = func(row *usbSourceRow, spec sourceSpec) {
		row.attachmentPath = spec.AttachmentPath
		row.matchKey = spec.Key
		row.identity = spec.Summary
		row.detected = spec.Detected
		row.dirtyReasons = append([]string(nil), spec.DirtyReasons...)
		row.detectedPixFmt = spec.Camera.PixFmt
		row.detectedSize = spec.Camera.Size
		row.detectedFPS = spec.Camera.Fps
		row.detectedFormats = append([]string(nil), spec.SupportedFormats...)

		enabledChanged := row.enabledCheck.OnChanged
		row.enabledCheck.OnChanged = nil
		row.enabledCheck.SetChecked(spec.Enabled)
		row.enabledCheck.OnChanged = enabledChanged
		if spec.Detected {
			row.enabledCheck.Enable()
		} else {
			row.enabledCheck.Disable()
		}

		nameChanged := row.nameEntry.OnChanged
		row.nameEntry.OnChanged = nil
		row.nameEntry.SetText(spec.Name)
		row.nameEntry.OnChanged = nameChanged

		shortIDChanged := row.shortIDEntry.OnChanged
		row.shortIDEntry.OnChanged = nil
		row.shortIDEntry.SetText(spec.ShortID)
		row.shortIDEntry.OnChanged = shortIDChanged

		portChanged := row.portEntry.OnChanged
		row.portEntry.OnChanged = nil
		if spec.OutputPort > 0 {
			row.portEntry.SetText(strconv.Itoa(spec.OutputPort))
		} else {
			row.portEntry.SetText("")
		}
		row.portEntry.OnChanged = portChanged

		formatChanged := row.formatSelect.OnChanged
		row.formatSelect.OnChanged = nil
		row.formatSelect.Options = append([]string{"Auto"}, spec.SupportedFormats...)
		row.formatSelect.Refresh()
		if spec.PreferredFormat != "" {
			row.formatSelect.SetSelected(spec.PreferredFormat)
		} else {
			row.formatSelect.SetSelected("Auto")
		}
		row.formatSelect.OnChanged = formatChanged
		row.monitoringOn = spec.MonitoringOn
		row.markClean()
		row.refreshRestartHighlight()
	}

	syncRTSPRowFromSpec = func(row *rtspSourceRow, spec sourceSpec) {
		row.sourceID = spec.Key
		row.isAddRow = false
		row.probeDirty = spec.RTSP.ProbeDirty
		row.dirtyReasons = append([]string(nil), spec.DirtyReasons...)
		row.detectedCodec = spec.RTSP.Codec
		row.detectedSize = spec.RTSP.ProbeSize
		row.detectedFPS = spec.RTSP.ProbeFPS

		enabledChanged := row.enabledCheck.OnChanged
		row.enabledCheck.OnChanged = nil
		row.enabledCheck.SetChecked(spec.Enabled)
		row.enabledCheck.OnChanged = enabledChanged

		nameChanged := row.nameEntry.OnChanged
		row.nameEntry.OnChanged = nil
		row.nameEntry.SetText(spec.Name)
		row.nameEntry.OnChanged = nameChanged

		shortIDChanged := row.shortIDEntry.OnChanged
		row.shortIDEntry.OnChanged = nil
		row.shortIDEntry.SetText(spec.ShortID)
		row.shortIDEntry.OnChanged = shortIDChanged

		urlChanged := row.urlEntry.OnChanged
		row.urlEntry.OnChanged = nil
		row.urlEntry.SetText(spec.RTSP.RTSPURL)
		row.urlEntry.OnChanged = urlChanged

		portChanged := row.portEntry.OnChanged
		row.portEntry.OnChanged = nil
		if spec.OutputPort > 0 {
			row.portEntry.SetText(strconv.Itoa(spec.OutputPort))
		} else {
			row.portEntry.SetText("")
		}
		row.portEntry.OnChanged = portChanged

		transportChanged := row.transportSelect.OnChanged
		row.transportSelect.OnChanged = nil
		transport := strings.TrimSpace(spec.RTSP.Transport)
		if transport == "" {
			transport = "tcp"
		}
		row.transportSelect.SetSelected(transport)
		row.transportSelect.OnChanged = transportChanged
		row.monitoringOn = spec.MonitoringOn
		row.markClean()
		row.refreshRestartHighlight()
	}

	saveUSBRow = func(row *usbSourceRow) error {
		previous, _ := findInventorySource(row.matchKey)
		assignment, err := row.assignment()
		if err != nil {
			return err
		}
		name := configRowName(assignment.Name, row.identity)
		if err := validateConfigPort("usb", row.matchKey, assignment.OutputPort, name); err != nil {
			return err
		}
		if previous != nil {
			assignment.On = boolRef(previous.MonitoringOn)
		} else {
			assignment.On = boolRef(true)
		}
		running := findStreamForSource(row.matchKey) != nil
		if running && previous != nil {
			if previous.Enabled != !assignment.Disabled ||
				previous.OutputPort != assignment.OutputPort ||
				strings.TrimSpace(previous.PreferredFormat) != strings.TrimSpace(assignment.PreferredPixelFormat) {
				assignment.DirtyReasons = addReason(assignment.DirtyReasons, "restart")
			}
		} else if !running {
			assignment.DirtyReasons = removeReason(assignment.DirtyReasons, "restart")
		}
		assignmentPtr := ensureDeviceAssignment(sourceSpec{AttachmentPath: row.attachmentPath, Key: row.matchKey, Name: assignment.Name, ShortID: assignment.ShortID})
		*assignmentPtr = assignment
		if err := camerascfg.SaveConfig(camerasConfig); err != nil {
			return fmt.Errorf("save failed: %w", err)
		}
		refreshInventoryFromConfig(false)
		if item, ok := findInventorySource(row.matchKey); ok {
			syncUSBRowFromSpec(row, *item)
		}
		renderUSBRows()
		message := fmt.Sprintf("Saved %s.", name)
		if running && hasDirtyReason(assignment.DirtyReasons, "restart") {
			message = fmt.Sprintf("Saved %s. Restart required.", name)
		}
		actionStatus.SetText(verificationMessage(message))
		return nil
	}

	removeUSBRow = func(row *usbSourceRow) error {
		if row == nil {
			return fmt.Errorf("source row not found")
		}
		if row.detected {
			return fmt.Errorf("only stale sources can be removed")
		}
		updatedAssignments, removed, ok := removeDeviceAssignment(camerasConfig.DeviceAssignments, row.attachmentPath, row.matchKey)
		if !ok {
			return fmt.Errorf("source %s not found", configRowName(row.nameEntry.Text, row.identity))
		}
		camerasConfig.DeviceAssignments = updatedAssignments
		if err := camerascfg.SaveConfig(camerasConfig); err != nil {
			return fmt.Errorf("save failed: %w", err)
		}
		refreshInventoryFromConfig(false)
		for i := range usbRows {
			if usbRows[i] == row {
				usbRows = append(usbRows[:i], usbRows[i+1:]...)
				break
			}
		}
		renderUSBRows()
		name := configRowName(removed.Name, row.identity)
		actionStatus.SetText(verificationMessage(fmt.Sprintf("Removed %s.", name)))
		return nil
	}

	saveRTSPRow = func(row *rtspSourceRow) error {
		previous, _ := findInventorySource(row.sourceID)
		source, skip, err := row.source()
		if err != nil {
			return err
		}
		if skip {
			return fmt.Errorf("RTSP URL is required for configured sources")
		}
		name := configRowName(source.Name, summarizeRTSPURL(source.RTSPURL))
		if err := validateConfigPort("rtsp", row.sourceID, source.OutputPort, name); err != nil {
			return err
		}
		if previous != nil {
			source.On = boolRef(previous.MonitoringOn)
		} else {
			source.On = boolRef(true)
		}
		running := findStreamForSource(row.sourceID) != nil
		if running && previous != nil {
			if previous.Enabled != source.Enabled ||
				previous.OutputPort != source.OutputPort ||
				strings.TrimSpace(previous.RTSP.RTSPURL) != strings.TrimSpace(source.RTSPURL) ||
				!strings.EqualFold(strings.TrimSpace(previous.Transport), strings.TrimSpace(source.Transport)) ||
				!strings.EqualFold(strings.TrimSpace(previous.RTSP.Codec), strings.TrimSpace(source.Codec)) {
				source.DirtyReasons = addReason(source.DirtyReasons, "restart")
			}
		} else if !running {
			source.DirtyReasons = removeReason(source.DirtyReasons, "restart")
		}
		index := -1
		for i := range camerasConfig.RTSPSources {
			if camerasConfig.RTSPSources[i].SourceID == source.SourceID {
				index = i
				break
			}
		}
		if index == -1 {
			index = len(camerasConfig.RTSPSources)
			camerasConfig.RTSPSources = append(camerasConfig.RTSPSources, source)
		} else {
			camerasConfig.RTSPSources[index] = source
		}
		if err := camerascfg.SaveConfig(camerasConfig); err != nil {
			return fmt.Errorf("save failed: %w", err)
		}
		if index >= 0 && index < len(camerasConfig.RTSPSources) {
			row.sourceID = camerasConfig.RTSPSources[index].SourceID
		}
		row.isAddRow = false
		refreshInventoryFromConfig(false)
		if item, ok := findInventorySource(row.sourceID); ok {
			syncRTSPRowFromSpec(row, *item)
		}
		renderRTSPRows()
		message := fmt.Sprintf("Saved %s.", name)
		if running && hasDirtyReason(source.DirtyReasons, "restart") {
			message = fmt.Sprintf("Saved %s. Restart required.", name)
		}
		actionStatus.SetText(verificationMessage(message))
		return nil
	}

	removeRTSPRow = func(row *rtspSourceRow) error {
		index := -1
		for i := range camerasConfig.RTSPSources {
			if camerasConfig.RTSPSources[i].SourceID == row.sourceID {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("source %s not found", configRowName(row.nameEntry.Text, summarizeRTSPURL(row.urlEntry.Text)))
		}
		name := configRowName(camerasConfig.RTSPSources[index].Name, summarizeRTSPURL(camerasConfig.RTSPSources[index].RTSPURL))
		camerasConfig.RTSPSources = append(camerasConfig.RTSPSources[:index], camerasConfig.RTSPSources[index+1:]...)
		if err := camerascfg.SaveConfig(camerasConfig); err != nil {
			return fmt.Errorf("save failed: %w", err)
		}
		refreshInventoryFromConfig(false)
		for i := range rtspRows {
			if rtspRows[i] == row {
				rtspRows = append(rtspRows[:i], rtspRows[i+1:]...)
				break
			}
		}
		renderRTSPRows()
		actionStatus.SetText(verificationMessage(fmt.Sprintf("Removed %s.", name)))
		return nil
	}

	table = widget.NewTable(
		func() (int, int) {
			return len(monitoringSources) + 1, len(headers)
		},
		func() fyne.CanvasObject {
			label := widget.NewLabel("template")
			button := widget.NewButton("Action", nil)
			button.Hide()
			return container.NewStack(label, button)
		},
		func(id widget.TableCellID, obj fyne.CanvasObject) {
			cell := obj.(*fyne.Container)
			label := cell.Objects[0].(*widget.Label)
			button := cell.Objects[1].(*widget.Button)
			if id.Col == 4 {
				label.Truncation = fyne.TextTruncateEllipsis
			} else {
				label.Truncation = fyne.TextTruncateOff
			}

			if id.Row == 0 {
				button.Hide()
				label.TextStyle = fyne.TextStyle{Bold: true}
				label.Show()
				label.SetText(headers[id.Col])
				return
			}

			srcIdx := id.Row - 1
			if srcIdx >= len(monitoringSources) {
				button.Hide()
				label.Show()
				label.SetText("")
				return
			}
			spec := monitoringSources[srcIdx]
			stream := findStreamForSource(spec.Key)

			var row [13]string
			row[0] = spec.Name
			row[1] = spec.ShortID
			row[2] = multicastPort(spec.OutputPort)
			row[3] = spec.Camera.PixFmt
			// Encoder column
			pixFmt := strings.ToLower(strings.TrimSpace(spec.Camera.PixFmt))
			switch pixFmt {
			case "h264", "hevc", "h265":
				row[4] = "copy"
			default:
				if currentEncoder != nil {
					row[4] = currentEncoder.Name
				} else {
					row[4] = "software"
				}
			}
			row[5] = spec.Camera.Size
			row[6] = formatExpectedFPSValue(spec.Camera.Fps)
			recoveryState := rtspRecoveryStates[spec.Key]
			if stream != nil {
				snapshot := stream.snapshotRow()
				row[5] = snapshot[2]
				row[7] = snapshot[4]
			}
			row[10] = monitoringSourceStatus(spec, stream, recoveryState)

			if id.Col == 8 {
				label.Hide()
				button.Show()
				button.SetText("")
				button.SetIcon(theme.MediaPlayIcon())
				button.Importance = widget.MediumImportance
				if spec.OutputPort <= 0 || (!spec.Detected && strings.EqualFold(spec.SourceType, "usb")) {
					button.Disable()
					button.OnTapped = nil
					return
				}
				if stream != nil && stream.isRunning() {
					button.Disable()
					button.OnTapped = nil
					return
				}
				button.Enable()
				capturedSpec := spec
				button.OnTapped = func() {
					startSourceFromUI(capturedSpec)
				}
				return
			}

			if id.Col == 9 {
				label.Hide()
				button.Show()
				button.SetText("")
				button.SetIcon(theme.MediaStopIcon())
				button.Importance = widget.DangerImportance
				if stream == nil || !stream.isRunning() {
					button.Disable()
					button.OnTapped = nil
					return
				}
				button.Enable()
				capturedSpec := spec
				button.OnTapped = func() {
					stopSourceFromUI(capturedSpec)
				}
				return
			}

			if id.Col == 11 {
				label.Hide()
				button.Show()
				button.SetText("Preview")
				button.SetIcon(nil)
				button.Importance = widget.MediumImportance
				if stream == nil || !stream.isInteractiveReady() {
					button.Disable()
					button.OnTapped = nil
					return
				}
				button.Enable()
				capturedStream := stream
				button.OnTapped = func() {
					logging.InfoLogger.Printf("Monitoring preview button tapped for %s [%s] (%s, port=%d)", capturedStream.camera.Name, capturedStream.shortID, capturedStream.camera.Size, capturedStream.port)
					if clipLinkTimer != nil {
						clipLinkTimer.Stop()
					}
					clipLink.Hide()
					actionStatus.SetText("Preview/Record: ready")
					if err := launchPreview(capturedStream, func() {
						fyne.Do(func() {
							actionStatus.SetText("Preview/Record: ready")
						})
					}); err != nil {
						actionStatus.SetText(fmt.Sprintf("Preview failed: %v", err))
						logging.ErrorLogger.Printf("Failed to start ffplay preview for %s (%s): %v", capturedStream.camera.Name, capturedStream.udpDest, err)
						return
					}
					actionStatus.SetText(fmt.Sprintf("Previewing: %s (%s)", capturedStream.camera.Name, capturedStream.camera.Size))
				}
				return
			}

			if id.Col == 12 {
				label.Hide()
				button.Show()
				button.SetText("Record 10s")
				button.SetIcon(nil)
				button.Importance = widget.MediumImportance
				if stream == nil || !stream.isInteractiveReady() {
					button.Disable()
					button.OnTapped = nil
					return
				}
				button.Enable()
				capturedStream := stream
				button.OnTapped = func() {
					if clipLinkTimer != nil {
						clipLinkTimer.Stop()
					}
					clipLink.Hide()
					actionStatus.SetText(fmt.Sprintf("Recording 10s: %s", capturedStream.camera.Name))
					go func(s *cameraStream) {
						outputPath, err := recordClip(s)
						if err != nil {
							fyne.Do(func() {
								actionStatus.SetText(fmt.Sprintf("Record failed: %v", err))
							})
							logging.ErrorLogger.Printf("Failed to record clip for %s (%s): %v", s.camera.Name, s.udpDest, err)
							return
						}
						path := outputPath
						fyne.Do(func() {
							actionStatus.SetText("Saved: ")
							clipLink.SetText(filepath.FromSlash(path))
							clipLink.OnTapped = func() {
								scheduleClipHide(1 * time.Minute)
								openFile(path, func() {
									fyne.Do(func() {
										if clipLinkTimer != nil {
											clipLinkTimer.Stop()
										}
										clipLink.Hide()
										actionStatus.SetText("Preview/Record: ready")
									})
								})
							}
							clipLink.Show()
							scheduleClipHide(2 * time.Minute)
						})
					}(capturedStream)
				}
				return
			}

			button.Hide()
			label.Show()
			label.TextStyle = fyne.TextStyle{}
			label.SetText(row[id.Col])
		},
	)

	table.SetColumnWidth(0, 220)
	table.SetColumnWidth(1, 85)
	table.SetColumnWidth(2, 65)
	table.SetColumnWidth(3, 70)
	table.SetColumnWidth(4, 100)
	table.SetColumnWidth(5, 100)
	table.SetColumnWidth(6, 100)
	table.SetColumnWidth(7, 100)
	table.SetColumnWidth(8, 48)
	table.SetColumnWidth(9, 48)
	table.SetColumnWidth(10, 180)
	table.SetColumnWidth(11, 80)
	table.SetColumnWidth(12, 100)

	stopAll := func() {
		stopAllPreviews()
		for _, stream := range *currentStreams {
			stopProcess(stream, "restart or shutdown all streams")
		}
		jobutil.KillAllFFmpeg()
	}

	restartWithInventory = func(inv sourceInventory, successMessage string) {
		actionStatus.SetText("Restarting streams...")
		stopAll()
		time.Sleep(500 * time.Millisecond)

		currentInventory = inv
		loadInventoryIntoRows(inv)
		renderMonitoringSourceToggles(inv)
		var newStreams []*cameraStream
		newEncoder := currentEncoder
		if inv.Encoder != nil {
			newEncoder = inv.Encoder
		}
		currentEncoder = newEncoder
		updateEncoderStatus()
		var unicastWarning string
		if len(inv.Errors) == 0 && len(inv.Active) > 0 {
			newStreams, unicastWarning = startAllStreams(inv.Active, newEncoder, &streamStartupCallbacks{action: actionStatus.SetText})
		}
		syncBroadcastUIFromConfig(true)
		*currentStreams = newStreams
		clearRestartDirtyForStreams(newStreams)
		table.Refresh()
		updateCameraStatusLabel(inv.Status)
		actionStatus.SetText(successMessage)
		if strings.TrimSpace(unicastWarning) != "" {
			dialog.ShowInformation("Unicast Destinations Disabled", unicastWarning, window)
		}
	}

	restartStreams = func() {
		if err := applyUIToConfig(); err != nil {
			actionStatus.SetText(err.Error())
			return
		}
		if err := camerascfg.SaveConfig(camerasConfig); err != nil {
			actionStatus.SetText(fmt.Sprintf("Save failed: %v", err))
			return
		}
		savedBroadcastSignature = currentBroadcastUISignature()
		restartWithInventory(buildCachedSourceInventory(currentInventory, currentEncoder), "Streams restarted")
		setBroadcastRestartDirty()
	}

	restartBtn.OnTapped = func() {
		restartStreams()
	}
	stopAllBtn.OnTapped = func() {
		dialog.ShowConfirm("Stop All Streams?",
			"This will stop all camera streams.",
			func(ok bool) {
				if !ok {
					return
				}
				actionStatus.SetText("Stopping all streams...")
				stopAll()
				*currentStreams = nil
				table.Refresh()
				updateCameraStatusLabel("All streams stopped.")
				actionStatus.SetText("All streams stopped.")
			}, window)
	}
	broadcastRestartBtn.OnTapped = func() {
		if err := applyBroadcastUIToConfig(); err != nil {
			actionStatus.SetText(fmt.Sprintf("Broadcast save failed: %v", err))
			return
		}
		if err := camerascfg.SaveConfig(camerasConfig); err != nil {
			actionStatus.SetText(fmt.Sprintf("Broadcast save failed: %v", err))
			return
		}
		savedBroadcastSignature = currentBroadcastUISignature()
		restartWithInventory(buildCachedSourceInventory(currentInventory, currentEncoder), "Broadcast settings applied")
		setBroadcastRestartDirty()
	}
	rescanBtn.OnTapped = func() {
		if err := applyUIToConfig(); err != nil {
			actionStatus.SetText(fmt.Sprintf("Rescan failed: %v", err))
			return
		}
		rescanBtn.Disable()
		actionStatus.SetText(detectionProgressUIStrings.RescanningSourcesStatus)
		logging.InfoLogger.Printf("Configuration rescan requested")
		progressDialog, reportProgress, progressHasErrors := newDetectionProgressDialog(window, detectionProgressUIStrings.RescanningSourcesTitle)
		progressDialog.Show()
		reportProgress(recording.ProgressMsg(recording.ProgPreparing, ""))
		go func() {
			startTime := time.Now()
			inv := buildSourceInventoryWithProgress(reportProgress)
			if inv.ConfigChanged {
				if err := camerascfg.SaveConfig(camerasConfig); err != nil {
					inv.Errors = append(inv.Errors, fmt.Sprintf("save disabled absent sources: %v", err))
				}
			}
			for _, item := range inv.Errors {
				reportProgress(recording.ProgressMsg(recording.ProgError, item))
			}
			fyne.Do(func() {
				currentInventory = inv
				if inv.Encoder != nil {
					currentEncoder = inv.Encoder
				}
				updateEncoderStatus()
				loadInventoryIntoRows(inv)
				renderMonitoringSourceToggles(inv)
				table.Refresh()
				updateCameraStatusLabel(inv.Status)
				if progressHasErrors() {
					actionStatus.SetText(detectionProgressUIStrings.RescanCompletedWithErrors)
				} else {
					actionStatus.SetText(verificationMessage(detectionProgressUIStrings.RescanCompleted))
					progressDialog.Hide()
				}
				rescanBtn.Enable()
			})
			logging.InfoLogger.Printf("Configuration rescan completed in %s", time.Since(startTime))
		}()
	}

	startInitialDetection := func() {
		startupProgressDialog, reportStartupProgress, startupHasErrors := newDetectionProgressDialog(window, detectionProgressUIStrings.DetectingSourcesTitle)
		startupProgressDialog.Show()
		reportStartupProgress(recording.ProgressMsg(recording.ProgPreparing, ""))
		go func() {
			startTime := time.Now()
			logging.InfoLogger.Printf("Initial source detection started")
			inv := buildSourceInventoryWithProgress(reportStartupProgress)
			if inv.ConfigChanged {
				if err := camerascfg.SaveConfig(camerasConfig); err != nil {
					inv.Errors = append(inv.Errors, fmt.Sprintf("save disabled absent sources: %v", err))
				}
			}
			if inv.Encoder != nil {
				logging.InfoLogger.Printf("Best encoder: %s (%s)", inv.Encoder.Name, inv.Encoder.Description)
			} else {
				logging.InfoLogger.Printf("No hardware encoder available, will use software: %s", ffmpegConfig.Software.OutputParameters)
			}
			if len(inv.Errors) > 0 {
				for _, item := range inv.Errors {
					logging.ErrorLogger.Println(item)
					reportStartupProgress(recording.ProgressMsg(recording.ProgError, item))
				}
			}

			var newStreams []*cameraStream
			var startupUnicastWarning string
			if len(inv.Active) > 0 && len(inv.Errors) == 0 {
				reportStartupProgress(recording.ProgressMsg(recording.ProgInventoryReady, fmt.Sprintf("Validating %d stream(s)...", len(inv.Active))))
				newStreams, startupUnicastWarning = startAllStreams(inv.Active, inv.Encoder, &streamStartupCallbacks{
					progress: reportStartupProgress,
					action: func(message string) {
						fyne.Do(func() {
							actionStatus.SetText(message)
						})
					},
				})
			}
			fyne.Do(func() {
				currentInventory = inv
				if inv.Encoder != nil {
					currentEncoder = inv.Encoder
				}
				updateEncoderStatus()
				loadInventoryIntoRows(inv)
				renderMonitoringSourceToggles(inv)
				if len(inv.Active) > 0 && len(inv.Errors) == 0 {
					syncBroadcastUIFromConfig(true)
					*currentStreams = newStreams
					if len(newStreams) > 0 {
						updateCameraStatusLabel(inv.Status)
					} else {
						updateCameraStatusLabel("No streams started successfully. Check logs for errors.")
					}
					if strings.TrimSpace(startupUnicastWarning) != "" {
						dialog.ShowInformation("Unicast Destinations Disabled", startupUnicastWarning, window)
					}
				} else {
					updateCameraStatusLabel(inv.Status)
				}
				if startupHasErrors() {
					actionStatus.SetText(detectionProgressUIStrings.DetectionCompletedWithErrors)
				} else {
					actionStatus.SetText("Preview/Record: ready")
					startupProgressDialog.Hide()
				}
				table.Refresh()
			})
			logging.InfoLogger.Printf("Initial source detection completed in %s", time.Since(startTime))
		}()
	}

	stopped := false
	stopOnce := sync.Once{}
	closeFn := func() {
		stopOnce.Do(func() {
			stopped = true
			stopAll()
		})
	}

	ticker := time.NewTicker(1 * time.Second)
	go func() {
		for range ticker.C {
			if stopped {
				return
			}
			autoRecoverRTSPStreams()
			fyne.Do(table.Refresh)
		}
	}()

	exitUI := func() {
		closeFn()
		ticker.Stop()
	}

	multicastRow := container.NewHBox(
		widget.NewLabel("Multicast IP:"),
		ipEntryField,
		localOnlyCheck,
	)
	unicastRow := container.NewVBox(
		widget.NewLabel("Unicast destinations:"),
		unicastContainer,
	)
	updateModeUI := func() {
		if strings.TrimSpace(modeSelect.Selected) == "Unicast" {
			multicastRow.Hide()
			unicastRow.Show()
		} else {
			unicastRow.Hide()
			multicastRow.Show()
		}
	}
	modeSelect.OnChanged = func(selected string) {
		updateModeUI()
		setBroadcastRestartDirty()
	}
	ipEntry.OnChanged = func(string) {
		setBroadcastRestartDirty()
	}
	ipEntry.onFocusLost = nil
	ipEntry.OnSubmitted = nil
	localOnlyCheck.OnChanged = func(bool) {
		setBroadcastRestartDirty()
	}
	updateModeUI()
	setBroadcastRestartDirty()

	portRow := container.NewHBox(
		widget.NewLabel("Mode:"),
		fixedWidth(120, modeSelect),
		broadcastRestartBtn,
	)

	broadcastSection := container.NewVBox(
		newSectionTitle("Broadcast Mode"),
		newVerticalGap(6),
		portRow,
		multicastRow,
		unicastRow,
	)
	encoderSection := container.NewVBox(
		newSectionTitle("Detected Encoder"),
		newVerticalGap(6),
		encoderStatusLabel,
	)
	topConfigSections := container.NewGridWithColumns(2,
		broadcastSection,
		encoderSection,
	)
	usbSection := container.NewVBox(
		newSectionTitle("Detected Sources"),
		newVerticalGap(6),
		usbRowsContainer,
	)
	rtspSection := container.NewVBox(
		newSectionTitle("RTSP Sources"),
		newVerticalGap(6),
		rtspRowsContainer,
	)

	monitoringTable := container.NewScroll(table)
	monitoringTab := container.NewBorder(
		container.NewVBox(
			cameraStatusLabel,
			container.NewHBox(actionStatus, clipLink, restartBtn, stopAllBtn),
		),
		nil,
		nil,
		nil,
		monitoringTable,
	)
	rescanBtn.Importance = widget.HighImportance
	restartFromConfigBtn := widget.NewButton("Restart Streams", func() {
		restartStreams()
	})
	restartFromConfigBtn.Importance = widget.HighImportance
	configurationTab := container.NewScroll(
		container.NewPadded(container.NewVBox(
			newVerticalGap(4),
			container.NewHBox(rescanBtn, restartFromConfigBtn),
			newVerticalGap(8),
			topConfigSections,
			newVerticalGap(12),
			usbSection,
			newVerticalGap(12),
			rtspSection,
			newVerticalGap(8),
		)),
	)
	configurationTab.SetMinSize(fyne.NewSize(0, 420))

	var includeInternalCamerasItem *fyne.MenuItem
	includeInternalCamerasItem = fyne.NewMenuItem("Include Internal Cameras", func() {
		if camerasConfig == nil {
			return
		}

		previous := camerasConfig.Cameras.IncludeAll
		camerasConfig.Cameras.IncludeAll = !previous
		if err := camerascfg.SaveConfig(camerasConfig); err != nil {
			camerasConfig.Cameras.IncludeAll = previous
			dialog.ShowError(fmt.Errorf("save Include Internal Cameras setting: %w", err), window)
			return
		}

		includeInternalCamerasItem.Checked = camerasConfig.Cameras.IncludeAll
		logging.InfoLogger.Printf("Include internal cameras set to %t", camerasConfig.Cameras.IncludeAll)
		rescanBtn.OnTapped()
	})
	includeInternalCamerasItem.Checked = camerasConfig.Cameras.IncludeAll

	menus := []*fyne.Menu{
		fyne.NewMenu("Cameras",
			includeInternalCamerasItem,
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Rescan Sources", func() {
				rescanBtn.OnTapped()
			}),
			fyne.NewMenuItem("Restart Streams", func() {
				restartStreams()
			}),
		),
	}

	return &UI{
		Monitoring:    monitoringTab,
		Configuration: configurationTab,
		Menus:         menus,
		Start:         startInitialDetection,
		Stop:          exitUI,
	}
}
