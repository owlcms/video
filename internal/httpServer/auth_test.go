package httpServer

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/owlcms/video/internal/access"
	"github.com/owlcms/video/internal/config"
)

func TestReplayAccessOpenAndRevocation(t *testing.T) {
	dir := t.TempDir()
	oldDir := config.GetVideoDir()
	t.Cleanup(func() { config.SetVideoDir(oldDir) })
	config.SetVideoDir(dir)
	if err := os.WriteFile(filepath.Join(dir, "test.mp4"), []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	service := access.Open(t.TempDir())
	SetAccess(service)
	t.Cleanup(func() { SetAccess(nil) })
	router := newRouter(ModuleAvailability{Replays: true})
	request := func(target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if got := request("/videos/test.mp4"); got.Code != 200 || got.Body.String() != "video" {
		t.Fatalf("open mode video = %d %q", got.Code, got.Body.String())
	}
	token, viewer, err := service.AddViewer("broadcaster")
	if err != nil {
		t.Fatal(err)
	}
	if got := request("/videos/test.mp4"); got.Code != 401 {
		t.Fatalf("missing credential: %d", got.Code)
	}
	if got := request("/videos/test.mp4?token=" + token); got.Code != 200 {
		t.Fatalf("valid credential: %d", got.Code)
	}
	exchange := request("/?token=" + token + "&session=one")
	if exchange.Code != 303 || exchange.Header().Get("Location") != "/?session=one" {
		t.Fatalf("exchange: %d %q", exchange.Code, exchange.Header().Get("Location"))
	}
	cookies := exchange.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("invalid session cookie")
	}
	if got := request("/videos/test.mp4?token=wrong", cookies[0]); got.Code != 401 {
		t.Fatalf("explicit invalid token fell back to cookie: %d", got.Code)
	}
	if err := service.RevokeViewer(viewer.ID); err != nil {
		t.Fatal(err)
	}
	if got := request("/videos/test.mp4"); got.Code != 200 || got.Body.String() != "video" {
		t.Fatalf("after last revoke: %d %q", got.Code, got.Body.String())
	}
}

func TestSignInAndSignOut(t *testing.T) {
	service := access.Open(t.TempDir())
	token, _, err := service.AddViewer("RAI")
	if err != nil {
		t.Fatal(err)
	}
	SetAccess(service)
	t.Cleanup(func() { SetAccess(nil) })
	router := newRouter(ModuleAvailability{Replays: true})
	form := url.Values{"token": {token}, "next": {"//evil.example"}}
	r := httptest.NewRequest(http.MethodPost, "/auth/token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 303 || w.Header().Get("Location") != "/" {
		t.Fatalf("sign in: %d %q", w.Code, w.Header().Get("Location"))
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	r = httptest.NewRequest(http.MethodPost, "/auth/signout", nil)
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 303 || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("sign out: %d %#v", w.Code, w.Result().Cookies())
	}
	r = httptest.NewRequest(http.MethodPost, "/auth/token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://foreign.example")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("foreign origin: %d", w.Code)
	}
}

func TestProtectedOptionsHasNoBody(t *testing.T) {
	service := access.Open(t.TempDir())
	if _, _, err := service.AddViewer("RAI"); err != nil {
		t.Fatal(err)
	}
	SetAccess(service)
	t.Cleanup(func() { SetAccess(nil) })
	r := httptest.NewRequest(http.MethodOptions, "/videos/test.mp4", nil)
	w := httptest.NewRecorder()
	newRouter(ModuleAvailability{Replays: true}).ServeHTTP(w, r)
	result := w.Result()
	defer result.Body.Close()
	body, _ := io.ReadAll(result.Body)
	if result.StatusCode != 204 || len(body) != 0 {
		t.Fatalf("OPTIONS: %d %q", result.StatusCode, body)
	}
}

func TestSignInPageAndSecureCookie(t *testing.T) {
	service := access.Open(t.TempDir())
	token, _, err := service.AddViewer("RAI")
	if err != nil {
		t.Fatal(err)
	}
	SetAccess(service)
	SetTrustedProxies([]string{"192.0.2.10"})
	t.Cleanup(func() { SetAccess(nil); SetTrustedProxies(nil) })
	router := newRouter(ModuleAvailability{Replays: true})
	r := httptest.NewRequest(http.MethodGet, "/?session=one", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 401 || !strings.Contains(w.Body.String(), `autocomplete="current-password"`) ||
		!strings.Contains(w.Body.String(), `value="/?session=one"`) {
		t.Fatalf("sign-in page: %d %q", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/?token="+token, nil)
	r.RemoteAddr = net.JoinHostPort("192.0.2.10", "1234")
	r.Header.Set("X-Forwarded-Proto", "https")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	cookies := w.Result().Cookies()
	if w.Code != 303 || len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("trusted proxy cookie: %d %#v", w.Code, cookies)
	}
	r = httptest.NewRequest(http.MethodGet, "/?token="+token, nil)
	r.RemoteAddr = net.JoinHostPort("192.0.2.11", "1234")
	r.Header.Set("X-Forwarded-Proto", "https")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	cookies = w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Secure {
		t.Fatalf("untrusted proxy cookie: %#v", cookies)
	}
}

func TestWebSocketTokenAndCookieOrigins(t *testing.T) {
	service := access.Open(t.TempDir())
	token, viewer, err := service.AddViewer("RAI")
	if err != nil {
		t.Fatal(err)
	}

	SetAccess(service)
	t.Cleanup(func() { SetAccess(nil) })
	server := httptest.NewServer(newRouter(ModuleAvailability{Replays: true}))
	defer server.Close()
	socketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	header := http.Header{"Origin": []string{"https://foreign.example"}}
	conn, response, err := websocket.DefaultDialer.Dial(socketURL+"?token="+token, header)
	if err != nil {
		t.Fatalf("explicit token from foreign origin: %v, %v", err, response)
	}
	conn.Close()
	value, _, err := service.IssueSession(viewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	header.Set("Cookie", service.CookieName()+"="+value)
	conn, response, err = websocket.DefaultDialer.Dial(socketURL, header)
	if err == nil {
		conn.Close()
		t.Fatal("foreign-origin cookie accepted")
	}
	if response == nil || response.StatusCode != 401 {
		t.Fatalf("foreign-origin cookie response: %v", response)
	}
	header.Set("Origin", server.URL)
	conn, response, err = websocket.DefaultDialer.Dial(socketURL, header)
	if err != nil {
		t.Fatalf("same-origin cookie: %v, %v", err, response)
	}
	conn.Close()
}

func TestFailedTokensAreThrottledPerClient(t *testing.T) {
	oldDir := config.GetVideoDir()
	config.SetVideoDir(t.TempDir())
	t.Cleanup(func() { config.SetVideoDir(oldDir) })
	service := access.Open(t.TempDir())
	token, _, err := service.AddViewer("RAI")
	if err != nil {
		t.Fatal(err)
	}
	SetAccess(service)
	t.Cleanup(func() {
		SetAccess(nil)
		failures.Lock()
		failures.entries = make(map[string]failureEntry)
		failures.Unlock()
	})
	router := newRouter(ModuleAvailability{Replays: true})
	for i := 1; i <= 20; i++ {
		r := httptest.NewRequest(http.MethodGet, "/api/replay-state?token=wrong", nil)
		r.RemoteAddr = "198.51.100.1:1234"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if i < 20 && w.Code != 401 || i == 20 && w.Code != 429 {
			t.Fatalf("failure %d: status %d", i, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/replay-state?token="+token, nil)
	r.RemoteAddr = "198.51.100.2:1234"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("another client was affected: %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/replay-state", nil)
	r.RemoteAddr = "198.51.100.1:1234"
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("missing credentials counted as failed token: %d", w.Code)
	}
}

func TestOpenModeMatchesExistingHandlers(t *testing.T) {
	dir := t.TempDir()
	oldDir := config.GetVideoDir()
	config.SetVideoDir(dir)
	t.Cleanup(func() {
		config.SetVideoDir(oldDir)
		SetAccess(nil)
	})
	if err := os.WriteFile(filepath.Join(dir, "test.mp4"), []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}

	router := newRouter(ModuleAvailability{Replays: true})
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/videos/test.mp4"},
		{http.MethodHead, "/videos/test.mp4"},
		{http.MethodOptions, "/videos/test.mp4"},
		{http.MethodGet, "/static/css/styles.css"},
		{http.MethodOptions, "/api/replay-state"},
	} {
		req := httptest.NewRequest(test.method, test.path, nil)
		baseline := httptest.NewRecorder()
		SetAccess(nil)
		router.ServeHTTP(baseline, req)
		open := httptest.NewRecorder()
		SetAccess(access.Open(t.TempDir()))
		router.ServeHTTP(open, httptest.NewRequest(test.method, test.path, nil))
		if baseline.Code != open.Code || baseline.Body.String() != open.Body.String() ||
			baseline.Header().Get("Access-Control-Allow-Origin") != open.Header().Get("Access-Control-Allow-Origin") {
			t.Errorf("%s %s changed in open mode", test.method, test.path)
		}
	}
}
