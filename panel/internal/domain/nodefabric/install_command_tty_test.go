//go:build unix

package nodefabric

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// 两段接入命令读令牌时都不回显，并且每条出路都把回显恢复。
func TestInstallCommandsReadTokenWithoutEcho(t *testing.T) {
	for name, cmd := range map[string]string{
		"默认模板": renderInstallCommand("", "https://pandora.example.com", "tok-123", "hk-01"),
		"兼容接入": RenderLegacyInstallCommand("https://pandora.example.com", "node-1", "vless"),
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(cmd, "read -s") || strings.Contains(cmd, "read -rs") {
				t.Fatalf("read -s 不是 POSIX，dash 不认：%s", cmd)
			}
			trapAt := strings.Index(cmd, "trap '")
			offAt := strings.Index(cmd, "stty -echo </dev/tty")
			readAt := strings.Index(cmd, "IFS= read -r ")
			onAt := strings.Index(cmd[readAt:], "stty echo </dev/tty")
			if trapAt < 0 || offAt <= trapAt || readAt <= offAt || onAt < 0 {
				t.Fatalf("要先装 trap、再关回显、读完立刻恢复：%s", cmd)
			}
			exitTrap := cmd[trapAt:strings.Index(cmd, "' EXIT;")]
			if !strings.Contains(exitTrap, "stty echo </dev/tty") || !strings.Contains(exitTrap, `rm -f "$PANDORA_TOKEN_FILE"`) {
				t.Fatalf("EXIT trap 必须恢复回显并删令牌文件：%s", exitTrap)
			}
			for _, sig := range []string{"trap 'exit 130' INT;", "trap 'exit 129' HUP;", "trap 'exit 143' TERM;"} {
				if !strings.Contains(cmd, sig) {
					t.Fatalf("中断要转成 exit 交给 EXIT trap 收尾，缺 %q", sig)
				}
			}
		})
	}
}

// 在本机能找到的 POSIX shell（含 dash）里真跑一遍：语法通过；没有控制终端时读不到
// 令牌也不卡住；安装器拿到令牌文件路径，命令以安装器的退出码结束，令牌文件不留下。
// 用假 curl 吐一段假安装器，不联网。子进程 setsid 脱离控制终端，/dev/tty 打不开。
func TestInstallCommandsRunUnderPOSIXShells(t *testing.T) {
	bin := t.TempDir()
	installer := `while [ $# -gt 0 ]; do printf '%s\n' "$1" >>"$PANDORA_TEST_OUT"; ` +
		`if [ "$1" = --token-file ]; then shift; printf '%s\n' "$1" >>"$PANDORA_TEST_OUT"; ` +
		`[ -f "$1" ] && echo token-file-present >>"$PANDORA_TEST_OUT"; fi; shift; done; exit 7`
	curl := "#!/bin/sh\ncat <<'PANDORA_FAKE_INSTALLER'\n" + installer + "\nPANDORA_FAKE_INSTALLER\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, shell := range []string{"sh", "dash", "bash"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		for name, cmd := range map[string]string{
			"default": renderInstallCommand("", "https://pandora.example.com", "tok-123", "hk-01"),
			"legacy":  RenderLegacyInstallCommand("https://pandora.example.com", "node-1", "vless"),
		} {
			t.Run(shell+"/"+name, func(t *testing.T) {
				if out, err := exec.Command(path, "-n", "-c", cmd).CombinedOutput(); err != nil {
					t.Fatalf("%s 语法检查失败：%v\n%s", shell, err, out)
				}
				out := filepath.Join(t.TempDir(), "installer.out")
				run := exec.Command(path, "-c", cmd)
				run.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "PANDORA_TEST_OUT="+out, "TMPDIR="+t.TempDir())
				run.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				run.Stdin = strings.NewReader("")
				output, err := run.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 {
					t.Fatalf("命令应以安装器的退出码 7 结束，得到 %v\n%s", err, output)
				}
				got, err := os.ReadFile(out)
				if err != nil {
					t.Fatalf("安装器没有被调用：%v\n%s", err, output)
				}
				lines := strings.Split(strings.TrimSpace(string(got)), "\n")
				var tokenFile string
				for i, l := range lines {
					if l == "--token-file" && i+1 < len(lines) {
						tokenFile = lines[i+1]
					}
				}
				if tokenFile == "" || !strings.Contains(string(got), "token-file-present") {
					t.Fatalf("安装器没拿到存在的令牌文件：%q", got)
				}
				if _, err := os.Stat(tokenFile); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("令牌文件 %s 在命令结束后仍在（err=%v）", tokenFile, err)
				}
			})
			ran++
		}
	}
	if ran == 0 {
		t.Skip("本机没有 POSIX shell")
	}
}
