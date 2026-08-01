package replays

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
	"github.com/owlcms/video/internal/config"
	camerascfg "github.com/owlcms/video/internal/config/cameras"
	replayscfg "github.com/owlcms/video/internal/config/replays"
	"github.com/owlcms/video/internal/logging"
	"github.com/owlcms/video/internal/monitor"
)

type startupScanResult struct {
	order int
	text  string
}

type cameraStreamProbeTarget struct {
	order     int
	index     int
	label     string
	ip        net.IP
	port      int
	multicast bool
}

type cameraStreamProbeResult struct {
	index  int
	label  string
	active bool
}

const startupCameraProbeTimeout = 150 * time.Millisecond
const startupCameraProbeSuccessText = "Cameras Module streams: all streams are producing data."

func mqttBrokerAddressText(broker string) string {
	trimmed := strings.TrimSpace(broker)
	if trimmed == "" {
		return ""
	}

	if host, port, err := net.SplitHostPort(trimmed); err == nil && strings.TrimSpace(host) != "" && strings.TrimSpace(port) != "" {
		return net.JoinHostPort(host, port)
	}

	trimmed = strings.Trim(trimmed, "[]")
	return net.JoinHostPort(trimmed, "1883")
}

func startupMQTTProbeSuccessText(broker string) string {
	address := mqttBrokerAddressText(broker)
	if address == "" {
		return "MQTT server found."
	}
	return fmt.Sprintf("MQTT server found at %s.", address)
}

func (target cameraStreamProbeTarget) listenEndpoint() string {
	ipText := "0.0.0.0"
	if target.ip != nil && target.ip.To4() == nil {
		ipText = "::"
	}
	if target.multicast && target.ip != nil {
		ipText = target.ip.String()
	}
	return net.JoinHostPort(ipText, strconv.Itoa(target.port))
}

func setStatusLabelText(label *widget.Label, text string, bold bool) {
	if label == nil {
		return
	}
	label.TextStyle = fyne.TextStyle{Bold: bold}
	label.SetText(text)
	if strings.TrimSpace(text) == "" {
		label.Hide()
	} else {
		label.Show()
	}
	label.Refresh()
}

func setMessageLabelText(label *widget.Label, text string) {
	if label == nil {
		return
	}
	label.SetText(text)
	if strings.TrimSpace(text) == "" {
		label.Hide()
	} else {
		label.Show()
	}
	label.Refresh()
}

func startStartupScans(cfg *replayscfg.Config, statusLabel, startupLabel *widget.Label) {
	if cfg == nil || statusLabel == nil || startupLabel == nil {
		return
	}
	noteText := strings.TrimSpace(localMulticastMismatchNote(cfg))
	messages := make([]string, 3)
	messages[0] = noteText
	if !config.NoMQTT {
		messages[1] = "Scanning for owlcms server..."
	}
	if cfg.Multicast.Enabled && len(cfg.Cameras) > 0 {
		messages[2] = "Scanning Cameras Module streams..."
	}
	setMessageLabelText(startupLabel, combineStartupMessages(messages...))

	go func() {
		results := make(chan startupScanResult, 3)
		var wg sync.WaitGroup

		if config.NoMQTT {
			logging.InfoLogger.Println("MQTT autodiscovery disabled via -noMQTT flag")
			fyne.Do(func() {
				setStatusLabelText(statusLabel, "MQTT disabled", false)
			})
			results <- startupScanResult{order: 1, text: ""}
		} else {
			wg.Add(1)
			go func() {
				defer wg.Done()
				broker, err := monitor.UpdateOwlcmsAddress(cfg, moduleConfigPath)
				if err != nil {
					logging.ErrorLogger.Printf("Failed to find MQTT broker: %v", err)
					fyne.Do(func() {
						setStatusLabelText(statusLabel, "", false)
					})
					results <- startupScanResult{order: 1, text: fmt.Sprintf("Error: Could not find owlcms server - %v", err)}
					return
				}

				cfg.OwlCMS = broker
				fyne.Do(func() {
					setStatusLabelText(statusLabel, "Ready", false)
				})
				results <- startupScanResult{order: 1, text: startupMQTTProbeSuccessText(broker)}

				// Start MQTT monitor which handles platform list retrieval.
				go monitor.Monitor(cfg)
			}()
		}

		if cfg.Multicast.Enabled && len(cfg.Cameras) > 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				missing := probeConfiguredCameraStreams(cfg.Cameras, startupCameraProbeTimeout)
				if len(missing) == 0 {
					results <- startupScanResult{order: 2, text: startupCameraProbeSuccessText}
					return
				}

				logging.ErrorLogger.Printf("Startup camera stream probe found no packets on: %s", strings.Join(missing, ", "))
				results <- startupScanResult{order: 2, text: cameraStreamProbeFailureText(missing)}
			}()
		} else {
			results <- startupScanResult{order: 2, text: ""}
		}

		go func() {
			wg.Wait()
			close(results)
		}()

		for result := range results {
			message := applyStartupScanResult(messages, result)
			fyne.Do(func() {
				setMessageLabelText(startupLabel, message)
			})
		}
	}()
}

func applyStartupScanResult(messages []string, result startupScanResult) string {
	if result.order >= 0 && result.order < len(messages) {
		messages[result.order] = result.text
	}
	return combineStartupMessages(messages...)
}

func orderedStartupScanMessages(count int, results []startupScanResult) string {
	if count <= 0 {
		return ""
	}
	messages := make([]string, count)
	for _, result := range results {
		if result.order >= 0 && result.order < len(messages) {
			messages[result.order] = result.text
		}
	}
	return combineStartupMessages(messages...)
}

func combineStartupMessages(messages ...string) string {
	lines := make([]string, 0, len(messages))
	for _, message := range messages {
		trimmed := strings.TrimSpace(message)
		if trimmed == "" {
			continue
		}
		lines = append(lines, trimmed)
	}
	return strings.Join(lines, "\n")
}

func probeConfiguredCameraStreams(cameras []config.CameraConfiguration, timeout time.Duration) []string {
	labelsByPort := loadStartupCameraStreamLabelsByPort()
	targets := make([]cameraStreamProbeTarget, 0, len(cameras))
	for index, camera := range cameras {
		if target, ok := parseCameraStreamProbeTarget(index, camera, labelsByPort); ok {
			target.order = len(targets)
			targets = append(targets, target)
		}
	}
	if len(targets) == 0 {
		return nil
	}

	results := make(chan cameraStreamProbeResult, len(targets))
	var wg sync.WaitGroup
	for _, target := range targets {
		logging.InfoLogger.Printf("Startup camera stream probe checking %s on %s", target.label, target.listenEndpoint())
		wg.Add(1)
		go func(target cameraStreamProbeTarget) {
			defer wg.Done()
			results <- cameraStreamProbeResult{
				index:  target.order,
				label:  target.label,
				active: probeCameraStreamTarget(target, timeout),
			}
		}(target)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	statusByIndex := make([]bool, len(targets))
	labelByIndex := make([]string, len(targets))
	for result := range results {
		statusByIndex[result.index] = result.active
		labelByIndex[result.index] = result.label
	}

	missing := make([]string, 0)
	for index, active := range statusByIndex {
		if !active {
			missing = append(missing, labelByIndex[index])
		}
	}

	return missing
}

func loadStartupCameraStreamLabelsByPort() map[int]string {
	labelsByPort := make(map[int]string)
	camerasCfg, _, err := loadStartupCamerasConfigForComparison()
	if err != nil {
		return labelsByPort
	}

	for _, stream := range collectLocalCamerasStreams(camerasCfg) {
		if stream.OutputPort <= 0 {
			continue
		}
		labelsByPort[stream.OutputPort] = formatStartupCameraStreamLabel(stream)
	}

	return labelsByPort
}

func formatStartupCameraStreamLabel(stream localCamerasStream) string {
	name := strings.TrimSpace(stream.Name)
	shortID := strings.TrimSpace(stream.ShortID)

	switch {
	case name != "" && shortID != "" && !strings.EqualFold(name, shortID):
		return fmt.Sprintf("%s [%s] (port %d)", name, shortID, stream.OutputPort)
	case name != "":
		return fmt.Sprintf("%s (port %d)", name, stream.OutputPort)
	case shortID != "":
		return fmt.Sprintf("%s (port %d)", shortID, stream.OutputPort)
	default:
		return fmt.Sprintf("port %d", stream.OutputPort)
	}
}

func cameraStreamProbeFailureText(missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("Error: no camera stream packets detected at startup on %s.", strings.Join(missing, ", "))
}

func parseCameraStreamProbeTarget(index int, camera config.CameraConfiguration, labelsByPort map[int]string) (cameraStreamProbeTarget, bool) {
	raw := strings.TrimSpace(camera.FfmpegCamera)
	if raw == "" || !strings.HasPrefix(strings.ToLower(raw), "udp:") {
		return cameraStreamProbeTarget{}, false
	}

	raw = strings.TrimPrefix(raw, "udp://")
	raw = strings.TrimPrefix(raw, "udp:")
	raw = strings.TrimSpace(raw)
	host, portText, err := net.SplitHostPort(raw)
	if err != nil {
		return cameraStreamProbeTarget{}, false
	}

	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil || port <= 0 || port > 65535 {
		return cameraStreamProbeTarget{}, false
	}

	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	label := fmt.Sprintf("camera %d (port %d)", index+1, port)
	if labelsByPort != nil {
		if configuredLabel := strings.TrimSpace(labelsByPort[port]); configuredLabel != "" {
			label = configuredLabel
		}
	}
	return cameraStreamProbeTarget{
		index:     index,
		label:     label,
		ip:        ip,
		port:      port,
		multicast: ip != nil && ip.IsMulticast(),
	}, true
}

func probeCameraStreamTarget(target cameraStreamProbeTarget, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = 350 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	network := "udp4"
	if target.ip != nil && target.ip.To4() == nil {
		network = "udp6"
	}

	if target.multicast {
		conn, err := net.ListenMulticastUDP(network, nil, &net.UDPAddr{IP: target.ip, Port: target.port})
		if err != nil {
			logging.WarningLogger.Printf("Startup camera stream probe failed for %s on %s: %v", target.label, target.listenEndpoint(), err)
			return false
		}
		defer conn.Close()
		if err := conn.SetReadDeadline(deadline); err != nil {
			logging.WarningLogger.Printf("Startup camera stream probe deadline failed for %s on %s: %v", target.label, target.listenEndpoint(), err)
			return false
		}
		buffer := make([]byte, camerascfg.PktSize)
		if _, _, err := conn.ReadFromUDP(buffer); err == nil {
			logging.InfoLogger.Printf("Startup camera stream probe detected packets for %s on %s", target.label, target.listenEndpoint())
			return true
		} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			logging.WarningLogger.Printf("Startup camera stream probe timed out for %s on %s after %s", target.label, target.listenEndpoint(), timeout)
			return false
		} else {
			logging.WarningLogger.Printf("Startup camera stream probe read failed for %s on %s: %v", target.label, target.listenEndpoint(), err)
			return false
		}
	}

	conn, err := net.ListenUDP(network, &net.UDPAddr{Port: target.port})
	if err != nil {
		logging.WarningLogger.Printf("Startup camera stream probe failed for %s on %s: %v", target.label, target.listenEndpoint(), err)
		return false
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(deadline); err != nil {
		logging.WarningLogger.Printf("Startup camera stream probe deadline failed for %s on %s: %v", target.label, target.listenEndpoint(), err)
		return false
	}
	buffer := make([]byte, camerascfg.PktSize)
	if _, _, err := conn.ReadFromUDP(buffer); err == nil {
		logging.InfoLogger.Printf("Startup camera stream probe detected packets for %s on %s", target.label, target.listenEndpoint())
		return true
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		logging.WarningLogger.Printf("Startup camera stream probe timed out for %s on %s after %s", target.label, target.listenEndpoint(), timeout)
		return false
	} else {
		logging.WarningLogger.Printf("Startup camera stream probe read failed for %s on %s: %v", target.label, target.listenEndpoint(), err)
		return false
	}
}
