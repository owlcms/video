package cameras

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"github.com/owlcms/video/internal/config"
	camerascfg "github.com/owlcms/video/internal/config/cameras"
	"github.com/owlcms/video/internal/logging"
	"github.com/owlcms/video/internal/recording"
)

type sourceSpec struct {
	Key              string
	AttachmentPath   string
	SourceType       string
	Name             string
	ShortID          string
	Summary          string
	Enabled          bool
	Detected         bool
	MonitoringOn     bool
	OutputPort       int
	Transport        string
	ProbeDirty       bool
	DirtyReasons     []string
	SupportedFormats []string
	PreferredFormat  string
	Camera           recording.DetectedCamera
	RTSP             camerascfg.RTSPSource
}

type sourceInventory struct {
	USB                 []sourceSpec
	RTSP                []sourceSpec
	Active              []sourceSpec
	ConfigChanged       bool
	Status              string
	Errors              []string
	PendingVerification []string
	Encoder             *recording.HwEncoder
}

var detectUSBCamerasWithConfigAndProgress = recording.DetectCamerasWithConfigAndProgressFiltered

func currentStartPort() int {
	if camerasConfig != nil && camerasConfig.Unicast.Enabled {
		return camerasConfig.Unicast.StartPort
	}
	if camerasConfig != nil {
		return camerasConfig.Multicast.StartPort
	}
	return 9001
}

func buildSourceInventoryWithProgress(progress func(string)) sourceInventory {
	start := time.Now()
	logging.InfoLogger.Printf("Source inventory build started")
	startPort := currentStartPort()
	usedPorts := make(map[int]struct{})
	usedShortIDs := make(map[string]struct{})

	if progress != nil {
		progress(recording.ProgressMsg(recording.ProgCheckingRTSP, ""))
	}
	rtspSpecs := buildRTSPSourcesWithProgress(startPort, usedPorts, usedShortIDs, progress)
	logging.InfoLogger.Printf("Source inventory RTSP phase completed in %s with %d source(s)", time.Since(start), len(rtspSpecs))
	usbStart := time.Now()
	if progress != nil {
		progress(recording.ProgressMsg(recording.ProgCheckingLocal, ""))
	}
	usbSpecs, usbConfigChanged := buildUSBSourcesWithProgress(startPort, usedPorts, usedShortIDs, progress)
	logging.InfoLogger.Printf("Source inventory USB phase completed in %s with %d source(s)", time.Since(usbStart), len(usbSpecs))
	encoderStart := time.Now()
	if progress != nil {
		progress(recording.ProgressMsg(recording.ProgCheckingEncoders, ""))
	}
	encoders := recording.DetectEncodersWithConfigAndProgress(ffmpegConfig, progress)
	logging.InfoLogger.Printf("Source inventory encoder phase completed in %s with %d candidate(s)", time.Since(encoderStart), len(encoders))
	inv := assembleSourceInventory(usbSpecs, rtspSpecs, recording.PickBestEncoder(encoders))
	inv.ConfigChanged = usbConfigChanged
	logging.InfoLogger.Printf("Source inventory build completed in %s: usb=%d rtsp=%d active=%d errors=%d pending=%d",
		time.Since(start), len(inv.USB), len(inv.RTSP), len(inv.Active), len(inv.Errors), len(inv.PendingVerification))
	if progress != nil {
		progress(recording.ProgressMsg(recording.ProgInventoryReady, fmt.Sprintf("local=%d rtsp=%d active=%d", len(inv.USB), len(inv.RTSP), len(inv.Active))))
	}
	return inv
}

func buildCachedSourceInventory(previous sourceInventory, encoder *recording.HwEncoder) sourceInventory {
	startPort := currentStartPort()
	usedPorts := make(map[int]struct{})
	usedShortIDs := make(map[string]struct{})

	rtspSpecs := buildRTSPSources(startPort, usedPorts, usedShortIDs)
	usbSpecs := buildUSBSourcesFromDetected(cachedDetectedCameras(previous.USB), startPort, usedPorts, usedShortIDs, nil)
	return assembleSourceInventory(usbSpecs, rtspSpecs, encoder)
}

func assembleSourceInventory(usbSpecs, rtspSpecs []sourceSpec, encoder *recording.HwEncoder) sourceInventory {

	active := make([]sourceSpec, 0, len(rtspSpecs)+len(usbSpecs))
	enabledCount := 0
	for _, src := range usbSpecs {
		if src.Enabled && src.Detected {
			enabledCount++
		}
		if src.Enabled && src.Detected && src.MonitoringOn {
			active = append(active, src)
		}
	}
	for _, src := range rtspSpecs {
		if src.Enabled && strings.TrimSpace(src.RTSP.RTSPURL) != "" {
			enabledCount++
		}
		if src.Enabled && src.MonitoringOn && strings.TrimSpace(src.RTSP.RTSPURL) != "" {
			active = append(active, src)
		}
	}

	sort.Slice(active, func(i, j int) bool {
		if active[i].OutputPort != active[j].OutputPort {
			return active[i].OutputPort < active[j].OutputPort
		}
		return active[i].Name < active[j].Name
	})

	inv := sourceInventory{
		USB:                 usbSpecs,
		RTSP:                rtspSpecs,
		Active:              active,
		PendingVerification: collectPendingVerification(append(append([]sourceSpec{}, usbSpecs...), rtspSpecs...)),
		Encoder:             encoder,
	}

	if conflicts := detectPortConflicts(active); len(conflicts) > 0 {
		inv.Errors = append(inv.Errors, conflicts...)
		inv.Status = "Port conflicts detected. Resolve source output ports before streaming."
		inv.Active = nil
		return inv
	}

	switch {
	case enabledCount > 0 && len(inv.Active) == 0:
		inv.Status = "No sources are streaming. Use the Start buttons below to begin streaming."
	case len(inv.Active) == 0 && len(inv.RTSP) > 0:
		inv.Status = "No active sources. Enable an RTSP source or connect a camera."
	case len(inv.Active) == 0:
		inv.Status = "No cameras detected. Connect a camera or add an RTSP source."
	default:
		inv.Status = fmt.Sprintf("%d source(s) ready.", len(inv.Active))
	}

	return inv
}

func buildUSBSourcesWithProgress(startPort int, usedPorts map[int]struct{}, usedShortIDs map[string]struct{}, progress func(string)) ([]sourceSpec, bool) {
	cameras := detectUSBCamerasWithConfigAndProgress(ffmpegConfig, progress, nil)
	sources := buildUSBSourcesFromDetected(cameras, startPort, usedPorts, usedShortIDs, progress)
	changed := disableAbsentUSBAssignments(sources)
	if syncDetectedUSBAssignments(sources) {
		changed = true
	}
	return sources, changed
}

// syncDetectedUSBAssignments records every detected USB source in the camera
// configuration so that the persisted file lists what is actually available.
// Other modules (and the /api/cameras/config endpoint) read the saved
// configuration, so detected sources must not stay in memory only.
func syncDetectedUSBAssignments(sources []sourceSpec) bool {
	if camerasConfig == nil {
		return false
	}

	assignmentsByAttachment, assignmentsByMatchKey := deviceAssignmentMaps()
	changed := false
	missing := make([]sourceSpec, 0, len(sources))

	for _, source := range sources {
		if !source.Detected {
			continue
		}
		assignment := matchingDeviceAssignment(source.Camera, assignmentsByAttachment, assignmentsByMatchKey)
		if assignment == nil {
			missing = append(missing, source)
			continue
		}
		if strings.TrimSpace(assignment.Name) == "" && strings.TrimSpace(source.Name) != "" {
			assignment.Name = source.Name
			changed = true
		}
		if strings.TrimSpace(assignment.ShortID) == "" && strings.TrimSpace(source.ShortID) != "" {
			assignment.ShortID = source.ShortID
			changed = true
		}
		if assignment.OutputPort <= 0 && source.OutputPort > 0 {
			assignment.OutputPort = source.OutputPort
			changed = true
		}
	}

	// Appending reallocates the slice, so add new assignments only after the
	// pointers taken above are no longer used.
	for _, source := range missing {
		camerasConfig.DeviceAssignments = append(camerasConfig.DeviceAssignments, camerascfg.DeviceAssignment{
			AttachmentPath:   source.AttachmentPath,
			MatchKey:         source.Key,
			Name:             source.Name,
			ShortID:          source.ShortID,
			OutputPort:       source.OutputPort,
			ProbePixelFormat: source.Camera.PixFmt,
			ProbeSize:        source.Camera.Size,
			ProbeFPS:         source.Camera.Fps,
			ProbeFormats:     append([]string(nil), source.Camera.SupportedFormats...),
		})
		logging.InfoLogger.Printf("Recorded detected USB source %q (shortID=%s, port=%d, attachmentPath=%s)",
			source.Name, source.ShortID, source.OutputPort, source.AttachmentPath)
		changed = true
	}

	return changed
}

func buildUSBSourcesFromDetected(cameras []recording.DetectedCamera, startPort int, usedPorts map[int]struct{}, usedShortIDs map[string]struct{}, progress func(string)) []sourceSpec {
	assignmentsByAttachment, assignmentsByMatchKey := deviceAssignmentMaps()
	for i := range camerasConfig.DeviceAssignments {
		assignment := &camerasConfig.DeviceAssignments[i]
		if assignment.OutputPort > 0 {
			usedPorts[assignment.OutputPort] = struct{}{}
		}
		if strings.TrimSpace(assignment.ShortID) != "" {
			usedShortIDs[strings.ToUpper(strings.TrimSpace(assignment.ShortID))] = struct{}{}
		}
	}

	var filtered []recording.DetectedCamera
	for _, cam := range cameras {
		if !camerasConfig.Cameras.IncludeAll && isIntegratedCamera(cam) {
			if progress != nil {
				progress(recording.ProgressMsg(recording.ProgSkippedSource, cam.Name))
			}
			continue
		}
		filtered = append(filtered, cam)
	}

	sort.Slice(filtered, func(i, j int) bool {
		return ffmpegConfig.FormatPriorityValue(filtered[i].PixFmt) > ffmpegConfig.FormatPriorityValue(filtered[j].PixFmt)
	})

	sources := make([]sourceSpec, 0, len(filtered))
	matchedAssignments := make(map[*camerascfg.DeviceAssignment]struct{}, len(filtered))
	for _, cam := range filtered {
		assignment := matchingDeviceAssignment(cam, assignmentsByAttachment, assignmentsByMatchKey)
		if assignment != nil {
			matchedAssignments[assignment] = struct{}{}
			logging.InfoLogger.Printf("USB source %q matched assignment (disabled=%v, attachmentPath=%s)", cam.Name, assignment.Disabled, assignment.AttachmentPath)
		} else {
			logging.InfoLogger.Printf("USB source %q has no matching assignment (attachmentPath=%s, matchKey=%s) — defaults to enabled", cam.Name, cam.AttachmentPath, cam.MatchKey)
		}

		// Apply preferred pixel format before selecting mode
		if assignment != nil && assignment.PreferredPixelFormat != "" {
			cam.RepickForFormat(assignment.PreferredPixelFormat, ffmpegConfig)
		}
		if assignment != nil {
			assignment.ProbePixelFormat = cam.PixFmt
			assignment.ProbeSize = cam.Size
			assignment.ProbeFPS = cam.Fps
			assignment.ProbeFormats = append([]string(nil), cam.SupportedFormats...)
			assignment.DirtyReasons = removeDirtyReason(assignment.DirtyReasons, "probe")
		}

		name := ""
		if assignment != nil {
			name = strings.TrimSpace(assignment.Name)
		}
		if name == "" {
			name = cam.Name
		}
		shortID := ""
		if assignment != nil {
			shortID = strings.TrimSpace(assignment.ShortID)
		}
		if shortID == "" {
			shortID = nextShortID("C", usedShortIDs)
		} else {
			usedShortIDs[strings.ToUpper(shortID)] = struct{}{}
		}
		port := 0
		if assignment != nil {
			port = assignment.OutputPort
		}
		if port <= 0 {
			port = nextAvailablePort(startPort, usedPorts)
		}
		usedPorts[port] = struct{}{}

		supportedFormats := append([]string(nil), cam.SupportedFormats...)
		dirtyReasons := []string(nil)
		preferredFormat := ""
		enabled := true
		if assignment != nil {
			if len(supportedFormats) == 0 && len(assignment.ProbeFormats) > 0 {
				supportedFormats = append([]string(nil), assignment.ProbeFormats...)
			}
			dirtyReasons = normalizeSourceDirtyReasons(assignment.DirtyReasons)
			preferredFormat = assignment.PreferredPixelFormat
			enabled = !assignment.Disabled
			if !enabled {
				dirtyReasons = removeDirtyReason(dirtyReasons, "restart")
			}
		}
		monitoringOn := true
		if assignment != nil && assignment.On != nil {
			monitoringOn = *assignment.On
		}

		cam.Name = name
		sources = append(sources, sourceSpec{
			Key:              cam.MatchKey,
			AttachmentPath:   cam.AttachmentPath,
			SourceType:       "usb",
			Name:             name,
			ShortID:          shortID,
			Summary:          summarizeUSBIdentity(cam),
			Enabled:          enabled,
			Detected:         true,
			MonitoringOn:     monitoringOn,
			OutputPort:       port,
			ProbeDirty:       hasDirtyReason(dirtyReasons, "probe"),
			DirtyReasons:     dirtyReasons,
			SupportedFormats: supportedFormats,
			PreferredFormat:  preferredFormat,
			Camera:           cam,
		})
		if progress != nil {
			progress(recording.ProgressMsg(recording.ProgDetectedSource, name))
		}
	}

	for i := range camerasConfig.DeviceAssignments {
		assignment := &camerasConfig.DeviceAssignments[i]
		if strings.TrimSpace(assignment.MatchKey) == "" && strings.TrimSpace(assignment.AttachmentPath) == "" {
			continue
		}
		if _, matched := matchedAssignments[assignment]; matched {
			continue
		}
		sources = append(sources, storedUSBSourceSpec(assignment, startPort, usedPorts, usedShortIDs))
	}

	return sources
}

func disableAbsentUSBAssignments(sources []sourceSpec) bool {
	assignmentsByAttachment, assignmentsByMatchKey := deviceAssignmentMaps()
	changed := false
	for _, source := range sources {
		if source.Detected {
			continue
		}
		assignment := matchingDeviceAssignment(source.Camera, assignmentsByAttachment, assignmentsByMatchKey)
		if assignment == nil || assignment.Disabled {
			continue
		}
		assignment.Disabled = true
		assignment.DirtyReasons = removeDirtyReason(assignment.DirtyReasons, "restart")
		logging.InfoLogger.Printf("Disabled absent USB source %q (attachmentPath=%s, matchKey=%s)", source.Name, source.AttachmentPath, source.Key)
		changed = true
	}
	return changed
}

func cachedDetectedCameras(specs []sourceSpec) []recording.DetectedCamera {
	cameras := make([]recording.DetectedCamera, 0, len(specs))
	for _, spec := range specs {
		if !spec.Detected {
			continue
		}
		cameras = append(cameras, spec.Camera)
	}
	return cameras
}

func deviceAssignmentMaps() (map[string]*camerascfg.DeviceAssignment, map[string]*camerascfg.DeviceAssignment) {
	assignmentsByAttachment := make(map[string]*camerascfg.DeviceAssignment, len(camerasConfig.DeviceAssignments))
	assignmentsByMatchKey := make(map[string]*camerascfg.DeviceAssignment, len(camerasConfig.DeviceAssignments))
	if camerasConfig == nil {
		return assignmentsByAttachment, assignmentsByMatchKey
	}
	for i := range camerasConfig.DeviceAssignments {
		assignment := &camerasConfig.DeviceAssignments[i]
		if attachmentPath := strings.TrimSpace(assignment.AttachmentPath); attachmentPath != "" {
			assignmentsByAttachment[attachmentPath] = assignment
		}
		if matchKey := strings.TrimSpace(assignment.MatchKey); matchKey != "" {
			assignmentsByMatchKey[matchKey] = assignment
		}
	}
	return assignmentsByAttachment, assignmentsByMatchKey
}

func matchingDeviceAssignment(cam recording.DetectedCamera, assignmentsByAttachment, assignmentsByMatchKey map[string]*camerascfg.DeviceAssignment) *camerascfg.DeviceAssignment {
	return matchingDeviceAssignmentIdentity(cam.Name, cam.MatchKey, cam.AttachmentPath, assignmentsByAttachment, assignmentsByMatchKey)
}

func matchingDeviceAssignmentIdentity(name, matchKey, attachmentPath string, assignmentsByAttachment, assignmentsByMatchKey map[string]*camerascfg.DeviceAssignment) *camerascfg.DeviceAssignment {
	if attachmentPath = strings.TrimSpace(attachmentPath); attachmentPath != "" {
		if assignment, ok := assignmentsByAttachment[attachmentPath]; ok {
			return assignment
		}
	}
	if matchKey = strings.TrimSpace(matchKey); matchKey != "" {
		if assignment, ok := assignmentsByMatchKey[matchKey]; ok {
			return assignment
		}
	}
	legacyWindowsKey := "dshow:" + strings.ToLower(strings.TrimSpace(name))
	if assignment, ok := assignmentsByMatchKey[legacyWindowsKey]; ok {
		return assignment
	}
	return nil
}

func storedUSBSourceSpec(assignment *camerascfg.DeviceAssignment, startPort int, usedPorts map[int]struct{}, usedShortIDs map[string]struct{}) sourceSpec {
	cam := storedUSBCameraFromAssignment(assignment)
	name := strings.TrimSpace(assignment.Name)
	if name == "" {
		name = cam.Name
	}
	shortID := strings.TrimSpace(assignment.ShortID)
	if shortID == "" {
		shortID = nextShortID("C", usedShortIDs)
	} else {
		usedShortIDs[strings.ToUpper(shortID)] = struct{}{}
	}
	port := assignment.OutputPort
	if port <= 0 {
		port = nextAvailablePort(startPort, usedPorts)
	}
	usedPorts[port] = struct{}{}
	supportedFormats := append([]string(nil), assignment.ProbeFormats...)
	dirtyReasons := normalizeSourceDirtyReasons(assignment.DirtyReasons)
	if assignment.Disabled {
		dirtyReasons = removeDirtyReason(dirtyReasons, "restart")
	}
	monitoringOn := true
	if assignment.On != nil {
		monitoringOn = *assignment.On
	}
	return sourceSpec{
		Key:              cam.MatchKey,
		AttachmentPath:   cam.AttachmentPath,
		SourceType:       "usb",
		Name:             name,
		ShortID:          shortID,
		Summary:          summarizeUSBIdentity(cam),
		Enabled:          false,
		Detected:         false,
		MonitoringOn:     monitoringOn,
		OutputPort:       port,
		ProbeDirty:       hasDirtyReason(dirtyReasons, "probe"),
		DirtyReasons:     dirtyReasons,
		SupportedFormats: supportedFormats,
		PreferredFormat:  strings.TrimSpace(assignment.PreferredPixelFormat),
		Camera:           cam,
	}
}

func storedUSBCameraFromAssignment(assignment *camerascfg.DeviceAssignment) recording.DetectedCamera {
	name := strings.TrimSpace(assignment.Name)
	if name == "" {
		name = strings.TrimSpace(assignment.AttachmentPath)
	}
	if name == "" {
		name = strings.TrimSpace(assignment.MatchKey)
	}
	identity := strings.TrimSpace(assignment.AttachmentPath)
	if identity == "" {
		identity = strings.TrimSpace(assignment.MatchKey)
	}
	format := "v4l2"
	if runtime.GOOS == "windows" {
		format = "dshow"
	}
	return recording.DetectedCamera{
		Name:             name,
		Device:           identity,
		Format:           format,
		PixFmt:           strings.TrimSpace(assignment.ProbePixelFormat),
		Size:             strings.TrimSpace(assignment.ProbeSize),
		Fps:              assignment.ProbeFPS,
		MatchKey:         strings.TrimSpace(assignment.MatchKey),
		AttachmentPath:   strings.TrimSpace(assignment.AttachmentPath),
		Identity:         identity,
		SupportedFormats: append([]string(nil), assignment.ProbeFormats...),
	}
}

func buildRTSPSources(startPort int, usedPorts map[int]struct{}, usedShortIDs map[string]struct{}) []sourceSpec {
	return buildRTSPSourcesWithProgress(startPort, usedPorts, usedShortIDs, nil)
}

func buildRTSPSourcesWithProgress(startPort int, usedPorts map[int]struct{}, usedShortIDs map[string]struct{}, progress func(string)) []sourceSpec {
	for _, assignment := range camerasConfig.DeviceAssignments {
		if assignment.OutputPort > 0 {
			usedPorts[assignment.OutputPort] = struct{}{}
		}
		if strings.TrimSpace(assignment.ShortID) != "" {
			usedShortIDs[strings.ToUpper(strings.TrimSpace(assignment.ShortID))] = struct{}{}
		}
	}

	sources := make([]sourceSpec, 0, len(camerasConfig.RTSPSources))
	for i := range camerasConfig.RTSPSources {
		src := &camerasConfig.RTSPSources[i]
		if strings.TrimSpace(src.ShortID) != "" {
			usedShortIDs[strings.ToUpper(strings.TrimSpace(src.ShortID))] = struct{}{}
		}
	}
	for i := range camerasConfig.RTSPSources {
		src := &camerasConfig.RTSPSources[i]
		if progress != nil {
			name := strings.TrimSpace(src.Name)
			if name == "" {
				name = summarizeRTSPURL(src.RTSPURL)
			}
			if name == "" {
				name = fmt.Sprintf("RTSP %d", i+1)
			}
			progress(recording.ProgressMsg(recording.ProgRTSPSource, name))
		}
		shortID := strings.TrimSpace(src.ShortID)
		if shortID == "" {
			shortID = nextShortID("R", usedShortIDs)
		}
		port := src.OutputPort
		if port <= 0 {
			port = nextAvailablePort(startPort, usedPorts)
		}
		usedPorts[port] = struct{}{}

		camera := rtspSourceCamera(*src)
		camera.Name = strings.TrimSpace(src.Name)
		if camera.Name == "" {
			camera.Name = fmt.Sprintf("RTSP %d", i+1)
		}
		camera.MatchKey = src.SourceID
		camera.Identity = summarizeRTSPURL(src.RTSPURL)

		sources = append(sources, sourceSpec{
			Key:          src.SourceID,
			SourceType:   "rtsp",
			Name:         camera.Name,
			ShortID:      shortID,
			Summary:      summarizeRTSPURL(src.RTSPURL),
			Enabled:      src.Enabled,
			Detected:     true,
			MonitoringOn: src.On == nil || *src.On,
			OutputPort:   port,
			Transport:    src.Transport,
			ProbeDirty:   hasDirtyReason(rtspDirtyReasons(*src), "probe"),
			DirtyReasons: rtspDirtyReasons(*src),
			Camera:       camera,
			RTSP:         *src,
		})
		if progress != nil {
			progress(recording.ProgressMsg(recording.ProgDetectedSource, camera.Name))
		}
	}

	return sources
}

func nextAvailablePort(startPort int, used map[int]struct{}) int {
	port := startPort
	if port <= 0 {
		port = 9001
	}
	for {
		if _, exists := used[port]; !exists {
			return port
		}
		port++
	}
}

func nextShortID(prefix string, used map[string]struct{}) string {
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s%d", prefix, i)
		key := strings.ToUpper(candidate)
		if _, exists := used[key]; exists {
			continue
		}
		used[key] = struct{}{}
		return candidate
	}
}

func detectPortConflicts(sources []sourceSpec) []string {
	byPort := make(map[int][]string)
	for _, src := range sources {
		if src.OutputPort <= 0 {
			continue
		}
		byPort[src.OutputPort] = append(byPort[src.OutputPort], src.Name)
	}
	var conflicts []string
	for port, names := range byPort {
		if len(names) < 2 {
			continue
		}
		conflicts = append(conflicts, fmt.Sprintf("Port %d is assigned to %s", port, strings.Join(names, ", ")))
	}
	sort.Strings(conflicts)
	return conflicts
}

func summarizeUSBIdentity(cam recording.DetectedCamera) string {
	if strings.TrimSpace(cam.Identity) != "" {
		return cam.Identity
	}
	return cam.Device
}

func summarizeRTSPURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	parsed.User = nil
	path := strings.TrimSpace(parsed.Path)
	if path == "" {
		path = "/"
	}
	host := parsed.Host
	if host == "" {
		host = parsed.Opaque
	}
	return host + path
}

// normalizeRTSPURL ensures the URL has at least a "/" path so that servers
// which require it (e.g. many phone RTSP apps) are reached correctly.
func normalizeRTSPURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path != "" {
		return raw
	}
	parsed.Path = "/"
	return parsed.String()
}

// probeUSBSource re-detects all cameras and finds the one with the given MatchKey.
// Runs the detection in a goroutine and calls onDone on the UI goroutine.
// onDone receives nil if the camera is not found or not connected.
func probeUSBSource(matchKey string, onDone func(cam *recording.DetectedCamera)) {
	go func() {
		cameras := recording.DetectCamerasWithConfig(ffmpegConfig)
		for _, cam := range cameras {
			if cam.MatchKey == matchKey {
				c := cam
				fyne.Do(func() { onDone(&c) })
				return
			}
		}
		fyne.Do(func() { onDone(nil) })
	}()
}

// rtspSourceCamera builds a DetectedCamera for an RTSP source without probing.
// Uses the stored codec if probed previously; defaults to h264 otherwise.
func rtspSourceCamera(src camerascfg.RTSPSource) recording.DetectedCamera {
	codec := strings.ToLower(strings.TrimSpace(src.Codec))
	if codec == "" {
		codec = "h264"
	}
	size := strings.TrimSpace(src.ProbeSize)
	if size == "" {
		size = "-"
	}
	return recording.DetectedCamera{
		Name:     strings.TrimSpace(src.Name),
		Device:   normalizeRTSPURL(src.RTSPURL),
		Format:   "rtsp",
		PixFmt:   codec,
		Size:     size,
		Fps:      src.ProbeFPS,
		MatchKey: src.SourceID,
		Identity: summarizeRTSPURL(src.RTSPURL),
	}
}

func collectPendingVerification(sources []sourceSpec) []string {
	pending := make([]string, 0)
	for _, src := range sources {
		if len(src.DirtyReasons) == 0 {
			continue
		}
		name := strings.TrimSpace(src.Name)
		if name == "" {
			name = strings.TrimSpace(src.Summary)
		}
		if name != "" {
			pending = append(pending, fmt.Sprintf("%s (%s)", name, strings.Join(src.DirtyReasons, ", ")))
		}
	}
	sort.Strings(pending)
	return pending
}

func normalizeSourceDirtyReasons(reasons []string) []string {
	seen := make(map[string]struct{}, len(reasons))
	normalized := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		trimmed := strings.ToLower(strings.TrimSpace(reason))
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	sort.Strings(normalized)
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func removeDirtyReason(reasons []string, target string) []string {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return normalizeSourceDirtyReasons(reasons)
	}
	filtered := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if strings.ToLower(strings.TrimSpace(reason)) == target {
			continue
		}
		filtered = append(filtered, reason)
	}
	return normalizeSourceDirtyReasons(filtered)
}

func hasDirtyReason(reasons []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, reason := range reasons {
		if strings.ToLower(strings.TrimSpace(reason)) == target {
			return true
		}
	}
	return false
}

func rtspDirtyReasons(src camerascfg.RTSPSource) []string {
	reasons := append([]string(nil), src.DirtyReasons...)
	if src.ProbeDirty {
		reasons = append(reasons, "probe")
	}
	return normalizeSourceDirtyReasons(reasons)
}

// probeAndFillRTSPRow runs ffprobe on the row's URL in the background,
// updates detectedCodec, and calls onDone(codec, size, fps, err).
func probeAndFillRTSPRow(src camerascfg.RTSPSource, onDone func(codec, size string, fps int, err error)) {
	go func() {
		detected := probeRTSPSource(src)
		codec := strings.ToLower(strings.TrimSpace(detected.PixFmt))
		if codec == "unknown" || codec == "" {
			fyne.Do(func() { onDone("", "-", 0, fmt.Errorf("probe failed or no video stream found")) })
			return
		}
		fyne.Do(func() { onDone(codec, detected.Size, detected.Fps, nil) })
	}()
}

// probeRTSPSource connects to the RTSP URL with ffprobe to detect stream parameters.
// Not used by default — see rtspSourceCamera. Retained for potential future use.
func probeRTSPSource(src camerascfg.RTSPSource) recording.DetectedCamera {
	normalizedURL := normalizeRTSPURL(src.RTSPURL)
	cam := recording.DetectedCamera{
		Name:     strings.TrimSpace(src.Name),
		Device:   normalizedURL,
		Format:   "rtsp",
		PixFmt:   "unknown",
		Size:     "-",
		Fps:      0,
		MatchKey: src.SourceID,
		Identity: summarizeRTSPURL(src.RTSPURL),
	}
	if !src.Enabled || strings.TrimSpace(src.RTSPURL) == "" {
		return cam
	}

	ffprobePath := resolveFFprobePathForRTSP()
	args := []string{"-v", "error"}
	transport := strings.ToLower(strings.TrimSpace(src.Transport))
	if transport == "tcp" || transport == "udp" {
		args = append(args, "-rtsp_transport", transport)
	}
	args = append(args,
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name,width,height,avg_frame_rate",
		"-of", "default=noprint_wrappers=1:nokey=0",
		normalizedURL,
	)

	cmd := recording.CreateHiddenCmd(ffprobePath, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		logging.WarningLogger.Printf("ffprobe RTSP probe failed for %s: %v", summarizeRTSPURL(src.RTSPURL), err)
		return cam
	}

	values := make(map[string]string)
	for _, line := range strings.Split(out.String(), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}

	codec := strings.ToLower(strings.TrimSpace(values["codec_name"]))
	if codec != "" {
		cam.PixFmt = codec
	}
	width, _ := strconv.Atoi(strings.TrimSpace(values["width"]))
	height, _ := strconv.Atoi(strings.TrimSpace(values["height"]))
	if width > 0 && height > 0 {
		cam.Size = fmt.Sprintf("%dx%d", width, height)
	}
	cam.Fps = parseProbeFPS(values["avg_frame_rate"])
	return cam
}

func parseProbeFPS(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0/0" {
		return 0
	}
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0
		}
		return int(value + 0.5)
	}
	numerator, errNum := strconv.ParseFloat(parts[0], 64)
	denominator, errDen := strconv.ParseFloat(parts[1], 64)
	if errNum != nil || errDen != nil || denominator == 0 {
		return 0
	}
	return int((numerator / denominator) + 0.5)
}

func resolveFFprobePathForRTSP() string {
	if envPath := strings.TrimSpace(config.GetFFmpegPath()); envPath != "" {
		name := "ffprobe"
		if runtime.GOOS == "windows" {
			name = "ffprobe.exe"
		}
		candidate := filepath.Join(filepath.Dir(envPath), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	name := "ffprobe"
	if runtime.GOOS == "windows" {
		name = "ffprobe.exe"
	}
	if shared := config.FindSharedFFmpegExecutable(name); shared != "" {
		return shared
	}
	return name
}
