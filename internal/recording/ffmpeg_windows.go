//go:build windows

package recording

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/logging"
	"golang.org/x/sys/windows"
)

var ffmpegLogSeq uint64

// InitializeFFmpeg finds and stores the ffmpeg path in config
func InitializeFFmpeg() error {
	var path string
	if envPath := strings.TrimSpace(os.Getenv("VIDEO_FFMPEG_PATH")); envPath != "" {
		logging.InfoLogger.Printf("Using ffmpeg from VIDEO_FFMPEG_PATH: %s", envPath)
		path = envPath
	} else if sharedPath := config.FindSharedFFmpegExecutable("ffmpeg.exe"); sharedPath != "" {
		logging.InfoLogger.Printf("Using shared Control Panel ffmpeg: %s", sharedPath)
		path = sharedPath
	} else {
		path = findFFmpeg()
	}

	return applyFFmpegPath(path)
}

func applyFFmpegPath(path string) error {
	if _, err := os.Stat(path); err != nil {
		logging.ErrorLogger.Printf("FFmpeg not found at %s: %v", path, err)
		logging.ErrorLogger.Printf("Please ensure FFmpeg is properly downloaded to the installation directory")
		config.SetFFmpegPath(path)
		return fmt.Errorf("ffmpeg not found at expected location: %s", path)
	}

	config.SetFFmpegPath(path)
	logging.InfoLogger.Printf("FFmpeg executable set to: %s", path)
	return nil
}

// on Windows, we use the locally downloaded ffmpeg
func findFFmpeg() string {
	installDir := config.GetInstallDir()
	ffmpegPath := filepath.Join(installDir, FfmpegBuild, "bin", "ffmpeg.exe")
	logging.InfoLogger.Printf("Trying ffmpeg at installation directory: %s", ffmpegPath)

	if _, err := os.Stat(ffmpegPath); err == nil {
		logging.InfoLogger.Printf("Found ffmpeg at: %s", ffmpegPath)
		return ffmpegPath
	} else {
		logging.ErrorLogger.Printf("Could not find ffmpeg at expected location %s: %v", ffmpegPath, err)

		// Try to check if the directory structure exists
		binDir := filepath.Join(installDir, FfmpegBuild, "bin")
		if entries, err := os.ReadDir(binDir); err == nil {
			logging.InfoLogger.Printf("Contents of %s:", binDir)
			for _, entry := range entries {
				logging.InfoLogger.Printf("  - %s", entry.Name())
			}
		} else {
			logging.ErrorLogger.Printf("Could not read ffmpeg bin directory %s: %v", binDir, err)
		}

		// Return the expected path even if not found - the error will be handled upstream
		// This ensures we never try to use ffmpeg from PATH
		return ffmpegPath
	}
}

// CreateFfmpegCmd creates an exec.Cmd for ffmpeg with Windows-specific process attributes
func CreateFfmpegCmd(args []string, operation string, forcedLogLevel ...string) *exec.Cmd {
	// Use the stored ffmpeg path from config
	path := config.GetFFmpegPath()

	// Handle loglevel based on logging preference or forced level
	var targetLoglevel string
	if len(forcedLogLevel) > 0 && forcedLogLevel[0] != "" {
		targetLoglevel = forcedLogLevel[0]
	} else {
		logFfmpeg := config.GetLogFfmpeg()
		targetLoglevel = "quiet"
		if logFfmpeg {
			targetLoglevel = "info"
		}
	}

	// Check if -loglevel already exists in args and update it, or add it
	foundLoglevel := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-loglevel" {
			args[i+1] = targetLoglevel
			foundLoglevel = true
			break
		}
	}

	// If no loglevel found, add it at the beginning
	if !foundLoglevel {
		args = append([]string{"-loglevel", targetLoglevel}, args...)
	}

	// Log the command being executed for debugging
	logging.InfoLogger.Printf("Creating ffmpeg command with path: %s", path)
	logging.InfoLogger.Printf("FFmpeg args (%d total):", len(args))
	for i, arg := range args {
		logging.InfoLogger.Printf("  [%d]: %s", i, arg)
	}

	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
	}

	// Create logs directory and redirect ffmpeg output to timestamped files only if logFfmpeg is enabled
	if config.GetLogFfmpeg() {
		installDir := config.GetInstallDir()
		logsDir := filepath.Join(installDir, "logs")
		if err := os.MkdirAll(logsDir, 0755); err != nil {
			logging.ErrorLogger.Printf("Failed to create logs directory: %v", err)
		} else {
			timestamp := time.Now().Format("20060102_150405_000000000")
			seq := atomic.AddUint64(&ffmpegLogSeq, 1)
			logFile := filepath.Join(logsDir, fmt.Sprintf("ffmpeg_%s_%s_%d.log", timestamp, operation, seq))

			if file, err := os.Create(logFile); err != nil {
				logging.ErrorLogger.Printf("Failed to create ffmpeg log file %s: %v", logFile, err)
			} else {
				logging.InfoLogger.Printf("FFmpeg output will be logged to: %s", logFile)
				cmd.Stdout = file
				cmd.Stderr = file
			}
		}
	}

	return cmd
}

func forceKillCmd(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	logging.InfoLogger.Printf("Killing ffmpeg process %d", cmd.Process.Pid)
	kill := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := kill.CombinedOutput()
	if err != nil {
		message := strings.ToLower(strings.TrimSpace(string(out)))
		if strings.Contains(message, "no running instance") ||
			strings.Contains(message, "not found") ||
			strings.Contains(message, "no tasks are running") {
			return nil
		}
		if message != "" {
			return fmt.Errorf("taskkill /F /T /PID %d: %s: %w", cmd.Process.Pid, strings.TrimSpace(string(out)), err)
		}
		return fmt.Errorf("taskkill /F /T /PID %d: %w", cmd.Process.Pid, err)
	}
	return nil
}

// CreateHiddenCmd creates a command that runs without a visible console window on Windows.
// Use this for any external commands during auto-detection or background operations.
func CreateHiddenCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	return cmd
}
