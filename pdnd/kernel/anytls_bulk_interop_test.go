//go:build interop

package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
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
				// 按常见代理的 32KB 写；单次 128KB 的写见 TestAnyTLSClientLargeSingleWrites。
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

// AnyTLS 帧长是 16 位：单次写超过 65535 字节必须拆成多帧。sing-anytls v0.0.11
// 的客户端与我方服务端 fork 都不拆（长度截断成乱帧），v0.0.13 起拆。这里用单次
// 128KB 的写把上下行各走一遍，并逐字节核对内容（摘要比对）。
func TestAnyTLSClientLargeSingleWrites(t *testing.T) {
	runAnyTLSLargeSingleWrites(t, false)
}

// largeWriteSize 是单次写的大小，largeWriteTotal 是每个方向的总量。
const (
	largeWriteSize  = 128 << 10
	largeWriteTotal = 8 << 20
)

func largeWritePattern() []byte {
	chunk := make([]byte, largeWriteSize)
	for i := range chunk {
		chunk[i] = byte(i*31 + i>>8)
	}
	return chunk
}

// runAnyTLSLargeSingleWrites 起一个摘要回显端：'U'+长度 读满后回 SHA-256；
// 'D'+长度 按单次 128KB 写出图样。useTLS 时外层套 TLS。
func runAnyTLSLargeSingleWrites(t *testing.T, useTLS bool) {
	chunk := largeWritePattern()
	want := sha256.New()
	for i := 0; i < largeWriteTotal/largeWriteSize; i++ {
		want.Write(chunk)
	}
	wantSum := want.Sum(nil)

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
				var head [9]byte
				if _, err := io.ReadFull(c, head[:]); err != nil {
					return
				}
				total := int64(binary.BigEndian.Uint64(head[1:]))
				switch head[0] {
				case 'U':
					h := sha256.New()
					if _, err := io.CopyN(h, c, total); err != nil {
						return
					}
					_, _ = c.Write(h.Sum(nil))
					// 等客户端读完摘要再关。
					_, _ = c.Read(head[:1])
				case 'D':
					for sent := int64(0); sent < total; sent += largeWriteSize {
						if _, err := c.Write(chunk); err != nil {
							return
						}
					}
					_, _ = c.Read(head[:1])
				}
			}()
		}
	}()

	user := core.User{ID: 6201, UUID: "anytls-large-secret"}
	raw := map[string]any{}
	if useTLS {
		certPath, keyPath := testXHTTPServerCertFiles(t)
		raw = map[string]any{"tls": true, "cert_path": certPath, "key_path": keyPath}
	}
	_, port, _ := startLifecycleCore(t, lifecycleProto{name: "anytls", raw: raw}, []core.User{user})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dialOut := func(ctx context.Context) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil || !useTLS {
			return conn, err
		}
		tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec -- 测试证书。
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return tlsConn, nil
	}
	client, err := anytls.NewClient(ctx, anytls.ClientConfig{Password: user.UUID, DialOut: util.DialOutFunc(dialOut), Logger: logger.NOP()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	target := M.SocksaddrFromNet(sink.Addr())
	for _, dir := range []string{"up", "down"} {
		t.Run(dir, func(t *testing.T) {
			stream, err := client.CreateProxy(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			_ = stream.SetDeadline(time.Now().Add(30 * time.Second))
			var head [9]byte
			binary.BigEndian.PutUint64(head[1:], largeWriteTotal)
			if dir == "up" {
				head[0] = 'U'
				if _, err := stream.Write(head[:]); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < largeWriteTotal/largeWriteSize; i++ {
					// 单次 Write 就是 128KB，客户端必须拆成两帧以上。
					if n, err := stream.Write(chunk); err != nil || n != len(chunk) {
						t.Fatalf("第 %d 次 128KB 写 n=%d err=%v", i, n, err)
					}
				}
				got := make([]byte, sha256.Size)
				if _, err := io.ReadFull(stream, got); err != nil {
					t.Fatalf("读上行摘要：%v", err)
				}
				if !bytes.Equal(got, wantSum) {
					t.Fatal("上行内容与发出的不一致")
				}
				return
			}
			head[0] = 'D'
			if _, err := stream.Write(head[:]); err != nil {
				t.Fatal(err)
			}
			h := sha256.New()
			buffer := make([]byte, largeWriteSize)
			var got int64
			for got < largeWriteTotal {
				n, err := io.ReadFull(stream, buffer[:min(int64(len(buffer)), largeWriteTotal-got)])
				h.Write(buffer[:n])
				got += int64(n)
				if err != nil {
					t.Fatalf("下行收到 %d 字节断开：%v", got, err)
				}
			}
			if !bytes.Equal(h.Sum(nil), wantSum) {
				t.Fatal("下行内容与发出的不一致")
			}
		})
	}
}
