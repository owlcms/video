package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/config/replays"
	"github.com/owlcms/video/internal/httpServer"
	"github.com/owlcms/video/internal/logging"
	"github.com/owlcms/video/internal/recording"
	"github.com/owlcms/video/internal/state"
)

// ConfigMessage represents the configuration data from owlcms
type ConfigMessage struct {
	JurySize  int      `json:"jurySize"`
	Platforms []string `json:"platforms"`
	Version   string   `json:"version"`
}

var (
	mqttClient mqtt.Client
	// Channel to notify when platform list is updated
	PlatformListChan = make(chan []string, 1)
	// Add new function to show platform dialog
	ShowPlatformDialogFunc func()
	lastConfigPayload      string
	lastConfigTimestamp    time.Time
	// Store the list of validated platforms
	ValidatedPlatforms []string
)

// Monitor listens to the owlcms broker for specific messages
func Monitor(cfg *replays.Config) {
	// First establish MQTT connection
	mqttAddress := fmt.Sprintf("tcp://%s:1883", cfg.OwlCMS)
	opts := mqtt.NewClientOptions().AddBroker(mqttAddress)

	// Get machine's IP address for unique client ID
	ip, err := getLocalIP()
	if err != nil {
		logging.ErrorLogger.Printf("Failed to get local IP address: %v", err)
		return
	}
	opts.SetClientID(fmt.Sprintf("replays-monitor-%s", ip))
	opts.SetDefaultPublishHandler(messageHandler())
	opts.SetResumeSubs(true) // Ensure subscriptions are resumed on reconnect

	mqttClient = mqtt.NewClient(opts)
	if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
		logging.ErrorLogger.Printf("Failed to connect to MQTT broker: %v", token.Error())
		return
	}

	// Wait for connection to be established
	attempts := 0
	maxAttempts := 5
	for !mqttClient.IsConnected() && attempts < maxAttempts {
		time.Sleep(100 * time.Millisecond)
		attempts++
	}

	if !mqttClient.IsConnected() {
		logging.ErrorLogger.Printf("Failed to establish MQTT connection after %d attempts", maxAttempts)
		return
	}

	// First subscribe to config topic
	configTopic := "owlcms/fop/config"
	logging.InfoLogger.Printf("Subscribing to topic %s", configTopic)
	if token := mqttClient.Subscribe(configTopic, 0, nil); token.Wait() && token.Error() != nil {
		logging.ErrorLogger.Printf("Failed to subscribe to topic %s: %v", configTopic, token.Error())
		mqttClient.Disconnect(250)
		return
	}

	// Get platform list and validate current platform
	platforms, isValid := GetValidatedPlatforms(cfg)
	if platforms == nil {
		logging.ErrorLogger.Printf("No response from MQTT broker for platform list")
		mqttClient.Disconnect(250)
		return
	}

	if !isValid {
		if !AutoSelectPlatform(cfg, platforms) && len(platforms) > 1 {
			if ShowPlatformDialogFunc != nil {
				logging.InfoLogger.Println("Multiple platforms detected, showing selection dialog")
				ShowPlatformDialogFunc()
			}
			mqttClient.Disconnect(250)
			return // Don't proceed without platform selection
		}
	}

	// Don't proceed if no platform is selected
	if cfg.Platform == "" {
		logging.ErrorLogger.Printf("No platform selected, cannot subscribe to MQTT topics")
		mqttClient.Disconnect(250)
		return
	}

	// Subscribe to platform-specific topics
	platformTopics := []string{
		"owlcms/fop/start",
		"owlcms/fop/stop",
		"owlcms/fop/refereesDecision",
	}

	for _, topic := range platformTopics {
		fullTopic := topic + "/" + cfg.Platform
		logging.InfoLogger.Printf("Subscribing to topic %s", fullTopic)
		if token := mqttClient.Subscribe(fullTopic, 0, nil); token.Wait() && token.Error() != nil {
			logging.ErrorLogger.Printf("Failed to subscribe to topic %s: %v", fullTopic, token.Error())
		}
	}

	logging.InfoLogger.Printf("MQTT monitoring started on %s", mqttAddress)
}

func validatePlatform(cfg *replays.Config, platforms []string) bool {
	if cfg.Platform == "" {
		return false
	}
	for _, p := range platforms {
		if p == cfg.Platform {
			return true
		}
	}
	// Platform not found in list, clear it
	cfg.Platform = ""
	return false
}

// GetValidatedPlatforms returns the validated list of platforms and whether the current platform is valid
func GetValidatedPlatforms(cfg *replays.Config) ([]string, bool) {
	if mqttClient == nil || !mqttClient.IsConnected() {
		logging.ErrorLogger.Printf("MQTT client not initialized or not connected - cannot retrieve platform list")
		return nil, false
	}

	// Clean out any pending messages from previous requests
	select {
	case <-PlatformListChan:
	default:
	}

	// Request fresh platform list using existing MQTT client
	topic := "owlcms/config"
	token := mqttClient.Publish(topic, 0, false, "requesting configuration")
	if token.Wait() && token.Error() != nil {
		logging.ErrorLogger.Printf("Failed to publish config request: %v", token.Error())
		return nil, false
	}

	// Wait for response
	select {
	case platforms := <-PlatformListChan:
		logging.InfoLogger.Printf("Retrieved platforms from MQTT config: %v", platforms)
		isValid := validatePlatform(cfg, platforms)
		// Store the validated platforms
		ValidatedPlatforms = platforms
		return platforms, isValid
	case <-time.After(2 * time.Second):
		logging.ErrorLogger.Printf("No response from MQTT broker for platform list")
		return nil, false
	}
}

// PublishConfig simplified as it's now only used with the existing connection
func PublishConfig(platform string) {
	if mqttClient == nil || !mqttClient.IsConnected() {
		logging.ErrorLogger.Printf("Cannot publish config request: MQTT client not connected")
		return
	}
	topic := "owlcms/config"
	token := mqttClient.Publish(topic, 0, false, "requesting configuration")
	if token.Wait() && token.Error() != nil {
		logging.ErrorLogger.Printf("Failed to publish config request: %v", token.Error())
	}
}

func messageHandler() mqtt.MessageHandler {
	return func(client mqtt.Client, msg mqtt.Message) {
		topic := msg.Topic()
		payload := string(msg.Payload())

		// Split topic for message handling
		topicParts := strings.Split(topic, "/")
		if len(topicParts) < 3 {
			return
		}
		topic = strings.Join(topicParts[:3], "/")

		switch topic {
		case "owlcms/fop/start":
			handleStart(payload)
		case "owlcms/fop/stop":
			handleStop(payload)
		case "owlcms/fop/break":
			handleBreak(payload)
		case "owlcms/fop/refereesDecision":
			handleRefereesDecision()
		case "owlcms/fop/config":
			handleConfig(payload)
		}
	}
}

func handleBreak(payload string) {
	if payload == "GROUP_DONE" {
		logging.InfoLogger.Println("Session ended")
		state.CurrentSession = ""                                    // Clear current session
		httpServer.SendStatus(httpServer.Ready, "No active session") // Update web UI with session state
	}
}

func handleConfig(payload string) {
	// Discard identical responses received within a second
	if payload == lastConfigPayload && time.Since(lastConfigTimestamp) < time.Second {
		//logging.InfoLogger.Printf("Discarding duplicate config response")
		return
	}

	lastConfigPayload = payload
	lastConfigTimestamp = time.Now()

	var configMsg ConfigMessage
	if err := json.Unmarshal([]byte(payload), &configMsg); err != nil {
		logging.ErrorLogger.Printf("Error parsing config message: %v", err)
		return
	}

	// Store available platforms
	state.AvailablePlatforms = configMsg.Platforms
	// Also update ValidatedPlatforms
	ValidatedPlatforms = configMsg.Platforms

	// Log the available platforms
	logging.InfoLogger.Printf("Available platforms: %v", state.AvailablePlatforms)

	// Send platform list to channel
	select {
	case PlatformListChan <- configMsg.Platforms:
	default:
		// If the channel is full, do not block
	}
}

func handleStart(payload string) {
	// Handle start message
	logging.InfoLogger.Printf("Handling start message: %s", payload)
	state.UpdateStateFromStartMessage(payload)

	// Stop any existing recording
	if recording.IsRecording() {
		logging.InfoLogger.Println("Stopping running recordings")
		if _, err := recording.StopRecording(); err != nil {
			logging.ErrorLogger.Printf("Error stopping recording: %v", err)
			return
		}
	}

	// Clean up old .mkv files in the video directory
	cleanUpOldMkvFiles()

	if err := recording.StartRecording(state.CurrentAthlete, state.CurrentLiftType, state.CurrentAttempt); err != nil {
		logging.ErrorLogger.Printf("Failed to start recording: %v", err)
		return
	}
}

// cleanUpOldMkvFiles finds and deletes .mkv files directly in the video directory
func cleanUpOldMkvFiles() {
	videoDir := config.GetVideoDir()
	if videoDir == "" {
		logging.ErrorLogger.Println("Video directory not set, cannot clean up old .mkv files")
		return
	}

	// Run in background to avoid delaying recording start
	go func() {
		files, err := os.ReadDir(videoDir)
		if err != nil {
			logging.ErrorLogger.Printf("Error reading video directory for cleanup: %v", err)
			return
		}

		var deletedCount int
		for _, file := range files {
			// Skip directories and non-mkv files
			if file.IsDir() || !strings.HasSuffix(strings.ToLower(file.Name()), ".mkv") {
				continue
			}

			filePath := filepath.Join(videoDir, file.Name())
			logging.InfoLogger.Printf("Removing old temporary file: %s", filePath)

			if err := os.Remove(filePath); err != nil {
				logging.ErrorLogger.Printf("Failed to remove old .mkv file %s: %v", filePath, err)
			} else {
				deletedCount++
			}
		}

		if deletedCount > 0 {
			logging.InfoLogger.Printf("Cleaned up %d old .mkv files from video directory", deletedCount)
		}
	}()
}

func handleStop(payload string) {
	// Handle stop message
	logging.InfoLogger.Printf("Handling stop message: %s", payload)
	state.UpdateStateFromStopMessage(payload)
}

func handleRefereesDecision() {
	// Handle refereesDecision message
	logging.InfoLogger.Printf("Handling refereesDecision message")
	state.LastDecisionTime = time.Now().UnixNano() / int64(time.Millisecond)

	logging.InfoLogger.Println("Trimming video")
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.ErrorLogger.Printf("Recovered from panic in decision handler: %v", r)
			}
		}()

		// wait to see the decision on the replay
		time.Sleep(2 * time.Second)
		if err := recording.StopRecordingAndTrim(state.LastDecisionTime); err != nil {
			logging.ErrorLogger.Printf("Error during trimming: %v", err)
			return
		}
	}()
}

// AutoSelectPlatform attempts to automatically select a platform when there's only one available
func AutoSelectPlatform(cfg *replays.Config, platforms []string) bool {
	if len(platforms) == 1 {
		// Auto-select single platform
		platform := platforms[0]
		configFilePath := config.ReplaysConfigPath()
		if err := replays.UpdatePlatform(configFilePath, platform); err != nil {
			logging.ErrorLogger.Printf("Error updating platform: %v", err)
			return false
		}
		logging.InfoLogger.Printf("Automatically selected platform: %s", platform)
		cfg.Platform = platform
		return true
	}
	return false
}

// getLocalIP retrieves the local IP address of the machine
func getLocalIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				return ipNet.IP.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no IP address found")
}

// Add a getter function for ValidatedPlatforms
func GetStoredPlatforms() []string {
	return ValidatedPlatforms
}

// DisconnectMQTT cleanly disconnects the MQTT client
func DisconnectMQTT() {
	if mqttClient != nil && mqttClient.IsConnected() {
		logging.InfoLogger.Println("Disconnecting from MQTT broker")
		mqttClient.Disconnect(250)
	}
}

// Reconnect re-establishes the MQTT session so that a new server address or
// platform takes effect without restarting. It blocks and must not be called
// from the UI thread.
func Reconnect(cfg *replays.Config) {
	DisconnectMQTT()
	Monitor(cfg)
}

// GetPlatformsForSelection returns platforms for user selection, with appropriate error handling
func GetPlatformsForSelection() ([]string, error) {
	if mqttClient == nil || !mqttClient.IsConnected() {
		return nil, errors.New("No connection to owlcms server. Please check that owlcms is running and the server address is correct.")
	}

	if len(ValidatedPlatforms) == 0 {
		return nil, errors.New("No platforms available. Please ensure owlcms is properly configured with competition platforms.")
	}

	return ValidatedPlatforms, nil
}

// IsConnected returns whether the MQTT client is connected
func IsConnected() bool {
	return mqttClient != nil && mqttClient.IsConnected()
}
