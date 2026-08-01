package httpServer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/state"
)

func withReplayTestVideoDir(t *testing.T) string {
	t.Helper()

	oldVideoDir := config.GetVideoDir()
	oldSession := state.CurrentSession
	oldWaitTimeout := replayReadyWaitTimeout
	oldPollInterval := replayReadyPollInterval
	publishedReplayMu.Lock()
	oldPublishedReplays := make(map[int]ReplayCameraState, len(publishedReplays))
	for camera, replay := range publishedReplays {
		oldPublishedReplays[camera] = replay
	}
	publishedReplayMu.Unlock()
	replayGenerationMu.Lock()
	oldGenerations := make(map[int]int64, len(replayGenerations))
	for camera, generation := range replayGenerations {
		oldGenerations[camera] = generation
	}
	replayGenerationMu.Unlock()

	videoDir := t.TempDir()
	config.SetVideoDir(videoDir)
	state.CurrentSession = "3"
	replayReadyWaitTimeout = 20 * time.Millisecond
	replayReadyPollInterval = time.Millisecond
	publishedReplayMu.Lock()
	publishedReplays = make(map[int]ReplayCameraState)
	publishedReplayMu.Unlock()

	t.Cleanup(func() {
		config.SetVideoDir(oldVideoDir)
		state.CurrentSession = oldSession
		replayReadyWaitTimeout = oldWaitTimeout
		replayReadyPollInterval = oldPollInterval
		publishedReplayMu.Lock()
		publishedReplays = oldPublishedReplays
		publishedReplayMu.Unlock()
		replayGenerationMu.Lock()
		replayGenerations = oldGenerations
		replayGenerationMu.Unlock()
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

func TestHandleReplayRequiresPublishedState(t *testing.T) {
	videoDir := withReplayTestVideoDir(t)
	filename := "2026-05-08_11h09m59s_LARRIVEE_Mariane_CLEANJERK_attempt1_Camera1.mp4"
	writeReplayTestFile(t, videoDir, "3", filename)

	recorder := httptest.NewRecorder()
	handleReplay(recorder, mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/replay/1", nil), map[string]string{"camera": "1"}))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected missing published state to return 404, got %d", recorder.Code)
	}

	if err := PublishReplayState(1, "3", filename, 12345); err != nil {
		t.Fatalf("failed to publish replay state: %v", err)
	}

	recorder = httptest.NewRecorder()
	handleReplay(recorder, mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/replay/1", nil), map[string]string{"camera": "1"}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected published state to return 200, got %d", recorder.Code)
	}
	if recorder.Body.String() != "video" {
		t.Fatalf("unexpected response body %q", recorder.Body.String())
	}
	if recorder.Header().Get("X-Replay-One-Shot") != "true" {
		t.Fatalf("expected replay response to be marked one-shot")
	}
	if recorder.Header().Get("Connection") != "close" {
		t.Fatalf("expected replay response to request connection close")
	}

	recorder = httptest.NewRecorder()
	handleReplay(recorder, mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/replay/1", nil), map[string]string{"camera": "1"}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected reusable published state to serve another independent request, got %d", recorder.Code)
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

func TestReplayResponseRecorderAbortsWhenGenerationChanges(t *testing.T) {
	withReplayTestVideoDir(t)

	generation := currentReplayGeneration(1)
	response := httptest.NewRecorder()
	recorder := &replayResponseRecorder{
		ResponseWriter: response,
		camera:         1,
		generation:     generation,
	}

	bumpReplayGeneration(1)
	written, err := recorder.Write([]byte("video"))
	if err == nil {
		t.Fatal("expected stale replay response write to fail")
	}
	if written != 0 {
		t.Fatalf("expected no bytes to be written after generation change, got %d", written)
	}
	if !recorder.aborted {
		t.Fatal("expected recorder to mark response aborted")
	}
}
