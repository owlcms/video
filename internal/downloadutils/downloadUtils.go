package downloadutils

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/owlcms/video/internal/logging"
	"github.com/owlcms/video/internal/recording"
)

// ProgressCallback is a function type that receives download progress updates
type ProgressCallback func(downloaded, total int64)

// DownloadArchive downloads a file and reports progress through the callback. It also accepts a cancel channel.
func DownloadArchive(url, destPath string, progress ProgressCallback, cancel <-chan bool) error {
	logging.InfoLogger.Printf("Attempting to download from URL: %s\n", url)

	client := &http.Client{}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Call progress callback immediately to update UI before network request
	if progress != nil {
		progress(0, 100) // Use placeholder total size of 100
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned non-200 status: %s for %s", resp.Status, url)
	}

	// Update progress with actual total size now that we have the response
	if progress != nil && resp.ContentLength > 0 {
		progress(0, resp.ContentLength)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file %s: %w", destPath, err)
	}
	defer out.Close()

	// Create a proxy reader that will report progress
	counter := &WriteCounter{
		Total:    resp.ContentLength,
		Progress: progress,
		Cancel:   cancel, // Pass the cancel channel to the counter
	}

	_, err = io.Copy(out, io.TeeReader(resp.Body, counter))
	if err != nil {
		return fmt.Errorf("failed to copy data: %w", err)
	}

	logging.InfoLogger.Printf("Successfully downloaded file to: %s\n", destPath)
	return nil
}

// WriteCounter counts bytes written and reports progress
type WriteCounter struct {
	Downloaded int64
	Total      int64
	Progress   ProgressCallback
	Cancel     <-chan bool // Add a cancel channel
	lastReport time.Time
}

func (wc *WriteCounter) Write(p []byte) (int, error) {
	select {
	case <-wc.Cancel:
		return 0, fmt.Errorf("download cancelled") // Check for cancellation
	default:
		n := len(p)
		wc.Downloaded += int64(n)

		// Report progress at most every 100ms to avoid overwhelming the UI
		if time.Since(wc.lastReport) > 100*time.Millisecond {
			if wc.Progress != nil {
				wc.Progress(wc.Downloaded, wc.Total)
			}
			wc.lastReport = time.Now()
		}

		return n, nil
	}
}

// IsWSL checks if the program is running under Windows Subsystem for Linux.
func IsWSL() bool {
	_, err := os.Stat("/proc/version")
	if err != nil {
		return false
	}
	version, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	return strings.Contains(string(version), "Microsoft")
}

// GetDownloadURL returns the correct download URL based on the operating system.
func GetDownloadURL() string {
	return "https://github.com/GyanD/codexffmpeg/releases/download/7.1/" + recording.FfmpegBuild + ".zip"
}

func getLinuxSharedDownloadURL(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linux64-gpl-shared.tar.xz", nil
	case "arm64":
		return "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linuxarm64-gpl-shared.tar.xz", nil
	default:
		return "", fmt.Errorf("unsupported linux architecture for bundled ffmpeg: %s", goarch)
	}
}

func GetGoos() string {
	return runtime.GOOS
}

// ExtractZip extracts a zip archive to the specified destination directory.
func ExtractZip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("failed to open zip file %s: %w", src, err)
	}

	for _, f := range r.File {
		if f.Name == "Procfile" || f.Name == "system.properties" {
			continue
		}

		fpath := filepath.Join(dest, f.Name)

		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return fmt.Errorf("failed to open file for writing: %w", err)
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("failed to open file inside zip: %w", err)
		}

		_, err = io.Copy(outFile, rc)

		outFile.Close()
		rc.Close()

		if err != nil {
			return fmt.Errorf("failed to copy file data from zip: %w", err)
		}

		// Restore file modification and creation times
		if err := os.Chtimes(fpath, f.Modified, f.Modified); err != nil {
			return fmt.Errorf("failed to change file times: %w", err)
		}
	}

	r.Close() // Close the zip file after extracting

	// Remove the downloaded ZIP file
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("failed to remove downloaded file %s: %w", src, err)
	}

	return nil
}

// ExtractTarGz extracts a tar.gz archive to the specified destination directory.
func ExtractTarGz(tarGzPath, dest string) error {
	r, err := os.Open(tarGzPath)
	if err != nil {
		return err
	}

	gzr, err := gzip.NewReader(r)
	if err != nil {
		r.Close()
		return err
	}

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			gzr.Close()
			r.Close()
			return err
		}

		if header.Name == "Procfile" || header.Name == "system.properties" {
			continue
		}

		target := filepath.Join(dest, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				gzr.Close()
				r.Close()
				return err
			}
			if err := os.Chtimes(target, header.AccessTime, header.ModTime); err != nil {
				gzr.Close()
				r.Close()
				return err
			}
		case tar.TypeReg:
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				gzr.Close()
				r.Close()
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				gzr.Close()
				r.Close()
				return err
			}
			if err := os.Chtimes(target, header.AccessTime, header.ModTime); err != nil {
				log.Printf("failed to change file times: %v  %s %s", err, header.AccessTime, header.ModTime)
				gzr.Close()
				r.Close()
				return err
			}
			outFile.Close()
		}
	}

	gzr.Close()
	r.Close() // Close the archive after extracting

	// Remove the downloaded tar.gz file
	if err := os.Remove(tarGzPath); err != nil {
		return fmt.Errorf("failed to remove downloaded file %s: %w", tarGzPath, err)
	}

	return nil
}

// ExtractTarXz extracts a tar.xz archive using the system tar command.
func ExtractTarXz(tarXzPath, dest string) error {
	cmd := exec.Command("tar", "-xJf", tarXzPath, "-C", dest)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to extract tar.xz %s: %w (%s)", tarXzPath, err, strings.TrimSpace(string(output)))
	}

	if err := os.Remove(tarXzPath); err != nil {
		return fmt.Errorf("failed to remove downloaded file %s: %w", tarXzPath, err)
	}

	return nil
}

func appendPathEnv(existing, value string) string {
	if strings.TrimSpace(value) == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return value
	}
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	for _, part := range strings.Split(existing, sep) {
		if filepath.Clean(strings.TrimSpace(part)) == filepath.Clean(value) {
			return existing
		}
	}
	return value + sep + existing
}

func exportRuntimePaths(ffmpegPath string) {
	if strings.TrimSpace(ffmpegPath) == "" {
		return
	}

	binDir := filepath.Dir(ffmpegPath)
	name := "ffprobe"
	play := "ffplay"
	if runtime.GOOS == "windows" {
		name = "ffprobe.exe"
		play = "ffplay.exe"
	}

	probePath := filepath.Join(binDir, name)
	playPath := filepath.Join(binDir, play)

	_ = os.Setenv("VIDEO_FFMPEG_PATH", ffmpegPath)
	if _, err := os.Stat(probePath); err == nil {
		_ = os.Setenv("VIDEO_FFPROBE_PATH", probePath)
	}
	if _, err := os.Stat(playPath); err == nil {
		_ = os.Setenv("VIDEO_FFPLAY_PATH", playPath)
	}

	if runtime.GOOS == "linux" {
		rootDir := filepath.Dir(binDir)
		libDir := filepath.Join(rootDir, "lib")
		if st, err := os.Stat(libDir); err == nil && st.IsDir() {
			_ = os.Setenv("LD_LIBRARY_PATH", appendPathEnv(os.Getenv("LD_LIBRARY_PATH"), libDir))
		}
	}
}

func findBundledFFmpegPath(installDir string) string {
	if runtime.GOOS == "windows" {
		candidate := filepath.Join(installDir, recording.FfmpegBuild, "bin", "ffmpeg.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		return ""
	}

	linuxCandidates := []string{}
	entries, err := os.ReadDir(installDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				linuxCandidates = append(linuxCandidates, filepath.Join(installDir, entry.Name(), "bin", "ffmpeg"))
			}
		}
	}
	linuxCandidates = append(linuxCandidates, filepath.Join(installDir, "bin", "ffmpeg"))

	for _, ffmpegPath := range linuxCandidates {
		if _, err := os.Stat(ffmpegPath); err == nil {
			binDir := filepath.Dir(ffmpegPath)
			ffplayPath := filepath.Join(binDir, "ffplay")
			if _, err := os.Stat(ffplayPath); err != nil {
				continue
			}
			rootDir := filepath.Dir(binDir)
			if st, err := os.Stat(filepath.Join(rootDir, "lib")); err != nil || !st.IsDir() {
				continue
			}
			return ffmpegPath
		}
	}

	return ""
}

// EnsureFFmpegRuntime ensures FFmpeg runtime is available under installDir.
// It returns the resolved ffmpeg path and exports VIDEO_FFMPEG_PATH / VIDEO_FFPROBE_PATH / VIDEO_FFPLAY_PATH.
func EnsureFFmpegRuntime(installDir string, progress ProgressCallback, cancel <-chan bool) (string, error) {
	controlPanelDir := strings.TrimSpace(os.Getenv("VIDEO_CONTROLPANEL_DIR"))
	if controlPanelDir == "" {
		logging.InfoLogger.Printf("FFmpeg runtime mode: local (VIDEO_CONTROLPANEL_DIR not set), using %s", installDir)
	} else {
		logging.InfoLogger.Printf("FFmpeg runtime mode: control-panel (%s), using %s", controlPanelDir, installDir)
	}

	if err := os.MkdirAll(installDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create install dir %s: %w", installDir, err)
	}

	if existing := findBundledFFmpegPath(installDir); existing != "" {
		exportRuntimePaths(existing)
		return existing, nil
	}

	var downloadURL string
	var archivePath string
	if runtime.GOOS == "windows" {
		downloadURL = GetDownloadURL()
		archivePath = filepath.Join(installDir, "ffmpeg.zip")
	} else if runtime.GOOS == "linux" {
		linuxURL, urlErr := getLinuxSharedDownloadURL(runtime.GOARCH)
		if urlErr != nil {
			return "", nil
		}
		downloadURL = linuxURL
		archivePath = filepath.Join(installDir, "ffmpeg-master-latest-linux"+runtime.GOARCH+"-gpl-shared.tar.xz")
	} else {
		return "", nil
	}

	if err := DownloadArchive(downloadURL, archivePath, progress, cancel); err != nil {
		return "", err
	}

	if runtime.GOOS == "windows" {
		if err := ExtractZip(archivePath, installDir); err != nil {
			return "", err
		}
	} else {
		if err := ExtractTarXz(archivePath, installDir); err != nil {
			return "", err
		}
	}

	resolved := findBundledFFmpegPath(installDir)
	if resolved == "" {
		return "", fmt.Errorf("ffmpeg executable not found after extraction in %s", installDir)
	}

	exportRuntimePaths(resolved)
	return resolved, nil
}
