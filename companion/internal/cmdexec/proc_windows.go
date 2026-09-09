//go:build windows

package cmdexec

import (
	"fmt"
	osexec "os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procGroup wraps the child in a Job Object. Windows has no process group to
// signal, and killing the direct child leaves whatever it spawned running.
// A job with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE also covers the case this
// package cannot handle itself: if the Companion dies, the handle closes and
// the tree goes with it instead of outliving the process that started it.
type procGroup struct {
	job windows.Handle
}

func newProcGroup() *procGroup { return &procGroup{} }

func (g *procGroup) prepare(cmd *osexec.Cmd) {
	// A new process group additionally keeps Ctrl-C in the console that
	// started the Companion from reaching the child.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// adopt assigns the started process to a fresh job. os/exec gives no hook
// between CreateProcess and the child's first instruction, so this runs just
// after Start; anything the child spawns in that window escapes the job. The
// window is microseconds and the alternative — CREATE_SUSPENDED plus a manual
// ResumeThread — needs a thread handle os/exec does not expose.
func (g *procGroup) adopt(cmd *osexec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("creating job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("configuring job object: %w", err)
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("opening child process: %w", err)
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("assigning child to job object: %w", err)
	}
	g.job = job
	return nil
}

// kill terminates every process in the job at once. There is no SIGTERM
// equivalent that a non-console child would reliably act on, so unlike the
// Unix path this does not offer a grace period.
func (g *procGroup) kill(cmd *osexec.Cmd) error {
	if g.job != 0 {
		return windows.TerminateJobObject(g.job, 1)
	}
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func (g *procGroup) close() {
	if g.job != 0 {
		windows.CloseHandle(g.job)
		g.job = 0
	}
}
