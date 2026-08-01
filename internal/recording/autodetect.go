package recording

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/config/ffmpeg"
	"github.com/owlcms/video/internal/logging"
)

// DetectedCamera holds information about a detected camera device
type DetectedCamera struct {
	Name             string
	Device           string       // device path (Linux), device name (Windows), or video index (macOS)
	Format           string       // v4l2, dshow, avfoundation, or rtsp
	TransportType    string       // native transport identifier when the platform exposes one (for example, "usb ")
	PixFmt           string       // mjpeg, yuyv422, etc.
	Size             string       // best resolution found
	Fps              int          // best fps for that resolution
	MatchKey         string       // stable-ish key for persistence across restarts
	AttachmentPath   string       // stable path/moniker for the physical attachment location
	Identity         string       // human-readable stable identity (USB topology, by-path, etc.)
	SupportedFormats []string     // unique pixel format names (e.g. "mjpeg", "yuyv422")
	modes            []cameraMode // all detected modes, for RepickForFormat
}

// RepickForFormat re-selects the best camera mode restricted to the given pixel format.
// If preferredFormat is empty or unavailable, the camera is left unchanged.
func (cam *DetectedCamera) RepickForFormat(preferredFormat string, cfg *ffmpeg.Config) {
	if preferredFormat == "" || len(cam.modes) == 0 {
		return
	}
	var filtered []cameraMode
	for _, m := range cam.modes {
		if m.pixFmt == preferredFormat {
			filtered = append(filtered, m)
		}
	}
	if len(filtered) == 0 {
		return
	}
	best := PickBestCameraModeWithConfig(filtered, cfg)
	cam.PixFmt = best.pixFmt
	cam.Size = fmt.Sprintf("%dx%d", best.width, best.height)
	cam.Fps = best.fps
}

// uniqueFormats returns sorted unique format names from a set of camera modes.
func uniqueFormats(modes []cameraMode) []string {
	seen := make(map[string]struct{})
	var formats []string
	for _, m := range modes {
		if _, ok := seen[m.pixFmt]; !ok {
			seen[m.pixFmt] = struct{}{}
			formats = append(formats, m.pixFmt)
		}
	}
	sort.Strings(formats)
	return formats
}

// HwEncoder holds information about a detected hardware encoder
type HwEncoder struct {
	Name             string // h264_nvenc, h264_vaapi, h264_amf, h264_qsv
	Description      string
	InputParameters  string
	OutputParameters string
	VideoFilter      string
	TestInit         string // extra flags needed for encoder test (e.g. VAAPI/QSV init)
	FFmpegPath       string // optional per-encoder ffmpeg path override
}

type cameraMode struct {
	pixFmt string
	width  int
	height int
	fps    int
}

type ProbeProgressFunc func(string)

// DetectAndWriteConfig probes cameras and GPU encoders, then writes auto.toml.
// It loads ffmpeg.toml configuration so auto.toml benefits from the same
// intelligent encoder definitions, format priorities, and mode priorities
// used by the cameras program.
func DetectAndWriteConfig(window fyne.Window) {
	logging.InfoLogger.Println("Starting hardware auto-detection...")

	progressLabel := widget.NewLabel("Detecting hardware encoders...")
	progressDialog := dialog.NewCustomWithoutButtons("Auto-Detecting Hardware", progressLabel, window)
	progressDialog.Show()

	go func() {
		defer fyne.Do(progressDialog.Hide)

		// Load shared ffmpeg.toml config so encoder and camera priorities have one source of truth.
		cameraCfg, cfgErr := ffmpeg.LoadConfig()
		if cfgErr != nil {
			logging.ErrorLogger.Printf("Could not load ffmpeg.toml for automatic detection: %v", cfgErr)
			fyne.Do(func() {
				dialog.ShowError(fmt.Errorf("could not load ffmpeg.toml for automatic detection: %v", cfgErr), window)
			})
			return
		}

		// Step 1: Detect available H.264 hardware encoders from ffmpeg.toml definitions.
		fyne.Do(func() {
			progressLabel.SetText("Detecting hardware encoders...")
		})
		encoders := DetectEncodersWithConfig(cameraCfg)
		logging.InfoLogger.Printf("Detected %d hardware encoders", len(encoders))

		// Step 2: Detect cameras using ffmpeg.toml mode priorities.
		fyne.Do(func() {
			progressLabel.SetText("Detecting cameras...")
		})
		cameras := DetectCamerasWithConfig(cameraCfg)
		logging.InfoLogger.Printf("Detected %d cameras", len(cameras))

		// Step 3: Write auto.toml (even with 0 cameras, to show detected encoders)
		fyne.Do(func() {
			progressLabel.SetText("Writing auto.toml...")
		})
		// Output to autoTomlDir if set, otherwise install dir
		var outputPath string
		if config.AutoTomlDir != "" {
			outputPath = filepath.Join(config.AutoTomlDir, "auto.toml")
		} else {
			outputPath = filepath.Join(config.GetInstallDir(), "auto.toml")
		}
		err := writeAutoConfig(outputPath, cameras, encoders, cameraCfg)
		if err != nil {
			logging.ErrorLogger.Printf("Failed to write auto.toml: %v", err)
			fyne.Do(func() {
				dialog.ShowError(fmt.Errorf("failed to write auto.toml: %v", err), window)
			})
			return
		}

		// Step 4: Show results
		summary := buildSummary(cameras, encoders, outputPath)
		fyne.Do(func() {
			showAutoDetectResults(summary, outputPath, window)
		})
	}()
}

// DetectEncoders probes ffmpeg for available H.264 hardware encoders using ffmpeg.toml.
func DetectEncoders() []HwEncoder {
	cfg, err := ffmpeg.LoadConfig()
	if err != nil {
		logging.ErrorLogger.Printf("Failed to load ffmpeg.toml for encoder detection: %v", err)
		return nil
	}
	return DetectEncodersWithConfig(cfg)
}

// DetectEncodersWithConfig probes ffmpeg for available H.264 hardware encoders,
// using the encoder definitions from cfg.
func DetectEncodersWithConfig(cfg *ffmpeg.Config) []HwEncoder {
	return DetectEncodersWithConfigAndProgress(cfg, nil)
}

func DetectEncodersWithConfigAndProgress(cfg *ffmpeg.Config, progress ProbeProgressFunc) []HwEncoder {
	if cfg == nil {
		loaded, err := ffmpeg.LoadConfig()
		if err != nil {
			logging.ErrorLogger.Printf("Failed to load ffmpeg.toml for encoder detection: %v", err)
			if progress != nil {
				progress(ProgressMsg(ProgHwMsg, "Error: failed to load ffmpeg.toml for encoder detection"))
			}
			return nil
		}
		cfg = loaded
	}

	path := config.GetFFmpegPath()
	if path == "" {
		path = "ffmpeg"
	}
	logging.InfoLogger.Printf("Probing hardware encoders with ffmpeg: %s", path)

	found := detectEncodersWithPath(path, cfg, progress)
	if !shouldRetrySystemFFmpegProbe(found, cfg) {
		return found
	}

	fallbackPath := findSystemFFmpegPath(path)
	if fallbackPath == "" {
		return found
	}

	logging.InfoLogger.Printf("Retrying hardware encoder probe with system ffmpeg %s after bundled probe with %s", fallbackPath, path)
	if progress != nil {
		progress(ProgressMsg(ProgHwMsg, "Retrying hardware encoders with system ffmpeg..."))
	}

	fallbackFound := detectEncodersWithPath(fallbackPath, cfg, progress)
	if len(fallbackFound) == 0 {
		return found
	}

	merged := mergeFallbackEncoders(found, fallbackFound, cfg)
	if len(merged) > len(found) {
		logging.InfoLogger.Printf("Using system ffmpeg fallback encoders after bundled probe failure: %s", fallbackPath)
	}
	return merged
}

func shouldRetrySystemFFmpegProbe(found []HwEncoder, cfg *ffmpeg.Config) bool {
	vendors := detectGPUVendors()
	if cfg == nil {
		return false
	}

	for _, enc := range cfg.Encoders {
		if enc.Platform != "" && !encoderPlatformMatchesRuntime(enc.Platform) {
			continue
		}
		if !encoderGPUVendorMatches(enc.GpuVendors, vendors) {
			continue
		}
		if containsHwEncoder(found, enc.Name) {
			continue
		}
		logging.InfoLogger.Printf("GPU-backed encoder %s was not verified with the current ffmpeg; checking for system ffmpeg fallback", enc.Name)
		return true
	}

	return false
}

func containsHwEncoder(encoders []HwEncoder, name string) bool {
	for _, enc := range encoders {
		if enc.Name == name {
			return true
		}
	}
	return false
}

func mergeFallbackEncoders(primary, fallback []HwEncoder, cfg *ffmpeg.Config) []HwEncoder {
	if len(fallback) == 0 {
		return primary
	}

	byName := make(map[string]HwEncoder, len(primary)+len(fallback))
	for _, enc := range primary {
		byName[enc.Name] = enc
	}
	for _, enc := range fallback {
		if _, exists := byName[enc.Name]; !exists {
			byName[enc.Name] = enc
		}
	}

	ordered := make([]HwEncoder, 0, len(byName))
	seen := make(map[string]struct{}, len(byName))
	for _, name := range encoderPreferenceOrder(cfg) {
		enc, ok := byName[name]
		if !ok {
			continue
		}
		ordered = append(ordered, enc)
		seen[name] = struct{}{}
	}
	for _, enc := range primary {
		if _, ok := seen[enc.Name]; ok {
			continue
		}
		ordered = append(ordered, enc)
		seen[enc.Name] = struct{}{}
	}
	for _, enc := range fallback {
		if _, ok := seen[enc.Name]; ok {
			continue
		}
		ordered = append(ordered, enc)
		seen[enc.Name] = struct{}{}
	}

	return ordered
}

func encoderPreferenceOrder(cfg *ffmpeg.Config) []string {
	if cfg == nil {
		return nil
	}
	order := make([]string, 0, len(cfg.Encoders))
	seen := make(map[string]struct{}, len(cfg.Encoders))
	for _, enc := range cfg.Encoders {
		if _, ok := seen[enc.Name]; ok {
			continue
		}
		seen[enc.Name] = struct{}{}
		order = append(order, enc.Name)
	}
	return order
}

func detectEncodersWithPath(path string, cfg *ffmpeg.Config, progress ProbeProgressFunc) []HwEncoder {
	if progress != nil {
		progress(ProgressMsg(ProgHwMsg, "Checking available hardware encoders..."))
	}

	detectedGPUVendors := detectGPUVendors()
	if len(detectedGPUVendors) > 0 {
		logging.InfoLogger.Printf("Detected GPU vendors: %s", strings.Join(sortedVendorKeys(detectedGPUVendors), ", "))
	}

	encoderQueryArgs := []string{"-hide_banner", "-encoders"}
	logging.InfoLogger.Printf("Querying ffmpeg encoders: %s", formatCommandLine(path, encoderQueryArgs))
	cmd := CreateHiddenCmd(path, encoderQueryArgs...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		logging.ErrorLogger.Printf("Failed to query ffmpeg encoders: %v", err)
		return nil
	}

	availableEncoders := parseAvailableH264Encoders(out.Bytes())
	if progress != nil {
		reportUnconfiguredEncoders(availableEncoders, cfg, progress)
	}

	// Build candidate list from config definitions (order = preference)
	var candidates []HwEncoder
	for _, enc := range cfg.Encoders {
		if !encoderIsProbeCandidate(enc.Name, enc.GpuVendors, availableEncoders, detectedGPUVendors) {
			continue
		}
		// Check platform restriction (supports dshow/v4l2 and linux/windows)
		if enc.Platform != "" && !encoderPlatformMatchesRuntime(enc.Platform) {
			logging.InfoLogger.Printf("Skipping encoder %s: platform=%s, current_os=%s", enc.Name, enc.Platform, runtime.GOOS)
			continue
		}
		logging.InfoLogger.Printf("Encoder %s is a candidate (gpuVendors=%v, detected=%v)", enc.Name, enc.GpuVendors, sortedVendorKeys(detectedGPUVendors))
		candidates = append(candidates, HwEncoder{
			Name:             enc.Name,
			Description:      enc.Description,
			InputParameters:  enc.InputParameters,
			OutputParameters: enc.OutputParameters,
			VideoFilter:      enc.VideoFilter,
			TestInit:         enc.TestInit,
		})
	}

	// Verify each candidate encoder actually works on this hardware
	var found []HwEncoder
	for _, enc := range candidates {
		if progress != nil {
			progress(ProgressMsg(ProgEncoder, enc.Name))
		}
		if testEncoderWithInit(path, enc) {
			enc.FFmpegPath = path
			logging.InfoLogger.Printf("Encoder %s verified working", enc.Name)
			found = append(found, enc)
		} else {
			logging.InfoLogger.Printf("Encoder %s compiled in but not functional on this hardware", enc.Name)
		}
	}
	return found
}

func reportUnconfiguredEncoders(available map[string]bool, cfg *ffmpeg.Config, progress ProbeProgressFunc) {
	if cfg == nil {
		return
	}
	configured := make(map[string]struct{}, len(cfg.Encoders))
	for _, enc := range cfg.Encoders {
		name := strings.TrimSpace(enc.Name)
		if name != "" {
			configured[name] = struct{}{}
		}
	}

	var missing []string
	for name, ok := range available {
		if !ok {
			continue
		}
		if _, exists := configured[name]; !exists {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		logging.InfoLogger.Printf("Skipping encoder %s: no ffmpeg.toml settings", name)
	}
}

func parseAvailableH264Encoders(output []byte) map[string]bool {
	available := make(map[string]bool)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(strings.TrimSpace(scanner.Text()))
		if len(fields) < 2 {
			continue
		}
		flags := fields[0]
		name := fields[1]
		if len(flags) != 6 || !strings.HasPrefix(flags, "V") || !strings.HasPrefix(name, "h264_") {
			continue
		}
		available[name] = true
	}
	return available
}

func encoderIsProbeCandidate(name string, gpuVendors []string, available map[string]bool, detectedGPUVendors map[string]bool) bool {
	if !available[name] {
		logging.InfoLogger.Printf("Skipping encoder %s: not reported by ffmpeg -encoders", name)
		return false
	}
	if !encoderGPUVendorMatches(gpuVendors, detectedGPUVendors) {
		logging.InfoLogger.Printf("Skipping encoder %s: required GPU vendors=%v, detected=%v", name, gpuVendors, sortedVendorKeys(detectedGPUVendors))
		return false
	}
	return true
}

func encoderGPUVendorMatches(required []string, detected map[string]bool) bool {
	if len(required) == 0 {
		return true
	}

	hasRequirement := false
	for _, vendor := range required {
		vendor = strings.ToLower(strings.TrimSpace(vendor))
		if vendor == "" {
			continue
		}
		hasRequirement = true
		if detected[vendor] {
			return true
		}
	}
	if !hasRequirement {
		return true
	}
	return false
}

func encoderPlatformMatchesRuntime(platform string) bool {
	p := strings.ToLower(strings.TrimSpace(platform))
	if p == "" {
		return true
	}

	switch runtime.GOOS {
	case "windows":
		return p == "windows" || p == "dshow"
	case "linux":
		return p == "linux" || p == "v4l2"
	case "darwin":
		return p == "darwin" || p == "macos" || p == "avfoundation"
	default:
		return p == runtime.GOOS
	}
}

func detectGPUVendors() map[string]bool {
	vendors := map[string]bool{}

	if runtime.GOOS == "windows" {
		return detectGPUVendorsWindows(vendors)
	}
	if runtime.GOOS == "darwin" {
		return vendors
	}

	// Method 1: Check NVIDIA driver file
	if _, err := os.Stat("/proc/driver/nvidia/version"); err == nil {
		logging.InfoLogger.Printf("NVIDIA detected via /proc/driver/nvidia/version")
		vendors["nvidia"] = true
	}

	// Method 2: Check nvidia-smi (works even if /proc file is missing)
	if !vendors["nvidia"] {
		cmd := CreateHiddenCmd("nvidia-smi", "-L")
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err == nil && strings.Contains(strings.ToLower(out.String()), "gpu") {
			logging.InfoLogger.Printf("NVIDIA detected via nvidia-smi")
			vendors["nvidia"] = true
		}
	}

	// Method 3: Check /sys/class/drm for card drivers
	drmPath := "/sys/class/drm"
	if entries, err := os.ReadDir(drmPath); err == nil {
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "card") || strings.Contains(entry.Name(), "-") {
				continue
			}
			driverLink := filepath.Join(drmPath, entry.Name(), "device", "driver")
			if target, err := os.Readlink(driverLink); err == nil {
				driverName := strings.ToLower(filepath.Base(target))
				if strings.Contains(driverName, "nvidia") || strings.Contains(driverName, "nouveau") {
					if !vendors["nvidia"] {
						logging.InfoLogger.Printf("NVIDIA detected via /sys/class/drm (%s)", driverName)
					}
					vendors["nvidia"] = true
				}
				if strings.Contains(driverName, "amdgpu") || strings.Contains(driverName, "radeon") {
					if !vendors["amd"] {
						logging.InfoLogger.Printf("AMD detected via /sys/class/drm (%s)", driverName)
					}
					vendors["amd"] = true
				}
				if strings.Contains(driverName, "i915") || strings.Contains(driverName, "xe") {
					if !vendors["intel"] {
						logging.InfoLogger.Printf("Intel detected via /sys/class/drm (%s)", driverName)
					}
					vendors["intel"] = true
				}
			}
		}
	}

	// Method 4: Fallback to lspci for any vendors not yet detected
	cmd := CreateHiddenCmd("lspci", "-nn")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		logging.InfoLogger.Printf("Could not run lspci for GPU detection: %v", err)
		return vendors
	}

	for _, line := range strings.Split(strings.ToLower(out.String()), "\n") {
		if line == "" {
			continue
		}
		// Only check VGA/3D/Display controller lines
		if !strings.Contains(line, "vga") && !strings.Contains(line, "3d") && !strings.Contains(line, "display") {
			continue
		}
		if strings.Contains(line, "nvidia") && !vendors["nvidia"] {
			logging.InfoLogger.Printf("NVIDIA detected via lspci")
			vendors["nvidia"] = true
		}
		if (strings.Contains(line, "advanced micro devices") || strings.Contains(line, "[amd") || strings.Contains(line, " amd/") || strings.Contains(line, "ati")) && !vendors["amd"] {
			logging.InfoLogger.Printf("AMD detected via lspci")
			vendors["amd"] = true
		}
		if strings.Contains(line, "intel") && !vendors["intel"] {
			logging.InfoLogger.Printf("Intel detected via lspci")
			vendors["intel"] = true
		}
	}

	return vendors
}

func sortedVendorKeys(vendors map[string]bool) []string {
	keys := make([]string, 0, len(vendors))
	for vendor, enabled := range vendors {
		if enabled {
			keys = append(keys, vendor)
		}
	}
	sort.Strings(keys)
	return keys
}

// detectGPUVendorsWindows uses wmic to enumerate GPU adapters on Windows.
func detectGPUVendorsWindows(vendors map[string]bool) map[string]bool {
	cmd := CreateHiddenCmd("wmic", "path", "win32_VideoController", "get", "Name")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		logging.InfoLogger.Printf("Could not query GPU via wmic: %v", err)
		return vendors
	}

	for _, line := range strings.Split(strings.ToLower(out.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "name" {
			continue
		}
		if strings.Contains(line, "nvidia") && !vendors["nvidia"] {
			logging.InfoLogger.Printf("NVIDIA detected via wmic (%s)", line)
			vendors["nvidia"] = true
		}
		if (strings.Contains(line, "amd") || strings.Contains(line, "radeon")) && !vendors["amd"] {
			logging.InfoLogger.Printf("AMD detected via wmic (%s)", line)
			vendors["amd"] = true
		}
		if strings.Contains(line, "intel") && !vendors["intel"] {
			logging.InfoLogger.Printf("Intel detected via wmic (%s)", line)
			vendors["intel"] = true
		}
	}

	return vendors
}

func summarizeEncoderProbeError(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return "no ffmpeg error details"
	}

	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		const maxLen = 180
		if len(line) > maxLen {
			return line[:maxLen] + "..."
		}
		return line
	}

	return "no ffmpeg error details"
}

func formatCommandLine(name string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, strconv.Quote(name))
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

var ffmpegVersionPattern = regexp.MustCompile(`(?i)^ffmpeg version `)

func isSupportedSystemFFmpeg(path string) bool {
	cmd := CreateHiddenCmd(path, "-version")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		logging.InfoLogger.Printf("Skipping system ffmpeg candidate %s: failed to query version: %v", path, err)
		return false
	}

	versionLine := strings.TrimSpace(out.String())
	if idx := strings.Index(versionLine, "\n"); idx >= 0 {
		versionLine = strings.TrimSpace(versionLine[:idx])
	}
	if versionLine == "" {
		logging.InfoLogger.Printf("Skipping system ffmpeg candidate %s: empty version output", path)
		return false
	}
	if !ffmpegVersionPattern.MatchString(versionLine) {
		logging.InfoLogger.Printf("Skipping system ffmpeg candidate %s: unexpected version output %s", path, versionLine)
		return false
	}

	logging.InfoLogger.Printf("System ffmpeg candidate %s is supported: %s", path, versionLine)
	return true
}

// testEncoderWithInit tests whether an encoder actually works on this hardware,
// using the TestInit field for any required hardware init flags.
func testEncoderWithInit(ffmpegPath string, enc HwEncoder) bool {
	var args []string

	if enc.TestInit != "" {
		args = append(args, "-hide_banner", "-loglevel", "error")
		args = append(args, strings.Fields(enc.TestInit)...)
		args = append(args, "-f", "lavfi", "-i", "nullsrc=s=64x64:d=0.1")
		// VAAPI needs hwupload filter
		if strings.Contains(enc.Name, "vaapi") {
			args = append(args, "-vf", "format=nv12,hwupload")
		}
		args = append(args, "-c:v", enc.Name, "-f", "null", "-")
	} else {
		args = []string{"-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "nullsrc=s=64x64:d=0.1",
			"-c:v", enc.Name, "-f", "null", "-"}
	}

	logging.InfoLogger.Printf("Testing encoder %s with command: %s", enc.Name, formatCommandLine(ffmpegPath, args))

	cmd := CreateHiddenCmd(ffmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		reason := summarizeEncoderProbeError(stderr.String())
		logging.InfoLogger.Printf("Encoder test for %s failed: %v (%s)", enc.Name, err, reason)
		return false
	}
	return true
}

// DetectCameras detects available camera devices and their capabilities using ffmpeg.toml.
func DetectCameras() []DetectedCamera {
	cfg, err := ffmpeg.LoadConfig()
	if err != nil {
		logging.ErrorLogger.Printf("Failed to load ffmpeg.toml for camera detection: %v", err)
		return nil
	}
	return DetectCamerasWithConfig(cfg)
}

// DetectCamerasWithConfig detects cameras using config-driven mode priorities.
func DetectCamerasWithConfig(cfg *ffmpeg.Config) []DetectedCamera {
	return DetectCamerasWithConfigAndProgress(cfg, nil)
}

func DetectCamerasWithConfigAndProgress(cfg *ffmpeg.Config, progress ProbeProgressFunc) []DetectedCamera {
	return DetectCamerasWithConfigAndProgressFiltered(cfg, progress, nil)
}

func DetectCamerasWithConfigAndProgressFiltered(cfg *ffmpeg.Config, progress ProbeProgressFunc, skip func(name, matchKey, attachmentPath string) bool) []DetectedCamera {
	if cfg == nil {
		loaded, err := ffmpeg.LoadConfig()
		if err != nil {
			logging.ErrorLogger.Printf("Failed to load ffmpeg.toml for camera detection: %v", err)
			if progress != nil {
				progress(ProgressMsg(ProgHwMsg, "Error: failed to load ffmpeg.toml for camera detection"))
			}
			return nil
		}
		cfg = loaded
	}

	switch runtime.GOOS {
	case "linux":
		return detectCamerasLinux(cfg, progress, skip)
	case "windows":
		return detectCamerasWindows(cfg, progress, skip)
	case "darwin":
		return detectCamerasDarwin(cfg, progress, skip)
	default:
		return nil
	}
}

type avfoundationDevice struct {
	index string
	name  string
}

type avFoundationDeviceMetadata struct {
	transportType string
}

var avfoundationVideoDeviceRe = regexp.MustCompile(`\[(\d+)\]\s+(.+)$`)
var avfoundationModeRe = regexp.MustCompile(`(\d+)x(\d+)@\[([^\]]+)\]fps`)
var avfoundationPixelFormatRe = regexp.MustCompile(`\]\s+([a-zA-Z0-9]+)\s*$`)

// detectCamerasDarwin uses FFmpeg's AVFoundation input to detect macOS cameras.
func detectCamerasDarwin(cfg *ffmpeg.Config, progress ProbeProgressFunc, skip func(name, matchKey, attachmentPath string) bool) []DetectedCamera {
	path := config.GetFFmpegPath()
	if path == "" {
		path = "ffmpeg"
	}
	if progress != nil {
		progress(ProgressMsg(ProgListing, "AVFoundation devices"))
	}

	cmd := CreateHiddenCmd(path, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	_ = cmd.Run() // Listing devices intentionally fails because there is no input.

	metadataByName := avFoundationVideoDeviceMetadata()
	var cameras []DetectedCamera
	for _, device := range parseAVFoundationVideoDevices(out.String()) {
		matchKey, attachmentPath, _ := resolveAVFoundationCameraIdentity(device.name)
		if skip != nil && skip(device.name, matchKey, attachmentPath) {
			continue
		}
		if progress != nil {
			progress(ProgressMsg(ProgLocalSource, device.name))
		}
		if camera := probeAVFoundationDevice(path, device, cfg); camera != nil {
			if metadata, ok := metadataByName[strings.ToLower(strings.TrimSpace(device.name))]; ok {
				camera.TransportType = metadata.transportType
			} else {
				logging.WarningLogger.Printf("No native AVFoundation transport metadata for %s; includeAll=false will exclude it", device.name)
			}
			cameras = append(cameras, *camera)
		}
	}
	return cameras
}

func parseAVFoundationVideoDevices(output string) []avfoundationDevice {
	var devices []avfoundationDevice
	inVideoSection := false
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "AVFoundation video devices:") {
			inVideoSection = true
			continue
		}
		if strings.Contains(line, "AVFoundation audio devices:") {
			break
		}
		if !inVideoSection {
			continue
		}
		match := avfoundationVideoDeviceRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name := strings.TrimSpace(match[2])
		if name == "" || strings.HasPrefix(strings.ToLower(name), "capture screen") || isDeskViewCamera(name) {
			continue
		}
		devices = append(devices, avfoundationDevice{index: match[1], name: name})
	}
	return devices
}

func isDeskViewCamera(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), "desk view camera")
}

// ResolveAVFoundationDeviceIndex returns the current AVFoundation device index
// for the named device. AVFoundation numeric indices are NOT stable: they
// change when cameras are connected/disconnected or when another process opens
// a device, so an index captured at detection time can later point at a
// different physical camera. Re-resolving by name immediately before opening
// the device avoids streaming from the wrong camera (which manifests as a false
// "framerate not supported" failure when the wrong camera has a lower max fps).
func ResolveAVFoundationDeviceIndex(name string) (string, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if trimmed == "" {
		return "", false
	}
	path := config.GetFFmpegPath()
	if path == "" {
		path = "ffmpeg"
	}
	cmd := CreateHiddenCmd(path, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	_ = cmd.Run() // Listing devices intentionally fails because there is no input.

	for _, device := range parseAVFoundationVideoDevices(out.String()) {
		if strings.ToLower(strings.TrimSpace(device.name)) == trimmed {
			return device.index, true
		}
	}
	return "", false
}

func probeAVFoundationDevice(ffmpegPath string, device avfoundationDevice, cfg *ffmpeg.Config) *DetectedCamera {
	input := device.index + ":none"
	modeProbe := CreateHiddenCmd(ffmpegPath, "-hide_banner", "-f", "avfoundation", "-framerate", "1000", "-i", input)
	var modeOutput bytes.Buffer
	modeProbe.Stdout = &modeOutput
	modeProbe.Stderr = &modeOutput
	_ = modeProbe.Run() // The invalid framerate prints the supported modes without capturing.

	modes := parseAVFoundationModes(modeOutput.String())
	if len(modes) == 0 {
		logging.InfoLogger.Printf("No AVFoundation modes reported for %s", device.name)
		return nil
	}
	best := PickBestCameraModeWithConfig(modes, cfg)

	pixelProbe := CreateHiddenCmd(ffmpegPath,
		"-hide_banner",
		"-f", "avfoundation",
		"-framerate", strconv.Itoa(best.fps),
		"-video_size", fmt.Sprintf("%dx%d", best.width, best.height),
		"-pixel_format", "yuv420p",
		"-i", input,
		"-frames:v", "1",
		"-f", "null", "-",
	)
	var pixelOutput bytes.Buffer
	pixelProbe.Stdout = &pixelOutput
	pixelProbe.Stderr = &pixelOutput
	_ = pixelProbe.Run() // The unsupported default format prints supported formats; one null frame bounds the probe.
	pixFmt := parseAVFoundationPixelFormat(pixelOutput.String())
	if pixFmt == "" {
		logging.InfoLogger.Printf("No AVFoundation pixel format reported for %s", device.name)
		return nil
	}

	matchKey, attachmentPath, identity := resolveAVFoundationCameraIdentity(device.name)
	return &DetectedCamera{
		Name:             device.name,
		Device:           device.index,
		Format:           "avfoundation",
		PixFmt:           pixFmt,
		Size:             fmt.Sprintf("%dx%d", best.width, best.height),
		Fps:              best.fps,
		MatchKey:         matchKey,
		AttachmentPath:   attachmentPath,
		Identity:         identity,
		SupportedFormats: uniqueFormats(modes),
		modes:            modes,
	}
}

func parseAVFoundationModes(output string) []cameraMode {
	var modes []cameraMode
	for _, match := range avfoundationModeRe.FindAllStringSubmatch(output, -1) {
		width := atoi(match[1])
		height := atoi(match[2])
		maxFPS := 0
		for _, value := range strings.Fields(match[3]) {
			fps := parseFps(value)
			if fps > maxFPS {
				maxFPS = fps
			}
		}
		if width > 0 && height > 0 && maxFPS > 0 {
			modes = append(modes, cameraMode{pixFmt: "raw", width: width, height: height, fps: maxFPS})
		}
	}
	return modes
}

func parseAVFoundationPixelFormat(output string) string {
	inFormats := false
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Supported pixel formats:") {
			inFormats = true
			continue
		}
		if !inFormats {
			continue
		}
		match := avfoundationPixelFormatRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		return strings.ToLower(match[1])
	}
	return ""
}

// detectCamerasLinux uses v4l2-ctl to detect cameras and their formats
func detectCamerasLinux(cfg *ffmpeg.Config, progress ProbeProgressFunc, skip func(name, matchKey, attachmentPath string) bool) []DetectedCamera {
	if progress != nil {
		progress(ProgressMsg(ProgListing, "V4L2 devices"))
	}
	// First get the device list
	cmd := CreateHiddenCmd("v4l2-ctl", "--list-devices")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		logging.ErrorLogger.Printf("v4l2-ctl --list-devices failed: %v", err)
		return nil
	}

	// Parse device list: camera name followed by indented /dev/videoN lines
	type deviceEntry struct {
		name     string
		device   string
		location string
	}
	var devices []deviceEntry
	var currentName string
	var currentLocation string
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") {
			currentLocation = ""
			// Camera name line - keep any parenthetical suffix as a stable identity hint.
			if idx := strings.Index(line, " ("); idx != -1 {
				currentName = strings.TrimSpace(line[:idx])
				currentLocation = strings.TrimRight(strings.TrimSpace(line[idx+1:]), "):")
			} else {
				currentName = strings.TrimRight(strings.TrimSpace(line), ":")
			}
		} else {
			trimmed := strings.TrimSpace(line)
			// Skip /dev/media entries, only consider /dev/videoN
			if strings.HasPrefix(trimmed, "/dev/video") && currentName != "" {
				// Strip "Webcam gadget: " prefix from Linux USB gadget virtual cameras
				currentName = strings.TrimPrefix(currentName, "Webcam gadget: ")
				devices = append(devices, deviceEntry{name: currentName, device: trimmed, location: currentLocation})
				currentName = "" // skip subsequent devices for same camera
				currentLocation = ""
			}
		}
	}

	var cameras []DetectedCamera
	for _, dev := range devices {
		matchKey, attachmentPath, _ := resolveStableCameraIdentity(dev.name, dev.device, dev.location)
		if skip != nil && skip(dev.name, matchKey, attachmentPath) {
			continue
		}
		if progress != nil {
			progress(ProgressMsg(ProgLocalSource, dev.name))
		}
		cam := probeV4L2Device(dev.name, dev.device, dev.location, cfg)
		if cam != nil {
			cameras = append(cameras, *cam)
		}
	}
	return cameras
}

// probeV4L2Device probes a single v4l2 device for its best format
func probeV4L2Device(name, device, location string, cfg *ffmpeg.Config) *DetectedCamera {
	cmd := CreateHiddenCmd("v4l2-ctl", "-d", device, "--list-formats-ext")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		logging.ErrorLogger.Printf("Failed to probe %s: %v", device, err)
		return nil
	}

	// Parse the output to find MJPEG and YUYV formats with their resolutions and fps
	// The output format is:
	//   [N]: 'MJPG' (Motion-JPEG, compressed)
	//           Size: Discrete 1920x1080
	//                   Interval: Discrete 0.033s (30.000 fps)
	//                   Interval: Discrete 0.042s (24.000 fps)
	//           Size: Discrete 1280x720
	//                   ...
	type formatInfo struct {
		pixFmt string
		width  int
		height int
		fps    int
	}

	var formats []formatInfo
	var currentPixFmt string
	var currentWidth, currentHeight int

	// Match common V4L2 pixel formats
	formatRe := regexp.MustCompile(`'(MJPG|YUYV|H264|NV12|RGB3|BGR3|UYVY)'`)
	sizeRe := regexp.MustCompile(`Size:\s+Discrete\s+(\d+)x(\d+)`)
	fpsRe := regexp.MustCompile(`\(([0-9]+(?:\.[0-9]+)?)\s+fps\)`)

	// First pass: collect all lines
	var lines []string
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	// Second pass: parse with proper state tracking
	for _, line := range lines {
		// Check for format header
		if m := formatRe.FindStringSubmatch(line); m != nil {
			switch m[1] {
			// Compressed formats
			case "MJPG":
				currentPixFmt = "mjpeg"
			case "H264":
				currentPixFmt = "h264"
			// Raw formats - no decode needed
			case "YUYV":
				currentPixFmt = "yuyv422"
			case "NV12":
				currentPixFmt = "nv12"
			case "RGB3":
				currentPixFmt = "rgb24"
			case "BGR3":
				currentPixFmt = "bgr24"
			case "UYVY":
				currentPixFmt = "uyvy422"
			}
			currentWidth = 0
			currentHeight = 0
			continue
		}

		// Check for size line
		if m := sizeRe.FindStringSubmatch(line); m != nil {
			currentWidth = atoi(m[1])
			currentHeight = atoi(m[2])
			continue
		}

		// Check for fps line
		if m := fpsRe.FindStringSubmatch(line); m != nil && currentPixFmt != "" && currentWidth > 0 {
			fps := parseFps(m[1])
			// Only record the highest fps for each format+size combination
			found := false
			for i := range formats {
				if formats[i].pixFmt == currentPixFmt && formats[i].width == currentWidth && formats[i].height == currentHeight {
					if fps > formats[i].fps {
						formats[i].fps = fps
					}
					found = true
					break
				}
			}
			if !found {
				formats = append(formats, formatInfo{pixFmt: currentPixFmt, width: currentWidth, height: currentHeight, fps: fps})
			}
		}
	}

	if len(formats) == 0 {
		return nil
	}

	var modes []cameraMode
	for _, f := range formats {
		modes = append(modes, cameraMode{pixFmt: f.pixFmt, width: f.width, height: f.height, fps: f.fps})
	}

	best := PickBestCameraModeWithConfig(modes, cfg)
	matchKey, attachmentPath, identity := resolveStableCameraIdentity(name, device, location)

	return &DetectedCamera{
		Name:             name,
		Device:           device,
		Format:           "v4l2",
		PixFmt:           best.pixFmt,
		Size:             fmt.Sprintf("%dx%d", best.width, best.height),
		Fps:              best.fps,
		MatchKey:         matchKey,
		AttachmentPath:   attachmentPath,
		Identity:         identity,
		SupportedFormats: uniqueFormats(modes),
		modes:            modes,
	}
}

// detectCamerasWindows uses ffmpeg dshow to detect cameras
func detectCamerasWindows(cfg *ffmpeg.Config, progress ProbeProgressFunc, skip func(name, matchKey, attachmentPath string) bool) []DetectedCamera {
	path := config.GetFFmpegPath()
	if path == "" {
		path = "ffmpeg"
	}
	if progress != nil {
		progress(ProgressMsg(ProgListing, "DirectShow devices"))
	}

	// List devices
	cmd := CreateHiddenCmd(path, "-hide_banner", "-f", "dshow", "-list_devices", "true", "-i", "dummy")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.Run() // This always returns error because "dummy" isn't a real device

	type dshowDeviceEntry struct {
		name            string
		alternativeName string
	}
	var devices []dshowDeviceEntry
	lastVideoIndex := -1
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "(video)") {
			start := strings.Index(line, "\"")
			end := strings.LastIndex(line, "\"")
			if start != -1 && end != -1 && start != end {
				devices = append(devices, dshowDeviceEntry{name: line[start+1 : end]})
				lastVideoIndex = len(devices) - 1
			}
			continue
		}
		if strings.Contains(line, "Alternative name") && lastVideoIndex >= 0 {
			start := strings.Index(line, "\"")
			end := strings.LastIndex(line, "\"")
			if start != -1 && end != -1 && start != end {
				devices[lastVideoIndex].alternativeName = line[start+1 : end]
			}
			lastVideoIndex = -1
		}
	}

	var cameras []DetectedCamera
	for _, device := range devices {
		matchKey, attachmentPath, _ := resolveWindowsCameraIdentity(device.name, device.alternativeName)
		if skip != nil && skip(device.name, matchKey, attachmentPath) {
			continue
		}
		if progress != nil {
			progress(ProgressMsg(ProgLocalSource, device.name))
		}
		cam := probeDshowDevice(path, device.name, device.alternativeName, cfg)
		if cam != nil {
			cameras = append(cameras, *cam)
		}
	}
	return cameras
}

func resolveFFprobePath(ffmpegPath string) string {
	if envPath := strings.TrimSpace(os.Getenv("VIDEO_FFPROBE_PATH")); envPath != "" {
		return envPath
	}

	if ffmpegPath != "" {
		dir := filepath.Dir(ffmpegPath)
		name := "ffprobe"
		if runtime.GOOS == "windows" {
			name = "ffprobe.exe"
		}
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	name := "ffprobe"
	if runtime.GOOS == "windows" {
		name = "ffprobe.exe"
	}
	if sharedPath := config.FindSharedFFmpegExecutable(name); sharedPath != "" {
		return sharedPath
	}

	if runtime.GOOS == "windows" {
		return "ffprobe.exe"
	}
	return "ffprobe"
}

func verifyDshowH264Delivery(ffprobePath, name string) bool {
	cmd := CreateHiddenCmd(ffprobePath, "-hide_banner", "-f", "dshow", "-i", fmt.Sprintf("video=%s", name))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		logging.InfoLogger.Printf("ffprobe dshow probe for %s exited with: %v", name, err)
	}

	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.Contains(line, "Video:") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "video: h264") {
				return true
			}
			return false
		}
	}

	return false
}

// probeDshowDevice probes a single dshow device for its capabilities.
func probeDshowDevice(ffmpegPath, name, alternativeName string, cfg *ffmpeg.Config) *DetectedCamera {
	cmd := CreateHiddenCmd(ffmpegPath, "-hide_banner", "-f", "dshow", "-list_options", "true", "-i", fmt.Sprintf("video=%s", name))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.Run() // Always returns error

	matchKey, attachmentPath, identity := resolveWindowsCameraIdentity(name, alternativeName)

	// Parse output for pixel formats, sizes, fps
	// Lines like: "  pixel_format=mjpeg  min s=1920x1080 fps=30 ..."
	//         or: "  vcodec=mjpeg  min s=1920x1080 fps=30 ..."
	type optionInfo struct {
		pixFmt string
		width  int
		height int
		fps    int
	}

	var options []optionInfo
	// dshow -list_options lines have the form:
	//   vcodec=h264  min s=1920x1080 fps=15 max s=1920x1080 fps=60.0002
	// We want the MAX size and fps from each line.
	maxSizeFpsRe := regexp.MustCompile(`max\s+s=(\d+)x(\d+)\s+fps=([0-9.]+)`)
	// Fallback for lines without min/max (just a single s=... fps=...)
	fallbackSizeFpsRe := regexp.MustCompile(`s=(\d+)x(\d+)\s+fps=([0-9.]+)`)

	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		line := scanner.Text()
		// Determine pixel format from the line
		var pixFmt string
		// Compressed formats
		if strings.Contains(line, "pixel_format=mjpeg") || strings.Contains(line, "vcodec=mjpeg") {
			pixFmt = "mjpeg"
		} else if strings.Contains(line, "vcodec=h264") {
			pixFmt = "h264"
			// Raw formats - no decode needed, just encode
		} else if strings.Contains(line, "pixel_format=yuyv422") || strings.Contains(line, "pixel_format=yuyv") {
			pixFmt = "yuyv422"
		} else if strings.Contains(line, "pixel_format=nv12") {
			pixFmt = "nv12"
		} else if strings.Contains(line, "pixel_format=rgb24") {
			pixFmt = "rgb24"
		} else if strings.Contains(line, "pixel_format=bgr24") {
			pixFmt = "bgr24"
		} else if strings.Contains(line, "pixel_format=uyvy422") {
			pixFmt = "uyvy422"
		} else if strings.Contains(line, "pixel_format=") {
			// Catch other raw formats generically
			re := regexp.MustCompile(`pixel_format=(\w+)`)
			if m := re.FindStringSubmatch(line); m != nil {
				pixFmt = m[1]
			}
		} else {
			continue
		}

		// Try to match the "max" portion first; fall back to first s=...fps=...
		m := maxSizeFpsRe.FindStringSubmatch(line)
		if m == nil {
			m = fallbackSizeFpsRe.FindStringSubmatch(line)
		}
		if m == nil {
			continue
		}

		w := atoi(m[1])
		h := atoi(m[2])
		fps := parseFps(m[3])
		options = append(options, optionInfo{pixFmt: pixFmt, width: w, height: h, fps: fps})
	}

	if len(options) == 0 {
		// Camera found but couldn't parse formats; add with defaults
		return &DetectedCamera{
			Name:           name,
			Device:         name,
			Format:         "dshow",
			PixFmt:         "unknown",
			Size:           "1280x720",
			Fps:            30,
			MatchKey:       matchKey,
			AttachmentPath: attachmentPath,
			Identity:       identity,
		}
	}

	var modes []cameraMode
	for _, o := range options {
		modes = append(modes, cameraMode{pixFmt: o.pixFmt, width: o.width, height: o.height, fps: o.fps})
	}

	effectiveModes := modes
	best := PickBestCameraModeWithConfig(modes, cfg)
	if best.pixFmt == "h264" {
		ffprobePath := resolveFFprobePath(ffmpegPath)
		if !verifyDshowH264Delivery(ffprobePath, name) {
			var nonH264Modes []cameraMode
			for _, mode := range modes {
				if mode.pixFmt != "h264" {
					nonH264Modes = append(nonH264Modes, mode)
				}
			}
			if len(nonH264Modes) > 0 {
				effectiveModes = nonH264Modes
				fallback := PickBestCameraModeWithConfig(nonH264Modes, cfg)
				logging.InfoLogger.Printf("Camera %s advertised h264 on dshow but probe did not confirm it; falling back to %s %dx%d@%dfps", name, fallback.pixFmt, fallback.width, fallback.height, fallback.fps)
				best = fallback
			}
		}
	}

	return &DetectedCamera{
		Name:             name,
		Device:           name,
		Format:           "dshow",
		PixFmt:           best.pixFmt,
		Size:             fmt.Sprintf("%dx%d", best.width, best.height),
		Fps:              best.fps,
		MatchKey:         matchKey,
		AttachmentPath:   attachmentPath,
		Identity:         identity,
		SupportedFormats: uniqueFormats(effectiveModes),
		modes:            effectiveModes,
	}
}

func resolveWindowsCameraIdentity(name, alternativeName string) (string, string, string) {
	trimmedAlt := strings.TrimSpace(alternativeName)
	if trimmedAlt != "" {
		return "dshow-alt:" + strings.ToLower(trimmedAlt), trimmedAlt, trimmedAlt
	}
	weakName := strings.ToLower(strings.TrimSpace(name))
	if weakName == "" {
		weakName = "unknown"
	}
	return "dshow:" + weakName, "", name
}

func resolveAVFoundationCameraIdentity(name string) (string, string, string) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		trimmedName = "unknown"
	}
	return "avfoundation:" + strings.ToLower(trimmedName), "", trimmedName
}

func resolveStableCameraIdentity(name, device, location string) (string, string, string) {
	if linkName := resolveV4LLinkBase("/dev/v4l/by-path", device); linkName != "" {
		return "by-path:" + strings.ToLower(strings.TrimSpace(linkName)), linkName, linkName
	}
	if trimmedLocation := strings.TrimSpace(location); trimmedLocation != "" {
		return "usb:" + strings.ToLower(trimmedLocation), trimmedLocation, trimmedLocation
	}
	if linkName := resolveV4LLinkBase("/dev/v4l/by-id", device); linkName != "" {
		return "by-id:" + strings.ToLower(strings.TrimSpace(linkName)), linkName, linkName
	}
	weakName := strings.ToLower(strings.TrimSpace(name))
	if weakName == "" {
		weakName = strings.ToLower(strings.TrimSpace(device))
	}
	return "device:" + weakName, "", name
}

func resolveV4LLinkBase(dirPath, device string) string {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return ""
	}
	resolvedDevice, err := filepath.EvalSymlinks(device)
	if err != nil {
		resolvedDevice = device
	}
	for _, entry := range entries {
		linkPath := filepath.Join(dirPath, entry.Name())
		resolvedLink, err := filepath.EvalSymlinks(linkPath)
		if err != nil {
			continue
		}
		if filepath.Clean(resolvedLink) == filepath.Clean(resolvedDevice) {
			return entry.Name()
		}
	}
	return ""
}

// PickBestCameraModeWithConfig selects the best camera mode using ffmpeg.toml priorities.
func PickBestCameraModeWithConfig(allModes []cameraMode, cfg *ffmpeg.Config) cameraMode {
	if len(allModes) == 0 {
		return cameraMode{pixFmt: "unknown", width: 1280, height: 720, fps: 30}
	}

	const maxWidth = 1920
	const maxHeight = 1080

	var candidates []cameraMode
	for _, mode := range allModes {
		if mode.width <= maxWidth && mode.height <= maxHeight {
			candidates = append(candidates, mode)
		}
	}
	if len(candidates) == 0 {
		candidates = allModes
	}

	if config.GetMjpeg720pOnly() {
		filtered := filterMjpegToMax720p(candidates)
		if len(filtered) > 0 {
			candidates = filtered
		}
	}

	best := candidates[0]
	for _, mode := range candidates[1:] {
		if modePreferredOverWithConfig(mode, best, cfg) {
			best = mode
		}
	}

	return best
}

func filterMjpegToMax720p(modes []cameraMode) []cameraMode {
	var filtered []cameraMode
	for _, mode := range modes {
		if mode.pixFmt == "mjpeg" && (mode.width > 1280 || mode.height > 720) {
			continue
		}
		filtered = append(filtered, mode)
	}
	return filtered
}

func modePreferredOverWithConfig(candidate, current cameraMode, cfg *ffmpeg.Config) bool {
	var candidateFormat, currentFormat int
	var candidateProfile, currentProfile int

	if cfg != nil {
		candidateFormat = cfg.FormatPriorityValue(candidate.pixFmt)
		currentFormat = cfg.FormatPriorityValue(current.pixFmt)
		candidateProfile = cfg.ProfilePriorityValue(candidate.width, candidate.height, candidate.fps)
		currentProfile = cfg.ProfilePriorityValue(current.width, current.height, current.fps)
	}

	if candidateFormat != currentFormat {
		return candidateFormat > currentFormat
	}

	if candidateProfile != currentProfile {
		return candidateProfile > currentProfile
	}

	if candidate.fps != current.fps {
		return candidate.fps > current.fps
	}

	candidatePixels := candidate.width * candidate.height
	currentPixels := current.width * current.height
	return candidatePixels > currentPixels
}

// writeAutoConfig generates auto.toml from detected hardware using ffmpeg.toml settings.
func writeAutoConfig(outputPath string, cameras []DetectedCamera, encoders []HwEncoder, cfg *ffmpeg.Config) error {
	if cfg == nil {
		return fmt.Errorf("ffmpeg config is required to write auto.toml")
	}

	// replays app behavior for auto.toml generation
	// - input
	//   - input format is obtained by probing cameras
	//   - format preference is defined in [cameras] priorities
	// - recording output in auto.toml
	//   - H.264 input is copied
	//   - MJPEG input is copied with recode=true (no live encoding; recoding happens during trimming)
	//   - raw input uses encoder block settings to produce H.264 (or software fallback when no hardware encoder is available)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}

	// Backup existing auto.toml with versioned suffix (.1, .2, .3, ...)
	if _, err := os.Stat(outputPath); err == nil {
		for i := 1; ; i++ {
			backup := fmt.Sprintf("%s.%d", outputPath, i)
			if _, err := os.Stat(backup); os.IsNotExist(err) {
				if err := os.Rename(outputPath, backup); err != nil {
					logging.ErrorLogger.Printf("Failed to backup auto.toml to %s: %v", backup, err)
				} else {
					logging.InfoLogger.Printf("Backed up existing auto.toml to %s", backup)
				}
				break
			}
		}
	}

	var buf bytes.Buffer

	buf.WriteString("# Auto-detected camera configuration\n")
	buf.WriteString("# Generated by hardware auto-detection\n")
	buf.WriteString("# Keep this file as the baseline; add only manual overrides or extra sources to config.toml\n\n")
	buf.WriteString("# Camera source loading in replays:\n")
	buf.WriteString("# 1) [mpeg-ts] section in config.toml (when enabled = true)\n")
	buf.WriteString("# 2) when mpeg-ts is disabled, auto.toml and config.toml camera sections are merged\n")
	buf.WriteString("#    - auto.toml [cameraN] sections load first\n")
	buf.WriteString("#    - config.toml [windows*]/[linux*] sections append additional sources\n")
	buf.WriteString("#    - matching manual sources override the same source from auto.toml\n")
	buf.WriteString("# If no source is configured by any of the above, replays starts with no camera sources.\n")
	buf.WriteString("#\n")
	buf.WriteString("# Recommended workflow:\n")
	buf.WriteString("# - leave auto.toml as the generated baseline\n")
	buf.WriteString("# - keep config.toml camera sections only for manual additions or explicit overrides\n")
	buf.WriteString("# - do not copy the full auto.toml into config.toml unless you want to take over everything manually\n")
	buf.WriteString("#\n")
	buf.WriteString("# Note: this precedence applies only to camera source definitions.\n")
	buf.WriteString("# Other application settings still come from config.toml.\n\n")

	// Pick the best hardware encoder (prefer GPU over software)
	bestEncoder := PickBestEncoder(encoders)

	softwareFallback := strings.TrimSpace(cfg.Software.OutputParameters)
	if softwareFallback == "" {
		return fmt.Errorf("ffmpeg config is missing software outputParameters")
	}

	for i, cam := range cameras {
		sectionName := fmt.Sprintf("camera%d", i+1)
		buf.WriteString(fmt.Sprintf("[%s]\n", sectionName))
		buf.WriteString(fmt.Sprintf("    description = \"[%s] %s (%s, %s)\"\n", runtime.GOOS, cam.Name, cam.PixFmt, cam.Size))
		buf.WriteString("    enabled = true\n")

		if cam.Format == "dshow" {
			buf.WriteString(fmt.Sprintf("    camera = 'video=%s'\n", cam.Device))
		} else if cam.Format == "avfoundation" {
			buf.WriteString(fmt.Sprintf("    camera = '%s:none'\n", cam.Device))
		} else {
			buf.WriteString(fmt.Sprintf("    camera = '%s'\n", cam.Device))
		}

		buf.WriteString(fmt.Sprintf("    format = '%s'\n", cam.Format))
		buf.WriteString(fmt.Sprintf("    size = \"%s\"\n", cam.Size))
		buf.WriteString(fmt.Sprintf("    fps = %d\n", cam.Fps))
		buf.WriteString("\n")

		// Determine if format is compressed (needs decode) or raw (no decode needed)
		isCompressed := cam.PixFmt == "mjpeg" || cam.PixFmt == "h264"

		if isCompressed {
			// Compressed formats: copy during recording
			switch cam.PixFmt {
			case "mjpeg":
				// MJPEG: copy during recording, recode on trim
				if cam.Format == "dshow" {
					buf.WriteString("    inputParameters = '-vcodec mjpeg -rtbufsize 512M -thread_queue_size 4096'\n")
				} else {
					buf.WriteString("    inputParameters = '-input_format mjpeg -rtbufsize 512M -thread_queue_size 4096'\n")
				}
				buf.WriteString("    outputParameters = '-vcodec copy -an'\n")
				buf.WriteString("    recode = true\n")
			case "h264":
				// H.264: copy during recording, no recode
				buf.WriteString("    inputParameters = '-rtbufsize 512M -thread_queue_size 4096'\n")
				buf.WriteString("    outputParameters = '-c:v copy -an'\n")
				buf.WriteString("    recode = false\n")
			}
		} else {
			// Raw formats (yuyv422, nv12, rgb24, etc.): no decode needed, just encode
			// Use camera's fps for GOP size and output frame rate
			fpsParams := fmt.Sprintf("-g %d -keyint_min %d -r %d -vsync cfr -an", cam.Fps, cam.Fps, cam.Fps)

			// Start with encoder init params (e.g. -init_hw_device qsv=hw …) if present,
			// then add buffer/queue settings that are not already provided by the encoder.
			var initPart string
			if bestEncoder != nil && bestEncoder.InputParameters != "" {
				initPart = bestEncoder.InputParameters
			}
			baseBuf := ""
			if initPart == "" || !strings.Contains(initPart, "rtbufsize") {
				baseBuf = "-rtbufsize 512M"
			}
			baseQueue := ""
			if initPart == "" || !strings.Contains(initPart, "thread_queue_size") {
				baseQueue = "-thread_queue_size 4096"
			}

			// Specify the pixel format for proper raw input handling
			var fmtFlag string
			if cam.Format == "dshow" || cam.Format == "avfoundation" {
				fmtFlag = fmt.Sprintf("-pixel_format %s", cam.PixFmt)
			} else {
				fmtFlag = fmt.Sprintf("-input_format %s", cam.PixFmt)
			}

			// Assemble: [encoder init] [pixel format] [rtbufsize] [thread_queue_size]
			parts := []string{}
			if initPart != "" {
				parts = append(parts, initPart)
			}
			parts = append(parts, fmtFlag)
			if baseBuf != "" {
				parts = append(parts, baseBuf)
			}
			if baseQueue != "" {
				parts = append(parts, baseQueue)
			}
			inputParams := strings.Join(parts, " ")

			buf.WriteString(fmt.Sprintf("    inputParameters = '%s'\n", inputParams))

			if bestEncoder != nil {
				vfPart := ""
				if strings.TrimSpace(bestEncoder.VideoFilter) != "" {
					vfPart = fmt.Sprintf("-vf %s ", strings.TrimSpace(bestEncoder.VideoFilter))
				}
				buf.WriteString(fmt.Sprintf("    outputParameters = '%s%s %s'\n", vfPart, bestEncoder.OutputParameters, fpsParams))
			} else {
				// Software encoder settings come from ffmpeg.toml.
				buf.WriteString(fmt.Sprintf("    outputParameters = '%s %s'\n", softwareFallback, fpsParams))
			}
			buf.WriteString("    recode = false\n")
		}
		buf.WriteString("\n")
	}

	// If no local cameras were detected, add UDP stream templates
	if len(cameras) == 0 {
		buf.WriteString("# No local cameras detected.\n")
		buf.WriteString("# Sample UDP stream configurations for cameras attached to other machines on the network.\n\n")
		for i := 1; i <= 3; i++ {
			buf.WriteString(fmt.Sprintf("[camera%d]\n", i))
			buf.WriteString(fmt.Sprintf("    description = \"[%s] UDP Stream from Camera %d\"\n", runtime.GOOS, i))
			buf.WriteString("    enabled = false\n")
			buf.WriteString(fmt.Sprintf("    camera = 'udp://239.255.0.1:900%d'\n", i))
			buf.WriteString("    format = 'mpegts'\n")
			buf.WriteString("    size = \"1920x1080\"\n")
			buf.WriteString("    fps = 60\n")
			buf.WriteString("\n")
			buf.WriteString("    # Input parameters: none needed - reading from multicast UDP stream\n")
			buf.WriteString("    inputParameters = ''\n")
			buf.WriteString("\n")
			buf.WriteString("    # Output parameters: copy H.264 stream directly (already encoded by sender)\n")
			buf.WriteString("    outputParameters = '-c:v copy -an'\n")
			buf.WriteString("\n")
			buf.WriteString("    # No recoding needed - stream is already H.264\n")
			buf.WriteString("    recode = false\n\n")
		}
	}

	// Append detected encoders as reference
	buf.WriteString("# =======================================================\n")
	buf.WriteString("# Detected H.264 encoders on this system\n")
	buf.WriteString("# =======================================================\n")
	if len(encoders) == 0 {
		buf.WriteString("# None found - only software encoding (libx264) is available\n")
	}
	for _, enc := range encoders {
		buf.WriteString(fmt.Sprintf("# %s - %s\n", enc.Name, enc.Description))
	}
	buf.WriteString("# libx264 - Software encoder (always available)\n")

	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return err
	}

	logging.InfoLogger.Printf("Wrote auto-detected config to: %s", outputPath)
	return nil
}

// PickBestEncoder selects the first verified encoder, preserving ffmpeg.toml preference order.
func PickBestEncoder(encoders []HwEncoder) *HwEncoder {
	if len(encoders) > 0 {
		return &encoders[0]
	}
	return nil
}

// buildSummary creates a human-readable summary of detected hardware
func buildSummary(cameras []DetectedCamera, encoders []HwEncoder, outputPath string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Configuration written to:\n%s\n\n", outputPath))

	sb.WriteString("Cameras detected:\n")
	for i, cam := range cameras {
		sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, cam.Name))
		sb.WriteString(fmt.Sprintf("     Device: %s\n", cam.Device))
		sb.WriteString(fmt.Sprintf("     Format: %s  Size: %s  FPS: %d\n", cam.PixFmt, cam.Size, cam.Fps))
	}

	sb.WriteString("\nH.264 encoders available:\n")
	if len(encoders) == 0 {
		sb.WriteString("  (none - software encoding only)\n")
	}
	for _, enc := range encoders {
		sb.WriteString(fmt.Sprintf("  - %s (%s)\n", enc.Name, enc.Description))
	}
	sb.WriteString("  - libx264 (software, always available)\n")

	sb.WriteString("\nUse auto.toml as the baseline and add only manual overrides or extra sources to config.toml")
	return sb.String()
}

// showAutoDetectResults shows the detection summary in a dialog
func showAutoDetectResults(summary, _ string, window fyne.Window) {
	textArea := widget.NewMultiLineEntry()
	textArea.SetText(summary)
	textArea.Wrapping = fyne.TextWrapWord
	textArea.SetMinRowsVisible(12)

	scrollable := container.NewScroll(textArea)
	scrollable.SetMinSize(fyne.NewSize(500, 300))

	d := dialog.NewCustom("Auto-Detect Results", "Close", scrollable, window)
	d.Resize(fyne.NewSize(550, 400))
	d.Show()
}

// atoi is a simple string-to-int helper that returns 0 on error
func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

// parseFps parses a possibly-decimal fps string (e.g. "60.0002", "29.97")
// and returns the nearest integer, rounding to handle NTSC-style fractional values.
func parseFps(s string) int {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return atoi(s)
	}
	return int(f + 0.5)
}
