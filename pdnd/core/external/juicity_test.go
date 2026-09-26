// [INPUT]: 依赖 juicity.go 的 NewJuicity 与 DefaultJuicityWorkDir，读取 release/pandora-native.service 的 ReadWritePaths
// [OUTPUT]: 对外提供 TestJuicityDefaultWorkDirIsWritableByService
// [POS]: pdnd/core/external 的单元测试：配置不写 work_dir 时，缺省目录必须落在 systemd 单元允许写的路径里（⑫）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package external

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJuicityDefaultWorkDirIsWritableByService(t *testing.T) {
	j := NewJuicity(JuicityOptions{Tag: "t", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if j.workDir != DefaultJuicityWorkDir {
		t.Fatalf("empty work_dir resolved to %q, want %q", j.workDir, DefaultJuicityWorkDir)
	}
	unit, err := os.ReadFile(filepath.Join("..", "..", "release", "pandora-native.service"))
	if err != nil {
		t.Fatal(err)
	}
	var writable []string
	for _, line := range strings.Split(string(unit), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ReadWritePaths="); ok {
			writable = append(writable, strings.Fields(rest)...)
		}
	}
	for _, dir := range writable {
		if DefaultJuicityWorkDir == dir || strings.HasPrefix(DefaultJuicityWorkDir, dir+"/") {
			return
		}
	}
	t.Fatalf("default juicity work dir %s is outside the service ReadWritePaths %v", DefaultJuicityWorkDir, writable)
}
