//go:build !windows

package jobutil

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"syscall"
)

func findUDPPortOwners(port int) ([]PortProcess, error) {
	if _, err := exec.LookPath("ss"); err == nil {
		out, cmdErr := exec.Command("ss", "-H", "-lunp").CombinedOutput()
		if cmdErr == nil {
			return parseSSUDPPortOwners(out, port), nil
		}
	}
	if _, err := exec.LookPath("lsof"); err == nil {
		out, cmdErr := exec.Command("lsof", "-nP", "-iUDP:"+strconv.Itoa(port)).CombinedOutput()
		if cmdErr == nil {
			return parseLsofUDPPortOwners(out, port), nil
		}
	}
	if canBindUDPPort(port) {
		return nil, nil
	}
	return nil, fmt.Errorf("unable to inspect UDP port %d; neither ss nor lsof is available", port)
}

func interruptProcess(pid int) error {
	return signalProcessGroup(pid, syscall.SIGINT)
}

func terminateProcess(pid int) error {
	return signalProcessGroup(pid, syscall.SIGTERM)
}

func killProcess(pid int) error {
	return signalProcessGroup(pid, syscall.SIGKILL)
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 0 {
		if err := syscall.Kill(-pgid, signal); err == nil || err == syscall.ESRCH {
			return nil
		}
	}
	if err := syscall.Kill(pid, signal); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

func processStillRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// KillAllFFmpeg kills any remaining ffmpeg/ffplay processes that may have been
// orphaned or missed by per-stream stopProcess calls.
func KillAllFFmpeg() {
	for _, name := range []string{"ffmpeg", "ffplay"} {
		if pkill, err := exec.LookPath("pkill"); err == nil {
			_ = exec.Command(pkill, "-f", name).Run()
		}
	}
}

func canBindUDPPort(port int) bool {
	conn, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
