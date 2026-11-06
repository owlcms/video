package httpServer

import (
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/owlcms/video/internal/logging"
)

var proxyConfig struct {
	sync.RWMutex
	networks []*net.IPNet
}

func SetTrustedProxies(entries []string) {
	var networks []*net.IPNet
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		ip := net.ParseIP(entry)
		if ip != nil {
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			logging.WarningLogger.Printf("Ignoring invalid trusted proxy %q", entry)
			continue
		}
		networks = append(networks, network)
	}
	proxyConfig.Lock()
	proxyConfig.networks = networks
	proxyConfig.Unlock()
	logging.InfoLogger.Printf("Trusted proxies: %v", networks)
}

func clientOf(r *http.Request) (string, bool) {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	proxyConfig.RLock()
	defer proxyConfig.RUnlock()
	trusted := func(ip net.IP) bool {
		for _, network := range proxyConfig.networks {
			if network.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !trusted(net.ParseIP(peer)) {
		return peer, r.TLS != nil
	}
	addr := peer
	parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip != nil && !trusted(ip) {
			addr = ip.String()
			break
		}
	}
	proto := strings.Split(strings.Join(r.Header.Values("X-Forwarded-Proto"), ","), ",")
	https := r.TLS != nil || strings.EqualFold(strings.TrimSpace(proto[len(proto)-1]), "https")
	return addr, https
}
