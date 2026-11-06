package httpServer

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/logging"
	"github.com/owlcms/video/internal/state"
)

func withReplayTestVideoDir(t *testing.T) string {
	t.Helper()

	oldVideoDir := config.GetVideoDir()
	oldSession := state.CurrentSession
	publishedReplayMu.Lock()
	oldPublishedReplays := publishedReplays
	oldInFlightSince := inFlightSince
	publishedReplays = make(map[int]ReplayCameraState)
	inFlightSince = make(map[int]time.Time)
	publishedReplayMu.Unlock()

	videoDir := t.TempDir()
	config.SetVideoDir(videoDir)
	state.CurrentSession = "3"

	t.Cleanup(func() {
		config.SetVideoDir(oldVideoDir)
		state.CurrentSession = oldSession
		publishedReplayMu.Lock()
		publishedReplays = oldPublishedReplays
		inFlightSince = oldInFlightSince
		publishedReplayMu.Unlock()
	})

	return videoDir
}

func writeReplayTestFile(t *testing.T, videoDir string, session string, filename string) {
	t.Helper()

	sessionDir := filepath.Join(videoDir, session)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("failed to create session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, filename), []byte("video"), 0644); err != nil {
		t.Fatalf("failed to write replay file: %v", err)
	}
}

func TestModuleRoutesReflectAvailability(t *testing.T) {
	oldConfigDir := config.ConfigDir
	config.ConfigDir = t.TempDir()
	t.Cleanup(func() {
		config.ConfigDir = oldConfigDir
	})

	camerasConfig := []byte("[multicast]\nstartPort = 9001\n")
	if err := os.WriteFile(config.CamerasConfigPath(), camerasConfig, 0644); err != nil {
		t.Fatalf("write cameras config: %v", err)
	}

	camerasOnly := newRouter(ModuleAvailability{Cameras: true})
	recorder := httptest.NewRecorder()
	camerasOnly.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/cameras/config", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected cameras config from cameras-only server, got %d", recorder.Code)
	}
	if recorder.Body.String() != string(camerasConfig) {
		t.Fatalf("unexpected cameras config response %q", recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	camerasOnly.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected replays route to be unavailable, got %d", recorder.Code)
	}

	replaysOnly := newRouter(ModuleAvailability{Replays: true})
	recorder = httptest.NewRecorder()
	replaysOnly.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/cameras/config", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected cameras config to be unavailable, got %d", recorder.Code)
	}
}

func TestPublishedReplayStatePublishesAndClears(t *testing.T) {
	videoDir := withReplayTestVideoDir(t)
	filename := "2026-05-08_11h09m59s_LARRIVEE_Mariane_CLEANJERK_attempt1_Camera1.mp4"
	writeReplayTestFile(t, videoDir, "3", filename)

	if _, err := findPublishedReplayForCamera(1); !os.IsNotExist(err) {
		t.Fatalf("expected no replay before state publish, got %v", err)
	}

	if err := PublishReplayState(1, "3", filename, 12345); err != nil {
		t.Fatalf("failed to publish replay state: %v", err)
	}

	replay, err := findPublishedReplayForCamera(1)
	if err != nil {
		t.Fatalf("failed to read published replay state: %v", err)
	}
	if replay.VideoPath != "/videos/3/"+filename {
		t.Fatalf("unexpected replay path %q", replay.VideoPath)
	}
	if replay.DurationMs != 12345 {
		t.Fatalf("unexpected replay duration %d", replay.DurationMs)
	}

	if err := ClearPublishedReplayState(1); err != nil {
		t.Fatalf("failed to clear published replay state: %v", err)
	}
	if _, err := findPublishedReplayForCamera(1); !os.IsNotExist(err) {
		t.Fatalf("expected no replay after state clear, got %v", err)
	}
}

func TestHandleReplayServesLatestReadyClipFromDisk(t *testing.T) {
	videoDir := withReplayTestVideoDir(t)
	get := func(camera string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/replay/"+camera, nil), map[string]string{"camera": camera})
		handleReplay(recorder, request)
		return recorder
	}
	write := func(name, body string, modTime time.Time) {
		t.Helper()
		path := filepath.Join(videoDir, "3", name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}

	started := time.Now()
	if got := get("1"); got.Code != http.StatusNotFound {
		t.Fatalf("no clip yet: got %d, want 404", got.Code)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("no clip yet: answered after %s, must not wait", elapsed)
	}

	// After a restart nothing is published; the newest clip on disk is served.
	previous := "2026-05-08_11h09m59s_LARRIVEE_Mariane_CLEANJERK_attempt1_Camera1.mp4"
	write(previous, "previous", time.Now().Add(-time.Minute))
	write("2026-05-08_10h00m00s_OTHER_Athlete_SNATCH_attempt1_Camera1.mp4", "older", time.Now().Add(-2*time.Minute))
	got := get("1")
	if got.Code != http.StatusOK || got.Body.String() != "previous" {
		t.Fatalf("after restart: got %d %q, want the newest clip on disk", got.Code, got.Body.String())
	}
	if got.Header().Get("X-Replay-Filename") != previous || got.Header().Get("Connection") != "close" {
		t.Fatalf("unexpected replay headers %v", got.Header())
	}

	// The next athlete's clock starts: the previous clip is still served.
	if err := ClearPublishedReplayState(1); err != nil {
		t.Fatal(err)
	}
	if got := get("1"); got.Code != http.StatusOK || got.Body.String() != "previous" {
		t.Fatalf("while recording: got %d %q, want the previous clip", got.Code, got.Body.String())
	}

	// The trim writes the new clip in place; until published it is skipped.
	next := "2026-05-08_11h12m30s_SMITH_Jane_SNATCH_attempt2_Camera1.mp4"
	write(next, "partial", time.Now().Add(time.Second))
	if got := get("1"); got.Code != http.StatusOK || got.Body.String() != "previous" {
		t.Fatalf("while trimming: got %d %q, want the previous clip", got.Code, got.Body.String())
	}

	if err := PublishReplayState(1, "3", next, 12345); err != nil {
		t.Fatal(err)
	}
	if got := get("1"); got.Code != http.StatusOK || got.Body.String() != "partial" {
		t.Fatalf("after publish: got %d %q, want the new clip", got.Code, got.Body.String())
	}

	if got := get("2"); got.Code != http.StatusNotFound {
		t.Fatalf("camera without clips: got %d, want 404", got.Code)
	}
}

func TestHandleReplayOmitsQueryFromRequestLog(t *testing.T) {
	videoDir := withReplayTestVideoDir(t)
	filename := "2026-05-08_11h09m59s_LARRIVEE_Mariane_CLEANJERK_attempt1_Camera1.mp4"
	writeReplayTestFile(t, videoDir, "3", filename)
	if err := PublishReplayState(1, "3", filename, 12345); err != nil {
		t.Fatalf("publish replay state: %v", err)
	}

	var logOutput bytes.Buffer
	oldWriter := logging.InfoLogger.Writer()
	logging.InfoLogger.SetOutput(&logOutput)
	t.Cleanup(func() { logging.InfoLogger.SetOutput(oldWriter) })

	request := httptest.NewRequest(http.MethodGet, "/replay/1.mp4?token=sample-secret&other=sample-query", nil)
	recorder := httptest.NewRecorder()
	newRouter(ModuleAvailability{Replays: true}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "video" {
		t.Fatalf("replay response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	logged := logOutput.String()
	if !strings.Contains(logged, `path="/replay/1.mp4"`) {
		t.Fatalf("request path missing from log: %s", logged)
	}
	if strings.Contains(logged, "sample-secret") || strings.Contains(logged, "sample-query") {
		t.Fatalf("request query leaked into log: %s", logged)
	}
}

func TestHandleReplayStateIncludesPublishedDuration(t *testing.T) {
	videoDir := withReplayTestVideoDir(t)
	filename := "2026-05-08_11h09m59s_LARRIVEE_Mariane_CLEANJERK_attempt1_Camera1.mp4"
	writeReplayTestFile(t, videoDir, "3", filename)

	if err := PublishReplayState(1, "3", filename, 12345); err != nil {
		t.Fatalf("failed to publish replay state: %v", err)
	}

	recorder := httptest.NewRecorder()
	handleReplayState(recorder, httptest.NewRequest(http.MethodGet, "/api/replay-state", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected replay state to return 200, got %d", recorder.Code)
	}

	var response ReplayStateResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode replay state response: %v", err)
	}
	if len(response.Cameras) != 4 {
		t.Fatalf("expected 4 cameras, got %d", len(response.Cameras))
	}
	if response.Cameras[0].DurationMs != 12345 {
		t.Fatalf("unexpected replay state duration %d", response.Cameras[0].DurationMs)
	}
	if response.Cameras[0].VideoPath != "/videos/3/"+filename {
		t.Fatalf("unexpected replay state path %q", response.Cameras[0].VideoPath)
	}
}
