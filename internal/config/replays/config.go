package replays

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/logging"
)

// Config represents the replays configuration file structure.
type Config struct {
	Port          int                          `toml:"port"`
	VideoDir      string                       `toml:"videoDir"`
	Width         int                          `toml:"width"`
	Height        int                          `toml:"height"`
	Fps           int                          `toml:"fps"`
	OwlCMS        string                       `toml:"owlcms"`
	Platform      string                       `toml:"platform"`
	CamerasServer string                       `toml:"camerasServer"`
	LogFfmpeg     bool                         `toml:"logFfmpeg"`
	Multicast     config.MulticastSettings     `toml:"mpeg-ts"`
	Cameras       []config.CameraConfiguration `toml:"-"`
}

var currentConfig *Config

// LoadConfig loads the configuration from the specified file.
func LoadConfig(configFile string) (*Config, error) {
	if config.InstallDir == "" {
		config.InstallDir = config.GetInstallDir()
	}

	if err := ExtractDefaultConfig(configFile); err != nil {
		return nil, fmt.Errorf("failed to extract default config: %w", err)
	}

	var cfg Config
	if _, err := toml.DecodeFile(configFile, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file '%s': %w\n\nPlease check the file syntax and ensure all values are properly formatted", configFile, err)
	}

	if cfg.VideoDir == "" {
		cfg.VideoDir = "videos"
	}
	if !filepath.IsAbs(cfg.VideoDir) {
		cfg.VideoDir = filepath.Join(config.GetInstallDir(), cfg.VideoDir)
	}
	if err := os.MkdirAll(cfg.VideoDir, os.ModePerm); err != nil {
		return nil, fmt.Errorf("failed to create video directory '%s': %w", cfg.VideoDir, err)
	}
	logging.InfoLogger.Printf("Videos will be stored in: %s", cfg.VideoDir)

	// Replays is a pure MPEG-TS receiver: cameras are always provided as UDP
	// streams from the Cameras module (local or remote). There is no local
	// capture / autodetection path.
	cfg.Multicast.Enabled = true
	cfg.Multicast.ApplyDefaults()

	cameras := cfg.Multicast.BuildCameraConfigs()
	if len(cameras) == 0 {
		logging.WarningLogger.Printf("No camera stream ports configured in the [mpeg-ts] section of %s. Replays will start with no camera sources.", configFile)
	} else {
		logging.InfoLogger.Printf("MPEG-TS receiver: loaded %d camera stream(s) from %s", len(cameras), configFile)
	}

	cfg.Cameras = cameras

	config.SetVideoDir(cfg.VideoDir)
	config.SetVideoConfig(cfg.Width, cfg.Height, cfg.Fps)

	logging.InfoLogger.Printf("Configuration loaded from %s:\n"+
		"    Port: %d\n"+
		"    VideoDir: %s\n",
		configFile, cfg.Port, cfg.VideoDir)

	for i, camera := range cameras {
		suffix := ""
		if i > 0 {
			suffix = strconv.Itoa(i + 1)
		}
		logging.InfoLogger.Printf("Camera stream %s%s:\n"+
			"    FFmpeg Camera: %s\n"+
			"    Format: %s\n"+
			"    Recode: %t",
			"camera", suffix,
			camera.FfmpegCamera, camera.Format, camera.Recode)
	}

	currentConfig = &cfg
	config.LogFfmpeg = cfg.LogFfmpeg
	return &cfg, nil
}

// GetCurrentConfig returns the current configuration.
func GetCurrentConfig() *Config {
	return currentConfig
}

// ValidateCamera checks if camera configuration is correct for the platform.
func (c *Config) ValidateCamera() error {
	if len(c.Cameras) == 0 || c.Cameras[0].FfmpegCamera == "" {
		return fmt.Errorf("camera not configured")
	}
	return nil
}

// UpdateConfigFile updates the owlcms address in the config file.
func UpdateConfigFile(configFile, owlcmsAddress string) error {
	content, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}

	lines := strings.Split(string(content), "\n")
	foundOwlcms := false
	portLineIndex := -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# owlcms =") ||
			strings.HasPrefix(trimmed, "owlcms =") ||
			trimmed == "# owlcms" {
			address := owlcmsAddress
			if strings.Contains(address, ":") {
				address = strings.Split(address, ":")[0]
			}
			leadingSpace := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = fmt.Sprintf("%sowlcms = \"%s\"", leadingSpace, address)
			foundOwlcms = true
			break
		}
		if strings.HasPrefix(trimmed, "port") {
			portLineIndex = i
		}
	}

	if !foundOwlcms && portLineIndex >= 0 {
		address := owlcmsAddress
		if strings.Contains(address, ":") {
			address = strings.Split(address, ":")[0]
		}
		leadingSpace := lines[portLineIndex][:len(lines[portLineIndex])-len(strings.TrimLeft(lines[portLineIndex], " \t"))]
		newLine := fmt.Sprintf("%sowlcms = \"%s\"", leadingSpace, address)
		lines = append(lines[:portLineIndex+1], append([]string{newLine}, lines[portLineIndex+1:]...)...)
	}

	return os.WriteFile(configFile, []byte(strings.Join(lines, "\n")), 0644)
}

// UpdatePlatform updates the platform value in the config file.
func UpdatePlatform(configFile, platform string) error {
	input, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}

	lines := strings.Split(string(input), "\n")
	platformFound := false

	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "platform") {
			lines[i] = fmt.Sprintf("platform = \"%s\"", platform)
			platformFound = true
			break
		}
	}

	if !platformFound {
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "owlcms") {
				newLines := make([]string, 0, len(lines)+1)
				newLines = append(newLines, lines[:i+1]...)
				newLines = append(newLines, fmt.Sprintf("platform = \"%s\"", platform))
				newLines = append(newLines, lines[i+1:]...)
				lines = newLines
				break
			}
		}
	}

	output := strings.Join(lines, "\n")
	if err := os.WriteFile(configFile, []byte(output), 0644); err != nil {
		return fmt.Errorf("failed to write config file: %v", err)
	}
	return nil
}

// UpdateCamerasServer records the remote Video instance that supplies the Cameras inventory.
func UpdateCamerasServer(configFile, server string) error {
	input, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("read replays config: %w", err)
	}

	lines := strings.Split(string(input), "\n")
	serverLine := fmt.Sprintf("camerasServer = %q", strings.TrimSpace(server))
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "camerasServer") {
			lines[index] = serverLine
			return os.WriteFile(configFile, []byte(strings.Join(lines, "\n")), 0644)
		}
	}

	insertAt := len(lines)
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			insertAt = index
			break
		}
	}
	lines = append(lines, "")
	copy(lines[insertAt+1:], lines[insertAt:])
	lines[insertAt] = serverLine
	return os.WriteFile(configFile, []byte(strings.Join(lines, "\n")), 0644)
}

// UpdateMpegTSConfig writes the [mpeg-ts] section to the config file.
func UpdateMpegTSConfig(configFile string, settings config.MulticastSettings) error {
	content, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}

	lines := strings.Split(string(content), "\n")
	sectionStart := -1
	sectionEnd := len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[mpeg-ts]" {
			sectionStart = i
			continue
		}
		if sectionStart >= 0 && sectionStart != i &&
			strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[") {
			sectionEnd = i
			break
		}
	}

	settings.ApplyDefaults()
	newSection := []string{
		"[mpeg-ts]",
		fmt.Sprintf("    enabled = %v", settings.Enabled),
		fmt.Sprintf("    ip = \"%s\"", settings.IP),
		fmt.Sprintf("    camera1Port = %d", settings.Camera1Port),
		fmt.Sprintf("    camera2Port = %d", settings.Camera2Port),
		fmt.Sprintf("    camera3Port = %d", settings.Camera3Port),
		fmt.Sprintf("    camera4Port = %d", settings.Camera4Port),
	}

	var newLines []string
	if sectionStart >= 0 {
		newLines = append(newLines, lines[:sectionStart]...)
		newLines = append(newLines, newSection...)
		newLines = append(newLines, lines[sectionEnd:]...)
	} else {
		insertAt := len(lines)
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[windows") || strings.HasPrefix(trimmed, "[linux") {
				insertAt = i
				break
			}
		}
		newLines = append(newLines, lines[:insertAt]...)
		newLines = append(newLines, "")
		newLines = append(newLines, newSection...)
		newLines = append(newLines, "")
		newLines = append(newLines, lines[insertAt:]...)
	}

	return os.WriteFile(configFile, []byte(strings.Join(newLines, "\n")), 0644)
}
