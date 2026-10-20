package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func testParentUpgrade(t *testing.T) (parent, upgrade string) {
	parent = filepath.Join(t.TempDir(), "parent")
	upgrade = filepath.Join(parent, StateSubdir)
	if err := os.MkdirAll(upgrade, 0o755); err != nil {
		t.Fatal(err)
	}
	return parent, upgrade
}

func mustOpenState(t *testing.T, parent string) *os.Root {
	state, err := openStateDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestReadState(t *testing.T) {
	parent, upgrade := testParentUpgrade(t)
	state := mustOpenState(t, parent)
	defer state.Close()

	const max = 64
	good := filepath.Join(upgrade, "good")
	if err := os.WriteFile(good, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := readState(state, "good", max)
	if err != nil {
		t.Fatalf("普通文件: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("内容 %q", data)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(upgrade, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(state, "link", max); err == nil {
		t.Fatal("符号链接应失败")
	}

	if err := os.Mkdir(filepath.Join(upgrade, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(state, "adir", max); err == nil {
		t.Fatal("目录应失败")
	}

	fifo := filepath.Join(upgrade, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := readState(state, "fifo", max); err == nil {
		t.Fatal("FIFO 应失败")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("读 FIFO 卡住超过 2 秒")
	}

	big := filepath.Join(upgrade, "big")
	if err := os.WriteFile(big, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, max+1); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(state, "big", max); err == nil {
		t.Fatal("超过 max 应失败")
	}
}

func TestCopyFromState(t *testing.T) {
	parent, upgrade := testParentUpgrade(t)
	state := mustOpenState(t, parent)
	defer state.Close()

	const max = 128
	src := filepath.Join(upgrade, "payload")
	payload := []byte("copy-me")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out")
	if err := copyFromState(state, "payload", dst, max); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("内容 %q", got)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("权限 %o", fi.Mode().Perm())
	}
	if _, err := os.Stat(dst + ".tmp"); err == nil {
		t.Fatal("不应留下 .tmp")
	}

	outside := filepath.Join(t.TempDir(), "ext")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(upgrade, "badlink")); err != nil {
		t.Fatal(err)
	}
	dst2 := filepath.Join(t.TempDir(), "out2")
	if err := copyFromState(state, "badlink", dst2, max); err == nil {
		t.Fatal("符号链接源应失败")
	}
	if _, err := os.Stat(dst2); err == nil {
		t.Fatal("失败时不应创建 dst")
	}
}

func TestOpenStateDir(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "p-only")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := openStateDir(parent)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("缺 upgrade 目录: got %v", err)
	}

	outside := filepath.Join(t.TempDir(), "real-upgrade")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	parent2 := filepath.Join(t.TempDir(), "p2")
	if err := os.MkdirAll(parent2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(parent2, StateSubdir)); err != nil {
		t.Fatal(err)
	}
	if _, err := openStateDir(parent2); err == nil {
		t.Fatal("upgrade 指向外部目录应失败")
	}
}

func TestWriteState(t *testing.T) {
	parent, upgrade := testParentUpgrade(t)
	state := mustOpenState(t, parent)
	defer state.Close()

	outside := filepath.Join(t.TempDir(), "victim")
	orig := []byte("unchanged")
	if err := os.WriteFile(outside, orig, 0o644); err != nil {
		t.Fatal(err)
	}

	data := []byte("{\"job\":\"1\"}")
	if err := os.Symlink(outside, filepath.Join(upgrade, FileResult)); err != nil {
		t.Fatal(err)
	}
	if err := writeState(state, FileResult, data); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(upgrade, FileResult))
	if err != nil {
		t.Fatal(err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o644 {
		t.Fatalf("result 应为 0644 普通文件: %v", fi.Mode())
	}
	got, err := os.ReadFile(filepath.Join(upgrade, FileResult))
	if err != nil || string(got) != string(data) {
		t.Fatalf("result 内容 %q err %v", got, err)
	}
	still, err := os.ReadFile(outside)
	if err != nil || string(still) != string(orig) {
		t.Fatalf("外部文件被改动: %q", still)
	}

	if err := os.Symlink(outside, filepath.Join(upgrade, "."+FileResult+".tmp")); err != nil {
		t.Fatal(err)
	}
	data2 := []byte("second")
	if err := writeState(state, FileResult, data2); err != nil {
		t.Fatal(err)
	}
	got2, err := os.ReadFile(filepath.Join(upgrade, FileResult))
	if err != nil || string(got2) != string(data2) {
		t.Fatalf("第二次写入 %q", got2)
	}
	still2, err := os.ReadFile(outside)
	if err != nil || string(still2) != string(orig) {
		t.Fatalf("外部文件被改动: %q", still2)
	}
}

func TestRemoveState(t *testing.T) {
	parent, _ := testParentUpgrade(t)
	state := mustOpenState(t, parent)
	defer state.Close()
	if err := removeState(state, "missing"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureWorkDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "work")
	if err := ensureWorkDir(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("目录权限 %o", fi.Mode().Perm())
	}

	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := ensureWorkDir(link); err == nil {
		t.Fatal("符号链接路径应失败")
	}
}

func TestCopyFileSyncAndSHA256File(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	content := []byte("hash-me")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	const mode = 0o755
	if err := copyFileSync(src, dst, mode); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])
	got, err := sha256File(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("摘要 got %s want %s", got, want)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != mode {
		t.Fatalf("权限 %o", fi.Mode().Perm())
	}

	outside := filepath.Join(dir, "out")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "lnk")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := copyFileSync(link, filepath.Join(dir, "nope"), mode); err == nil {
		t.Fatal("符号链接源 copyFileSync 应失败")
	}
	if _, err := sha256File(link); err == nil {
		t.Fatal("符号链接源 sha256File 应失败")
	}
}

func TestSystemctlRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows 无 systemctl 桩")
	}

	logOK := filepath.Join(t.TempDir(), "ok.log")
	scriptOK := filepath.Join(t.TempDir(), "ok.sh")
	okBody := "#!/bin/sh\necho \"$@\" >> " + logOK + "\nexit 0\n"
	if err := os.WriteFile(scriptOK, []byte(okBody), 0o755); err != nil {
		t.Fatal(err)
	}
	unit := MainUnit
	if err := (Systemctl{Bin: scriptOK}).Restart(t.Context(), unit); err != nil {
		t.Fatal(err)
	}
	lines, err := os.ReadFile(logOK)
	if err != nil {
		t.Fatal(err)
	}
	log := string(lines)
	wantLog := "reset-failed " + unit + "\nrestart " + unit + "\n"
	if log != wantLog {
		t.Fatalf("日志:\n%s", log)
	}

	logFail := filepath.Join(t.TempDir(), "fail.log")
	scriptFail := filepath.Join(t.TempDir(), "fail.sh")
	failBody := "#!/bin/sh\nif [ \"$1\" = restart ]; then echo \"$@\" >> " + logFail + "; exit 1; fi\nexit 0\n"
	if err := os.WriteFile(scriptFail, []byte(failBody), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (Systemctl{Bin: scriptFail}).Restart(t.Context(), unit); err == nil {
		t.Fatal("restart 退出 1 应返回错误")
	}
}
