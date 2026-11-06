package httpServer

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/owlcms/video/internal/access"
	"github.com/owlcms/video/internal/config"
)

var guardedRoutes = []string{
	"/",
	"/api/sessions",
	"/api/sessions/3/lifts",
	"/api/replay-state",
	"/replay/1",
	"/replay/1.mp4",
	"/videos/3/clip.mp4",
	"/ws",
}

func protectedRouter(t *testing.T) (*access.Service, http.Handler, string) {
	t.Helper()
	oldDir := config.GetVideoDir()
	config.SetVideoDir(t.TempDir())
	securityDir := t.TempDir()
	service := access.Open(securityDir)
	SetAccess(service)
	t.Cleanup(func() {
		config.SetVideoDir(oldDir)
		SetAccess(nil)
	})
	return service, newRouter(ModuleAvailability{Replays: true}), securityDir
}

func status(router http.Handler, method, target string) int {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w.Code
}

func TestEveryGuardedRouteRefusesWithoutCredentials(t *testing.T) {
	service, router, _ := protectedRouter(t)
	if _, _, err := service.AddViewer("RAI"); err != nil {
		t.Fatal(err)
	}
	for _, route := range guardedRoutes {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			if code := status(router, method, route); code != http.StatusUnauthorized {
				t.Errorf("%s %s without credentials: %d, want 401", method, route, code)
			}
		}
	}
	if code := status(router, http.MethodGet, "/static/css/styles.css"); code != http.StatusOK {
		t.Errorf("static files must stay open: %d", code)
	}
}

func TestExpiredAndRegeneratedTokensAreRefused(t *testing.T) {
	service, router, _ := protectedRouter(t)
	oldValue, viewer, err := service.AddViewer("EBU")
	if err != nil {
		t.Fatal(err)
	}
	otherValue, _, err := service.AddViewer("RAI")
	if err != nil {
		t.Fatal(err)
	}
	newValue, _, err := service.RegenerateViewer(viewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if code := status(router, http.MethodGet, "/api/sessions?token="+oldValue); code != http.StatusUnauthorized {
		t.Errorf("regenerated token's old value: %d, want 401", code)
	}
	if code := status(router, http.MethodGet, "/api/sessions?token="+newValue); code != http.StatusOK {
		t.Errorf("regenerated token's new value: %d, want 200", code)
	}
	if code := status(router, http.MethodGet, "/api/sessions?token="+otherValue); code != http.StatusOK {
		t.Errorf("other token after regenerate: %d, want 200", code)
	}

	expiredDir := t.TempDir()
	expiredToken := "ExpiredTok12"
	hash := sha256.Sum256([]byte(expiredToken))
	record := fmt.Sprintf(`{"version":1,"instanceId":"0a0b0c0d","sessionKey":%q,"viewer":{"tokens":[`+
		`{"id":%q,"name":"OBS","hash":%q,"createdAt":"2026-01-01T00:00:00Z","expiresAt":"2026-01-22T00:00:00Z"}]}}`,
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
		base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
		hex.EncodeToString(hash[:]))
	if err := os.WriteFile(filepath.Join(expiredDir, "video-access.json"), []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	expired := access.Open(expiredDir)
	if state, _ := expired.ViewerState(); state != access.StateProtected {
		t.Fatalf("expired-token fixture not loaded: state %v, %v", state, expired.LoadError())
	}
	SetAccess(expired)
	if code := status(router, http.MethodGet, "/api/sessions?token="+expiredToken); code != http.StatusUnauthorized {
		t.Errorf("expired token: %d, want 401", code)
	}
	if body := getBody(router, "/?token="+expiredToken); !strings.Contains(body, `name="token"`) {
		t.Error("expired token on the page must show the sign-in form")
	}
}

func getBody(router http.Handler, target string) string {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w.Body.String()
}

func TestUnreadableTokenFileRefusesEverything(t *testing.T) {
	_, _, securityDir := protectedRouter(t)
	if err := os.WriteFile(filepath.Join(securityDir, "video-access.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	SetAccess(access.Open(securityDir))
	router := newRouter(ModuleAvailability{Replays: true})
	for _, route := range guardedRoutes {
		if code := status(router, http.MethodGet, route+"?token=anything"); code != http.StatusServiceUnavailable {
			t.Errorf("GET %s with unreadable token file: %d, want 503", route, code)
		}
	}
}

func TestWebSocketRefusedAfterRevoke(t *testing.T) {
	service, router, _ := protectedRouter(t)
	token, viewer, err := service.AddViewer("RAI")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.AddViewer("OBS"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	socketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(socketURL, nil)
	if err != nil {
		t.Fatalf("before revoke: %v", err)
	}
	conn.Close()
	if err := service.RevokeViewer(viewer.ID); err != nil {
		t.Fatal(err)
	}
	conn, response, err := websocket.DefaultDialer.Dial(socketURL, nil)
	if err == nil {
		conn.Close()
		t.Fatal("revoked token still opens a WebSocket")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after revoke: %v, want 401", response)
	}
}
