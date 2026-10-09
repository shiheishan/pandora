package kernel

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
)

// peekTestConn 在 memTestClientConn 之上逐包记下写回客户端的负载与来源。
type peekTestConn struct {
	*memTestClientConn
	mu      sync.Mutex
	got     [][]byte
	sources []M.Socksaddr
}

func (c *peekTestConn) WritePacket(buffer *buf.Buffer, source M.Socksaddr) error {
	c.mu.Lock()
	c.got = append(c.got, bytes.Clone(buffer.Bytes()))
	c.sources = append(c.sources, source)
	c.mu.Unlock()
	return c.memTestClientConn.WritePacket(buffer, source)
}

func (c *peekTestConn) snapshot() ([][]byte, []M.Socksaddr) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.got...), append([]M.Socksaddr(nil), c.sources...)
}

// 下行窥视等包、借池子缓冲收包：一串大小不一的数据报（含 1 字节与接近 64KB 的）
// 逐字节、按序写回客户端，来源地址正确，计数相等；收完缓冲与批量组名额都还回，
// 取消后会话立即收尾（窥视被读截止打断）。IPv4 与 IPv6 上游各一遍（IPv6 不走
// 批量发送，但下行同样窥视收包）。
func TestHy2DownlinkPeekDeliversDatagramsIntact(t *testing.T) {
	supported := hy2UDPBatchSupported
	defer func() { hy2UDPBatchSupported = supported }()
	for _, network := range []struct{ name, loopback string }{{"udp4", "127.0.0.1"}, {"udp6", "::1"}} {
		for _, batch := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/batch=%v", network.name, batch), func(t *testing.T) {
				hy2UDPBatchSupported = batch
				upstream, err := net.ListenUDP(network.name, &net.UDPAddr{IP: net.ParseIP(network.loopback)})
				if err != nil {
					t.Skipf("本机没有 %s 回环：%v", network.name, err)
				}
				defer upstream.Close()
				sender, err := net.ListenUDP(network.name, &net.UDPAddr{IP: net.ParseIP(network.loopback)})
				if err != nil {
					t.Fatal(err)
				}
				defer sender.Close()
				_ = sender.SetWriteBuffer(1 << 20)

				conn := &peekTestConn{memTestClientConn: newMemTestClientConn()}
				u := newHy2UDPUpstream(upstream)
				// 一轮背靠背 40 包，收包缓冲放大一些，免得测的是内核丢包。
				_ = upstream.SetReadBuffer(1 << 20)
				if !hy2UDPPeekSupported {
					t.Skip("非 unix 没有窥视收包，下行走逐包阻塞读（见 hysteria2_udp_peek_other.go）")
				}
				if u.reader == nil {
					t.Fatal("裸 socket 应有窥视收包的 reader")
				}
				var down atomic.Int64
				done := make(chan struct{})
				go func() {
					defer close(done)
					hy2DownlinkUDP(context.Background(), conn, u, hy2DownlinkBatchShares.join(1), &down)
					hy2DownlinkBatchShares.leave(1)
				}()

				var want [][]byte
				var total int64
				sizes := []int{1, 1200, 0, 512, 60000, 1200, 1200, 1200}
				for round := range 3 {
					// 每轮一串背靠背的包（大于小组与批量组），再隔一会儿让会话回到空闲窥视。
					for i := range 40 {
						size := sizes[i%len(sizes)]
						if size > 1500 && i > len(sizes) {
							// 每轮只发一个接近 64KB 的包，总量留在缺省收包缓冲之内。
							size = 1300
						}
						payload := bytes.Repeat([]byte{byte(round*40 + i + 1)}, size)
						if _, err := sender.WriteTo(payload, upstream.LocalAddr()); err != nil {
							t.Fatal(err)
						}
						want = append(want, payload)
						total += int64(size)
					}
					deadline := time.Now().Add(5 * time.Second)
					for {
						got, _ := conn.snapshot()
						if len(got) == len(want) {
							break
						}
						if time.Now().After(deadline) {
							t.Fatalf("第 %d 轮只收到 %d / %d 包", round, len(got), len(want))
						}
						time.Sleep(5 * time.Millisecond)
					}
					time.Sleep(50 * time.Millisecond)
				}
				got, sources := conn.snapshot()
				from := M.SocksaddrFromNet(sender.LocalAddr()).Unwrap()
				for i := range want {
					if !bytes.Equal(got[i], want[i]) {
						t.Fatalf("第 %d 包：收到 %d 字节，期望 %d 字节且逐字节一致", i, len(got[i]), len(want[i]))
					}
					if sources[i] != from {
						t.Fatalf("第 %d 包来源 %v，期望 %v", i, sources[i], from)
					}
				}
				if down.Load() != total {
					t.Fatalf("下行计数 %d，期望 %d", down.Load(), total)
				}
				if n := len(hy2DownlinkBatchSlots); n != 0 {
					t.Fatalf("空闲时仍借着 %d 个批量组名额", n)
				}

				// 会话收尾：读截止打断阻塞的窥视。
				_ = upstream.SetDeadline(time.Now())
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("读截止没打断空闲窥视")
				}
			})
		}
	}
}

// relayHy2UDP 的收尾：上下行任一方向结束（客户端会话关闭、上游 socket 被关），
// 另一方向的阻塞读被打断，整个转发返回、不留 goroutine；父 ctx 取消同样如此。
// 「上游先关」一例守着下行结束后的那次 cancel：去掉它，上行永远等在会话队列上。
func TestHy2RelayEndsWhenEitherSideEnds(t *testing.T) {
	for _, name := range []string{"client-closed", "upstream-closed", "ctx-cancelled"} {
		t.Run(name, func(t *testing.T) {
			// 存货回收的后台 goroutine 是进程级的，先起好，不算进本用例。
			hy2StockJanitor.start()
			before := runtime.NumGoroutine()
			upstream, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer upstream.Close()
			conn := newMemTestClientConn()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var up, down atomic.Int64
			done := make(chan struct{})
			go func() {
				defer close(done)
				relayHy2UDP(ctx, conn, upstream, M.SocksaddrFromNet(upstream.LocalAddr()), 1, &up, &down)
			}()
			time.Sleep(50 * time.Millisecond)
			switch name {
			case "client-closed":
				_ = conn.Close()
			case "upstream-closed":
				_ = upstream.Close()
			default:
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("转发没有收尾")
			}
			deadline := time.Now().Add(2 * time.Second)
			for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if after := runtime.NumGoroutine(); after > before {
				t.Fatalf("goroutine %d → %d，转发留下了 goroutine", before, after)
			}
		})
	}
}
