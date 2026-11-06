package httpServer

import (
	"net/http/httptest"
	"testing"
)

func TestClientOf(t *testing.T) {
	SetTrustedProxies([]string{"192.168.1.10", "10.0.0.0/8", "invalid"})
	t.Cleanup(func() { SetTrustedProxies(nil) })
	r := httptest.NewRequest("GET", "http://example.test/", nil)
	r.RemoteAddr = "192.168.1.11:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	r.Header.Set("X-Forwarded-Proto", "https")
	if addr, https := clientOf(r); addr != "192.168.1.11" || https {
		t.Fatalf("untrusted peer: %s %t", addr, https)
	}
	r.RemoteAddr = "192.168.1.10:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.1, invalid, 10.1.2.3")
	if addr, https := clientOf(r); addr != "203.0.113.1" || !https {
		t.Fatalf("trusted peer: %s %t", addr, https)
	}
}
