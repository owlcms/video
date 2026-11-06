package httpServer

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owlcms/video/internal/access"
	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/logging"
)

func TestProtectedRangesAndCredentialConflicts(t *testing.T) {
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
	service := access.Open(t.TempDir())
	token, _, err := service.AddViewer("RAI")
	if err != nil {
		t.Fatal(err)
	}
	SetAccess(service)
	router := newRouter(ModuleAvailability{Replays: true})
	r := httptest.NewRequest(http.MethodGet, "/videos/test.mp4?token="+token, nil)
	r.Header.Set("Range", "bytes=0-1")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != http.StatusPartialContent || w.Body.String() != "vi" {
		t.Fatalf("range response: %d %q", w.Code, w.Body.String())
	}
	for _, target := range []string{
		"/videos/test.mp4?token=" + token + "&token=" + token,
		"/videos/test.mp4?token=wrong",
	} {
		r = httptest.NewRequest(http.MethodGet, target, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("conflicting credentials: %d", w.Code)
		}
	}
}

func TestAccessLogsDoNotContainTokens(t *testing.T) {
	service := access.Open(t.TempDir())
	token, _, err := service.AddViewer("RAI")
	if err != nil {
		t.Fatal(err)
	}
	SetAccess(service)
	t.Cleanup(func() { SetAccess(nil) })
	oldInfo := logging.InfoLogger.Writer()
	oldWarning := logging.WarningLogger.Writer()
	var logs bytes.Buffer
	logging.InfoLogger.SetOutput(&logs)
	logging.WarningLogger.SetOutput(&logs)
	t.Cleanup(func() {
		logging.InfoLogger.SetOutput(oldInfo)
		logging.WarningLogger.SetOutput(oldWarning)
	})
	router := newRouter(ModuleAvailability{Replays: true})
	for _, target := range []string{"/?token=" + token, "/api/sessions?token=badsecret"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
	}
	if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "badsecret") {
		t.Fatal("access log contains a credential")
	}
	if !strings.Contains(logs.String(), "RAI") {
		t.Fatal("access log does not identify token holder")
	}
}

func TestHandleReplayAndStateAcceptGeneratedToken(t *testing.T) {
	videoDir := withReplayTestVideoDir(t)
	filename := "2026-05-08_11h09m59s_LARRIVEE_Mariane_CLEANJERK_attempt1_Camera1.mp4"
	writeReplayTestFile(t, videoDir, "3", filename)
	if err := PublishReplayState(1, "3", filename, 12345); err != nil {
		t.Fatalf("publish replay state: %v", err)
	}

	service := access.Open(t.TempDir())
	SetAccess(service)
	t.Cleanup(func() { SetAccess(nil) })
	router := newRouter(ModuleAvailability{Replays: true})

	for _, path := range []string{"/replay/1", "/replay/1.mp4", "/api/replay-state"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("open mode %s: got %d, want 200", path, w.Code)
		}
	}

	token, viewer, err := service.AddViewer("OBS")
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/replay/1", "/replay/1.mp4", "/api/replay-state"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("protected without token %s: got %d, want 401", path, w.Code)
		}

		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"?token="+token, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("protected with query token %s: got %d, want 200", path, w.Code)
		}

		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("protected with bearer token %s: got %d, want 200", path, w.Code)
		}
	}

	if err := service.RevokeViewer(viewer.ID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/replay/1", nil))
	if w.Code != http.StatusOK || w.Body.String() != "video" {
		t.Fatalf("after revoking last token: got %d %q, want 200 \"video\"", w.Code, w.Body.String())
	}
}
