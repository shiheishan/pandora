package kernel

import (
	"net"
	"net/netip"
	"runtime"

	"github.com/sagernet/sing/common/buf"
)

// SS UDP 的慢路径：读循环快路径解不开、或来源地址还没有会话的包，复制进池化缓冲交给
// 本入站的有界 worker 池，按全部用户逐个试解。worker 数 = GOMAXPROCS，队列满直接丢
// （计 udpDrops.queue），不阻塞读循环：灌垃圾包只占满这个池，已建会话的快路径照常走。
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
func (a *shadowsocksAdapter) dispatchSlowUDP(wire []byte, from netip.AddrPort) {
	pkt := buf.Get(len(wire))
	copy(pkt, wire)
	select {
	case a.udpJobs <- ssUDPJob{pkt: pkt, from: from}:
	default:
		_ = buf.Put(pkt)
		a.udpDrops.queue.Add(1)
	}
}

// udpWorker 处理慢路径队列，入站 ctx 取消即退出（队列里剩下的包随之丢弃）。
func (a *shadowsocksAdapter) udpWorker() {
	defer a.wg.Done()
	for {
		select {
		case <-a.ctx.Done():
			return
		case job := <-a.udpJobs:
			if err := a.slowPathUDP(job.pkt, job.from); err != nil {
				a.connErr.addr(StageSession, net.UDPAddrFromAddrPort(job.from), err)
			}
			_ = buf.Put(job.pkt)
		}
	}
}

// slowPathUDP 对全部用户试解一个包；认证通过后找到或新建 (地址, 用户) 会话并转发。
// 明文借池化缓冲（不能原地解：GCM 失败会清零 dst），forwardUDP 同步写完或复制进
// 建立期小队列后不再引用，返回前归还。
func (a *shadowsocksAdapter) slowPathUDP(wire []byte, from netip.AddrPort) error {
	salt := wire[:a.method.SaltLen]
	ciphertext := wire[a.method.SaltLen:]
	plain := buf.Get(len(ciphertext))
	defer func() { _ = buf.Put(plain) }()
	epoch := a.sessions.epoch()
	user, dest, payload, err := a.decodeUDPPacket(salt, ciphertext, plain, from)
	if err != nil {
		return errSSUDPAuth
	}
	if !a.acceptUDPSalt(salt) {
		return errSSUDPReplay
	}
	sess, err := a.getOrOpenUDPSession(from, user, epoch, dest)
	if err != nil {
		return err
	}
	return a.forwardUDP(sess, dest, payload)
}
