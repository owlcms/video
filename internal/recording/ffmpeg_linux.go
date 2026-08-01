//go:build linux

package recording

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/owlcms/video/internal/config"
	"github.com/owlcms/video/internal/logging"
)

var ffmpegLogSeq uint64
var activeFFmpegLibDir string

// InitializeFFmpeg finds and stores the ffmpeg path in config for Linux
func InitializeFFmpeg() error {
	var path string
	if envPath := strings.TrimSpace(os.Getenv("VIDEO_FFMPEG_PATH")); envPath != "" {
		logging.InfoLogger.Printf("Using ffmpeg from VIDEO_FFMPEG_PATH: %s", envPath)
		path = envPath
	} else if sharedPath := config.FindSharedFFmpegExecutable("ffmpeg"); sharedPath != "" {
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
		logging.ErrorLogger.Printf("Please install FFmpeg using your package manager")
		config.SetFFmpegPath(path)
		return fmt.Errorf("ffmpeg not found at expected location: %s", path)
	}

	config.SetFFmpegPath(path)
	logging.InfoLogger.Printf("FFmpeg executable set to: %s", path)
	applyLinuxFFmpegLibraryPath(path)
	return nil
}

func applyLinuxFFmpegLibraryPath(path string) {
	binDir := filepath.Dir(path)
	libDir := filepath.Join(filepath.Dir(binDir), "lib")
	existingEntries := strings.FieldsFunc(os.Getenv("LD_LIBRARY_PATH"), func(r rune) bool {
		return r == ':'
	})

	if activeFFmpegLibDir != "" && activeFFmpegLibDir != libDir {
		existingEntries = removePathEntry(existingEntries, activeFFmpegLibDir)
		activeFFmpegLibDir = ""
	}

	if st, err := os.Stat(libDir); err == nil && st.IsDir() {
		if !containsPathEntry(existingEntries, libDir) {
			existingEntries = append([]string{libDir}, existingEntries...)
		}
		activeFFmpegLibDir = libDir
	} else if activeFFmpegLibDir != "" {
		existingEntries = removePathEntry(existingEntries, activeFFmpegLibDir)
		activeFFmpegLibDir = ""
	}

	newValue := strings.Join(existingEntries, ":")
	_ = os.Setenv("LD_LIBRARY_PATH", newValue)
	if newValue != "" {
		logging.InfoLogger.Printf("LD_LIBRARY_PATH set to: %s", newValue)
	} else {
		logging.InfoLogger.Printf("LD_LIBRARY_PATH cleared for ffmpeg runtime")
	}
}

func removePathEntry(entries []string, target string) []string {
	if target == "" {
		return entries
	}

	filtered := entries[:0]
	for _, entry := range entries {
		if entry == "" || entry == target {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func containsPathEntry(entries []string, target string) bool {
	for _, entry := range entries {
		if entry == target {
			return true
		}
	}
	return false
}

// on Linux, we use the system-installed ffmpeg
func findFFmpeg() string {
	// Try common locations for ffmpeg
	commonPaths := []string{
		"/usr/bin/ffmpeg",
		"/usr/local/bin/ffmpeg",
		"/bin/ffmpeg",
	}

	for _, path := range commonPaths {
		if _, err := os.Stat(path); err == nil {
			logging.InfoLogger.Printf("Found ffmpeg at: %s", path)
			return path
		}
	}

	// If not found in common locations, try PATH
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		logging.InfoLogger.Printf("Found ffmpeg in PATH at: %s", path)
		return path
	}

	// Return default path if not found
	logging.ErrorLogger.Printf("Could not find ffmpeg in common locations or PATH")
	return "/usr/bin/ffmpeg"
}

// CreateFfmpegCmd creates an exec.Cmd for ffmpeg on Linux
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
	// Set up process group for proper cleanup on Linux
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
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

	// Kill the entire process group on Linux (equivalent to Windows /T flag)
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		// Kill the process group
		if killErr := syscall.Kill(-pgid, syscall.SIGKILL); killErr != nil && killErr != syscall.ESRCH {
			return killErr
		}
	}

	// Also kill the main process as fallback
	if err := cmd.Process.Kill(); err != nil && !strings.Contains(strings.ToLower(err.Error()), "process already finished") {
		return err
	}
	return nil
}

// CreateHiddenCmd creates a command. On Linux, no special handling is needed
// as there's no console window to hide.
func CreateHiddenCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	return cmd
}
