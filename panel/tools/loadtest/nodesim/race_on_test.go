//go:build race

package nodesim

// raceEnabled 为真时跳过 1000 节点的资源自检：竞态检测让内存与 CPU 大几倍，量到的不是生产里的数。
const raceEnabled = true
