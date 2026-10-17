package kernel

import (
	"net"
	"net/netip"
	"runtime"

	"github.com/sagernet/sing/common/buf"
)

// 经典 SS 与 SS2022 共用的慢路径池：读循环快路径处理不了的包（经典：解不开或来源地址还没有会话，
// 要按全部用户逐个试解；SS2022：认出了用户但还没有会话，要派生密钥、建会话），复制进池化缓冲
// 交给本入站的有界 worker 池，由适配器传给 udpWorker 的处理函数处理。worker 数 = GOMAXPROCS，
// 队列满直接丢（计 udpDrops.queue），不阻塞读循环：灌垃圾包只占满这个池，已建会话的快路径照常走。
// 代价：被灌时新会话的首包也可能在队列满时被丢，客户端重发即可。

const ssUDPSlowQueue = 256 // 慢路径队列长度：满了丢包

// ssUDPSlowWorkers 是每个入站的慢路径 worker 数。
func ssUDPSlowWorkers() int { return runtime.GOMAXPROCS(0) }

// ssUDPJob 是交给慢路径的一个包：pkt 从 buf.Get 借来，由处理它的 worker 归还。
type ssUDPJob struct {
	pkt  []byte
	from netip.AddrPort
}

// dispatchSlowUDP 把 wire 复制进池化缓冲放进队列；队列满就丢（计数），不等待。
func (h *ssUDPHub) dispatchSlowUDP(wire []byte, from netip.AddrPort) {
	pkt := buf.Get(len(wire))
	copy(pkt, wire)
	select {
	case h.udpJobs <- ssUDPJob{pkt: pkt, from: from}:
	default:
		_ = buf.Put(pkt)
		h.udpDrops.queue.Add(1)
	}
}

// udpWorker 处理慢路径队列，入站 ctx 取消即退出（队列里剩下的包随之丢弃）。
func (h *ssUDPHub) udpWorker(handle func(pkt []byte, from netip.AddrPort) error) {
	defer h.udpWG.Done()
	for {
		select {
		case <-h.udpCtx.Done():
			return
		case job := <-h.udpJobs:
			if err := handle(job.pkt, job.from); err != nil {
				h.udpErr.addr(StageSession, net.UDPAddrFromAddrPort(job.from), err)
			}
			_ = buf.Put(job.pkt)
		}
	}
}
