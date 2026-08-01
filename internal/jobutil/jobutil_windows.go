//go:build windows

package jobutil

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

var jobObject windows.Handle

// Init creates a Windows Job Object with KILL_ON_JOB_CLOSE.
// When the parent process exits (for any reason), Windows will automatically
// kill all child processes assigned to this job.
func Init() error {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("CreateJobObject: %w", err)
	}

	// Set the job to kill all processes when the handle is closed
	// (which happens automatically when the parent process exits).
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, err = windows.SetInformationJobObject(
		handle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		windows.CloseHandle(handle)
		return fmt.Errorf("SetInformationJobObject: %w", err)
	}

	jobObject = handle
	return nil
}

// Assign adds a running process to the job object so it will
// be killed when the parent process exits.
func Assign(cmd *exec.Cmd) error {
	if jobObject == 0 || cmd == nil || cmd.Process == nil {
		return nil
	}

	h, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return fmt.Errorf("OpenProcess(%d): %w", cmd.Process.Pid, err)
	}
	defer windows.CloseHandle(h)

	if err := windows.AssignProcessToJobObject(jobObject, h); err != nil {
		return fmt.Errorf("AssignProcessToJobObject(%d): %w", cmd.Process.Pid, err)
	}
	return nil
}
