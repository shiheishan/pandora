//go:build race

package kernel

// raceEnabled：race 检测器同时存活的 goroutine 上限是 8128，规模类测试据此缩量。
const raceEnabled = true
