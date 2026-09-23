//go:build linux

package ca44runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type childResult struct {
	stdout   *BoundedCapture
	stderr   *BoundedCapture
	exitCode int
	signal   int
	timedOut bool
}

func runProtectedChild(
	parent context.Context,
	executableChildFD int,
	stdin *os.File,
	extraFiles []*os.File,
	args []string,
	timeout time.Duration,
	waitDelay time.Duration,
	stdoutLimit int,
) (childResult, error) {
	result := childResult{
		stdout:   NewBoundedCapture(stdoutLimit),
		stderr:   NewBoundedCapture(MaxStderrBytes),
		exitCode: -1,
	}
	if parent == nil || executableChildFD < 3 || timeout <= 0 || waitDelay <= 0 || stdoutLimit <= 0 {
		return result, errors.New("invalid child supervision configuration")
	}
	childContext, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	executablePath := fmt.Sprintf("/proc/self/fd/%d", executableChildFD)
	cmd := exec.CommandContext(childContext, executablePath, args...)
	cmd.Stdin = stdin
	cmd.Stdout = result.stdout
	cmd.Stderr = result.stderr
	cmd.Env = CleanEnvironment()
	cmd.ExtraFiles = append([]*os.File(nil), extraFiles...)
	pidFD := -1
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, Pdeathsig: syscall.SIGKILL, PidFD: &pidFD,
	}
	cmd.WaitDelay = waitDelay
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		if errors.Is(err, unix.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if err := cmd.Start(); err != nil {
		return result, err
	}
	if pidFD < 0 {
		_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		_ = cmd.Wait()
		return result, errors.New("child pidfd unavailable")
	}
	defer unix.Close(pidFD)
	if err := waitPidFD(pidFD); err != nil {
		_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		_ = cmd.Wait()
		return result, err
	}
	// The direct child is now exited but not yet reaped, so its PID/PGID cannot
	// be reused. Kill the entire task group before Wait reaps the leader. This
	// closes the normal-exit descendant gap as well as the timeout path.
	if err := unix.Kill(-cmd.Process.Pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		_ = cmd.Wait()
		return result, err
	}
	waitErr := cmd.Wait()
	result.timedOut = errors.Is(childContext.Err(), context.DeadlineExceeded)
	if cmd.ProcessState != nil {
		result.exitCode = cmd.ProcessState.ExitCode()
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			result.signal = int(status.Signal())
		}
	}
	if err := reapProcessGroup(cmd.Process.Pid, waitDelay); err != nil {
		return result, err
	}
	if waitErr != nil {
		var exitError *exec.ExitError
		if !errors.As(waitErr, &exitError) {
			// exec.ErrWaitDelay and pipe-copy failures are never success, even
			// when the direct child happened to return exit code zero.
			return result, waitErr
		}
	}
	return result, nil
}

func waitPidFD(pidFD int) error {
	descriptors := []unix.PollFd{{Fd: int32(pidFD), Events: unix.POLLIN}}
	for {
		count, err := unix.Poll(descriptors, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if count == 1 && descriptors[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
			return nil
		}
		return errors.New("child pidfd poll returned no terminal event")
	}
}

func reapProcessGroup(pgid int, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		for {
			var status unix.WaitStatus
			pid, err := unix.Wait4(-pgid, &status, unix.WNOHANG, nil)
			if pid > 0 {
				continue
			}
			if err != nil && !errors.Is(err, unix.ECHILD) {
				return err
			}
			break
		}
		err := unix.Kill(-pgid, 0)
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		if err != nil && !errors.Is(err, unix.EPERM) {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("child process group did not terminate")
		}
		if err := unix.Kill(-pgid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
