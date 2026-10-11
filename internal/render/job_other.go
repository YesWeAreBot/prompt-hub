//go:build !windows

package render

import (
	"os"
	"syscall"
)

type job struct {
	pid int
}

func newJob(l Limits) (*job, error) { return &job{}, nil }

func (j *job) assign(p *os.Process) error {
	j.pid = p.Pid
	return nil
}

func (j *job) terminate() {
	if j == nil || j.pid == 0 {
		return
	}

	_ = syscall.Kill(-j.pid, syscall.SIGKILL)
}

func (j *job) close() {}

func procAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
