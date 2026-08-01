//go:build windows

package jobutil

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func findUDPPortOwners(port int) ([]PortProcess, error) {
	cmd := exec.Command("netstat", "-ano", "-p", "udp")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("netstat udp owners: %w", err)
	}
	owners := parseWindowsNetstatUDPPortOwners(out, port)
	for i := range owners {
		owners[i].Command = windowsProcessName(owners[i].PID)
	}
	return owners, nil
}

func interruptProcess(pid int) error {
	return runTaskkill("/PID", strconv.Itoa(pid))
}

func terminateProcess(pid int) error {
	return runTaskkill("/T", "/PID", strconv.Itoa(pid))
}

func killProcess(pid int) error {
	// Try the Win32 API first (OpenProcess + TerminateProcess). This is what
	// taskkill /F ultimately wraps, but going through it directly avoids the
	// fragile parent-shell + console / CSRSS round-trips that can leave
	// taskkill reporting "ERROR: The process ... could not be terminated"
	// for orphaned ffmpeg children. We still fall back to taskkill /F /T as
	// a backup so descendants are reaped on the rare occasion the API call
	// fails.
	if err := terminateProcessNative(pid); err == nil {
		// taskkill /T also kills children; mirror that by following up with
		// a tree-kill so any helpers ffmpeg may have spawned are reaped too.
		_ = runTaskkill("/F", "/T", "/PID", strconv.Itoa(pid))
		return nil
	}
	return runTaskkill("/F", "/T", "/PID", strconv.Itoa(pid))
}

// terminateProcessNative opens the process by PID and calls TerminateProcess.
// Returns nil if the process is already gone or was successfully terminated.
func terminateProcessNative(pid int) error {
	if pid <= 0 {
		return nil
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// ERROR_INVALID_PARAMETER (87) is what OpenProcess returns when the
		// PID does not (or no longer) exists — treat as success.
		if errno, ok := err.(syscall.Errno); ok && errno == 87 {
			return nil
		}
		return fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 1); err != nil {
		// ERROR_ACCESS_DENIED (5) on a process that has already terminated
		// surfaces here; verify and treat as success in that case.
		if !processStillRunning(pid) {
			return nil
		}
		return fmt.Errorf("TerminateProcess(%d): %w", pid, err)
	}
	// Wait briefly for the process to actually exit so callers that re-check
	// port ownership immediately don't race.
	_, _ = windows.WaitForSingleObject(h, 500)
	return nil
}

func runTaskkill(args ...string) error {
	cmd := exec.Command("taskkill", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.ToLower(strings.TrimSpace(string(out)))
		if strings.Contains(message, "no running instance") ||
			strings.Contains(message, "not found") ||
			strings.Contains(message, "no tasks are running") {
			return nil
		}
		// The polite (non-/F) taskkill step routinely fails when ffmpeg
		// children refuse to exit on a WM_CLOSE message — Windows reports
		// "could not be terminated ... can only be terminated forcefully
		// (with /F option)" with exit code 128. StopProcessTree follows up
		// with a forced /F /T kill, so swallow this expected outcome here
		// instead of surfacing it to the user.
		hasForceFlag := false
		for _, a := range args {
			if strings.EqualFold(a, "/F") {
				hasForceFlag = true
				break
			}
		}
		if !hasForceFlag && (strings.Contains(message, "terminated forcefully") ||
			strings.Contains(message, "with /f option") ||
			strings.Contains(message, "child processes of this process were still running") ||
			strings.Contains(message, "could not be terminated")) {
			return nil
		}
		if message != "" {
			return fmt.Errorf("taskkill %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
		}
		return fmt.Errorf("taskkill %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// KillAllFFmpeg kills any remaining ffmpeg/ffplay processes that may have been
// orphaned or missed by per-stream stopProcess calls.
func KillAllFFmpeg() {
	for _, name := range []string{"ffmpeg.exe", "ffplay.exe"} {
		cmd := exec.Command("taskkill", "/F", "/IM", name)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
		_ = cmd.Run()
	}
}

func windowsProcessName(pid int) string {
	if pid <= 0 {
		return ""
	}
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	name := parseWindowsTasklistName(out)
	if strings.EqualFold(name, "INFO: No tasks are running which match the specified criteria.") {
		return ""
	}
	return name
}

func processStillRunning(pid int) bool {
	return windowsProcessName(pid) != ""
}
