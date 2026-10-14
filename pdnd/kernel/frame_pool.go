package kernel

import "sync"

// 数据路径的分帧缓冲池（vmess 分块、gRPC 消息）：一帧 64KB 以内从池里借，
// 用完还；更大的帧（gRPC 允许到 16MB，罕见）照旧临时分配。
const framePoolSize = 64<<10 + 64

var framePool = sync.Pool{New: func() any { b := make([]byte, framePoolSize); return &b }}

// getFrameBuf 借一块至少 n 字节的缓冲；返回的 *[]byte 为 nil 表示是临时分配的，
// 不用还。
func getFrameBuf(n int) (*[]byte, []byte) {
	if n <= framePoolSize {
		bp := framePool.Get().(*[]byte)
		return bp, (*bp)[:n]
	}
	return nil, make([]byte, n)
}

func putFrameBuf(bp *[]byte) {
	if bp != nil {
		framePool.Put(bp)
	}
}
