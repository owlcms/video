//go:build !darwin

package recording

func avFoundationVideoDeviceMetadata() map[string]avFoundationDeviceMetadata {
	return nil
}
