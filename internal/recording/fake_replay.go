package recording

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/owlcms/video/internal/config"
)

// FakeReplayResult reports whether a camera source produced a playable test recording.
type FakeReplayResult struct {
	Camera int
	Err    error
}

// RunFakeReplayTest captures a short disposable recording from each camera using
// the same FFmpeg input and output arguments as a real replay recording.
func RunFakeReplayTest(cameras []config.CameraConfiguration, duration, timeout time.Duration) []FakeReplayResult {
	return RunFakeReplayTestContext(context.Background(), cameras, duration, timeout)
}

// RunFakeReplayTestContext captures a short disposable recording from each
// camera and stops all active captures if ctx is canceled.
func RunFakeReplayTestContext(ctx context.Context, cameras []config.CameraConfiguration, duration, timeout time.Duration) []FakeReplayResult {
	results := make([]FakeReplayResult, len(cameras))
	if duration <= 0 {
		duration = 2 * time.Second
	}
	if timeout <= duration {
		timeout = duration + 8*time.Second
	}

	probeDir, err := os.MkdirTemp(config.GetVideoDir(), ".replay-probe-")
	if err != nil {
		for index := range results {
			results[index] = FakeReplayResult{Camera: index + 1, Err: fmt.Errorf("create temporary recording directory: %w", err)}
		}
		return results
	}
	defer os.RemoveAll(probeDir)

	var waitGroup sync.WaitGroup
	for index, camera := range cameras {
		waitGroup.Add(1)
		go func(index int, camera config.CameraConfiguration) {
			defer waitGroup.Done()
			if err := ctx.Err(); err != nil {
				results[index] = FakeReplayResult{Camera: index + 1, Err: err}
				return
			}
			filename := filepath.Join(probeDir, fmt.Sprintf("camera-%d.mkv", index+1))
			args := buildRecordingArgs(filename, camera)
			args = append(args[:len(args)-1], "-t", fmt.Sprintf("%.3f", duration.Seconds()), filename)
			cmd := CreateFfmpegCmd(args, "fake replay test")
			if err := cmd.Start(); err != nil {
				results[index] = FakeReplayResult{Camera: index + 1, Err: fmt.Errorf("start ffmpeg: %w", err)}
				return
			}

			finished := make(chan error, 1)
			go func() { finished <- cmd.Wait() }()
			select {
			case err := <-finished:
				if err != nil {
					results[index] = FakeReplayResult{Camera: index + 1, Err: fmt.Errorf("capture test replay: %w", err)}
					return
				}
			case <-time.After(timeout):
				_ = forceKillCmd(cmd)
				<-finished
				results[index] = FakeReplayResult{Camera: index + 1, Err: fmt.Errorf("capture test replay timed out after %s", timeout)}
				return
			case <-ctx.Done():
				_ = forceKillCmd(cmd)
				<-finished
				results[index] = FakeReplayResult{Camera: index + 1, Err: ctx.Err()}
				return
			}

			info, err := os.Stat(filename)
			if err != nil {
				results[index] = FakeReplayResult{Camera: index + 1, Err: fmt.Errorf("read test recording: %w", err)}
				return
			}
			if info.Size() == 0 || probeVideoDurationMs(filename) <= 0 {
				results[index] = FakeReplayResult{Camera: index + 1, Err: fmt.Errorf("test recording contains no playable video")}
				return
			}
			results[index] = FakeReplayResult{Camera: index + 1}
		}(index, camera)
	}
	waitGroup.Wait()

	return results
}
