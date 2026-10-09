//go:build unix

package kernel

import (
	"context"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/internal/udprecv"
)

// countingRawConn 记下收包器在 socket 上的每一次收包尝试：udprecv 的回调里每调一次
// 就是一次 recvmmsg（别的 unix 是 recvfrom），落空的也算。
type countingRawConn struct {
	syscall.RawConn
	recvs atomic.Int64
}

func (c *countingRawConn) Read(f func(uintptr) bool) error {
	return c.RawConn.Read(func(fd uintptr) bool {
		c.recvs.Add(1)
		return f(fd)
	})
}

func (c *countingRawConn) Control(f func(uintptr)) error {
	return c.RawConn.Control(func(fd uintptr) {
		c.recvs.Add(1)
		f(fd)
	})
}

// deadlineCountingConn 记设过非零读截止的次数（热态每段设一次，进热态即有）。
type deadlineCountingConn struct {
	net.PacketConn
	armed atomic.Int64
}

func (c *deadlineCountingConn) SetReadDeadline(t time.Time) error {
	if !t.IsZero() {
		c.armed.Add(1)
	}
	return c.PacketConn.SetReadDeadline(t)
}

// 每包收包系统调用的上限（复审 review-r6 第 1 条）：真 UDP socket 经生产入口
// hy2DownlinkUDP 收包，数收包器的系统调用。
//   - 冷态（每 5ms 一包，每秒 200 包，不进热态）：每次醒来「落空 + 收到」两次，与
//     持有缓冲阻塞读（sing-box 的 ReadFrom）同形状；
//   - 中速（每 300µs 一包，约每秒 3300 包，会进热态）：同样两次。
//
// 上限 2.05（容几次 netpoller 的虚假唤醒）。改回「先窥视等包、再借缓冲收」，冷态
// 每包三次，这里变红。
func TestHy2DownlinkRecvSyscallsPerPacket(t *testing.T) {
	if !udprecv.Supported {
		t.Skip("本平台没有就绪收包")
	}
	for _, tc := range []struct {
		name     string
		interval time.Duration
		packets  int
		warm     bool
	}{
		{"cold-5ms", 5 * time.Millisecond, 200, false},
		{"medium-300us", 300 * time.Microsecond, 3000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer upstream.Close()
			_ = upstream.SetReadBuffer(1 << 20)
			sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			rc, err := upstream.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			counting := &countingRawConn{RawConn: rc}
			deadlines := &deadlineCountingConn{PacketConn: upstream}
			u := hy2UDPUpstream{conn: deadlines, recv: udprecv.NewReceiver(counting)}
			conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
			ctx, cancel := context.WithCancel(context.Background())
			userID := int64(66000)
			share := hy2DownlinkBatchShares.join(userID)
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer hy2DownlinkBatchShares.leave(userID)
				var down atomic.Int64
				hy2DownlinkUDP(ctx, conn, u, share, &down)
			}()
			// 等会话进入空闲等待，开头那次落空不算进来。
			waitDownlink(t, "会话开始等包", func() bool { return counting.recvs.Load() >= 1 })
			before := counting.recvs.Load()
			warmBefore := deadlines.armed.Load()
			payload := make([]byte, 1200)
			start := time.Now()
			for i := range tc.packets {
				// 按绝对时刻发，不靠计时器精度；冷态的间隔靠睡、中速忙等。
				due := start.Add(time.Duration(i) * tc.interval)
				if wait := time.Until(due); wait > time.Millisecond {
					time.Sleep(wait)
				}
				for time.Now().Before(due) {
				}
				if _, err := sender.WriteTo(payload, upstream.LocalAddr()); err != nil {
					t.Fatal(err)
				}
			}
			waitDownlink(t, "收完", func() bool { return conn.writes.Load() == int64(tc.packets) })
			// 收完最后一包之后会话回到等待，那次落空属于下一次醒来，等它发生再数。
			time.Sleep(5 * time.Millisecond)
			recvs := counting.recvs.Load() - before
			warmed := deadlines.armed.Load() - warmBefore
			cancel()
			_ = upstream.SetReadDeadline(time.Now())
			<-done
			perPacket := float64(recvs) / float64(tc.packets)
			t.Logf("%s：%d 包，收包系统调用 %d 次，每包 %.3f 次；设热态读截止 %d 次", tc.name, tc.packets, recvs, perPacket, warmed)
			if perPacket > 2.05 {
				t.Fatalf("每包 %.3f 次收包系统调用，超过阻塞读的 2 次", perPacket)
			}
			if tc.warm && warmed == 0 {
				t.Fatal("中速会话没进热态")
			}
			if !tc.warm && warmed != 0 {
				t.Fatalf("每秒 200 包的会话设了 %d 次热态读截止", warmed)
			}
		})
	}
}
