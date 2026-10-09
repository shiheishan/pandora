//go:build !unix

package kernel

// 非 unix 没有 MSG_PEEK / MSG_DONTWAIT 的统一写法：下行退回逐包阻塞读（节点只在
// Linux 上部署）。
const (
	hy2UDPPeekSupported = false
	hy2UDPPeekFlag      = 0
	hy2UDPDontWaitFlag  = 0
)

func isHy2UDPWouldBlock(error) bool { return false }
