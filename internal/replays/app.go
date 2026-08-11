package replays

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/config/cameras"
	replayscfg "github.com/owlcms/video/internal/config/replays"
	"github.com/owlcms/video/internal/httpServer"
	"github.com/owlcms/video/internal/logging"
	"github.com/owlcms/video/internal/monitor"
	"github.com/owlcms/video/internal/opendir"
	"github.com/owlcms/video/internal/recording"
)

var (
	titleLabel             *widget.Label
	camerasAvailable       bool
	localCamerasConfigPath string

	// moduleConfig is the loaded replays configuration; moduleConfigPath is the
	// file every in-app edit is written back to.
	moduleConfig      *replayscfg.Config
	moduleConfigPath  string
	startupScanMu     sync.Mutex
	startupScanCancel context.CancelFunc
	terminated        bool
)

// beginStartupScan cancels any in-flight scan and returns the context for a new
// one. It reports false once the module has been shut down for good.
func beginStartupScan() (context.Context, bool) {
	startupScanMu.Lock()
	defer startupScanMu.Unlock()
	if terminated {
		return nil, false
	}
	if startupScanCancel != nil {
		startupScanCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	startupScanCancel = cancel
	return ctx, true
}

func isTerminated() bool {
	startupScanMu.Lock()
	defer startupScanMu.Unlock()
	return terminated
}

// stopServices tears down the Replays services. When terminal is true the
// module is stopped for good and cannot be restarted.
func stopServices(terminal bool) {
	startupScanMu.Lock()
	if terminal {
		terminated = true
	}
	if startupScanCancel != nil {
		startupScanCancel()
		startupScanCancel = nil
	}
	startupScanMu.Unlock()

	recording.TerminateRecordings()
	httpServer.StopServer()
	monitor.DisconnectMQTT()
}

func getReplayListHost() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		if localAddr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			ip := localAddr.IP
			if ip != nil && !ip.IsLoopback() {
				if v4 := ip.To4(); v4 != nil {
					return v4.String()
				}
				return ip.String()
			}
		}
	}

	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil || ipNet.IP.IsLoopback() {
				continue
			}
			if v4 := ipNet.IP.To4(); v4 != nil {
				return v4.String()
			}
		}
	}

	return "localhost"
}

// Shutdown stops all Replays services permanently; the module cannot be
// restarted afterwards.
func Shutdown() {
	logging.InfoLogger.Println("Shutting down replays module...")
	stopServices(true)
	logging.InfoLogger.Println("Replays module shutdown complete")
}

// showOwlCMSServerAddress shows a dialog with the OwlCMS server address
func showOwlCMSServerAddress(cfg *replayscfg.Config, window fyne.Window) {
	var message string
	if cfg.OwlCMS == "" {
		message = "No server located."
	} else {
		message = fmt.Sprintf("Current owlcms Server Address:\n%s", cfg.OwlCMS)
	}

	entry := widget.NewEntry()
	entry.SetPlaceHolder("Enter new server address")

	updateFunc := func() {
		newAddress := entry.Text
		if newAddress != "" {
			cfg.OwlCMS = newAddress
			configFilePath := moduleConfigPath
			if err := replayscfg.UpdateConfigFile(configFilePath, newAddress); err != nil {
				logging.ErrorLogger.Printf("Error updating config file: %v", err)
				dialog.ShowError(err, window)
				return
			}
			go monitor.Reconnect(cfg)
			dialog.ShowInformation("Success", "owlcms server address updated.", window)
		}
	}

	// Set the entry's OnSubmitted handler
	entry.OnSubmitted = func(string) { updateFunc() }

	form := container.NewBorder(
		nil,
		nil,
		nil,
		widget.NewButton("Update", updateFunc),
		entry,
	)

	content := container.NewVBox(
		widget.NewLabel(message),
		form,
	)
	dialog := dialog.NewCustom("OwlCMS Server Address", "Close", content, window)
	dialog.Resize(fyne.NewSize(400, 0))
	dialog.Show()
}

func showRemoteCamerasImportDialog(cfg *replayscfg.Config, window fyne.Window) {
	serverAddress := widget.NewEntry()
	serverAddress.SetPlaceHolder("http://video-cameras.local:8090")
	content := container.NewVBox(
		widget.NewLabel("Cameras server address"),
		serverAddress,
		widget.NewLabel("The server must expose /api/cameras/config and send its configured streams to this machine."),
	)

	dialog.NewCustomConfirm("Use Streams from Cameras Server", "Fetch and Use", "Cancel", content,
		func(use bool) {
			if !use {
				return
			}

			address := serverAddress.Text
			go func() {
				preview, err := loadRemoteCamerasImportPreview(address)
				if err == nil && len(preview.OrderedStreams) == 0 {
					err = fmt.Errorf("the Cameras server has no enabled streams")
				}
				if err == nil && !preview.CompatibilityAllowed {
					err = fmt.Errorf("%s", preview.CompatibilityMessage)
				}

				fyne.Do(func() {
					if err != nil {
						dialog.ShowError(fmt.Errorf("load Cameras server configuration: %w", err), window)
						return
					}
					showCameraSelectionDialog(cfg, preview, address, window)
				})
			}()
		}, window).Show()
}

func showSelectCamerasDialog(cfg *replayscfg.Config, window fyne.Window) {
	go func() {
		var (
			preview      *localCamerasImportPreview
			sourceServer string
			err          error
		)
		if camerasAvailable {
			preview, err = loadLocalCamerasImportPreview(localCamerasConfigPath)
		} else {
			sourceServer = strings.TrimSpace(cfg.CamerasServer)
			if sourceServer == "" {
				fyne.Do(func() {
					showRemoteCamerasImportDialog(cfg, window)
				})
				return
			}
			preview, err = loadRemoteCamerasImportPreview(sourceServer)
		}

		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(fmt.Errorf("load available Cameras: %w", err), window)
				return
			}
			if len(preview.OrderedStreams) == 0 {
				dialog.ShowInformation("No Cameras", "The selected Cameras source has no enabled streams.", window)
				return
			}
			showCameraSelectionDialog(cfg, preview, sourceServer, window)
		})
	}()
}

func showCameraSelectionDialog(cfg *replayscfg.Config, preview *localCamerasImportPreview, sourceServer string, window fyne.Window) {
	selected, available := selectedCamerasFirst(preview, cfg.Multicast)
	optionByLabel := make(map[string]localCamerasStream, len(preview.OrderedStreams))
	formatOption := func(stream localCamerasStream) string {
		return fmt.Sprintf("%s  %s [%s]  port %d", formatPreviewShortID(stream.ShortID), stream.Name, stream.Kind, stream.OutputPort)
	}
	options := make([]string, 0, len(preview.OrderedStreams))
	for _, stream := range append(append([]localCamerasStream{}, selected...), available...) {
		label := formatOption(stream)
		optionByLabel[label] = stream
		options = append(options, label)
	}

	selectors := make([]*widget.Select, maxImportedCameraStreams)
	formItems := make([]*widget.FormItem, 0, maxImportedCameraStreams)
	for index := range selectors {
		selector := widget.NewSelect(options, nil)
		selector.PlaceHolder = "Not selected"
		if index < len(selected) {
			selector.SetSelected(formatOption(selected[index]))
		}
		selectors[index] = selector
		selectorContainer := container.NewGridWrap(fyne.NewSize(360, selector.MinSize().Height), selector)
		clearButton := widget.NewButtonWithIcon("", theme.ContentClearIcon(), selector.ClearSelected)
		formItems = append(formItems, widget.NewFormItem(fmt.Sprintf("Camera %d", index+1), container.NewBorder(nil, nil, nil, clearButton, selectorContainer)))
	}

	content := container.NewVBox(
		widget.NewLabel("Selected cameras and replay order"),
		widget.NewForm(formItems...),
	)

	dialog.NewCustomConfirm("Select Cameras", "Save", "Cancel", content, func(save bool) {
		if !save {
			return
		}
		newSelection := make([]localCamerasStream, 0, maxImportedCameraStreams)
		selectedPorts := make(map[int]struct{}, maxImportedCameraStreams)
		for _, selector := range selectors {
			if selector.Selected == "" {
				continue
			}
			stream := optionByLabel[selector.Selected]
			if _, duplicate := selectedPorts[stream.OutputPort]; duplicate {
				dialog.ShowError(fmt.Errorf("a camera can only be selected once"), window)
				return
			}
			selectedPorts[stream.OutputPort] = struct{}{}
			newSelection = append(newSelection, stream)
		}
		if len(newSelection) == 0 {
			dialog.ShowError(fmt.Errorf("select at least one camera"), window)
			return
		}
		if err := applyLocalCamerasImport(cfg, preview, newSelection, moduleConfigPath); err != nil {
			dialog.ShowError(fmt.Errorf("save selected cameras: %w", err), window)
			return
		}
		if sourceServer != "" {
			if err := replayscfg.UpdateCamerasServer(moduleConfigPath, sourceServer); err != nil {
				dialog.ShowError(fmt.Errorf("save Cameras server: %w", err), window)
				return
			}
			cfg.CamerasServer = sourceServer
		}
		cfg.Cameras = cfg.Multicast.BuildCameraConfigs()
		config.SetCameraConfigs(cfg.Cameras)
		if err := recording.EnsureCompatibleFFmpegForRecording(cfg.Cameras); err != nil {
			logging.WarningLogger.Printf("Warning: failed to switch to compatible ffmpeg for recording: %v", err)
		}
		dialog.ShowInformation("Success", "Selected cameras saved.", window)
	}, window).Show()
}

// showPlatformSelection shows a dialog with platform selection dropdown
func showPlatformSelection(cfg *replayscfg.Config, window fyne.Window) {
	// Use the stored validated platforms if available
	platforms := monitor.GetStoredPlatforms()

	// If no platforms are stored, try to request them
	if len(platforms) == 0 {
		// Check if we have a server connection before trying to request platforms
		if cfg.OwlCMS == "" {
			dialog.ShowInformation("No Server Connection", "Please configure the owlcms server address first.", window)
			return
		}

		// Request fresh platform list
		monitor.PublishConfig(cfg.Platform)

		// Wait up to 2 seconds for response
		select {
		case platforms = <-monitor.PlatformListChan:
			// got platforms
		case <-time.After(2 * time.Second):
			dialog.ShowInformation("Not Available", "No response from owlcms server. Please check server connection.", window)
			return
		}
	}

	if len(platforms) == 0 {
		dialog.ShowInformation("No Platforms", "No platforms configured on owlcms server", window)
		return
	}

	combo := widget.NewSelect(platforms, nil)
	// Only set the current platform if it exists in the list
	for _, p := range platforms {
		if p == cfg.Platform {
			combo.SetSelected(cfg.Platform)
			break
		}
	}

	content := container.NewVBox(
		widget.NewLabel("Select Platform:"),
		combo,
	)

	dialog := dialog.NewCustomConfirm("Platform Selection", "Update", "Cancel", content,
		func(update bool) {
			if update && combo.Selected != "" {
				cfg.Platform = combo.Selected
				configFilePath := moduleConfigPath
				if err := replayscfg.UpdatePlatform(configFilePath, combo.Selected); err != nil {
					dialog.ShowError(err, window)
					return
				}
				go monitor.Reconnect(cfg)
				dialog.ShowInformation("Success", "Platform updated.", window)
			}
		}, window)
	dialog.Resize(fyne.NewSize(300, 0))
	dialog.Show()
}

func updateTitle() {
	cfg := replayscfg.GetCurrentConfig()
	platform := cfg.Platform
	if platform == "" {
		platform = "No Platform Selected"
	}
	titleLabel.SetText(fmt.Sprintf("OWLCMS Jury Replays - Platform %s", platform))
}

// isUnicastIP reports whether ip is a unicast listen address (0.0.0.0 or a
// non-multicast IP), as opposed to a multicast group address.
func isUnicastIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return !parsed.IsMulticast()
}

func localMulticastMismatchNote(cfg *replayscfg.Config) string {
	replaysIP := strings.TrimSpace(cfg.Multicast.IP)

	// If replays is configured for unicast listening, show an informational note
	if isUnicastIP(replaysIP) {
		logging.InfoLogger.Printf("Replays is in unicast mode (listening on %s)", replaysIP)
		return fmt.Sprintf("Unicast mode: listening on %s.\nReplay receiver: localhost (%s).", replaysIP, cameras.PreviewLoopbackAddress)
	}

	camerasCfg, camerasConfigPath, err := loadStartupCamerasConfigForComparison()
	if err != nil {
		logging.WarningLogger.Printf("Skipping cameras/replays multicast check: %v", err)
		return ""
	}

	if camerasConfigPath == "" {
		return ""
	}

	camerasIP := strings.TrimSpace(camerasCfg.Multicast.IP)
	if replaysIP == "" || camerasIP == "" || replaysIP == camerasIP {
		return ""
	}

	logging.WarningLogger.Printf("Replays multicast IP (%s) differs from Cameras multicast IP (%s)", replaysIP, camerasIP)
	return fmt.Sprintf("Warning: local Cameras Module multicast IP is %s, Replays is %s. This is OK when the Cameras Module runs on another machine.", camerasIP, replaysIP)
}

func loadStartupCamerasConfigForComparison() (*cameras.Config, string, error) {
	configPath := config.CamerasConfigPath()
	cfg, err := cameras.LoadConfigFromFile(configPath)
	if err != nil {
		return nil, "", err
	}
	return cfg, configPath, nil
}

// Options carries the host-supplied startup settings for the Replays module.
type Options struct {
	ConfigPath        string
	CamerasConfigPath string
	CamerasAvailable  bool
	Enabled           bool
}

// UI exposes the Replays module content, menus and lifecycle hooks to the host.
type UI struct {
	Content             fyne.CanvasObject
	Menus               []*fyne.Menu
	Start               func(camerasStartupComplete <-chan struct{})
	StartServer         func(camerasAvailable, replaysAvailable bool)
	SetCamerasAvailable func(bool)
}

// Init loads the Replays module configuration and prepares the recording runtime.
// It must be called before BuildUI.
func Init(opts Options) error {
	moduleConfigPath = opts.ConfigPath
	camerasAvailable = opts.CamerasAvailable
	localCamerasConfigPath = opts.CamerasConfigPath

	cfg, err := replayscfg.LoadConfig(opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("loading replays config %s: %w", opts.ConfigPath, err)
	}
	if opts.Enabled && opts.CamerasAvailable {
		if err := syncLocalCamerasConfig(cfg); err != nil {
			return err
		}
	}

	config.SetCameraConfigs(cfg.Cameras)
	moduleConfig = cfg
	if !opts.Enabled {
		return nil
	}

	if err := recording.InitializeFFmpeg(); err != nil {
		logging.WarningLogger.Printf("Warning: %v", err)
	}
	if err := recording.EnsureCompatibleFFmpegForRecording(cfg.Cameras); err != nil {
		logging.WarningLogger.Printf("Warning: failed to switch to compatible ffmpeg for recording: %v", err)
	}

	recording.SetNoVideo(config.NoVideo)
	recording.SetVideoDir(cfg.VideoDir)
	recording.SetVideoConfig(cfg.Width, cfg.Height, cfg.Fps)
	return nil
}

// RefreshLocalCameras applies the persisted Cameras configuration to Replays.
func RefreshLocalCameras() error {
	if !camerasAvailable || moduleConfig == nil {
		return nil
	}
	if err := syncLocalCamerasConfig(moduleConfig); err != nil {
		return err
	}
	if err := recording.EnsureCompatibleFFmpegForRecording(moduleConfig.Cameras); err != nil {
		logging.WarningLogger.Printf("Warning: failed to switch to compatible ffmpeg for recording: %v", err)
	}
	return nil
}

func syncLocalCamerasConfig(cfg *replayscfg.Config) error {
	camerasConfigPath := localCamerasConfigPath
	if camerasConfigPath == "" {
		camerasConfigPath = config.CamerasConfigPath()
	}
	preview, err := loadLocalCamerasImportPreview(camerasConfigPath)
	if err != nil {
		return fmt.Errorf("loading local cameras config %s: %w", camerasConfigPath, err)
	}
	selected, _ := selectedCamerasFirst(preview, cfg.Multicast)
	if err := applyLocalCamerasImport(cfg, preview, selected, moduleConfigPath); err != nil {
		return fmt.Errorf("using local cameras config %s: %w", camerasConfigPath, err)
	}
	cfg.Cameras = cfg.Multicast.BuildCameraConfigs()
	config.SetCameraConfigs(cfg.Cameras)
	return nil
}

// BuildUI constructs the Replays module interface inside the host window.
func BuildUI(window fyne.Window) *UI {
	cfg := moduleConfig

	titleLabel = widget.NewLabel("")
	titleLabel.TextStyle = fyne.TextStyle{Bold: true}
	updateTitle()
	selectCamerasButton := widget.NewButton("Select Cameras", func() {
		showSelectCamerasDialog(cfg, window)
	})
	selectCamerasButton.Importance = widget.HighImportance

	var initialStatus string
	if err := cfg.ValidateCamera(); err != nil {
		initialStatus = "Error: " + err.Error()
	}

	topContainer := container.NewVBox(titleLabel, container.NewHBox(selectCamerasButton))

	statusLabel := widget.NewLabel(initialStatus)
	statusLabel.Wrapping = fyne.TextWrapWord
	if strings.HasPrefix(initialStatus, "Error:") {
		statusLabel.TextStyle = fyne.TextStyle{Bold: true}
	} else if initialStatus == "" {
		statusLabel.Hide()
	}
	startupMessages := widget.NewLabel("")
	startupMessages.Wrapping = fyne.TextWrapWord
	startupMessages.Hide()

	host := getReplayListHost()
	urlStr := fmt.Sprintf("http://%s:%d", host, cfg.Port)
	parsedURL, _ := url.Parse(urlStr)
	replaysListLabel := widget.NewLabel("Open replay list in browser:")
	hyperlink := widget.NewHyperlink(urlStr, parsedURL)

	upperContent := container.NewVBox(
		topContainer,
		container.NewHBox(replaysListLabel, hyperlink),
		widget.NewSeparator(),
		startupMessages,
		statusLabel,
	)
	content := container.NewPadded(upperContent)

	remoteCamerasItem := fyne.NewMenuItem("Use Streams from Cameras Server", func() {
		showRemoteCamerasImportDialog(cfg, window)
	})
	remoteCamerasItem.Disabled = camerasAvailable
	replaysMenuItems := []*fyne.MenuItem{
		fyne.NewMenuItem("Open Replays Directory", func() {
			if err := opendir.Open(cfg.VideoDir); err != nil {
				dialog.ShowError(fmt.Errorf("open replays directory: %w", err), window)
			}
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Select Cameras", func() {
			showSelectCamerasDialog(cfg, window)
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Platform Selection", func() {
			showPlatformSelection(cfg, window)
			updateTitle() // Update title after platform selection
		}),
		fyne.NewMenuItem("owlcms Server Address", func() {
			showOwlCMSServerAddress(cfg, window)
		}),
		fyne.NewMenuItemSeparator(),
		remoteCamerasItem,
	}
	var menus []*fyne.Menu

	// Register platform dialog function for monitor package
	monitor.ShowPlatformDialogFunc = func() {
		// Called from the MQTT monitor goroutine.
		fyne.Do(func() {
			showPlatformSelection(cfg, window)
		})
	}

	// Status update goroutine
	go func() {
		var hideTimer *time.Timer
		for msg := range httpServer.StatusChan {
			if hideTimer != nil {
				hideTimer.Stop()
			}

			// Skip showing "Reloading..." in the Fyne window
			if msg.Text == "Reloading..." {
				msg.Text = "Ready"
			}

			text := msg.Text
			isError := strings.HasPrefix(text, "Error:")
			fyne.Do(func() {
				setStatusLabelText(statusLabel, text, isError)
			})

			if msg.Code == httpServer.Ready {
				hideTimer = time.AfterFunc(10*time.Second, func() {
					fyne.Do(func() {
						setStatusLabelText(statusLabel, "Ready", false)
					})
				})
			}
		}
	}()

	start := func(camerasStartupComplete <-chan struct{}) {
		ctx, ok := beginStartupScan()
		if !ok {
			return
		}
		startStartupScans(ctx, cfg, statusLabel, startupMessages, camerasStartupComplete)
	}
	startServer := func(camerasAvailable, replaysAvailable bool) {
		if isTerminated() {
			return
		}
		go httpServer.StartServer(cfg.Port, httpServer.ModuleAvailability{
			Cameras: camerasAvailable,
			Replays: replaysAvailable,
		})
	}
	var restartMu sync.Mutex
	restart := func() {
		go func() {
			restartMu.Lock()
			defer restartMu.Unlock()

			logging.InfoLogger.Println("Restarting replays module...")
			stopServices(false)
			startServer(camerasAvailable, true)
			start(nil)
		}()
	}
	replaysMenuItems = append(replaysMenuItems,
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Restart Replays", restart),
	)
	menus = []*fyne.Menu{
		fyne.NewMenu("Replays", replaysMenuItems...),
	}

	setCamerasAvailable := func(available bool) {
		camerasAvailable = available
		remoteCamerasItem.Disabled = available
	}

	return &UI{
		Content:             content,
		Menus:               menus,
		Start:               start,
		StartServer:         startServer,
		SetCamerasAvailable: setCamerasAvailable,
	}
}
