package iputils

import (
	"net"
)

// GetLocalIPv4Addresses returns a slice of local IPv4 addresses of the computer
func GetLocalIPv4Addresses() ([]string, error) {
	var ipv4Addresses []string

	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil || ipNet.IP.IsLoopback() {
				continue
			}

			ip := ipNet.IP.To4()
			if ip != nil && !ip.IsLinkLocalUnicast() {
				ipv4Addresses = append(ipv4Addresses, ip.String())
			}
		}
	}

	return ipv4Addresses, nil
}
