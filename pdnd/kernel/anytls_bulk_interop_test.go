//go:build interop

package kernel

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/util"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
)

// iperf3 的形状：一条空闲的控制流 + 一条单向大流量的数据流，控制流在整个传输期间
// 不能被断开（1c1g 实测 anytls 报 control socket has closed unexpectedly）。
func TestAnyTLSClientBulkKeepsIdleControlStream(t *testing.T) {
	for _, dir := range []string{"up", "down"} {
		t.Run(dir, func(t *testing.T) {
			sink, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer sink.Close()
			go func() {
				for {
					c, err := sink.Accept()
					if err != nil {
						return
					}
					go func() {
						defer c.Close()
						var mode [1]byte
						if _, err := io.ReadFull(c, mode[:]); err != nil {
							return
						}
						switch mode[0] {
						case 'c': // 控制流：一直开着，偶尔回一个字节
							buf := make([]byte, 1)
							for {
								if _, err := c.Read(buf); err != nil {
									return
								}
								_, _ = c.Write(buf)
							}
						case 'u':
							_, _ = io.Copy(io.Discard, c)
						case 'd':
							chunk := make([]byte, 64<<10)
							for i := 0; i < (512<<20)/len(chunk); i++ {
								if _, err := c.Write(chunk); err != nil {
									return
								}
							}
						}
					}()
				}
			}()
			user := core.User{ID: 6200, UUID: "anytls-bulk-secret"}
			c, port, _ := startLifecycleCore(t, lifecycleProto{name: "anytls", raw: map[string]any{}}, []core.User{user})
			_ = c
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			dialOut := func(ctx context.Context) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
			}
			client, err := anytls.NewClient(ctx, anytls.ClientConfig{Password: user.UUID, DialOut: util.DialOutFunc(dialOut), Logger: logger.NOP()})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			target := M.SocksaddrFromNet(sink.Addr())
			control, err := client.CreateProxy(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			defer control.Close()
			if _, err := control.Write([]byte{'c', 'x'}); err != nil {
				t.Fatal(err)
			}
			one := make([]byte, 1)
			if _, err := io.ReadFull(control, one); err != nil {
				t.Fatal(err)
			}
			data, err := client.CreateProxy(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if dir == "up" {
				_, _ = data.Write([]byte{'u'})
				// AnyTLS 帧长是 16 位：单次写超过 65535 字节，sing-anytls 客户端不拆帧，
				// 长度溢出成乱帧（客户端缺陷，不是服务端的事）。按常见代理的 32KB 写。
				chunk := make([]byte, 32<<10)
				for i := 0; i < (512<<20)/len(chunk); i++ {
					if _, err := data.Write(chunk); err != nil {
						t.Fatalf("上行写到 %d 块断开：%v", i, err)
					}
				}
			} else {
				_, _ = data.Write([]byte{'d'})
				n, err := io.CopyN(io.Discard, data, 512<<20)
				if err != nil {
					t.Fatalf("下行收到 %d 字节断开：%v", n, err)
				}
			}
			t.Logf("%s 512MB 用时 %s", dir, time.Since(start))
			_ = control.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := control.Write([]byte{'y'}); err != nil {
				t.Fatalf("大流量之后控制流写失败：%v", err)
			}
			if _, err := io.ReadFull(control, one); err != nil {
				t.Fatalf("大流量之后控制流读失败：%v", err)
			}
			_ = data.Close()
		})
	}
}
