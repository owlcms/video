package replays

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
	"github.com/owlcms/video/internal/config"
	replayscfg "github.com/owlcms/video/internal/config/replays"
	"github.com/owlcms/video/internal/logging"
	"github.com/owlcms/video/internal/monitor"
	"github.com/owlcms/video/internal/recording"
)

type startupScanResult struct {
	order int
	text  string
}

const startupFakeReplayDuration = 2 * time.Second
const startupFakeReplayTimeout = 10 * time.Second
const startupCameraProbeSuccessText = "Cameras Module streams: fake replay test completed."

var runFakeReplayTest = recording.RunFakeReplayTestContext

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

func startStartupScans(ctx context.Context, cfg *replayscfg.Config, statusLabel, startupLabel *widget.Label, camerasStartupComplete <-chan struct{}) {
	if cfg == nil || statusLabel == nil || startupLabel == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	noteText := strings.TrimSpace(localMulticastMismatchNote(cfg))
	messages := make([]string, 3)
	messages[0] = noteText
	if !config.NoMQTT {
		messages[1] = "Scanning for owlcms server..."
	}
	if cfg.Multicast.Enabled && len(cfg.Cameras) > 0 {
		messages[2] = "Testing Cameras Module streams..."
	}
	initialMessage := combineStartupMessages(messages...)
	fyne.Do(func() {
		if ctx.Err() != nil {
			return
		}
		setMessageLabelText(startupLabel, initialMessage)
	})

	go func() {
		results := make(chan startupScanResult, 3)
		var wg sync.WaitGroup

		if config.NoMQTT {
			logging.InfoLogger.Println("MQTT autodiscovery disabled via -noMQTT flag")
			fyne.Do(func() {
				if ctx.Err() != nil {
					return
				}
				setStatusLabelText(statusLabel, "MQTT disabled", false)
			})
			results <- startupScanResult{order: 1, text: ""}
		} else {
			wg.Add(1)
			go func() {
				defer wg.Done()
				broker, err := monitor.UpdateOwlcmsAddress(cfg, moduleConfigPath)
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					logging.ErrorLogger.Printf("Failed to find MQTT broker: %v", err)
					fyne.Do(func() {
						if ctx.Err() != nil {
							return
						}
						setStatusLabelText(statusLabel, "", false)
					})
					results <- startupScanResult{order: 1, text: fmt.Sprintf("Error: Could not find owlcms server - %v", err)}
					return
				}

				cfg.OwlCMS = broker
				fyne.Do(func() {
					if ctx.Err() != nil {
						return
					}
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
				if camerasStartupComplete != nil {
					select {
					case <-camerasStartupComplete:
					case <-ctx.Done():
						return
					}
				}
				missing := runFakeReplayProbe(ctx, cfg.Cameras)
				if ctx.Err() != nil {
					return
				}
				if len(missing) == 0 {
					results <- startupScanResult{order: 2, text: startupCameraProbeSuccessText}
					return
				}

				logging.ErrorLogger.Printf("Startup fake replay test failed for: %s", strings.Join(missing, ", "))
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
			if ctx.Err() != nil {
				continue
			}
			message := applyStartupScanResult(messages, result)
			fyne.Do(func() {
				if ctx.Err() != nil {
					return
				}
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

func runFakeReplayProbe(ctx context.Context, cameras []config.CameraConfiguration) []string {
	labelsByPort := loadStartupCameraStreamLabelsByPort()
	labels := make([]string, len(cameras))
	for index, camera := range cameras {
		labels[index] = fmt.Sprintf("camera %d", index+1)
		if port := startupCameraPort(camera); port > 0 {
			labels[index] = fmt.Sprintf("camera %d (port %d)", index+1, port)
			if configuredLabel := strings.TrimSpace(labelsByPort[port]); configuredLabel != "" {
				labels[index] = configuredLabel
			}
		}
	}

	missing := make([]string, 0)
	for index, result := range runFakeReplayTest(ctx, cameras, startupFakeReplayDuration, startupFakeReplayTimeout) {
		if ctx.Err() != nil {
			return nil
		}
		if result.Err != nil {
			logging.WarningLogger.Printf("Startup fake replay test failed for %s: %v", labels[index], result.Err)
			missing = append(missing, labels[index])
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
	return fmt.Sprintf("Error: fake replay test failed on %s.", strings.Join(missing, ", "))
}

func startupCameraPort(camera config.CameraConfiguration) int {
	raw := strings.TrimSpace(camera.FfmpegCamera)
	if raw == "" || !strings.HasPrefix(strings.ToLower(raw), "udp:") {
		return 0
	}

	raw = strings.TrimPrefix(raw, "udp://")
	raw = strings.TrimPrefix(raw, "udp:")
	raw = strings.TrimSpace(raw)
	separator := strings.LastIndex(raw, ":")
	if separator < 0 {
		return 0
	}
	var port int
	if _, err := fmt.Sscanf(raw[separator+1:], "%d", &port); err != nil || port <= 0 || port > 65535 {
		return 0
	}
	return port
}
