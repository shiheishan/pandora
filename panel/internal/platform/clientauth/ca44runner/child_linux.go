//go:build linux

// [INPUT]: 依赖 capture.go 的 BoundedCapture、x/sys/unix 的 close_range/fcntl/pidfd/wait4、/proc/self/fd
// [OUTPUT]: 包内提供 runProtectedChild（受保护子进程的启动、监督、整组清理）与 sealInheritedDescriptors
// [POS]: ca44runner 子进程监督核心，被 runner_linux.go 的 classifier/verifier 两段调用；子进程描述符面恰为声明集的保证在此兑现
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package ca44runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
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
	// 子进程只应看到 0-2 与 ExtraFiles 声明的描述符。Go 自己打开的都带
	// CLOEXEC，但本进程从启动方继承来的描述符不带（2026-09-24 GitHub runner
	// 实测留下两根管道），不封住就会一路漏进子进程。封不住则不启动。
	if err := sealInheritedDescriptors(); err != nil {
		return result, fmt.Errorf("seal inherited descriptors: %w", err)
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

// linuxCloseRangeCloexec 取自 Linux UAPI <linux/close_range.h>（5.11 起），x/sys 未导出。
const linuxCloseRangeCloexec = 1 << 2

// sealInheritedDescriptors 把本进程 3 号及以上的描述符统一设为 CLOEXEC。
//
// 只设标志、不关闭：本进程自己仍可使用它们，交给子进程的那几个由 os/exec
// 在 fork 后 dup2 到声明位置，新描述符不带 CLOEXEC，不受影响。
func sealInheritedDescriptors() error {
	if err := unix.CloseRange(3, ^uint(0), linuxCloseRangeCloexec); err == nil {
		return nil
	}
	// 内核 < 5.9 返回 ENOSYS，5.9/5.10 不认 CLOEXEC 标志返回 EINVAL，seccomp
	// 可能返回 EPERM。无论哪种都退到逐个遍历：退路要么把每一个都设上，要么报错，
	// 不会比 close_range 更宽松。
	return sealInheritedDescriptorsByProcScan()
}

func sealInheritedDescriptorsByProcScan() error {
	dir, err := os.Open("/proc/self/fd")
	if err != nil {
		return err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		fd, err := strconv.Atoi(name)
		if err != nil {
			return fmt.Errorf("unexpected /proc/self/fd entry %q", name)
		}
		if fd < 3 {
			continue
		}
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if errors.Is(err, unix.EBADF) {
			// 读目录与检查之间已被关闭（含遍历用的目录描述符自身），不再构成可继承的权限。
			continue
		}
		if err != nil {
			return fmt.Errorf("descriptor %d: %w", fd, err)
		}
		if flags&unix.FD_CLOEXEC != 0 {
			continue
		}
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC); err != nil {
			return fmt.Errorf("descriptor %d: %w", fd, err)
		}
	}
	return nil
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
