package cameras

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	camerascfg "github.com/owlcms/video/internal/config/cameras"
)

const (
	usbEnabledWidth  = 100
	usbIdentityWidth = 600
	usbNameWidth     = 180
	usbShortIDWidth  = 80
	usbPortWidth     = 80
	usbFormatWidth   = 110
	usbProbeWidth    = 80
	usbRestartWidth  = 80
	usbRemoveWidth   = 80

	rtspEnabledWidth   = 100
	rtspNameWidth      = 180
	rtspShortIDWidth   = 90
	rtspURLWidth       = 430
	rtspPortWidth      = 90
	rtspTransportWidth = 100
	rtspRestartWidth   = 80
	rtspProbeWidth     = 80
	rtspRemoveWidth    = 80
)

func fixedWidth(width float32, obj fyne.CanvasObject) fyne.CanvasObject {
	return container.NewGridWrap(fyne.NewSize(width, obj.MinSize().Height), obj)
}

func boolRef(v bool) *bool {
	value := v
	return &value
}

func newReadOnlyEntry(text string) *widget.Entry {
	entry := widget.NewEntry()
	entry.SetText(text)

	updating := false
	entry.OnChanged = func(value string) {
		if updating || value == text {
			return
		}
		updating = true
		entry.SetText(text)
		updating = false
	}

	return entry
}

func applyRestartButtonStyle(button *widget.Button, highlighted bool) {
	if button == nil {
		return
	}
	if highlighted {
		button.Importance = widget.DangerImportance
	} else {
		button.Importance = widget.MediumImportance
	}
	if button.Text == "" {
		button.SetText("Apply")
	}
	button.Refresh()
}

type usbSourceRow struct {
	attachmentPath  string
	matchKey        string
	identity        string
	detected        bool
	dirty           bool
	storedEnabled   bool
	storedName      string
	storedShortID   string
	storedPort      string
	storedFormat    string
	dirtyReasons    []string
	detectedPixFmt  string
	detectedSize    string
	detectedFPS     int
	detectedFormats []string
	monitoringOn    bool
	restartBtn      *widget.Button
	enabledCheck    *widget.Check
	nameEntry       *portTableEntry
	shortIDEntry    *portTableEntry
	portEntry       *portTableEntry
	formatSelect    *widget.Select
}

func newUSBSourceRow(spec sourceSpec) *usbSourceRow {
	enabledCheck := widget.NewCheck("", nil)
	enabledCheck.SetChecked(spec.Enabled)
	nameEntry := newPortTableEntry()
	nameEntry.SetText(spec.Name)
	shortIDEntry := newPortTableEntry()
	shortIDEntry.SetText(spec.ShortID)
	portEntry := newPortTableEntry()
	if spec.OutputPort > 0 {
		portEntry.SetText(strconv.Itoa(spec.OutputPort))
	}
	formatOptions := append([]string{"Auto"}, spec.SupportedFormats...)
	formatSelect := widget.NewSelect(formatOptions, nil)
	if spec.PreferredFormat != "" {
		formatSelect.SetSelected(spec.PreferredFormat)
	} else {
		formatSelect.SetSelected("Auto")
	}
	r := &usbSourceRow{
		attachmentPath:  spec.AttachmentPath,
		matchKey:        spec.Key,
		identity:        spec.Summary,
		detected:        spec.Detected,
		dirtyReasons:    append([]string(nil), spec.DirtyReasons...),
		detectedPixFmt:  spec.Camera.PixFmt,
		detectedSize:    spec.Camera.Size,
		detectedFPS:     spec.Camera.Fps,
		detectedFormats: append([]string(nil), spec.SupportedFormats...),
		monitoringOn:    spec.MonitoringOn,
		enabledCheck:    enabledCheck,
		nameEntry:       nameEntry,
		shortIDEntry:    shortIDEntry,
		portEntry:       portEntry,
		formatSelect:    formatSelect,
	}
	if !r.detected {
		enabledCheck.Disable()
	}
	markDirty := func(_ string) { r.markDirty() }
	nameEntry.OnChanged = markDirty
	shortIDEntry.OnChanged = markDirty
	portEntry.OnChanged = markDirty
	r.markClean()
	return r
}

func (r *usbSourceRow) markDirty() {
	r.dirty = r.hasPendingChanges()
	r.refreshRestartHighlight()
}

func (r *usbSourceRow) markClean() {
	r.storedEnabled = r.enabledCheck.Checked
	r.storedName = strings.TrimSpace(r.nameEntry.Text)
	r.storedShortID = strings.TrimSpace(r.shortIDEntry.Text)
	r.storedPort = strings.TrimSpace(r.portEntry.Text)
	r.storedFormat = r.currentFormat()
	r.dirty = false
	r.refreshRestartHighlight()
}

func (r *usbSourceRow) refreshRestartHighlight() {
	applyRestartButtonStyle(r.restartBtn, r.hasPendingChanges() || hasDirtyReason(r.dirtyReasons, "restart"))
	if r.restartBtn != nil {
		r.restartBtn.Refresh()
	}
	if r.enabledCheck != nil {
		r.enabledCheck.Refresh()
	}
}

func (r *usbSourceRow) currentFormat() string {
	if r.formatSelect == nil {
		return "Auto"
	}
	selected := strings.TrimSpace(r.formatSelect.Selected)
	if selected == "" {
		return "Auto"
	}
	return selected
}

func (r *usbSourceRow) hasPendingChanges() bool {
	if r == nil {
		return false
	}
	return r.enabledCheck.Checked != r.storedEnabled ||
		strings.TrimSpace(r.nameEntry.Text) != r.storedName ||
		strings.TrimSpace(r.shortIDEntry.Text) != r.storedShortID ||
		strings.TrimSpace(r.portEntry.Text) != r.storedPort ||
		r.currentFormat() != r.storedFormat
}

func (r *usbSourceRow) object(probe func(), _ func() bool, remove func(), restart func()) fyne.CanvasObject {
	r.nameEntry.onFocusLost = nil
	r.nameEntry.OnSubmitted = nil
	r.shortIDEntry.onFocusLost = nil
	r.shortIDEntry.OnSubmitted = nil
	r.portEntry.onFocusLost = nil
	r.portEntry.OnSubmitted = nil
	r.enabledCheck.OnChanged = func(_ bool) {
		r.markDirty()
	}
	r.formatSelect.OnChanged = func(_ string) {
		r.markDirty()
	}
	probeBtn := widget.NewButton("Probe", func() {
		if probe != nil {
			probe()
		}
	})
	restartBtn := widget.NewButton("Apply", func() {
		if restart != nil {
			restart()
		}
	})
	r.restartBtn = restartBtn
	r.refreshRestartHighlight()
	identity := newReadOnlyEntry(r.identity)
	removeControl := fyne.CanvasObject(widget.NewLabel(""))
	if !r.detected {
		removeControl = widget.NewButton("Remove", func() {
			if remove != nil {
				remove()
			}
		})
	}
	return container.NewHBox(
		fixedWidth(usbEnabledWidth, r.enabledCheck),
		fixedWidth(usbIdentityWidth, identity),
		fixedWidth(usbNameWidth, r.nameEntry),
		fixedWidth(usbShortIDWidth, r.shortIDEntry),
		fixedWidth(usbPortWidth, r.portEntry),
		fixedWidth(usbFormatWidth, r.formatSelect),
		fixedWidth(usbRestartWidth, restartBtn),
		fixedWidth(usbProbeWidth, probeBtn),
		fixedWidth(usbRemoveWidth, removeControl),
	)
}

func (r *usbSourceRow) assignment() (camerascfg.DeviceAssignment, error) {
	port, err := parseOptionalPort(r.portEntry.Text)
	if err != nil {
		return camerascfg.DeviceAssignment{}, err
	}
	preferredFormat := ""
	if r.formatSelect.Selected != "Auto" && r.formatSelect.Selected != "" {
		preferredFormat = r.formatSelect.Selected
	}
	return camerascfg.DeviceAssignment{
		AttachmentPath:       r.attachmentPath,
		MatchKey:             r.matchKey,
		Name:                 strings.TrimSpace(r.nameEntry.Text),
		ShortID:              strings.TrimSpace(r.shortIDEntry.Text),
		OutputPort:           port,
		Disabled:             !r.enabledCheck.Checked,
		On:                   boolRef(r.monitoringOn),
		PreferredPixelFormat: preferredFormat,
		ProbePixelFormat:     strings.TrimSpace(r.detectedPixFmt),
		ProbeSize:            strings.TrimSpace(r.detectedSize),
		ProbeFPS:             r.detectedFPS,
		ProbeFormats:         append([]string(nil), r.detectedFormats...),
		DirtyReasons:         append([]string(nil), r.dirtyReasons...),
	}, nil
}

type rtspSourceRow struct {
	sourceID         string
	isAddRow         bool
	dirty            bool
	storedEnabled    bool
	storedName       string
	storedShortID    string
	storedURL        string
	storedPort       string
	storedTransport  string
	probeDirty       bool
	dirtyReasons     []string
	restartBtn       *widget.Button
	probeStatus      *widget.Label
	enabledCheck     *widget.Check
	enabledChanged   func(bool)
	nameEntry        *portTableEntry
	shortIDEntry     *portTableEntry
	urlEntry         *portTableEntry
	portEntry        *portTableEntry
	transportSelect  *widget.Select
	transportChanged func(string)
	detectedCodec    string
	detectedSize     string
	detectedFPS      int
	monitoringOn     bool
}

func newRTSPSourceRow(spec sourceSpec) *rtspSourceRow {
	enabledCheck := widget.NewCheck("Enabled", nil)
	enabledCheck.SetChecked(spec.Enabled)
	nameEntry := newPortTableEntry()
	nameEntry.SetText(spec.Name)
	shortIDEntry := newPortTableEntry()
	shortIDEntry.SetText(spec.ShortID)
	urlEntry := newPortTableEntry()
	urlEntry.SetText(spec.RTSP.RTSPURL)
	portEntry := newPortTableEntry()
	if spec.OutputPort > 0 {
		portEntry.SetText(strconv.Itoa(spec.OutputPort))
	}
	transportSelect := widget.NewSelect([]string{"tcp", "udp", "auto"}, nil)
	transport := strings.ToLower(strings.TrimSpace(spec.Transport))
	if transport == "" {
		transport = "auto"
	}
	transportSelect.SetSelected(transport)

	probeStatus := widget.NewLabel("")

	row := &rtspSourceRow{
		sourceID:        spec.Key,
		probeDirty:      spec.RTSP.ProbeDirty,
		dirtyReasons:    append([]string(nil), spec.DirtyReasons...),
		probeStatus:     probeStatus,
		enabledCheck:    enabledCheck,
		nameEntry:       nameEntry,
		shortIDEntry:    shortIDEntry,
		urlEntry:        urlEntry,
		portEntry:       portEntry,
		transportSelect: transportSelect,
		detectedCodec:   spec.RTSP.Codec,
		detectedSize:    spec.RTSP.ProbeSize,
		detectedFPS:     spec.RTSP.ProbeFPS,
		monitoringOn:    spec.MonitoringOn,
	}
	row.installAutoEnable()
	row.enabledChanged = row.enabledCheck.OnChanged
	row.transportChanged = row.transportSelect.OnChanged
	row.markClean()
	return row
}

func newBlankRTSPSourceRow() *rtspSourceRow {
	row := newRTSPSourceRow(sourceSpec{Enabled: false, Transport: "tcp"})
	row.isAddRow = true
	row.enabledCheck.SetChecked(false)
	return row
}

func (r *rtspSourceRow) object(add func(), _ func() bool, restart func(), probe func(), remove func()) fyne.CanvasObject {
	if r.isAddRow {
		addButton := widget.NewButton("Add", func() {
			if add != nil {
				add()
			}
		})
		return container.NewHBox(
			fixedWidth(rtspEnabledWidth, r.enabledCheck),
			fixedWidth(rtspNameWidth, r.nameEntry),
			fixedWidth(rtspShortIDWidth, r.shortIDEntry),
			fixedWidth(rtspURLWidth, r.urlEntry),
			fixedWidth(rtspPortWidth, r.portEntry),
			fixedWidth(rtspTransportWidth, r.transportSelect),
			fixedWidth(rtspRestartWidth, addButton),
			fixedWidth(rtspProbeWidth, widget.NewLabel("")),
			fixedWidth(rtspRemoveWidth, widget.NewLabel("")),
		)
	}

	r.nameEntry.onFocusLost = nil
	r.nameEntry.OnSubmitted = nil
	r.shortIDEntry.onFocusLost = nil
	r.shortIDEntry.OnSubmitted = nil
	r.urlEntry.onFocusLost = nil
	r.urlEntry.OnSubmitted = nil
	r.portEntry.onFocusLost = nil
	r.portEntry.OnSubmitted = nil
	r.enabledCheck.OnChanged = func(value bool) {
		if r.enabledChanged != nil {
			r.enabledChanged(value)
		}
		r.markDirty()
	}
	r.transportSelect.OnChanged = func(value string) {
		if r.transportChanged != nil {
			r.transportChanged(value)
		}
		r.markDirty()
	}
	probeButton := widget.NewButton("Probe", func() {
		if probe != nil {
			probe()
		}
	})
	restartButton := widget.NewButton("Apply", func() {
		if restart != nil {
			restart()
		}
	})
	r.restartBtn = restartButton
	r.refreshRestartHighlight()
	removeButton := widget.NewButton("Remove", func() {
		if remove != nil {
			remove()
		}
	})
	return container.NewHBox(
		fixedWidth(rtspEnabledWidth, r.enabledCheck),
		fixedWidth(rtspNameWidth, r.nameEntry),
		fixedWidth(rtspShortIDWidth, r.shortIDEntry),
		fixedWidth(rtspURLWidth, r.urlEntry),
		fixedWidth(rtspPortWidth, r.portEntry),
		fixedWidth(rtspTransportWidth, r.transportSelect),
		fixedWidth(rtspRestartWidth, restartButton),
		fixedWidth(rtspProbeWidth, probeButton),
		fixedWidth(rtspRemoveWidth, removeButton),
	)
}

func (r *rtspSourceRow) markDirty() {
	if r.isAddRow {
		return
	}
	r.dirty = r.hasPendingChanges()
	r.refreshRestartHighlight()
}

func (r *rtspSourceRow) markClean() {
	r.storedEnabled = r.enabledCheck.Checked
	r.storedName = strings.TrimSpace(r.nameEntry.Text)
	r.storedShortID = strings.TrimSpace(r.shortIDEntry.Text)
	r.storedURL = strings.TrimSpace(r.urlEntry.Text)
	r.storedPort = strings.TrimSpace(r.portEntry.Text)
	r.storedTransport = r.currentTransport()
	r.dirty = false
	r.refreshRestartHighlight()
}

func (r *rtspSourceRow) refreshRestartHighlight() {
	applyRestartButtonStyle(r.restartBtn, r.hasPendingChanges() || hasDirtyReason(r.dirtyReasons, "restart"))
	if r.restartBtn != nil {
		r.restartBtn.Refresh()
	}
	if r.enabledCheck != nil {
		r.enabledCheck.Refresh()
	}
}

func (r *rtspSourceRow) currentTransport() string {
	if r.transportSelect == nil {
		return "tcp"
	}
	transport := strings.ToLower(strings.TrimSpace(r.transportSelect.Selected))
	if transport == "" {
		return "tcp"
	}
	return transport
}

func (r *rtspSourceRow) hasPendingChanges() bool {
	if r == nil || r.isAddRow {
		return false
	}
	return r.enabledCheck.Checked != r.storedEnabled ||
		strings.TrimSpace(r.nameEntry.Text) != r.storedName ||
		strings.TrimSpace(r.shortIDEntry.Text) != r.storedShortID ||
		strings.TrimSpace(r.urlEntry.Text) != r.storedURL ||
		strings.TrimSpace(r.portEntry.Text) != r.storedPort ||
		r.currentTransport() != r.storedTransport
}

func (r *rtspSourceRow) installAutoEnable() {
	autoEnable := func(_ string) {
		if !r.hasContent() {
			return
		}
		if !r.enabledCheck.Checked {
			r.enabledCheck.SetChecked(true)
		}
		r.markDirty()
	}
	r.nameEntry.OnChanged = autoEnable
	r.shortIDEntry.OnChanged = autoEnable
	r.urlEntry.OnChanged = func(value string) {
		autoEnable(value)
		if strings.TrimSpace(value) != "" {
			r.probeDirty = true
			r.dirtyReasons = addReason(r.dirtyReasons, "probe")
		}
	}
	r.portEntry.OnChanged = autoEnable
	r.transportSelect.OnChanged = func(_ string) {
		r.markDirty()
		if !r.isAddRow {
			r.probeDirty = true
			r.dirtyReasons = addReason(r.dirtyReasons, "probe")
		}
	}
	r.enabledCheck.OnChanged = func(_ bool) { r.markDirty() }
}

func (r *rtspSourceRow) hasContent() bool {
	return strings.TrimSpace(r.nameEntry.Text) != "" ||
		strings.TrimSpace(r.shortIDEntry.Text) != "" ||
		strings.TrimSpace(r.urlEntry.Text) != "" ||
		strings.TrimSpace(r.portEntry.Text) != ""
}

func (r *rtspSourceRow) source() (camerascfg.RTSPSource, bool, error) {
	urlValue := strings.TrimSpace(r.urlEntry.Text)
	nameValue := strings.TrimSpace(r.nameEntry.Text)
	if urlValue == "" && nameValue == "" {
		return camerascfg.RTSPSource{}, true, nil
	}
	if urlValue == "" {
		return camerascfg.RTSPSource{}, false, fmt.Errorf("RTSP URL is required for configured sources")
	}
	port, err := parseOptionalPort(r.portEntry.Text)
	if err != nil {
		return camerascfg.RTSPSource{}, false, err
	}
	transport := strings.ToLower(strings.TrimSpace(r.transportSelect.Selected))
	if transport == "" {
		transport = "tcp"
	}
	sourceID := strings.TrimSpace(r.sourceID)
	if sourceID == "" {
		sourceID = tempRTSPSourceID(urlValue)
	}
	probeDirty := r.probeDirty
	if strings.TrimSpace(r.detectedCodec) == "" {
		probeDirty = true
	}
	dirtyReasons := append([]string(nil), r.dirtyReasons...)
	if probeDirty {
		dirtyReasons = addReason(dirtyReasons, "probe")
	} else {
		dirtyReasons = removeReason(dirtyReasons, "probe")
	}
	return camerascfg.RTSPSource{
		SourceID:     sourceID,
		Name:         nameValue,
		ShortID:      strings.TrimSpace(r.shortIDEntry.Text),
		Enabled:      r.enabledCheck.Checked,
		On:           boolRef(r.monitoringOn),
		RTSPURL:      urlValue,
		OutputPort:   port,
		Transport:    transport,
		Codec:        r.detectedCodec,
		ProbeSize:    r.detectedSize,
		ProbeFPS:     r.detectedFPS,
		ProbeDirty:   probeDirty,
		DirtyReasons: dirtyReasons,
	}, false, nil
}

func addReason(reasons []string, reason string) []string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return append([]string(nil), reasons...)
	}
	for _, existing := range reasons {
		if strings.ToLower(strings.TrimSpace(existing)) == reason {
			return append([]string(nil), reasons...)
		}
	}
	updated := append([]string(nil), reasons...)
	updated = append(updated, reason)
	return updated
}

func removeReason(reasons []string, reason string) []string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return append([]string(nil), reasons...)
	}
	updated := make([]string, 0, len(reasons))
	for _, existing := range reasons {
		if strings.ToLower(strings.TrimSpace(existing)) == reason {
			continue
		}
		updated = append(updated, existing)
	}
	return updated
}

func tempRTSPSourceID(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err == nil {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		raw = parsed.String()
	}
	hash := sha1.Sum([]byte(strings.TrimSpace(raw)))
	return "rtsp-" + hex.EncodeToString(hash[:])[:10]
}

func parseOptionalPort(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q", raw)
	}
	return port, nil
}

func removeDeviceAssignment(assignments []camerascfg.DeviceAssignment, attachmentPath, matchKey string) ([]camerascfg.DeviceAssignment, camerascfg.DeviceAssignment, bool) {
	attachmentPath = strings.TrimSpace(attachmentPath)
	matchKey = strings.TrimSpace(matchKey)
	for i := range assignments {
		if attachmentPath != "" && strings.TrimSpace(assignments[i].AttachmentPath) == attachmentPath {
			removed := assignments[i]
			updated := append([]camerascfg.DeviceAssignment{}, assignments[:i]...)
			updated = append(updated, assignments[i+1:]...)
			return updated, removed, true
		}
		if matchKey != "" && strings.TrimSpace(assignments[i].MatchKey) == matchKey {
			removed := assignments[i]
			updated := append([]camerascfg.DeviceAssignment{}, assignments[:i]...)
			updated = append(updated, assignments[i+1:]...)
			return updated, removed, true
		}
	}
	return assignments, camerascfg.DeviceAssignment{}, false
}
