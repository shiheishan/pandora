package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/dbbackup"
)

// runBackup 执行 pandora backup <args…>，返回退出码与标准输出（错误文本按 Execute 的口径并入返回的 msg）。
func runBackup(t *testing.T, configPath string, args ...string) (code int, stdout, msg string) {
	t.Helper()
	cmd := newBackupCmd(&configPath)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		return exitCode(err), out.String(), err.Error()
	}
	return 0, out.String(), ""
}

// stubBackupRoot 替换 root 检查。
func stubBackupRoot(t *testing.T, err error) {
	t.Helper()
	orig := backupRequireRoot
	backupRequireRoot = func() error { return err }
	t.Cleanup(func() { backupRequireRoot = orig })
}

// 参数个数不对：退出码 2，打印用法。
func TestBackupUsageExitCode(t *testing.T) {
	stubBackupRoot(t, nil)
	for _, args := range [][]string{
		{"webdav-upload"},
		{"webdav-upload", "/a.dump.age"},
		{"webdav-upload", "/a", "/b", "/c"},
		{"seal-local", "/a", "/b", "/c"},
		{"verify-local-seal", "/a", "/b", "/c", "/d", "/e"},
		{"init-signing-key"},
		{"verify-manifest", "/a", "/b", "/c", "/d"},
		{"dir", "extra"},
	} {
		code, _, msg := runBackup(t, "", args...)
		if code != 2 || !strings.HasPrefix(msg, "usage: pandora backup ") {
			t.Fatalf("%v: code=%d msg=%q", args, code, msg)
		}
	}
}

// 非 root：每个要碰 root 文件的子命令都先拒绝，报 root_required，不往下碰任何文件。
func TestBackupCommandsRequireRoot(t *testing.T) {
	stubBackupRoot(t, errors.New("not root"))
	called := false
	orig := backupWebDAVRun
	backupWebDAVRun = func(context.Context, string, string, string) (string, error) {
		called = true
		return "", nil
	}
	t.Cleanup(func() { backupWebDAVRun = orig })

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"seal-local", "/nonexistent/pandora-postgres-20261010T011540Z.dump.age", "/nonexistent/x.sha256", "/nonexistent/key", "age1x"},
			"local backup seal failed: root_required"},
		{[]string{"verify-local-seal", "/nonexistent/a", "/nonexistent/b", "/nonexistent/c", "/nonexistent/d"},
			"local backup seal verification failed: root_required"},
		{[]string{"webdav-upload", "/nonexistent/a.dump.age", "/nonexistent/a.dump.age.sha256"},
			"webdav backup upload failed: root_required"},
		{[]string{"init-signing-key", "/nonexistent/seed"},
			"webdav backup key initialization failed: root_required"},
		{[]string{"verify-manifest", "/a", "/b", "/c", "/d", "/e"},
			"webdav backup verification failed: root_required"},
	} {
		code, _, msg := runBackup(t, "", tc.args...)
		if code != 1 || msg != tc.want {
			t.Fatalf("%s: code=%d msg=%q, want %q", tc.args[0], code, msg, tc.want)
		}
	}
	if called {
		t.Fatal("webdav upload ran without root")
	}
}

// webdav-upload：两个位置参数原样交给上传流程；配置缺省 /etc/pandora/backup-webdav.json，--webdav-config 可改；
// 成功时打印流程给的摘要，失败时原样返回阶段报错、退出码 1。
func TestBackupWebDAVUploadArguments(t *testing.T) {
	stubBackupRoot(t, nil)
	type call struct{ config, archive, checksum string }
	var got []call
	result := "webdav backup upload complete: archive=x bytes=1 sha256=y"
	var resultErr error
	orig := backupWebDAVRun
	backupWebDAVRun = func(ctx context.Context, config, archive, checksum string) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("upload context has no deadline")
		}
		got = append(got, call{config, archive, checksum})
		return result, resultErr
	}
	t.Cleanup(func() { backupWebDAVRun = orig })

	archive := "/var/backups/pandora/pandora-postgres-20261013T031700Z.dump.age"
	code, out, msg := runBackup(t, "", "webdav-upload", archive, archive+".sha256")
	if code != 0 || msg != "" || strings.TrimSpace(out) != result {
		t.Fatalf("default config: code=%d out=%q msg=%q", code, out, msg)
	}
	if want := (call{dbbackup.DefaultWebDAVConfigPath, archive, archive + ".sha256"}); len(got) != 1 || got[0] != want {
		t.Fatalf("calls=%+v want %+v", got, want)
	}
	if dbbackup.DefaultWebDAVConfigPath != "/etc/pandora/backup-webdav.json" {
		t.Fatalf("default webdav config moved: %s", dbbackup.DefaultWebDAVConfigPath)
	}

	got = nil
	code, _, _ = runBackup(t, "", "webdav-upload", "--webdav-config", "/srv/webdav.json", archive, archive+".sha256")
	if code != 0 || len(got) != 1 || got[0].config != "/srv/webdav.json" {
		t.Fatalf("--webdav-config: code=%d calls=%+v", code, got)
	}

	resultErr = errors.New("webdav backup upload failed: probe_failed")
	code, out, msg = runBackup(t, "", "webdav-upload", archive, archive+".sha256")
	if code != 1 || msg != "webdav backup upload failed: probe_failed" || out != "" {
		t.Fatalf("failure: code=%d out=%q msg=%q", code, out, msg)
	}
}

// 上传流程第一步核本地归档与校验文件：相对路径、不存在的文件都停在 local_pair_invalid，不读 WebDAV 配置。
func TestRunWebDAVUploadRejectsInvalidPair(t *testing.T) {
	dir := t.TempDir()
	for _, pair := range [][2]string{
		{"relative.dump.age", "relative.dump.age.sha256"},
		{filepath.Join(dir, "pandora-postgres-20261013T031700Z.dump.age"), filepath.Join(dir, "pandora-postgres-20261013T031700Z.dump.age.sha256")},
	} {
		_, err := runWebDAVUpload(context.Background(), filepath.Join(dir, "missing-webdav.json"), pair[0], pair[1])
		if err == nil || err.Error() != "webdav backup upload failed: local_pair_invalid" {
			t.Fatalf("%v: err=%v", pair, err)
		}
	}
}

// backup dir：缺省 /var/backups/pandora；配置文件的 backup.dir；PANDORA_BACKUP_DIR 覆盖；不规范的路径拒绝。
func TestBackupDir(t *testing.T) {
	t.Setenv("PANDORA_BACKUP_DIR", "")
	_ = os.Unsetenv("PANDORA_BACKUP_DIR")

	code, out, msg := runBackup(t, "", "dir")
	if code != 0 || strings.TrimSpace(out) != "/var/backups/pandora" {
		t.Fatalf("default: code=%d out=%q msg=%q", code, out, msg)
	}

	cfgDir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(cfgDir, "config.yaml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg := write("env: production\nbackup:\n  dir: /srv/pandora-backups\n")
	code, out, _ = runBackup(t, cfg, "dir")
	if code != 0 || strings.TrimSpace(out) != "/srv/pandora-backups" {
		t.Fatalf("config file: code=%d out=%q", code, out)
	}

	t.Setenv("PANDORA_BACKUP_DIR", "/data/backups")
	code, out, _ = runBackup(t, cfg, "dir")
	if code != 0 || strings.TrimSpace(out) != "/data/backups" {
		t.Fatalf("env override: code=%d out=%q", code, out)
	}
	_ = os.Unsetenv("PANDORA_BACKUP_DIR")

	for _, bad := range []string{`""`, "backups", "/", "/var/backups/../pandora", "/var/backups/pandora/"} {
		code, out, msg = runBackup(t, write("backup:\n  dir: "+bad+"\n"), "dir")
		if code != 1 || out != "" || !strings.Contains(msg, "backup.dir") {
			t.Fatalf("%q: code=%d out=%q msg=%q", bad, code, out, msg)
		}
	}

	code, _, msg = runBackup(t, filepath.Join(cfgDir, "missing.yaml"), "dir")
	if code != 1 || !strings.Contains(msg, "读取配置文件") {
		t.Fatalf("missing config: code=%d msg=%q", code, msg)
	}
}
