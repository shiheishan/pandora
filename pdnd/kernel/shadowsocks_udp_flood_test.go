package kernel

// 经典 Shadowsocks UDP 慢路径保护：守的是「未认证包的逐用户试解不在读循环里」——
// 改前慢路径在读循环里，201 个用户、每秒数万个垃圾包就把读循环打满，已建会话的包
// 在内核缓冲里被丢；时限放宽到 3 秒是为 -race。

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

func TestShadowsocksUDPGarbageFloodKeepsEstablishedSessions(t *testing.T) {
	user := core.User{ID: 8831, UUID: "ss-udp-flood-owner"}
	a := startSSUDPAdapter(t, &udpRelayTestPlane{}, user)

	fillerUsers := make([]core.User, 200)
	for i := 0; i < 200; i++ {
		fillerUsers[i] = core.User{
			ID:   int64(90000 + i),
			UUID: fmt.Sprintf("ss-udp-flood-filler-%d", i),
		}
	}
	if err := a.AddUsers(fillerUsers); err != nil {
		t.Fatal(err)
	}

	echo := hy2EchoUDP(t)
	pc := lifecycleSSUDPClient(t, a, user.UUID)

	if err := lifecycleSSUDPRoundTrip(pc, echo, "flood-warmup", 3*time.Second); err != nil {
		t.Fatalf("warmup 失败：%v", err)
	}

	stop := make(chan struct{})
	var floodWG sync.WaitGroup
	serverAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: a.spec.Config.Port}
	var floodConns []*net.UDPConn

	for i := 0; i < 2; i++ {
		conn, err := net.DialUDP("udp4", nil, serverAddr)
		if err != nil {
			t.Fatal(err)
		}
		floodConns = append(floodConns, conn)
		floodWG.Add(1)
		go func(c *net.UDPConn) {
			defer floodWG.Done()
			garbage := make([]byte, 1200)
			_, _ = rand.Read(garbage)
			var counter uint64
			for {
				select {
				case <-stop:
					return
				default:
				}
				for j := 0; j < 32; j++ {
					counter++
					binary.LittleEndian.PutUint64(garbage[:8], counter)
					if _, err := c.Write(garbage); err != nil {
						return
					}
				}
				time.Sleep(time.Millisecond)
			}
		}(conn)
	}

	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() {
			close(stop)
			floodWG.Wait()
			for _, c := range floodConns {
				_ = c.Close()
			}
		})
	})

	time.Sleep(200 * time.Millisecond)

	for i := 0; i < 10; i++ {
		if err := lifecycleSSUDPRoundTrip(pc, echo, fmt.Sprintf("flood-%d", i), 3*time.Second); err != nil {
			t.Fatalf("灌包期间已建会话往返失败 (第 %d 次): %v", i, err)
		}
	}

	t.Logf("慢路径队列丢包数 a.udpDrops.queue: %d", a.udpDrops.queue.Load())
}
