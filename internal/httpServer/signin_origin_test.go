package httpServer

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/owlcms/video/internal/access"
	"github.com/owlcms/video/internal/config"
)

// Browsers send "Origin: null" on form posts from a page served with
// "Referrer-Policy: no-referrer", so the sign-in page must not use it.
// A broadcaster signing in from anywhere posts from the page's own origin.
func TestSignInFormKeepsBrowserOrigin(t *testing.T) {
	service := access.Open(t.TempDir())
	token, _, err := service.AddViewer("RAI")
	if err != nil {
		t.Fatal(err)
	}
	SetAccess(service)
	t.Cleanup(func() { SetAccess(nil) })
	router := newRouter(ModuleAvailability{Replays: true})

	page := httptest.NewRecorder()
	router.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "https://a.replays.example.org/", nil))
	if page.Code != http.StatusUnauthorized {
		t.Fatalf("sign-in page: %d", page.Code)
	}
	if policy := page.Header().Get("Referrer-Policy"); policy != "same-origin" {
		t.Fatalf("sign-in page Referrer-Policy %q would make the browser post Origin: null", policy)
	}

	post := func(origin string) int {
		form := url.Values{"token": {token}, "next": {"/"}}
		r := httptest.NewRequest(http.MethodPost, "https://a.replays.example.org/auth/token", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}
	if code := post("https://a.replays.example.org"); code != http.StatusSeeOther {
		t.Fatalf("form post from the page's own origin: %d, want 303", code)
	}
	if code := post("https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("form post from another site: %d, want 403", code)
	}
	if code := post("null"); code != http.StatusForbidden {
		t.Fatalf("form post with opaque origin: %d, want 403", code)
	}
}

func TestBrowsingPageShowsSignOutWithSessionTooltip(t *testing.T) {
	oldDir := config.GetVideoDir()
	config.SetVideoDir(t.TempDir())
	t.Cleanup(func() { config.SetVideoDir(oldDir) })
	router := newRouter(ModuleAvailability{Replays: true})

	open := httptest.NewRecorder()
	router.ServeHTTP(open, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(open.Body.String(), `class="signout"`) {
		t.Fatal("sign-out button shown in open state")
	}

	service := access.Open(t.TempDir())
	token, _, err := service.AddViewer("RAI")
	if err != nil {
		t.Fatal(err)
	}
	SetAccess(service)
	t.Cleanup(func() { SetAccess(nil) })
	exchange := httptest.NewRecorder()
	router.ServeHTTP(exchange, httptest.NewRequest(http.MethodGet, "/?token="+token, nil))
	cookies := exchange.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("no session cookie: %d", exchange.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(cookies[0])
	page := httptest.NewRecorder()
	router.ServeHTTP(page, r)
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, `class="signout"`) ||
		!strings.Contains(body, `title="Signed in as RAI, valid until`) {
		t.Fatalf("browsing page lacks sign-out button with session tooltip: %d", page.Code)
	}
	if strings.Contains(body, "<p>Signed in as") {
		t.Fatal("session details still shown as a paragraph")
	}
}
