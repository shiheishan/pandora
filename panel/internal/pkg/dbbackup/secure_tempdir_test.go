package dbbackup

import (
	"os"
	"testing"
)

// secureTempDir 返回一个 0700 的临时目录，并在本测试内把运行测试的用户
// 当作私密路径的可信属主（生产只信 root，见 trustCurrentUserAsSecureOwner）。
//
// 收成 0700 只是让用例与最严的布局一致；父目录校验（validateSecureParent）只拒绝
// 组或其他人可写，t.TempDir() 的 0755 本身也能过。这条校验是 //go:build linux 门控的，
// 非 Linux 平台上是空实现，只在别的平台跑测试看不出它的效果。
func secureTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("收紧临时目录权限失败: %v", err)
	}
	trustCurrentUserAsSecureOwner(t)
	return dir
}

// withCheckpointHookRoot 把 Hook 允许存放的固定目录临时指向 dir。
//
// 生产上这个目录是写死的 /opt/pandora/checkpoint-sink，测试里显然
// 建不出来也不该建。t.Cleanup 保证改动只在单个测试内可见，
// 并行测试不共享这个包级变量，所以这些用例不能标 t.Parallel()。
func withCheckpointHookRoot(t *testing.T, dir string) {
	t.Helper()
	previous := checkpointHookRoot
	checkpointHookRoot = dir
	t.Cleanup(func() { checkpointHookRoot = previous })
}
