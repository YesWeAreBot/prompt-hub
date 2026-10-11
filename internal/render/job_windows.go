//go:build windows

package render

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type job struct {
	h windows.Handle
}

func newJob(l Limits) (*job, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	j := &job{h: h}

	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	flags := uint32(windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE)
	if l.MemoryBytes > 0 {
		info.ProcessMemoryLimit = uintptr(l.MemoryBytes)
		info.JobMemoryLimit = uintptr(l.MemoryBytes)
		flags |= windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY | windows.JOB_OBJECT_LIMIT_JOB_MEMORY
	}
	if l.MaxProcesses > 0 {
		info.BasicLimitInformation.ActiveProcessLimit = uint32(l.MaxProcesses)
		flags |= windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	}
	info.BasicLimitInformation.LimitFlags = flags

	if _, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	return j, nil
}

func (j *job) assign(p *os.Process) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(j.h, h); err != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return nil
}

func (j *job) terminate() {
	if j == nil || j.h == 0 {
		return
	}
	_ = windows.TerminateJobObject(j.h, 1)
}

func (j *job) close() {
	if j == nil || j.h == 0 {
		return
	}
	windows.CloseHandle(j.h)
	j.h = 0
}

func procAttr() *syscall.SysProcAttr { return nil }
