package httpServer

import (
	"context"
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
	replayReadyWaitTimeout  = 45 * time.Second
	replayReadyPollInterval = 100 * time.Millisecond
	publishedReplayMu       sync.Mutex
	publishedReplays        = make(map[int]ReplayCameraState)
	replayGenerationMu      sync.Mutex
	replayGenerations       = make(map[int]int64)
)

func currentReplayGeneration(camera int) int64 {
	replayGenerationMu.Lock()
	defer replayGenerationMu.Unlock()
	return replayGenerations[camera]
}

func bumpReplayGeneration(camera int) int64 {
	replayGenerationMu.Lock()
	defer replayGenerationMu.Unlock()
	replayGenerations[camera]++
	return replayGenerations[camera]
}

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

	generation := bumpReplayGeneration(camera)
	publishedReplayMu.Lock()
	_, existed := publishedReplays[camera]
	delete(publishedReplays, camera)
	publishedReplayMu.Unlock()

	if existed {
		logging.InfoLogger.Printf("=== REPLAY STATE CLEARED timestamp=%s camera=%d generation=%d ===", replayLogTimestamp(), camera, generation)
	} else {
		logging.InfoLogger.Printf("=== REPLAY STATE ALREADY ABSENT timestamp=%s camera=%d generation=%d ===", replayLogTimestamp(), camera, generation)
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

func waitForPublishedReplayForCamera(ctx context.Context, camera int) (*ReplayCameraState, error) {
	replay, err := findPublishedReplayForCamera(camera)
	if err == nil || !os.IsNotExist(err) {
		return replay, err
	}

	deadline := time.NewTimer(replayReadyWaitTimeout)
	defer deadline.Stop()

	ticker := time.NewTicker(replayReadyPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, os.ErrNotExist
		case <-ticker.C:
			replay, err = findPublishedReplayForCamera(camera)
			if err == nil || !os.IsNotExist(err) {
				return replay, err
			}
		}
	}
}
