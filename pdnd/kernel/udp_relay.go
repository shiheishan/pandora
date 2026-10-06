// [INPUT]: 只依赖标准库 context
// [OUTPUT]: 包内提供 relayUDPDirections：UDP 中继上行、下行各占一个 goroutine 的收尾骨架
// [POS]: kernel 的 UDP 中继公共骨架，被 proxy_udp.go（SOCKS5）、trojan_udp.go、vmess.go 的 handleUDP 共用；vless_udp.go 与 QUIC 系协议各自内联同一思路

package kernel

import "context"

// ============================================================================
//  上下行解耦
// ----------------------------------------------------------------------------
//  UDP 中继的两个方向必须互不等待：游戏、语音、QUIC 都是"发一个包、回一串包"，
//  下行若要等上行读超时或等上行来一个包才能转一个包，回程就被限成几包每秒。
//  所以每个方向各跑一个 goroutine，阻塞读、不设轮询超时；收尾集中在这里：
//
//    任一方向返回 / interrupt 来值 / ctx 结束
//      → 取消两个方向共用的 ctx（停下行的出队、停路由读协程的投递）
//      → stop() 关掉阻塞读所在的连接（读超时对 gRPC、XHTTP 承载是空操作，只能关）
//      → 等两个方向都退出，返回最先得到的那个结果
//
//  调用方在 relayUDPDirections 返回后再关各自的路由 PacketConn：此时两个方向
//  都已退出，路由表不再有并发访问。
// ============================================================================

// relayUDPDirections 并发运行 uplink 与 downlink，二者拿到的是同一个可取消的
// ctx。interrupt 可为 nil；stop 必须能打断两个方向上的阻塞读写。
func relayUDPDirections(ctx context.Context, interrupt <-chan error, stop func(), uplink, downlink func(context.Context) error) error {
	relayCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- uplink(relayCtx) }()
	go func() { results <- downlink(relayCtx) }()
	pending := 2
	var result error
	select {
	case result = <-results:
		pending--
	case result = <-interrupt:
	case <-ctx.Done():
		result = ctx.Err()
	}
	cancel()
	stop()
	for ; pending > 0; pending-- {
		<-results
	}
	return result
}
