//go:build linux

// [INPUT]: 依赖 child_linux.go 的 runProtectedChild/sealInheritedDescriptors、staging_linux.go 的 duplicateCloseOnExec，依赖 x/sys/unix 的 fcntl/fstat/prctl
// [OUTPUT]: 对外提供受保护子进程的描述符面、继承描述符封存、进程组清理、CLOEXEC 复制测试，及 TestCA44ProcessGroupHelper 子进程入口
// [POS]: ca44runner 子进程监督的 Linux 测试面；helper 以测试二进制自身为子进程，其 t.Fatal 输出走 stdout，父测试失败时一并打印
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package ca44runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRunProtectedChildHasExactDeclaredDescriptorSurface(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	fd3, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer fd3.Close()
	fd4, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer fd4.Close()
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	defer self.Close()

	// Keep a representative parent-side staging capability open while exec
	// happens. F_DUPFD_CLOEXEC must prevent it from becoming an undeclared
	// high descriptor in the child.
	parentOnly, err := duplicateCloseOnExec(int(fd4.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parentOnly)

	// 模拟启动方漏给本进程的描述符（GitHub runner 实测留下两根无 CLOEXEC 的
	// 管道）：runProtectedChild 必须在 exec 前把它封住，子进程里不得出现同一对象。
	leaked, leakedIdentity := openLeakedDescriptor(t)
	defer unix.Close(leaked)

	result, err := runProtectedChild(context.Background(), 5, input,
		[]*os.File{fd3, fd4, self},
		[]string{"-test.run=TestCA44ProcessGroupHelper", "--", "fd-inspector", leakedIdentity},
		5*time.Second, time.Second, MaxEnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}
	if result.exitCode != 0 || result.signal != 0 || result.timedOut {
		// helper 里 t.Fatal 的输出走的是子进程 stdout，只打印 stderr 会丢掉失败原因。
		stdout, _, _ := result.stdout.Snapshot()
		stderr, _, _ := result.stderr.Snapshot()
		t.Fatalf("unexpected helper result: exit=%d signal=%d timedOut=%v stdout=%q stderr=%q",
			result.exitCode, result.signal, result.timedOut, stdout, stderr)
	}
	if err := result.stdout.MatchExact([]byte("fd-layout=OK\n")); err != nil {
		stdout, _, _ := result.stdout.Snapshot()
		t.Fatalf("unexpected descriptor receipt: %q (%v)", stdout, err)
	}
}

func TestSealInheritedDescriptorsMarksLeakedDescriptorCloseOnExec(t *testing.T) {
	for _, tc := range []struct {
		name string
		seal func() error
	}{
		// CI 与现役内核都支持 close_range 的 CLOEXEC 标志，退路只能直接调用来覆盖。
		{"close_range", sealInheritedDescriptors},
		{"proc_scan", sealInheritedDescriptorsByProcScan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leaked, _ := openLeakedDescriptor(t)
			defer unix.Close(leaked)
			if err := tc.seal(); err != nil {
				t.Fatal(err)
			}
			flags, err := unix.FcntlInt(uintptr(leaked), unix.F_GETFD, 0)
			if err != nil {
				t.Fatal(err)
			}
			if flags&unix.FD_CLOEXEC == 0 {
				t.Fatalf("leaked descriptor %d is still inheritable", leaked)
			}
		})
	}
}

// openLeakedDescriptor 打开一个不带 CLOEXEC 的描述符，返回它与 "dev:ino" 身份。
// 先断言它确实可继承，免得测试因前提不成立而空转通过。
func openLeakedDescriptor(t *testing.T) (int, string) {
	t.Helper()
	path := t.TempDir() + "/leaked"
	if err := os.WriteFile(path, []byte("parent-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(path, unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC != 0 {
		unix.Close(fd)
		t.Fatalf("leaked descriptor precondition: flags=%#x err=%v", flags, err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		t.Fatal(err)
	}
	return fd, fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
}

func TestRunProtectedChildKillsDescendantBeforeReturn(t *testing.T) {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	marker := t.TempDir() + "/descendant-survived"
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	defer self.Close()
	extra := []*os.File{nil, nil, self}
	result, err := runProtectedChild(context.Background(), 5, nil, extra,
		[]string{"-test.run=TestCA44ProcessGroupHelper", "--", "leader", marker},
		5*time.Second, time.Second, MaxEnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}
	if result.exitCode != 0 || result.signal != 0 || result.timedOut {
		t.Fatalf("unexpected helper result: %+v", result)
	}
	// Only let a surviving descendant create the marker after supervision has
	// returned.  A fixed sleep inside the descendant can expire before the
	// leader exits under -race or a loaded builder, which would record a false
	// survival even though the process group is subsequently killed correctly.
	if err := os.WriteFile(marker+".supervisor-returned", []byte("returned"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil || !os.IsNotExist(err) {
		t.Fatalf("descendant survived task-domain cleanup: %v", err)
	}
}

func TestDuplicateCloseOnExecSetsDescriptorFlag(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "fd")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	duplicate, err := duplicateCloseOnExec(int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(duplicate)
	flags, err := unix.FcntlInt(uintptr(duplicate), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("duplicated descriptor is missing FD_CLOEXEC")
	}
}

func TestCA44ProcessGroupHelper(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator < 0 || len(os.Args) != separator+3 {
		return
	}
	mode, marker := os.Args[separator+1], os.Args[separator+2]
	switch mode {
	case "fd-inspector":
		// marker 是父进程故意漏出的那个对象的 "dev:ino"。
		var leakedDev, leakedIno uint64
		if _, err := fmt.Sscanf(marker, "%d:%d", &leakedDev, &leakedIno); err != nil {
			t.Fatalf("bad leaked identity %q: %v", marker, err)
		}
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		seen := make(map[int]bool)
		for _, entry := range entries {
			fd, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
			if err != nil {
				// os.ReadDir's own directory descriptor can disappear before this
				// check; an already-closed descriptor is not inherited authority.
				if err == unix.EBADF {
					continue
				}
				t.Fatal(err)
			}
			var stat unix.Stat_t
			if err := unix.Fstat(fd, &stat); err == nil &&
				uint64(stat.Dev) == leakedDev && uint64(stat.Ino) == leakedIno {
				t.Errorf("parent-only descriptor visible in child: %d", fd)
			}
			if fd <= 5 {
				seen[fd] = true
				continue
			}
			if flags&unix.FD_CLOEXEC == 0 {
				target, _ := os.Readlink("/proc/self/fd/" + entry.Name())
				t.Errorf("undeclared inherited descriptor: %d -> %s", fd, target)
			}
		}
		if t.Failed() {
			t.FailNow()
		}
		for fd := 0; fd <= 5; fd++ {
			if !seen[fd] {
				t.Fatalf("declared descriptor is closed: %d", fd)
			}
		}
		if _, err := os.Stdout.WriteString("fd-layout=OK\n"); err != nil {
			t.Fatal(err)
		}
		// 直接退出，不让 testing 在后面补一行 PASS。
		//
		// 这个 helper 是被父进程当子进程 exec 起来的，父进程用 MatchExact
		// 校验它的 stdout——那个严格性是有意的，要守的是「子进程不往
		// stdout 写任何计划外的东西」。testing 框架的 PASS 是框架的输出而
		// 不是被测代码的，正常 return 会让它混进来，把 MatchExact 判红。
		//
		// 走到这里说明上面的检查全过了（任何一条不过都是 t.Fatal，
		// 那条路径由父进程的 exitCode 检查兜住），退 0 是准确的。
		os.Exit(0)
	case "leader":
		command := exec.Command("/proc/self/exe", "-test.run=TestCA44ProcessGroupHelper",
			"--", "descendant", marker)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
	case "descendant":
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(marker + ".supervisor-returned"); err == nil {
				break
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := os.WriteFile(marker, []byte("survived"), 0o600); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper mode: %s", mode)
	}
}
