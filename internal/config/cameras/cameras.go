package cameras

import (
	"bytes"
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/logging"
)

//go:embed config.toml
var defaultInstanceConfig []byte

var configSourcePath string

// Config is the per-instance runtime configuration for the cameras program.
// Encoder/priority settings live in the separate ffmpeg package.
type Config struct {
	Multicast         MulticastConfig    `toml:"multicast"`
	Unicast           UnicastConfig      `toml:"unicast"`
	Cameras           CamerasSettings    `toml:"cameras"`
	RTSPSources       []RTSPSource       `toml:"rtsp"`
	DeviceAssignments []DeviceAssignment `toml:"deviceAssignment"`
}

// PktSize is the fixed UDP packet size for MPEG-TS streaming (7 × 188).
const PktSize = 1316

// PreviewLoopbackAddress is the mandatory local unicast leg used by preview.
const PreviewLoopbackAddress = "127.0.0.1"

// MulticastConfig holds multicast streaming settings.
type MulticastConfig struct {
	IP        string `toml:"ip"`
	StartPort int    `toml:"startPort"`
	LocalOnly bool   `toml:"localOnly"`
}

// UnicastDestination is one unicast target with an optional enabled flag.
type UnicastDestination struct {
	Address string `toml:"address"`
	Enabled bool   `toml:"enabled"`
}

// UnicastConfig holds unicast tee streaming settings.
// When Enabled is true, each camera stream is sent via ffmpeg tee
// to every address in Destinations, one UDP leg per destination.
type UnicastConfig struct {
	Enabled      bool                 `toml:"enabled"`
	StartPort    int                  `toml:"startPort"`
	Destinations []UnicastDestination `toml:"destinations"`
}

// CamerasSettings holds per-instance camera behaviour flags.
type CamerasSettings struct {
	IncludeAll bool `toml:"includeAll"`
}

// RTSPSource defines one configured RTSP input that should be republished.
type RTSPSource struct {
	SourceID     string   `toml:"sourceId"`
	Name         string   `toml:"name"`
	ShortID      string   `toml:"shortId"`
	Enabled      bool     `toml:"enabled"`
	On           *bool    `toml:"on,omitempty"`
	RTSPURL      string   `toml:"rtspUrl"`
	OutputPort   int      `toml:"outputPort"`
	Transport    string   `toml:"transport"`
	Codec        string   `toml:"codec"` // detected by Probe: h264, hevc, etc. Empty = assume h264.
	ProbeSize    string   `toml:"probeSize"`
	ProbeFPS     int      `toml:"probeFps"`
	ProbeDirty   bool     `toml:"probeDirty,omitempty"`
	DirtyReasons []string `toml:"dirtyReasons,omitempty"`
}

// DeviceAssignment persists operator-facing metadata for one autodetected USB source.
type DeviceAssignment struct {
	AttachmentPath       string   `toml:"attachmentPath,omitempty"`
	MatchKey             string   `toml:"matchKey"`
	Name                 string   `toml:"name"`
	ShortID              string   `toml:"shortId"`
	OutputPort           int      `toml:"outputPort"`
	Disabled             bool     `toml:"disabled,omitempty"`
	On                   *bool    `toml:"on,omitempty"`
	PreferredPixelFormat string   `toml:"preferredPixelFormat,omitempty"`
	ProbePixelFormat     string   `toml:"probePixelFormat,omitempty"`
	ProbeSize            string   `toml:"probeSize,omitempty"`
	ProbeFPS             int      `toml:"probeFps,omitempty"`
	ProbeFormats         []string `toml:"probeFormats,omitempty"`
	DirtyReasons         []string `toml:"dirtyReasons,omitempty"`
}

// decodeCameraConfig decodes TOML into a Config, migrating the legacy
// destinations = ["ip", ...] string-array format to []UnicastDestination.
func decodeCameraConfig(data string) (Config, error) {
	var cfg Config
	_, err := toml.Decode(data, &cfg)
	if err != nil && strings.Contains(err.Error(), "unicast.destinations") {
		// Legacy format: destinations was a plain string array.
		// Decode it separately and migrate each address to UnicastDestination.
		type legacyCfg struct {
			Unicast struct {
				Destinations []string `toml:"destinations"`
			} `toml:"unicast"`
		}
		var legacy legacyCfg
		if _, legErr := toml.Decode(data, &legacy); legErr == nil {
			cfg.Unicast.Destinations = nil
			for _, addr := range legacy.Unicast.Destinations {
				cfg.Unicast.Destinations = append(cfg.Unicast.Destinations,
					UnicastDestination{Address: addr, Enabled: true})
			}
			logging.InfoLogger.Printf("Migrated %d legacy unicast destinations", len(cfg.Unicast.Destinations))
			return cfg, nil
		}
	}
	return cfg, err
}

func loadConfigFile(path string, rememberSource bool) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	cfg, err := decodeCameraConfig(string(data))
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}

	logging.InfoLogger.Printf("Loading cameras instance config from %s", path)
	cfg.applyDefaults()
	if rememberSource {
		configSourcePath = path
	}

	return &cfg, nil
}

// LoadConfigFromFile loads a cameras configuration from an explicit config.toml path.
// Unlike LoadConfig, this does not change the remembered source path used by SaveConfig.
func LoadConfigFromFile(path string) (*Config, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, fmt.Errorf("empty cameras config path")
	}
	if absPath, err := filepath.Abs(trimmed); err == nil {
		trimmed = absPath
	}
	return loadConfigFile(trimmed, false)
}

// LoadConfigFromBytes parses a cameras configuration received from another Video instance.
func LoadConfigFromBytes(data []byte) (*Config, error) {
	cfg, err := decodeCameraConfig(string(data))
	if err != nil {
		return nil, fmt.Errorf("failed to parse cameras configuration: %w", err)
	}
	cfg.applyDefaults()
	return &cfg, nil
}

// LoadConfig reloads the cameras configuration from the file the host pinned
// with SetConfigSourcePath, falling back to cameras.toml in the config
// directory and finally to the embedded defaults.
func LoadConfig() (*Config, error) {
	var cfg Config

	path := configSourcePath
	if path == "" {
		path = config.CamerasConfigPath()
	}

	configSourcePath = ""
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("Cameras instance config: %s\n", path)
		loaded, loadErr := loadConfigFile(path, true)
		if loadErr != nil {
			return nil, loadErr
		}
		cfg = *loaded
	}

	if configSourcePath == "" {
		fmt.Println("Cameras instance config: using embedded defaults")
		logging.InfoLogger.Println("No cameras.toml found, using embedded instance defaults")
		var parseErr error
		cfg, parseErr = decodeCameraConfig(string(defaultInstanceConfig))
		if parseErr != nil {
			return nil, fmt.Errorf("failed to parse embedded cameras.toml: %w", parseErr)
		}
	}

	cfg.applyDefaults()
	return &cfg, nil
}

// GetConfigSourcePath returns the file path used by LoadConfig.
// Empty string means defaults were loaded from embedded config.
func GetConfigSourcePath() string {
	return configSourcePath
}

// SetConfigSourcePath pins the file that SaveConfig writes to.
func SetConfigSourcePath(path string) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		configSourcePath = ""
		return
	}
	if absPath, err := filepath.Abs(trimmed); err == nil {
		trimmed = absPath
	}
	configSourcePath = trimmed
}

// SaveConfig writes the full cameras config to disk.
// If config.toml was previously loaded from embedded defaults, it is written
// to the install directory and becomes the active config file.
func SaveConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("nil cameras config")
	}

	configPath := GetConfigSourcePath()
	if configPath == "" {
		configPath = config.CamerasConfigPath()
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	cfg.applyDefaults()
	cfg.ensureSourceIDs()
	content := cfg.serialize()
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write cameras config: %w", err)
	}
	configSourcePath = configPath
	return nil
}

// SaveMulticastSettings updates all [multicast] fields in the loaded config.toml file.
func SaveMulticastSettings(ip string, startPort int, localOnly bool) error {
	if strings.TrimSpace(ip) == "" {
		return fmt.Errorf("invalid multicast ip")
	}
	if startPort < 1 || startPort > 65535 {
		return fmt.Errorf("invalid startPort %d", startPort)
	}

	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	cfg.Multicast.IP = ip
	cfg.Multicast.StartPort = startPort
	cfg.Multicast.LocalOnly = localOnly
	return SaveConfig(cfg)
}

// SaveUnicastSettings updates unicast.enabled, unicast.startPort,
// and unicast.destinations in the loaded config.toml file.
func SaveUnicastSettings(enabled bool, startPort int, destinations []UnicastDestination) error {
	if startPort < 1 || startPort > 65535 {
		return fmt.Errorf("invalid startPort %d", startPort)
	}

	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	cfg.Unicast.Enabled = enabled
	cfg.Unicast.StartPort = startPort
	cfg.Unicast.Destinations = append([]UnicastDestination(nil), destinations...)
	return SaveConfig(cfg)
}

// SaveStartPort updates multicast.startPort in the loaded config.toml file.
func SaveStartPort(startPort int) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	return SaveMulticastSettings(cfg.Multicast.IP, startPort, cfg.Multicast.LocalOnly)
}

// ExtractDefaultConfigTo creates the cameras configuration at configPath if it
// does not already exist.
func ExtractDefaultConfigTo(configPath string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("create cameras configuration directory: %w", err)
	}
	if _, err := os.Stat(configPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check cameras configuration %s: %w", configPath, err)
	}
	if err := os.WriteFile(configPath, defaultInstanceConfig, 0644); err != nil {
		return fmt.Errorf("write cameras configuration %s: %w", configPath, err)
	}
	return nil
}

// applyDefaults fills in zero-value fields with sensible defaults.
func (c *Config) applyDefaults() {
	if c.Multicast.IP == "" {
		c.Multicast.IP = "239.255.0.1"
	}
	if c.Multicast.StartPort == 0 {
		c.Multicast.StartPort = 9001
	}
	// Unicast defaults
	if c.Unicast.StartPort == 0 {
		c.Unicast.StartPort = 9001
	}
	for i := range c.RTSPSources {
		if c.RTSPSources[i].On == nil {
			c.RTSPSources[i].On = boolPtr(true)
		}
		if strings.TrimSpace(c.RTSPSources[i].Transport) == "" {
			c.RTSPSources[i].Transport = "tcp"
		}
		if strings.TrimSpace(c.RTSPSources[i].RTSPURL) != "" && strings.TrimSpace(c.RTSPSources[i].Codec) == "" {
			c.RTSPSources[i].ProbeDirty = true
			c.RTSPSources[i].DirtyReasons = appendDirtyReason(c.RTSPSources[i].DirtyReasons, "probe")
		}
		c.RTSPSources[i].DirtyReasons = normalizeDirtyReasons(c.RTSPSources[i].DirtyReasons)
	}
	for i := range c.DeviceAssignments {
		if c.DeviceAssignments[i].On == nil {
			c.DeviceAssignments[i].On = boolPtr(true)
		}
		if strings.TrimSpace(c.DeviceAssignments[i].MatchKey) != "" && strings.TrimSpace(c.DeviceAssignments[i].ProbePixelFormat) == "" {
			c.DeviceAssignments[i].DirtyReasons = appendDirtyReason(c.DeviceAssignments[i].DirtyReasons, "probe")
		}
		c.DeviceAssignments[i].DirtyReasons = normalizeDirtyReasons(c.DeviceAssignments[i].DirtyReasons)
	}
	c.ensureSourceIDs()
}

func boolPtr(v bool) *bool {
	value := v
	return &value
}

func monitoringOn(value *bool) bool {
	return value == nil || *value
}

func normalizeDirtyReasons(reasons []string) []string {
	if len(reasons) == 0 {
		return nil
	}
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

func appendDirtyReason(reasons []string, reason string) []string {
	return normalizeDirtyReasons(append(reasons, reason))
}

func removeDirtyReason(reasons []string, target string) []string {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return normalizeDirtyReasons(reasons)
	}
	filtered := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if strings.ToLower(strings.TrimSpace(reason)) == target {
			continue
		}
		filtered = append(filtered, reason)
	}
	return normalizeDirtyReasons(filtered)
}

// ClearRestartDirtyReasons removes persisted restart markers from all sources.
// Returns true when any entry was changed.
func (c *Config) ClearRestartDirtyReasons() bool {
	if c == nil {
		return false
	}

	changed := false
	for i := range c.DeviceAssignments {
		updated := removeDirtyReason(c.DeviceAssignments[i].DirtyReasons, "restart")
		if strings.Join(updated, "\x00") != strings.Join(c.DeviceAssignments[i].DirtyReasons, "\x00") {
			c.DeviceAssignments[i].DirtyReasons = updated
			changed = true
		}
	}
	for i := range c.RTSPSources {
		updated := removeDirtyReason(c.RTSPSources[i].DirtyReasons, "restart")
		if strings.Join(updated, "\x00") != strings.Join(c.RTSPSources[i].DirtyReasons, "\x00") {
			c.RTSPSources[i].DirtyReasons = updated
			changed = true
		}
	}

	return changed
}

func (c *Config) ensureSourceIDs() {
	used := make(map[string]struct{}, len(c.RTSPSources))
	for i := range c.RTSPSources {
		src := &c.RTSPSources[i]
		id := strings.TrimSpace(src.SourceID)
		key := strings.ToLower(id)
		if id == "" {
			src.SourceID = generateRTSPSourceID(src.RTSPURL, i, used)
		} else if _, exists := used[key]; exists {
			src.SourceID = generateRTSPSourceID(src.RTSPURL, i, used)
		} else {
			used[key] = struct{}{}
		}
		if strings.TrimSpace(src.Transport) == "" {
			src.Transport = "tcp"
		}
	}
}

func generateRTSPSourceID(rtspURL string, index int, used map[string]struct{}) string {
	normalized := normalizeRTSPFingerprint(rtspURL)
	base := ""
	if normalized == "" {
		base = fmt.Sprintf("rtsp-%d", index+1)
	} else {
		hash := sha1.Sum([]byte(normalized))
		base = "rtsp-" + hex.EncodeToString(hash[:])[:10]
	}

	for suffix := 1; ; suffix++ {
		candidate := base
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		key := strings.ToLower(candidate)
		if _, exists := used[key]; exists {
			continue
		}
		used[key] = struct{}{}
		return candidate
	}
}

func normalizeRTSPFingerprint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func (c *Config) serialize() string {
	var buf bytes.Buffer

	buf.WriteString("# =========================================================================\n")
	buf.WriteString("# config.toml — per-instance runtime configuration for cameras\n")
	buf.WriteString("# =========================================================================\n\n")
	buf.WriteString("[multicast]\n")
	buf.WriteString(fmt.Sprintf("    ip = %s\n", strconv.Quote(c.Multicast.IP)))
	buf.WriteString(fmt.Sprintf("    startPort = %d\n", c.Multicast.StartPort))
	buf.WriteString(fmt.Sprintf("    localOnly = %t\n\n", c.Multicast.LocalOnly))

	buf.WriteString("[unicast]\n")
	buf.WriteString(fmt.Sprintf("    enabled = %t\n", c.Unicast.Enabled))
	buf.WriteString(fmt.Sprintf("    startPort = %d\n", c.Unicast.StartPort))
	for _, dest := range c.Unicast.Destinations {
		buf.WriteString("\n[[unicast.destinations]]\n")
		buf.WriteString(fmt.Sprintf("    address = %s\n", strconv.Quote(dest.Address)))
		buf.WriteString(fmt.Sprintf("    enabled = %t\n", dest.Enabled))
	}
	buf.WriteString("\n")

	buf.WriteString("[cameras]\n")
	buf.WriteString(fmt.Sprintf("    includeAll = %t\n", c.Cameras.IncludeAll))

	for _, assignment := range c.DeviceAssignments {
		if strings.TrimSpace(assignment.MatchKey) == "" && strings.TrimSpace(assignment.AttachmentPath) == "" {
			continue
		}
		buf.WriteString("\n[[deviceAssignment]]\n")
		if strings.TrimSpace(assignment.AttachmentPath) != "" {
			buf.WriteString(fmt.Sprintf("    attachmentPath = %s\n", strconv.Quote(assignment.AttachmentPath)))
		}
		buf.WriteString(fmt.Sprintf("    matchKey = %s\n", strconv.Quote(assignment.MatchKey)))
		if strings.TrimSpace(assignment.Name) != "" {
			buf.WriteString(fmt.Sprintf("    name = %s\n", strconv.Quote(assignment.Name)))
		}
		if strings.TrimSpace(assignment.ShortID) != "" {
			buf.WriteString(fmt.Sprintf("    shortId = %s\n", strconv.Quote(assignment.ShortID)))
		}
		if assignment.OutputPort > 0 {
			buf.WriteString(fmt.Sprintf("    outputPort = %d\n", assignment.OutputPort))
		}
		if assignment.Disabled {
			buf.WriteString("    disabled = true\n")
		}
		buf.WriteString(fmt.Sprintf("    on = %t\n", monitoringOn(assignment.On)))
		if strings.TrimSpace(assignment.PreferredPixelFormat) != "" {
			buf.WriteString(fmt.Sprintf("    preferredPixelFormat = %s\n", strconv.Quote(assignment.PreferredPixelFormat)))
		}
		if strings.TrimSpace(assignment.ProbePixelFormat) != "" {
			buf.WriteString(fmt.Sprintf("    probePixelFormat = %s\n", strconv.Quote(assignment.ProbePixelFormat)))
		}
		if strings.TrimSpace(assignment.ProbeSize) != "" {
			buf.WriteString(fmt.Sprintf("    probeSize = %s\n", strconv.Quote(assignment.ProbeSize)))
		}
		if assignment.ProbeFPS > 0 {
			buf.WriteString(fmt.Sprintf("    probeFps = %d\n", assignment.ProbeFPS))
		}
		if len(assignment.ProbeFormats) > 0 {
			buf.WriteString(fmt.Sprintf("    probeFormats = [%s]\n", quoteStrings(assignment.ProbeFormats)))
		}
		if len(assignment.DirtyReasons) > 0 {
			buf.WriteString(fmt.Sprintf("    dirtyReasons = [%s]\n", quoteStrings(assignment.DirtyReasons)))
		}
	}

	for _, source := range c.RTSPSources {
		buf.WriteString("\n[[rtsp]]\n")
		buf.WriteString(fmt.Sprintf("    sourceId = %s\n", strconv.Quote(source.SourceID)))
		buf.WriteString(fmt.Sprintf("    name = %s\n", strconv.Quote(source.Name)))
		if strings.TrimSpace(source.ShortID) != "" {
			buf.WriteString(fmt.Sprintf("    shortId = %s\n", strconv.Quote(source.ShortID)))
		}
		buf.WriteString(fmt.Sprintf("    enabled = %t\n", source.Enabled))
		buf.WriteString(fmt.Sprintf("    on = %t\n", monitoringOn(source.On)))
		buf.WriteString(fmt.Sprintf("    rtspUrl = %s\n", strconv.Quote(source.RTSPURL)))
		if source.OutputPort > 0 {
			buf.WriteString(fmt.Sprintf("    outputPort = %d\n", source.OutputPort))
		}
		buf.WriteString(fmt.Sprintf("    transport = %s\n", strconv.Quote(source.Transport)))
		if strings.TrimSpace(source.Codec) != "" {
			buf.WriteString(fmt.Sprintf("    codec = %s\n", strconv.Quote(source.Codec)))
		}
		if strings.TrimSpace(source.ProbeSize) != "" {
			buf.WriteString(fmt.Sprintf("    probeSize = %s\n", strconv.Quote(source.ProbeSize)))
		}
		if source.ProbeFPS > 0 {
			buf.WriteString(fmt.Sprintf("    probeFps = %d\n", source.ProbeFPS))
		}
		if source.ProbeDirty {
			buf.WriteString("    probeDirty = true\n")
		}
		if len(source.DirtyReasons) > 0 {
			buf.WriteString(fmt.Sprintf("    dirtyReasons = [%s]\n", quoteStrings(source.DirtyReasons)))
		}
	}

	return buf.String() + "\n"
}

func quoteStrings(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		quoted = append(quoted, strconv.Quote(trimmed))
	}
	return strings.Join(quoted, ", ")
}

// UnicastTeeOutput builds the ffmpeg -f tee output string for a given port.
// Each enabled destination gets its own "[f=mpegts:onfail=ignore]udp://ip:port?pkt_size=N" leg.
func (c *UnicastConfig) TeeOutput(port int) string {
	var legs []string
	seen := make(map[string]struct{}, len(c.Destinations)+1)
	addLeg := func(address string) {
		normalized := NormalizeUnicastDestinationAddress(address)
		if normalized == "" {
			return
		}
		key := strings.ToLower(normalized)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		legs = append(legs, fmt.Sprintf("[f=mpegts:onfail=ignore]udp://%s:%d?pkt_size=%d", normalized, port, PktSize))
	}

	addLeg(PreviewLoopbackAddress)
	for _, dest := range c.Destinations {
		if !dest.Enabled {
			continue
		}
		addLeg(dest.Address)
	}
	return strings.Join(legs, "|")
}

// NormalizeUnicastDestinationAddress canonicalizes loopback destinations so
// preview can always listen on a stable local IPv4 address.
func NormalizeUnicastDestinationAddress(address string) string {
	trimmed := strings.TrimSpace(address)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.Trim(trimmed, "[]")
	switch strings.ToLower(trimmed) {
	case "127.0.0.1", "localhost", "::1":
		return PreviewLoopbackAddress
	default:
		return trimmed
	}
}
