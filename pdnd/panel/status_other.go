//go:build !linux

package panel

// diskBytes 在非 Linux 上返回零。
//
// 节点端的目标平台只有 Linux，这个实现是为了让开发机（macOS / Windows）
// 上的 go build 和测试能过。报零而不是编译失败：一个跑不起来的构建会挡住
// 所有本地开发，而少一项磁盘指标只是面板上那一格显示 0。
func diskBytes(path string) (total, used uint64) {
	return 0, 0
}
