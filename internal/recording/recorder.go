package recording

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/httpServer"
	"github.com/owlcms/video/internal/logging"
	"github.com/owlcms/video/internal/state"
)

var (
	currentRecordings []*exec.Cmd
	currentStdin      []*os.File
	currentFileNames  []string
	currentAttempt    httpServer.StatusAttemptDetails
)

// cleanParams splits a parameter string and removes outer quotes from each parameter
func cleanParams(params string) []string {
	fields := strings.Fields(params)
	cleaned := make([]string, 0, len(fields))

	for _, field := range fields {
		// Remove outer quotes if present
		if (strings.HasPrefix(field, "\"") && strings.HasSuffix(field, "\"")) ||
			(strings.HasPrefix(field, "'") && strings.HasSuffix(field, "'")) {
			field = field[1 : len(field)-1]
		}
		cleaned = append(cleaned, field)
	}
	return cleaned
}

// buildRecordingArgs builds the ffmpeg arguments for recording
func buildRecordingArgs(fileName string, camera config.CameraConfiguration) []string {
	args := []string{"-y", "-f", camera.Format}

	// Check if the source is a UDP stream
	isUdpSource := strings.HasPrefix(camera.FfmpegCamera, "udp:")

	// Input parameters (before -i)
	if camera.InputParameters != "" {
		args = append(args, cleanParams(camera.InputParameters)...)
	}
	// Skip size and fps for UDP sources as they are pre-formatted
	if !isUdpSource {
		if camera.Size != "" {
			args = append(args, "-s", camera.Size)
		}
		if camera.Fps > 0 {
			args = append(args, "-r", fmt.Sprintf("%d", camera.Fps))
		}
	}

	// Input source
	args = append(args, "-i", camera.FfmpegCamera)

	// Output parameters (after -i)
	if camera.OutputParameters != "" {
		args = append(args, cleanParams(camera.OutputParameters)...)
	}
	// Treat legacy params as additional output parameters
	if camera.Params != "" {
		args = append(args, cleanParams(camera.Params)...)
	}

	args = append(args, fileName)
	return args
}

// buildTrimmingArgs builds the ffmpeg arguments for trimming.
//
// keepFromEndMs is the number of milliseconds to keep, counted backwards from
// the end of the input file. We seek from EOF (-sseof) instead of from the
// start because the recorder's start timestamp is unreliable: ffmpeg takes a
// variable amount of time (often several seconds) to write the first frame to
// the mkv after StartRecording is called, especially when waiting for the next
// IDR on the UDP stream. The end of the file, however, is always "now" — so
// keeping the last N seconds is independent of recorder startup latency.
func buildTrimmingArgs(keepFromEndMs int64, currentFileName, finalFileName string, camera config.CameraConfiguration) []string {
	args := []string{"-y"}
	// Note: InputParameters are NOT used during trimming as they are for camera capture only

	if keepFromEndMs > 0 {
		// -sseof takes a NEGATIVE value meaning "seek N seconds before end of file".
		// Use fractional seconds for sub-second accuracy. With -c copy this still
		// snaps to the previous keyframe, which is fine given the 1-second GOP.
		args = append(args, "-sseof", fmt.Sprintf("-%.3f", float64(keepFromEndMs)/1000.0))
	}
	// Input file
	args = append(args, "-i", currentFileName)

	if camera.Recode {
		// When recoding, use software encoder to convert to H.264
		// Do NOT use OutputParameters here as they are for recording, not transcoding
		logging.InfoLogger.Printf("Recode is enabled for camera: %s", camera.FfmpegCamera)
		args = append(args,
			"-c:v", "libx264",
			"-crf", "18",
			"-preset", "ultrafast",
			"-profile:v", "main",
			"-pix_fmt", "yuv420p",
			"-avoid_negative_ts", "make_zero",
		)
	} else {
		// When not recoding, just copy the stream (already in H.264 format)
		args = append(args,
			"-c", "copy",
			"-avoid_negative_ts", "make_zero",
			"-movflags", "+faststart",
		)
	}

	args = append(args, finalFileName)
	return args
}

// probeVideoDurationMs runs ffprobe against a finalized video file and returns
// its actual duration in milliseconds. Returns 0 (and logs a warning) if the
// duration cannot be determined; callers should fall back to the requested
// trim length in that case.
func probeVideoDurationMs(filePath string) int64 {
	ffmpegPath := config.GetFFmpegPath()
	ffprobePath := resolveFFprobePath(ffmpegPath)
	if ffprobePath == "" {
		logging.WarningLogger.Printf("ffprobe path not resolved; cannot probe duration of %s", filePath)
		return 0
	}
	cmd := CreateHiddenCmd(ffprobePath,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		logging.WarningLogger.Printf("ffprobe failed for %s: %v", filePath, err)
		return 0
	}
	trimmed := strings.TrimSpace(out.String())
	if trimmed == "" {
		logging.WarningLogger.Printf("ffprobe returned no duration for %s", filePath)
		return 0
	}
	seconds, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || seconds <= 0 {
		logging.WarningLogger.Printf("ffprobe duration parse failed for %s (raw=%q): %v", filePath, trimmed, err)
		return 0
	}
	return int64(seconds*1000.0 + 0.5)
}

// StartRecording starts recording videos using ffmpeg for all configured cameras
func StartRecording(fullName, liftTypeKey string, attemptNumber int) error {
	Recording = true
	cameras := config.GetCameraConfigs()
	if len(cameras) == 0 {
		return fmt.Errorf("no camera configurations available")
	}

	if err := os.MkdirAll(config.GetVideoDir(), os.ModePerm); err != nil {
		return fmt.Errorf("failed to create video directory: %w", err)
	}

	for i := range cameras {
		cameraNumber := i + 1
		if err := httpServer.ClearPublishedReplayState(cameraNumber); err != nil {
			logging.ErrorLogger.Printf("Failed to clear published replay state at recording start for Camera %d: %v", cameraNumber, err)
		}
	}

	displayName := strings.ReplaceAll(fullName, "_", " ")
	currentAttempt = httpServer.StatusAttemptDetails{
		Session:       state.CurrentSession,
		AthleteName:   displayName,
		LiftType:      liftTypeKey,
		AttemptNumber: attemptNumber,
	}

	fullName = strings.ReplaceAll(fullName, " ", "_")

	var cmds []*exec.Cmd
	var stdins []*os.File
	var fileNames []string

	for i, camera := range cameras {
		fileName := filepath.Join(config.GetVideoDir(), fmt.Sprintf("%s_%s_attempt%d_Camera%d_%d.mkv", fullName, liftTypeKey, attemptNumber, i+1, state.LastStartTime))
		args := buildRecordingArgs(fileName, camera)

		if config.NoVideo {
			cmd := CreateFfmpegCmd(args, "recording")
			logging.InfoLogger.Printf("Simulating start recording video for Camera %d: %s", i+1, cmd.String())
			logging.InfoLogger.Printf("ffmpeg command for Camera %d: %s", i+1, cmd.String())
			fileNames = append(fileNames, fileName)
			state.LastTimerStopTime = 0
			continue
		}

		cmd := CreateFfmpegCmd(args, "recording")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("failed to create stdin pipe for Camera %d: %w", i+1, err)
		}

		logging.InfoLogger.Printf("Executing command for Camera %d: %s", i+1, cmd.String())
		if err := cmd.Start(); err != nil {
			stdin.Close()
			return fmt.Errorf("failed to start ffmpeg for Camera %d: %w", i+1, err)
		}

		cmds = append(cmds, cmd)
		stdins = append(stdins, stdin.(*os.File))
		fileNames = append(fileNames, fileName)
	}

	currentRecordings = cmds
	currentStdin = stdins
	currentFileNames = fileNames
	state.LastTimerStopTime = 0

	httpServer.SendStatusWithDetails(httpServer.Recording, fmt.Sprintf("Recording: %s - %s attempt %d",
		currentAttempt.AthleteName,
		currentAttempt.LiftType,
		currentAttempt.AttemptNumber), currentAttempt)

	logging.InfoLogger.Printf("Started recording videos: %v", fileNames)
	return nil
}

// trimVideo handles the trimming of a single video file.
// keepFromEndMs is the number of milliseconds to keep counted from end-of-file
// (see buildTrimmingArgs for rationale).
func trimVideo(wg *sync.WaitGroup, i int, currentFileName string, keepFromEndMs int64, startTime int64, sessionDir string, fullSessionDir string, timestamp string, finalFileNames []string, attemptDetails httpServer.StatusAttemptDetails) {
	defer wg.Done()
	cameraNumber := i + 1
	if err := httpServer.ClearPublishedReplayState(cameraNumber); err != nil {
		logging.ErrorLogger.Printf("Failed to clear published replay state for Camera %d: %v", cameraNumber, err)
	}

	baseFileName := strings.TrimSuffix(filepath.Base(currentFileName), filepath.Ext(currentFileName))
	baseFileName = baseFileName[:len(baseFileName)-len(fmt.Sprintf("_%d", state.LastStartTime))]
	finalFileName := filepath.Join(fullSessionDir, fmt.Sprintf("%s_%s.mp4", timestamp, baseFileName))
	finalFileNames[i] = finalFileName

	attemptInfo := fmt.Sprintf("%s - %s attempt %d",
		attemptDetails.AthleteName,
		attemptDetails.LiftType,
		attemptDetails.AttemptNumber)

	logging.InfoLogger.Printf("Trimming video for Camera %d: %s", cameraNumber, attemptInfo)

	var err error
	if startTime == 0 {
		logging.InfoLogger.Printf("Start time is 0, not trimming the video for Camera %d", cameraNumber)
		if config.NoVideo {
			logging.InfoLogger.Printf("Simulating rename video for Camera %d: %s -> %s", cameraNumber, currentFileName, finalFileName)
		} else if err = os.Rename(currentFileName, finalFileName); err != nil {
			logging.ErrorLogger.Printf("Failed to rename video file for Camera %d to %s: %v", cameraNumber, finalFileName, err)
			return
		}
		if !config.NoVideo {
			if err := httpServer.PublishReplayState(cameraNumber, sessionDir, filepath.Base(finalFileName), 0); err != nil {
				logging.ErrorLogger.Printf("Failed to publish replay state for Camera %d: %v", cameraNumber, err)
			}
		}
	} else {
		for j := 0; j < 5; j++ {
			args := buildTrimmingArgs(keepFromEndMs, currentFileName, finalFileName, config.GetCameraConfigs()[i])
			cmd := CreateFfmpegCmd(args, "trimming")

			if j == 0 {
				logging.InfoLogger.Printf("Executing trim command for Camera %d: %s", cameraNumber, cmd.String())
			}

			if err = cmd.Run(); err != nil {
				logging.ErrorLogger.Printf("Waiting for input video for Camera %d (attempt %d/5): %v", cameraNumber, j+1, err)
				time.Sleep(1 * time.Second)
			} else {
				break
			}
			if j == 4 {
				logging.ErrorLogger.Printf("Failed to open input video for Camera %d after 5 attempts: %v", cameraNumber, err)
				httpServer.SendStatus(httpServer.Ready, fmt.Sprintf("Error: Failed to trim video for Camera %d after 5 attempts", cameraNumber))
				return
			}
		}
		// Probe the actual on-disk duration of the trimmed file. ffmpeg's
		// -sseof snaps to the previous keyframe, so the resulting clip is
		// usually shorter than keepFromEndMs. Publishing the requested
		// keepFromEndMs caused downstream players (OBS scenes in tracker)
		// to wait for non-existent frames and show a black tail.
		probedDurationMs := probeVideoDurationMs(finalFileName)
		publishedDurationMs := probedDurationMs
		if publishedDurationMs <= 0 {
			logging.WarningLogger.Printf("Falling back to requested duration %dms for Camera %d (%s); ffprobe did not return a usable value", keepFromEndMs, cameraNumber, finalFileName)
			publishedDurationMs = keepFromEndMs
		} else if keepFromEndMs > 0 && probedDurationMs != keepFromEndMs {
			logging.InfoLogger.Printf("Camera %d: probed duration %dms differs from requested %dms (delta=%dms) for %s", cameraNumber, probedDurationMs, keepFromEndMs, probedDurationMs-keepFromEndMs, finalFileName)
		}
		if err = httpServer.PublishReplayState(cameraNumber, sessionDir, filepath.Base(finalFileName), publishedDurationMs); err != nil {
			logging.ErrorLogger.Printf("Failed to publish replay state for Camera %d: %v", cameraNumber, err)
			return
		}
		if err = os.Remove(currentFileName); err != nil {
			logging.ErrorLogger.Printf("Failed to remove untrimmed video file for Camera %d: %v", cameraNumber, err)
			return
		}
	}
}

// StopRecordingAndTrim stops the current recordings and trims the videos
func StopRecordingAndTrim(decisionTime int64) error {
	shouldReturn, err := StopRecording()
	if shouldReturn {
		return err
	}

	attemptDetails := currentAttempt
	if attemptDetails.AthleteName == "" {
		attemptDetails.AthleteName = strings.ReplaceAll(state.CurrentAthlete, "_", " ")
	}
	if attemptDetails.LiftType == "" {
		attemptDetails.LiftType = state.CurrentLiftType
	}
	if attemptDetails.AttemptNumber == 0 {
		attemptDetails.AttemptNumber = state.CurrentAttempt
	}
	if attemptDetails.Session == "" {
		attemptDetails.Session = state.CurrentSession
	}

	// leadInMs is how much footage to keep BEFORE the moment the timer was stopped.
	const leadInMs int64 = 5000

	startTime := state.LastStartTime
	// Keep from EOF: everything after the timer stop (decision + a couple of
	// seconds of post-decision tail captured between stop and ffmpeg shutdown)
	// plus leadInMs of footage before the stop.
	nowMs := time.Now().UnixNano() / int64(time.Millisecond)
	keepFromEndMs := (nowMs - state.LastTimerStopTime) + leadInMs
	if state.LastTimerStopTime == 0 {
		// No stop message was received — fall back to keeping the whole file.
		keepFromEndMs = 0
	}
	logging.InfoLogger.Printf("Trim: keeping last %d ms (lead-in %d ms before timer stop)", keepFromEndMs, leadInMs)

	timestamp := time.Now().Format("2006-01-02_15h04m05s")
	finalFileNames := make([]string, len(currentFileNames))

	// Create session directory if it doesn't exist
	sessionDir := attemptDetails.Session
	if sessionDir == "" {
		sessionDir = "unsorted"
	}
	sessionDir = strings.ReplaceAll(sessionDir, " ", "_")
	fullSessionDir := filepath.Join(config.GetVideoDir(), sessionDir)
	if err := os.MkdirAll(fullSessionDir, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}
	attemptDetails.Session = sessionDir

	// Update status to "Trimming videos for XXX attempt YYY"
	statusMessage := fmt.Sprintf("Trimming videos for %s -- %s attempt %d", attemptDetails.AthleteName, attemptDetails.LiftType, attemptDetails.AttemptNumber)
	httpServer.SendStatusWithDetails(httpServer.Trimming, statusMessage, attemptDetails)

	var wg sync.WaitGroup
	for i, currentFileName := range currentFileNames {
		wg.Add(1)
		go trimVideo(&wg, i, currentFileName, keepFromEndMs, startTime, sessionDir, fullSessionDir, timestamp, finalFileNames, attemptDetails)
	}

	wg.Wait()

	// Send single "Videos ready" message after all cameras are done
	httpServer.SendStatusWithDetails(httpServer.Ready, "Videos ready", attemptDetails)

	logging.InfoLogger.Printf("Stopped recording and saved videos: %v", finalFileNames)
	currentRecordings = nil
	currentStdin = nil
	currentFileNames = nil

	return nil
}

func StopRecording() (bool, error) {
	Recording = false
	if len(currentRecordings) == 0 && !config.NoVideo {
		return true, fmt.Errorf("no ongoing recordings to stop")
	}

	if config.NoVideo {
		for i, fileName := range currentFileNames {
			logging.InfoLogger.Printf("Simulating stop recording video for Camera %d: %s", i+1, fileName)
		}
	} else {
		logging.InfoLogger.Println("Attempting to stop ffmpeg gracefully...")
		for i, cmd := range currentRecordings {
			if err := RequestFFmpegStop(cmd, currentStdin[i]); err != nil {
				logging.InfoLogger.Printf("Could not gracefully stop ffmpeg for Camera %d (this is normal if process exited): %v", i+1, err)
			}
		}
		time.Sleep(100 * time.Millisecond)
		for i, stdin := range currentStdin {
			if err := CloseFFmpegStdin(stdin); err != nil {
				logging.InfoLogger.Printf("Could not close stdin for Camera %d (this is normal if process exited): %v", i+1, err)
			}
		}
		var wg sync.WaitGroup
		for i, cmd := range currentRecordings {
			wg.Add(1)
			go func(i int, cmd *exec.Cmd) {
				defer wg.Done()
				// Give ffmpeg a brief window to react to the graceful stop
				// (q on stdin / EOF / SIGINT). If it does not exit — typical
				// when ffmpeg is blocked in an input read with no packets
				// arriving on the UDP source — escalate to a hard kill so
				// the recorder always tears down promptly.
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()

				var err error
				select {
				case err = <-done:
				case <-time.After(2 * time.Second):
					logging.InfoLogger.Printf("ffmpeg did not stop gracefully for Camera %d; forcing kill", i+1)
					if killErr := forceKillCmd(cmd); killErr != nil {
						logging.ErrorLogger.Printf("Failed to force-kill ffmpeg for Camera %d: %v", i+1, killErr)
					}
					err = <-done
				}

				if err != nil {
					if isExpectedFFmpegStop(err) {
						logging.InfoLogger.Printf("ffmpeg stopped gracefully for Camera %d (signal exit): %v", i+1, err)
					} else {
						logging.InfoLogger.Printf("ffmpeg exited with error for Camera %d: %v", i+1, err)
					}
				} else {
					logging.InfoLogger.Printf("ffmpeg stopped gracefully for Camera %d", i+1)
				}
			}(i, cmd)
		}
		wg.Wait()
	}
	return false, nil
}

func isExpectedFFmpegStop(err error) bool {
	if err == nil {
		return true
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}

	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		if status.Signaled() {
			sig := status.Signal()
			return sig == syscall.SIGINT || sig == syscall.SIGTERM
		}
		if status.Exited() {
			code := status.ExitStatus()
			return code == 128 || code == 130 || code == 254 || code == 255
		}
	}

	return false
}

func TerminateRecordings() {
	if config.NoVideo {
		for i, fileName := range currentFileNames {
			logging.InfoLogger.Printf("Simulating forced stop recording video for Camera %d: %s", i+1, fileName)
		}
	} else {
		logging.InfoLogger.Println("Forcing stop ffmpeg if required...")
		for i, cmd := range currentRecordings {
			logging.InfoLogger.Printf("Attempting to stop ffmpeg %d gracefully...", i+1)
			if err := RequestFFmpegStop(cmd, currentStdin[i]); err != nil {
				logging.InfoLogger.Printf("Could not gracefully stop ffmpeg for Camera %d (this is normal if process exited): %v", i+1, err)
			}
		}

		time.Sleep(100 * time.Millisecond)

		var wg sync.WaitGroup
		for i, cmd := range currentRecordings {
			wg.Add(1)
			go func(i int, cmd *exec.Cmd) {
				defer wg.Done()
				if err := forceKillCmd(cmd); err != nil {
					logging.InfoLogger.Printf("ffmpeg exited for Camera %d: %v", i+1, err)
				} else {
					logging.InfoLogger.Printf("ffmpeg stopped gracefully for Camera %d", i+1)
				}
			}(i, cmd)
		}

		wg.Wait()
	}
}

// GetStartTimeMillis returns the start time in milliseconds
func GetStartTimeMillis() string {
	return strconv.FormatInt(state.LastStartTime, 10)
}
