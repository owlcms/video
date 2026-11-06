package httpServer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/logging"
)

var (
	publishedReplayMu sync.Mutex
	// publishedReplays is the current attempt's clip per camera, as reported by
	// /api/replay-state and the Ready message. It is cleared when a recording
	// starts, so clients can tell that a new clip is on its way.
	publishedReplays = make(map[int]ReplayCameraState)
	// inFlightSince records when the current recording started for a camera.
	// Until its clip is published, files written since then may be incomplete
	// and /replay/{camera} does not serve them.
	inFlightSince = make(map[int]time.Time)
)

func replayLogTimestamp() string {
	return time.Now().Format("2006-01-02 15:04:05.000 MST")
}

func sanitizeReplayFilename(filename string) (string, error) {
	trimmed := strings.TrimSpace(filename)
	if trimmed == "" || trimmed == "." || trimmed == ".." || strings.ContainsAny(trimmed, `/\\`) {
		return "", os.ErrInvalid
	}

	return trimmed, nil
}

// ClearPublishedReplayState removes the per-camera replay pointer before a new recording/trim starts.
func ClearPublishedReplayState(camera int) error {
	if camera < 1 {
		return fmt.Errorf("invalid camera number %d", camera)
	}

	publishedReplayMu.Lock()
	_, existed := publishedReplays[camera]
	delete(publishedReplays, camera)
	if _, inFlight := inFlightSince[camera]; !inFlight {
		inFlightSince[camera] = time.Now()
	}
	publishedReplayMu.Unlock()

	if existed {
		logging.InfoLogger.Printf("=== REPLAY STATE CLEARED timestamp=%s camera=%d ===", replayLogTimestamp(), camera)
	} else {
		logging.InfoLogger.Printf("=== REPLAY STATE ALREADY ABSENT timestamp=%s camera=%d ===", replayLogTimestamp(), camera)
	}
	return nil
}

// PublishReplayState atomically points /replay/{camera} and /api/replay-state at a completed replay file.
func PublishReplayState(camera int, session string, filename string, durationMs int64) error {
	if camera < 1 {
		return fmt.Errorf("invalid camera number %d", camera)
	}

	cleanSession, err := sanitizeReplaySessionID(session)
	if err != nil {
		return fmt.Errorf("invalid replay session %q: %w", session, err)
	}

	cleanFilename, err := sanitizeReplayFilename(filename)
	if err != nil {
		return fmt.Errorf("invalid replay filename %q: %w", filename, err)
	}

	parsedReplay, ok := parseReplayFilename(cleanSession, cleanFilename)
	if !ok {
		return fmt.Errorf("replay filename does not match expected format: %s", cleanFilename)
	}
	if parsedReplay.Camera != camera {
		return fmt.Errorf("replay filename camera %d does not match state camera %d", parsedReplay.Camera, camera)
	}

	videoPath := filepath.Join(config.GetVideoDir(), parsedReplay.Session, parsedReplay.Filename)
	info, err := os.Stat(videoPath)
	if err != nil {
		return fmt.Errorf("completed replay file is not available: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("completed replay path is a directory: %s", videoPath)
	}

	replayState := ReplayCameraState{
		Camera:        camera,
		Available:     true,
		Session:       parsedReplay.Session,
		Filename:      parsedReplay.Filename,
		VideoPath:     parsedReplay.URL,
		AthleteName:   parsedReplay.Athlete,
		LiftType:      parsedReplay.LiftType,
		AttemptNumber: parsedReplay.AttemptNumber,
		Timestamp:     parsedReplay.Timestamp,
	}
	if durationMs > 0 {
		replayState.DurationMs = durationMs
	}

	publishedReplayMu.Lock()
	publishedReplays[camera] = replayState
	delete(inFlightSince, camera)
	publishedReplayMu.Unlock()

	logging.InfoLogger.Printf("=== REPLAY STATE PUBLISHED timestamp=%s camera=%d session=%q filename=%q durationMs=%d video=%q ===", replayLogTimestamp(), camera, parsedReplay.Session, parsedReplay.Filename, replayState.DurationMs, videoPath)
	return nil
}

// snapshotPublishedReplays returns a copy of the per-camera published
// replay pointers (camera number ascending). Used to attach freshly-
// published clip URLs to the Ready status broadcast so subscribers don't
// need a follow-up GET /api/replay-state.
func snapshotPublishedReplays() []ReplayCameraState {
	publishedReplayMu.Lock()
	defer publishedReplayMu.Unlock()
	if len(publishedReplays) == 0 {
		return nil
	}
	cameras := make([]int, 0, len(publishedReplays))
	for camera := range publishedReplays {
		cameras = append(cameras, camera)
	}
	sort.Ints(cameras)
	out := make([]ReplayCameraState, 0, len(cameras))
	for _, camera := range cameras {
		out = append(out, publishedReplays[camera])
	}
	return out
}

// SnapshotPublishedReplays is the exported accessor.
func SnapshotPublishedReplays() []ReplayCameraState {
	return snapshotPublishedReplays()
}

func findPublishedReplayForCamera(camera int) (*ReplayCameraState, error) {
	if camera < 1 {
		return nil, fmt.Errorf("invalid camera number %d", camera)
	}

	publishedReplayMu.Lock()
	replayState, ok := publishedReplays[camera]
	publishedReplayMu.Unlock()
	if !ok {
		return nil, os.ErrNotExist
	}

	videoPath := filepath.Join(config.GetVideoDir(), replayState.Session, replayState.Filename)
	info, err := os.Stat(videoPath)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("published replay path is a directory: %s", videoPath)
	}

	return &replayState, nil
}

// latestReadyReplayForCamera returns the newest finished clip for a camera,
// read from disk as listed on the replays page. It never waits: a clip being
// recorded or trimmed is skipped, and the previous one is returned.
func latestReadyReplayForCamera(camera int) (*ReplayCameraState, error) {
	if camera < 1 {
		return nil, fmt.Errorf("invalid camera number %d", camera)
	}

	publishedReplayMu.Lock()
	notAfter := inFlightSince[camera]
	publishedReplayMu.Unlock()
	return newestReplayOnDisk(camera, notAfter)
}

// newestReplayOnDisk scans the session directories for the newest clip of a
// camera. Files modified at or after notAfter (when set) are ignored.
func newestReplayOnDisk(camera int, notAfter time.Time) (*ReplayCameraState, error) {
	videoDir := config.GetVideoDir()
	sessions, err := os.ReadDir(videoDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}

	var newest *ParsedReplayFile
	for _, session := range sessions {
		if !session.IsDir() || session.Name() == "unsorted" {
			continue
		}
		files, err := os.ReadDir(filepath.Join(videoDir, session.Name()))
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.IsDir() {
				continue
			}
			parsed, ok := parseReplayFilename(session.Name(), file.Name())
			if !ok || parsed.Camera != camera {
				continue
			}
			if !notAfter.IsZero() {
				info, err := file.Info()
				if err != nil || !info.ModTime().Before(notAfter) {
					continue
				}
			}
			if newest == nil || parsed.Timestamp > newest.Timestamp ||
				(parsed.Timestamp == newest.Timestamp && parsed.Filename > newest.Filename) {
				newest = parsed
			}
		}
	}
	if newest == nil {
		return nil, os.ErrNotExist
	}

	return &ReplayCameraState{
		Camera:        camera,
		Available:     true,
		Session:       newest.Session,
		Filename:      newest.Filename,
		VideoPath:     newest.URL,
		AthleteName:   newest.Athlete,
		LiftType:      newest.LiftType,
		AttemptNumber: newest.AttemptNumber,
		Timestamp:     newest.Timestamp,
	}, nil
}
