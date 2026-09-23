package dbbackup

import (
	"os"
	"testing"
)

// secureTempDir 返回一个 0700 的临时目录。
//
// 不能直接用 t.TempDir()：它建目录时传的是 0777，会被 umask 削成 0755，
// 于是父目录带上了 group/other 位。备份路径的安全校验（validateSecureParent）
// 恰恰要求父目录不能对外开放，所以在 Linux 上所有用到私密路径的测试都会失败。
//
// 这一点在 Windows 上看不出来 —— requireSecureParentMode 是 //go:build linux
// 门控的，非 Linux 平台上是空实现。也就是说只在 Windows 上跑测试，
// 这类失败会全程静默，直到部署到 Linux 才暴露。
func secureTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("收紧临时目录权限失败: %v", err)
	}
	return dir
}

// withCheckpointHookRoot 把 Hook 允许存放的固定目录临时指向 dir。
//
// 生产上这个目录是写死的 /opt/aegispanel/checkpoint-sink，测试里显然
// 建不出来也不该建。t.Cleanup 保证改动只在单个测试内可见，
// 并行测试不共享这个包级变量，所以这些用例不能标 t.Parallel()。
func withCheckpointHookRoot(t *testing.T, dir string) {
	t.Helper()
	previous := checkpointHookRoot
	checkpointHookRoot = dir
	t.Cleanup(func() { checkpointHookRoot = previous })
}
