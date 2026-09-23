package kernel

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	M "github.com/sagernet/sing/common/metadata"
)

// 端到端验证限速真的作用到了一条 Trojan 连接上。
//
// 只测 speedLimitedConn 是不够的：限速这条链路上有三处独立的失败可能——
// 面板下发的值在 adapter 存储时被丢掉（这里原先就是这样）、认出用户后
// 忘了套桶、或者套的是每连接一个桶。走一遍真实协议才盖得住。
func TestTrojanAdapterEnforcesSpeedLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("限速测试需要真实等待")
	}
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			conn, acceptErr := upstream.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	port := reserveTCPPort(t)
	adapter := &trojanAdapter{
		users: make(map[string]trojanUser), traffic: make(map[int64]core.UserTraffic),
		online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{}),
	}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "trojan", Listen: "127.0.0.1", Port: port}}
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{
		DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()},
	}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	// 128 kbps = 16000 字节/秒。
	const kbps = 128
	if err := adapter.AddUsers([]core.User{{ID: 77, UUID: "speed-secret", SpeedLimit: kbps}}); err != nil {
		t.Fatal(err)
	}

	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))

	target := upstream.Addr().(*net.TCPAddr)
	header := []byte(trojanPasswordProof("speed-secret") + "\r\n")
	header = append(header, 1, 1)
	header = append(header, target.IP.To4()...)
	header = append(header, byte(target.Port>>8), byte(target.Port), '\r', '\n')
	if _, err := client.Write(header); err != nil {
		t.Fatal(err)
	}

	// 先把桶里的初始令牌耗掉，再计时量稳态速率——否则测到的是 burst。
	drain := make([]byte, core.SpeedLimitBurst(kbps))
	if _, err := client.Write(drain); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(client, make([]byte, len(drain))); err != nil {
		t.Fatal(err)
	}

	const sample = 16000 // 一秒的量
	start := time.Now()
	go func() { _, _ = client.Write(make([]byte, sample)) }()
	if _, err := io.ReadFull(client, make([]byte, sample)); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	// 回声要一上一下各过一次桶，理论上约 2 秒；下界取 1 秒，留足调度余量。
	if elapsed < time.Second {
		t.Fatalf("限速没作用到 Trojan 连接上：%d 字节往返只用了 %v", sample, elapsed)
	}
}
