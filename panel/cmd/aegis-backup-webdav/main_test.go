package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestRunRefusesUnexpectedArguments(t *testing.T) {
	if got := run(nil); got != 2 {
		t.Fatalf("run(nil)=%d", got)
	}
	if got := run([]string{"relative", "relative.sha256"}); got != 1 {
		t.Fatalf("run(invalid pair)=%d", got)
	}
}

// 两个封条子命令先核 root：非 root 时报 root_required，不往下碰任何文件（以 root 跑测试时跳过）
func TestLocalSealCommandsRequireRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the root check passes by design")
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"seal-local", "/nonexistent/aegis-postgres-20261010T011540Z.dump.age", "/nonexistent/x.sha256", "/nonexistent/key"},
			"local backup seal failed: root_required"},
		{[]string{"verify-local-seal", "/nonexistent/a", "/nonexistent/b", "/nonexistent/c", "/nonexistent/d"},
			"local backup seal verification failed: root_required"},
	} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		saved := os.Stderr
		os.Stderr = w
		code := run(tc.args)
		os.Stderr = saved
		_ = w.Close()
		out, _ := io.ReadAll(r)
		if code != 1 || !strings.Contains(string(out), tc.want) {
			t.Fatalf("%s: code=%d stderr=%q, want %q", tc.args[0], code, out, tc.want)
		}
	}
}
