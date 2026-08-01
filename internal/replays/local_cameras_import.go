package replays

import (
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/owlcms/video/internal/config"
	camerascfg "github.com/owlcms/video/internal/config/cameras"
	replayscfg "github.com/owlcms/video/internal/config/replays"
)

const maxImportedCameraStreams = 4

type localCamerasStream struct {
	Name       string
	ShortID    string
	OutputPort int
	Kind       string
}

type localCamerasImportPreview struct {
	ConfigPath           string
	Mode                 string
	ListenIP             string
	CompatibilityAllowed bool
	CompatibilityMessage string
	CamerasAddressLabel  string
	CamerasAddressValue  string
	ReplaysAddressValue  string
	LocalAddresses       []string
	EnabledDestinations  []string
	OrderedStreams       []localCamerasStream
	ImportedStreams      []localCamerasStream
	AdditionalStreams    []localCamerasStream
}

func monitoringEnabled(value *bool) bool {
	return value == nil || *value
}

func collectLocalCamerasStreams(cfg *camerascfg.Config) []localCamerasStream {
	streams := make([]localCamerasStream, 0, len(cfg.DeviceAssignments)+len(cfg.RTSPSources))

	for _, assignment := range cfg.DeviceAssignments {
		if assignment.Disabled || !monitoringEnabled(assignment.On) || assignment.OutputPort <= 0 {
			continue
		}
		name := strings.TrimSpace(assignment.Name)
		if name == "" {
			name = strings.TrimSpace(assignment.MatchKey)
		}
		streams = append(streams, localCamerasStream{
			Name:       name,
			ShortID:    strings.TrimSpace(assignment.ShortID),
			OutputPort: assignment.OutputPort,
			Kind:       "USB",
		})
	}

	for _, source := range cfg.RTSPSources {
		if !source.Enabled || !monitoringEnabled(source.On) || source.OutputPort <= 0 {
			continue
		}
		name := strings.TrimSpace(source.Name)
		if name == "" {
			name = strings.TrimSpace(source.SourceID)
		}
		streams = append(streams, localCamerasStream{
			Name:       name,
			ShortID:    strings.TrimSpace(source.ShortID),
			OutputPort: source.OutputPort,
			Kind:       "RTSP",
		})
	}

	sort.Slice(streams, func(i, j int) bool {
		if cmp := compareShortIDs(streams[i].ShortID, streams[j].ShortID); cmp != 0 {
			return cmp < 0
		}
		if streams[i].OutputPort != streams[j].OutputPort {
			return streams[i].OutputPort < streams[j].OutputPort
		}
		leftName := strings.ToLower(strings.TrimSpace(streams[i].Name))
		rightName := strings.ToLower(strings.TrimSpace(streams[j].Name))
		if leftName != rightName {
			return leftName < rightName
		}
		return streams[i].Kind < streams[j].Kind
	})

	return streams
}

func compareShortIDs(left, right string) int {
	left = strings.ToUpper(strings.TrimSpace(left))
	right = strings.ToUpper(strings.TrimSpace(right))

	if left == right {
		return 0
	}
	if left == "" {
		return 1
	}
	if right == "" {
		return -1
	}

	leftPrefix, leftNumber, leftHasNumber := splitShortID(left)
	rightPrefix, rightNumber, rightHasNumber := splitShortID(right)
	if leftPrefix != rightPrefix {
		if leftPrefix < rightPrefix {
			return -1
		}
		return 1
	}
	if leftHasNumber && rightHasNumber && leftNumber != rightNumber {
		if leftNumber < rightNumber {
			return -1
		}
		return 1
	}
	if left < right {
		return -1
	}
	return 1
}

func splitShortID(raw string) (string, int, bool) {
	index := len(raw)
	for i, r := range raw {
		if r >= '0' && r <= '9' {
			index = i
			break
		}
	}
	if index == len(raw) {
		return raw, 0, false
	}
	value, err := strconv.Atoi(raw[index:])
	if err != nil {
		return raw, 0, false
	}
	return raw[:index], value, true
}

func localCaptureAddresses() []string {
	seen := make(map[string]struct{})
	addresses := make([]string, 0, 8)
	add := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		addresses = append(addresses, trimmed)
	}

	add("127.0.0.1")
	add("localhost")
	if hostname, err := os.Hostname(); err == nil {
		add(hostname)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil {
				continue
			}
			if v4 := ipNet.IP.To4(); v4 != nil {
				add(v4.String())
			}
		}
	}

	sort.Strings(addresses)
	return addresses
}

func matchLocalUnicastDestination(destinations []string, localAddresses []string) (string, bool) {
	localSet := make(map[string]struct{}, len(localAddresses))
	for _, address := range localAddresses {
		localSet[strings.ToLower(strings.TrimSpace(address))] = struct{}{}
	}

	for _, destination := range destinations {
		trimmed := strings.TrimSpace(destination)
		if trimmed == "" {
			continue
		}
		if parsed := net.ParseIP(trimmed); parsed != nil && parsed.IsLoopback() {
			return trimmed, true
		}
		if _, ok := localSet[strings.ToLower(trimmed)]; ok {
			return trimmed, true
		}
	}

	return "", false
}

func loadLocalCamerasImportPreview(configPath string) (*localCamerasImportPreview, error) {
	cfg, err := camerascfg.LoadConfigFromFile(configPath)
	if err != nil {
		return nil, err
	}
	return buildCamerasImportPreview(cfg, configPath), nil
}

func buildCamerasImportPreview(cfg *camerascfg.Config, configPath string) *localCamerasImportPreview {
	if cfg == nil {
		return nil
	}

	ordered := collectLocalCamerasStreams(cfg)

	preview := &localCamerasImportPreview{
		ConfigPath:     configPath,
		LocalAddresses: localCaptureAddresses(),
		OrderedStreams: append([]localCamerasStream(nil), ordered...),
	}

	if cfg.Unicast.Enabled {
		preview.Mode = "unicast"
		preview.ListenIP = "0.0.0.0"
		preview.CamerasAddressLabel = "Cameras unicast address"
		preview.ReplaysAddressValue = preview.ListenIP
		for _, destination := range cfg.Unicast.Destinations {
			if !destination.Enabled {
				continue
			}
			trimmed := strings.TrimSpace(destination.Address)
			if trimmed == "" {
				continue
			}
			preview.EnabledDestinations = append(preview.EnabledDestinations, trimmed)
		}
		sort.Strings(preview.EnabledDestinations)
		if matched, ok := matchLocalUnicastDestination(preview.EnabledDestinations, preview.LocalAddresses); ok {
			preview.CompatibilityAllowed = true
			preview.CompatibilityMessage = "Cameras configuration allows capturing the replays."
			preview.CamerasAddressValue = matched
		} else {
			preview.CompatibilityAllowed = false
			preview.CompatibilityMessage = "Cameras configuration does not allow capturing the replays. Cameras is not unicasting to this machine."
			if len(preview.EnabledDestinations) == 0 {
				preview.CamerasAddressValue = "none"
			} else {
				preview.CamerasAddressValue = strings.Join(preview.EnabledDestinations, ", ")
			}
		}
	} else {
		preview.Mode = "multicast"
		preview.ListenIP = strings.TrimSpace(cfg.Multicast.IP)
		if preview.ListenIP == "" {
			preview.ListenIP = "239.255.0.1"
		}
		preview.CompatibilityAllowed = true
		preview.CompatibilityMessage = "Cameras configuration allows capturing the replays."
		preview.CamerasAddressLabel = "Cameras multicast address"
		preview.CamerasAddressValue = preview.ListenIP
		preview.ReplaysAddressValue = preview.ListenIP
	}

	limit := len(ordered)
	if limit > maxImportedCameraStreams {
		limit = maxImportedCameraStreams
	}
	preview.ImportedStreams = append([]localCamerasStream(nil), ordered[:limit]...)
	if limit < len(ordered) {
		preview.AdditionalStreams = append([]localCamerasStream(nil), ordered[limit:]...)
	}

	return preview
}

func camerasConfigEndpoint(serverAddress string) (string, error) {
	address := strings.TrimSpace(serverAddress)
	if address == "" {
		return "", fmt.Errorf("enter a Cameras server address")
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}

	parsed, err := neturl.Parse(address)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid Cameras server address %q", serverAddress)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/cameras/config"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func loadRemoteCamerasImportPreview(serverAddress string) (*localCamerasImportPreview, error) {
	endpoint, err := camerasConfigEndpoint(serverAddress)
	if err != nil {
		return nil, err
	}

	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("fetch cameras configuration from %s: %w", endpoint, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch cameras configuration from %s: server returned %s", endpoint, response.Status)
	}

	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read cameras configuration from %s: %w", endpoint, err)
	}
	cfg, err := camerascfg.LoadConfigFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("read cameras configuration from %s: %w", endpoint, err)
	}
	return buildCamerasImportPreview(cfg, endpoint), nil
}

func formatPreviewShortID(shortID string) string {
	trimmed := strings.TrimSpace(shortID)
	if trimmed == "" {
		return "(no short ID)"
	}
	return trimmed
}

func selectedCamerasFirst(preview *localCamerasImportPreview, settings config.MulticastSettings) ([]localCamerasStream, []localCamerasStream) {
	if preview == nil {
		return nil, nil
	}

	byPort := make(map[int]localCamerasStream, len(preview.OrderedStreams))
	for _, stream := range preview.OrderedStreams {
		byPort[stream.OutputPort] = stream
	}

	configuredPorts := []int{
		settings.Camera1Port,
		settings.Camera2Port,
		settings.Camera3Port,
		settings.Camera4Port,
	}
	selected := make([]localCamerasStream, 0, maxImportedCameraStreams)
	selectedPorts := make(map[int]struct{}, maxImportedCameraStreams)
	for _, port := range configuredPorts {
		stream, ok := byPort[port]
		if !ok || port <= 0 {
			continue
		}
		if _, alreadySelected := selectedPorts[port]; alreadySelected {
			continue
		}
		selected = append(selected, stream)
		selectedPorts[port] = struct{}{}
	}

	if len(selected) == 0 {
		for _, stream := range preview.ImportedStreams {
			if len(selected) == maxImportedCameraStreams {
				break
			}
			selected = append(selected, stream)
			selectedPorts[stream.OutputPort] = struct{}{}
		}
	}

	available := make([]localCamerasStream, 0, len(preview.OrderedStreams)-len(selected))
	for _, stream := range preview.OrderedStreams {
		if _, isSelected := selectedPorts[stream.OutputPort]; !isSelected {
			available = append(available, stream)
		}
	}
	return selected, available
}

func importedMpegTSSettings(current config.MulticastSettings, preview *localCamerasImportPreview, selected []localCamerasStream) (config.MulticastSettings, error) {
	if preview == nil {
		return config.MulticastSettings{}, fmt.Errorf("missing Cameras configuration")
	}
	settings := current
	settings.Enabled = true
	settings.IP = preview.ListenIP
	settings.Camera1Port = 0
	settings.Camera2Port = 0
	settings.Camera3Port = 0
	settings.Camera4Port = 0

	ports := []*int{&settings.Camera1Port, &settings.Camera2Port, &settings.Camera3Port, &settings.Camera4Port}
	for index, stream := range selected {
		if index >= len(ports) {
			break
		}
		*ports[index] = stream.OutputPort
	}
	return settings, nil
}

func applyLocalCamerasImport(cfg *replayscfg.Config, preview *localCamerasImportPreview, selected []localCamerasStream, configFilePath string) error {
	if cfg == nil {
		return fmt.Errorf("missing Replays configuration")
	}
	settings, err := importedMpegTSSettings(cfg.Multicast, preview, selected)
	if err != nil {
		return err
	}

	if err := replayscfg.UpdateMpegTSConfig(configFilePath, settings); err != nil {
		return err
	}

	cfg.Multicast = settings
	return nil
}
