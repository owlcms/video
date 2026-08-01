package ffmpeg

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/logging"
)

//go:embed ffmpeg.toml
var defaultConfig []byte

// Config is the top-level configuration parsed from ffmpeg.toml.
// It defines machine-specific encoder and camera-selection behaviour
// shared between cameras and replays on the same machine.
type Config struct {
	Cameras  CamerasSelection `toml:"cameras"`
	Software SoftwareEncoder  `toml:"software"`
	Encoders []EncoderConfig  `toml:"encoder"`
	Output   OutputConfig     `toml:"output"`
}

// CamerasSelection holds camera filtering and mode selection settings.
type CamerasSelection struct {
	FormatPriority []string `toml:"formatPriority"`
	ModePriority   []string `toml:"modePriority"`
}

// SoftwareEncoder holds the software (libx264) fallback parameters.
type SoftwareEncoder struct {
	OutputParameters string `toml:"outputParameters"`
}

// EncoderConfig defines one hardware encoder and its ffmpeg parameters.
type EncoderConfig struct {
	Name             string   `toml:"name"`
	Description      string   `toml:"description"`
	InputParameters  string   `toml:"inputParameters"`
	OutputParameters string   `toml:"outputParameters"`
	VideoFilter      string   `toml:"videoFilter"`
	TestInit         string   `toml:"testInit"`
	Platform         string   `toml:"platform"`   // "v4l2"/"dshow"/"avfoundation" preferred; OS aliases also accepted; "" means any
	GpuVendors       []string `toml:"gpuVendors"` // optional: nvidia, amd, intel
}

// OutputConfig holds common output flags.
type OutputConfig struct {
	GopMultiplier int    `toml:"gopMultiplier"`
	ExtraFlags    string `toml:"extraFlags"`
}

// ModePriorityEntry is a parsed version of a "WxH@FPS" string from the config.
type ModePriorityEntry struct {
	Width  int
	Height int
	MinFps int
}

// ResolveConfigPath returns the expected file-system path for ffmpeg.toml.
func ResolveConfigPath() string {
	return config.FFmpegConfigPath()
}

// LoadConfig loads ffmpeg.toml independently (no merging with other files).
// Search order: shared config dir → exe dir → cwd → install dir → embedded default.
func LoadConfig() (*Config, error) {
	var cfg Config

	// Collect candidate base directories
	baseDirs := []string{}
	if exe, err := os.Executable(); err == nil {
		baseDirs = append(baseDirs, filepath.Dir(exe))
	}
	if cwd, err := os.Getwd(); err == nil {
		baseDirs = append(baseDirs, cwd)
	}
	baseDirs = append(baseDirs, config.GetInstallDir())

	// Accepted filenames (backwards-compatible aliases)
	filenames := []string{"ffmpeg.toml", "cameras_config.toml", "camera_configs.toml"}

	loaded := false

	// 1) Try the shared config directory first
	sharedBaseDir := filepath.Dir(ResolveConfigPath())
	for _, name := range filenames {
		path := filepath.Join(sharedBaseDir, name)
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("FFmpeg config: %s\n", path)
			logging.InfoLogger.Printf("Loading ffmpeg config from %s", path)
			if _, err := toml.DecodeFile(path, &cfg); err != nil {
				return nil, fmt.Errorf("failed to parse %s: %w", path, err)
			}
			loaded = true
			break
		}
	}

	// 2) Fall back to candidate directories
	if !loaded {
		for _, dir := range baseDirs {
			for _, name := range filenames {
				path := filepath.Join(dir, name)
				if _, err := os.Stat(path); err == nil {
					fmt.Printf("FFmpeg config: %s\n", path)
					logging.InfoLogger.Printf("Loading ffmpeg config from %s", path)
					if _, err := toml.DecodeFile(path, &cfg); err != nil {
						return nil, fmt.Errorf("failed to parse %s: %w", path, err)
					}
					loaded = true
					break
				}
			}
			if loaded {
				break
			}
		}
	}

	// 3) Embedded default
	if !loaded {
		fmt.Println("FFmpeg config: using embedded defaults")
		logging.InfoLogger.Println("No ffmpeg.toml found, using embedded defaults")
		defaultCfg, err := parseEmbeddedDefaultConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to parse embedded ffmpeg.toml: %w", err)
		}
		cfg = defaultCfg
	}

	cfg.applyDefaults()
	cfg.filterEncodersForPlatform()
	return &cfg, nil
}

func parseEmbeddedDefaultConfig() (Config, error) {
	var cfg Config
	_, err := toml.Decode(string(defaultConfig), &cfg)
	return cfg, err
}

// ExtractDefaultConfigTo creates ffmpeg.toml at configPath if it does not
// already exist.
func ExtractDefaultConfigTo(configPath string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("create ffmpeg configuration directory: %w", err)
	}
	if _, err := os.Stat(configPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check ffmpeg configuration %s: %w", configPath, err)
	}
	if err := os.WriteFile(configPath, defaultConfig, 0644); err != nil {
		return fmt.Errorf("write ffmpeg configuration %s: %w", configPath, err)
	}
	return nil
}

// LoadConfigFromFile loads a specific ffmpeg.toml file.
func LoadConfigFromFile(configPath string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", configPath, err)
	}
	cfg.applyDefaults()
	cfg.filterEncodersForPlatform()
	return &cfg, nil
}

// filterEncodersForPlatform removes encoder entries that don't match the current OS.
// Platform values can be OS names or capture API names ("v4l2" for Linux,
// "dshow" for Windows, and "avfoundation" for macOS).
func (c *Config) filterEncodersForPlatform() {
	var filtered []EncoderConfig
	for _, enc := range c.Encoders {
		if encoderPlatformMatch(enc.Platform) {
			filtered = append(filtered, enc)
		}
	}
	c.Encoders = filtered
}

// encoderPlatformMatch reports whether a platform tag matches the current OS.
func encoderPlatformMatch(platform string) bool {
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

// applyDefaults fills in zero-value fields with sensible defaults.
func (c *Config) applyDefaults() {
	defaults, err := parseEmbeddedDefaultConfig()
	if err != nil {
		logging.WarningLogger.Printf("Failed to parse embedded ffmpeg defaults: %v", err)
	}
	if c.Software.OutputParameters == "" {
		c.Software.OutputParameters = defaults.Software.OutputParameters
		if c.Software.OutputParameters == "" {
			c.Software.OutputParameters = "-c:v libx264 -preset ultrafast -tune zerolatency -profile:v main -pix_fmt yuv420p -bf 0 -sc_threshold 0 -flags +cgop -b:v 4M -maxrate 4M -bufsize 4M"
		}
	}
	if len(c.Encoders) == 0 && len(defaults.Encoders) > 0 {
		c.Encoders = append([]EncoderConfig(nil), defaults.Encoders...)
	}
	if len(c.Cameras.FormatPriority) == 0 {
		c.Cameras.FormatPriority = append([]string(nil), defaults.Cameras.FormatPriority...)
		if len(c.Cameras.FormatPriority) == 0 {
			c.Cameras.FormatPriority = []string{"h264", "mjpeg"}
		}
	}
	if len(c.Cameras.ModePriority) == 0 {
		c.Cameras.ModePriority = append([]string(nil), defaults.Cameras.ModePriority...)
		if len(c.Cameras.ModePriority) == 0 {
			c.Cameras.ModePriority = []string{
				"1920x1080@59",
				"1280x720@59",
				"1920x1080@29",
				"1280x720@29",
			}
		}
	}
	if c.Output.GopMultiplier == 0 {
		c.Output.GopMultiplier = defaults.Output.GopMultiplier
		if c.Output.GopMultiplier == 0 {
			c.Output.GopMultiplier = 1
		}
	}
	if c.Output.ExtraFlags == "" {
		c.Output.ExtraFlags = defaults.Output.ExtraFlags
		if c.Output.ExtraFlags == "" {
			c.Output.ExtraFlags = "-an -f mpegts"
		}
	}
}

// FormatPriorityValue returns the priority of a pixel format (higher = better).
// Based on the order in [cameras] formatPriority (first = highest).
func (c *Config) FormatPriorityValue(pixFmt string) int {
	n := len(c.Cameras.FormatPriority)
	for i, f := range c.Cameras.FormatPriority {
		if f == pixFmt {
			return n - i // first entry gets highest value
		}
	}
	return 0
}

// ParseModePriority parses the modePriority list into structured entries.
func (c *Config) ParseModePriority() []ModePriorityEntry {
	var entries []ModePriorityEntry
	for _, s := range c.Cameras.ModePriority {
		entry, err := parseModePriorityString(s)
		if err != nil {
			logging.WarningLogger.Printf("Ignoring invalid modePriority entry %q: %v", s, err)
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// ProfilePriorityValue returns the profile priority for a given resolution+fps.
// Higher index in the modePriority list = lower priority (first = best).
func (c *Config) ProfilePriorityValue(width, height, fps int) int {
	entries := c.ParseModePriority()
	n := len(entries)
	for i, e := range entries {
		if width == e.Width && height == e.Height && fps >= e.MinFps {
			return n - i
		}
	}
	return 0
}

// parseModePriorityString parses "WIDTHxHEIGHT@FPS" into a ModePriorityEntry.
func parseModePriorityString(s string) (ModePriorityEntry, error) {
	atIdx := strings.Index(s, "@")
	if atIdx < 0 {
		return ModePriorityEntry{}, fmt.Errorf("missing @ separator")
	}
	dimPart := s[:atIdx]
	fpsPart := s[atIdx+1:]

	xIdx := strings.Index(dimPart, "x")
	if xIdx < 0 {
		return ModePriorityEntry{}, fmt.Errorf("missing x separator in dimensions")
	}

	w, err := strconv.Atoi(dimPart[:xIdx])
	if err != nil {
		return ModePriorityEntry{}, fmt.Errorf("invalid width: %w", err)
	}
	h, err := strconv.Atoi(dimPart[xIdx+1:])
	if err != nil {
		return ModePriorityEntry{}, fmt.Errorf("invalid height: %w", err)
	}
	f, err := strconv.Atoi(fpsPart)
	if err != nil {
		return ModePriorityEntry{}, fmt.Errorf("invalid fps: %w", err)
	}

	return ModePriorityEntry{Width: w, Height: h, MinFps: f}, nil
}
