//go:build darwin

package recording

import (
	"strings"

	"github.com/progrium/darwinkit/macos/avfoundation"
)

func avFoundationVideoDeviceMetadata() map[string]avFoundationDeviceMetadata {
	session := avfoundation.CaptureDeviceDiscoverySession_DiscoverySessionWithDeviceTypesMediaTypePosition(
		[]avfoundation.CaptureDeviceType{
			avfoundation.CaptureDeviceTypeBuiltInWideAngleCamera,
			avfoundation.CaptureDeviceTypeExternalUnknown,
		},
		avfoundation.MediaTypeVideo,
		avfoundation.CaptureDevicePositionUnspecified,
	)
	devices := session.Devices()
	metadataByName := make(map[string]avFoundationDeviceMetadata, len(devices))
	for _, device := range devices {
		name := strings.ToLower(strings.TrimSpace(device.LocalizedName()))
		if name == "" {
			continue
		}
		metadataByName[name] = avFoundationDeviceMetadata{
			transportType: fourCC(device.TransportType()),
		}
	}
	return metadataByName
}

func fourCC(value int32) string {
	return string([]byte{
		byte(value >> 24),
		byte(value >> 16),
		byte(value >> 8),
		byte(value),
	})
}
