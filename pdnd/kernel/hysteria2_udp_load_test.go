package kernel

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
	"github.com/aegispanel/nodeagent/route"
	hy2 "github.com/sagernet/sing-quic/hysteria2"
	M "github.com/sagernet/sing/common/metadata"
)

// 单条 QUIC 连接的 Hysteria2 UDP 转发压测：本机复现「约 100Mbps 起丢包」与
// 每 Gbps 的 CPU。默认跳过，设 PDND_HY2_LOAD=1 才跑：
//
//	PDND_HY2_LOAD=1 PDND_HY2_LOAD_MBPS=300 PDND_HY2_LOAD_DIR=up \
//	  go test -run '^TestHysteria2UDPSingleConnLoad$' -v ./kernel/
//
// 客户端（sing-quic 参考实现）与收发端放在子进程里，本进程只有 pdnd 适配器，
// getrusage 量到的就是服务端 CPU。up：客户端 → pdnd → 上游收包端；down：
// 上游发包端 → pdnd → 客户端。包长默认 1200 字节（与实测 iperf3 一致，会被
// Hysteria2 分成两片）。
func TestHysteria2UDPSingleConnLoad(t *testing.T) {
	if os.Getenv("PDND_HY2_LOAD_ROLE") == "client" {
		runHy2LoadClient(t)
		return
	}
	if os.Getenv("PDND_HY2_LOAD") == "" {
		t.Skip("设 PDND_HY2_LOAD=1 运行单连接 UDP 压测")
	}
	certPath, keyPath := testXHTTPServerCertFiles(t)
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "hysteria2", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"cert_path": certPath, "key_path": keyPath, "network": "udp",
	}}}
	adapter, err := newHysteria2Adapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: hy2LoadPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 1, UUID: "load-secret"}}); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run", "^TestHysteria2UDPSingleConnLoad$", "-test.v")
	cmd.Env = append(os.Environ(), "GOMAXPROCS=8", "PDND_HY2_LOAD_ROLE=client", "PDND_HY2_LOAD_SERVER=127.0.0.1:"+strconv.Itoa(port))
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var startUsage, endUsage syscall.Rusage
	var startMem, endMem runtime.MemStats
	var result hy2LoadResult
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "HY2LOAD START":
			_ = syscall.Getrusage(syscall.RUSAGE_SELF, &startUsage)
			runtime.ReadMemStats(&startMem)
			if path := os.Getenv("PDND_HY2_LOAD_PROFILE"); path != "" {
				if file, err := os.Create(path); err == nil {
					defer file.Close()
					_ = pprof.StartCPUProfile(file)
				}
			}
		case line == "HY2LOAD SENT":
			_ = syscall.Getrusage(syscall.RUSAGE_SELF, &endUsage)
			runtime.ReadMemStats(&endMem)
			if path := os.Getenv("PDND_HY2_LOAD_ALLOCS"); path != "" {
				if file, err := os.Create(path); err == nil {
					_ = pprof.Lookup("allocs").WriteTo(file, 0)
					_ = file.Close()
				}
			}
			pprof.StopCPUProfile()
		case strings.HasPrefix(line, "HY2LOAD RESULT "):
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "HY2LOAD RESULT ")), &result); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("客户端子进程失败：%v", err)
	}
	cpu := rusageSeconds(endUsage) - rusageSeconds(startUsage)
	loss := 0.0
	if result.Sent > 0 {
		loss = 100 * float64(result.Sent-result.Received) / float64(result.Sent)
	}
	deliveredGbit := float64(result.Received) * float64(result.Size) * 8 / 1e9
	cpuPerGbps := 0.0
	if deliveredGbit > 0 {
		cpuPerGbps = cpu / result.Seconds / (deliveredGbit / result.Seconds)
	}
	perPacket := func(v uint64) float64 {
		if result.Received == 0 {
			return 0
		}
		return float64(v) / float64(result.Received)
	}
	t.Logf("HY2 UDP %s 目标 %dMbps：发出 %d 包（%.0fMbps），收到 %d 包（%.0fMbps），丢包 %.2f%%；服务端 CPU %.2f 核·秒 / %.1f 秒 = %.2f 核，%.2f 核/Gbps；每包分配 %.1f 次 / %.0f 字节，GC %d 次",
		result.Direction, result.TargetMbps, result.Sent, float64(result.Sent)*float64(result.Size)*8/1e6/result.Seconds,
		result.Received, deliveredGbit*1e3/result.Seconds, loss, cpu, result.Seconds, cpu/result.Seconds, cpuPerGbps,
		perPacket(endMem.Mallocs-startMem.Mallocs), perPacket(endMem.TotalAlloc-startMem.TotalAlloc), endMem.NumGC-startMem.NumGC)
}

func rusageSeconds(u syscall.Rusage) float64 {
	return float64(u.Utime.Sec+u.Stime.Sec) + float64(u.Utime.Usec+u.Stime.Usec)/1e6
}

type hy2LoadResult struct {
	Direction  string  `json:"direction"`
	TargetMbps int     `json:"target_mbps"`
	Size       int     `json:"size"`
	Seconds    float64 `json:"seconds"`
	Sent       int64   `json:"sent"`
	Received   int64   `json:"received"`
}

// hy2LoadPlane 给每个 UDP 会话开真实的本机 UDP socket；外面再包一层，模拟生产
// 里的出站租约包装。PDND_HY2_LOAD_RAW=0 时包装不交出底层 socket（模拟租约
// 尚未实现 RawUDPConn），转发只能逐包收发。
type hy2LoadPlane struct{}

type hy2LoadPacketConn struct{ net.PacketConn }

type hy2LoadRawPacketConn struct{ net.PacketConn }

func (c hy2LoadRawPacketConn) RawUDPConn() *net.UDPConn {
	conn, _ := c.PacketConn.(*net.UDPConn)
	return conn
}

func (hy2LoadPlane) DialTCP(context.Context, route.Meta, M.Socksaddr) (net.Conn, error) {
	return nil, errors.New("load plane has no TCP")
}

func (hy2LoadPlane) ListenUDP(context.Context, route.Meta, M.Socksaddr) (net.PacketConn, error) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if os.Getenv("PDND_HY2_LOAD_RAW") == "0" {
		return hy2LoadPacketConn{conn}, nil
	}
	return hy2LoadRawPacketConn{conn}, nil
}

func hy2LoadEnvInt(name string, fallback int) int {
	if value, err := strconv.Atoi(os.Getenv(name)); err == nil && value > 0 {
		return value
	}
	return fallback
}

func runHy2LoadClient(t *testing.T) {
	mbps := hy2LoadEnvInt("PDND_HY2_LOAD_MBPS", 300)
	seconds := hy2LoadEnvInt("PDND_HY2_LOAD_SECONDS", 10)
	size := hy2LoadEnvInt("PDND_HY2_LOAD_SIZE", 1200)
	direction := os.Getenv("PDND_HY2_LOAD_DIR")
	if direction != "down" {
		direction = "up"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, err := hy2.NewClient(hy2.ClientOptions{
		Context: ctx, Dialer: outbound.NewDirect("load", outbound.StrategyPreferIPv4, nil),
		ServerAddress: M.ParseSocksaddr(os.Getenv("PDND_HY2_LOAD_SERVER")), Password: "load-secret",
		SendBPS: 2 * uint64(mbps) * 1e6 / 8, ReceiveBPS: 2 * uint64(mbps) * 1e6 / 8,
		TLSConfig: &hysteria2TLSConfig{std: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseWithError(nil)
	tunnel, err := client.ListenPacket(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	peer, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	_ = peer.(*net.UDPConn).SetReadBuffer(8 << 20)
	var received atomic.Int64
	// 收包端：up 时是上游收包端（peer），down 时是隧道客户端。
	sink := peer
	if direction == "down" {
		sink = tunnel
	}
	pps := float64(mbps) * 1e6 / 8 / float64(size)
	payload := make([]byte, size)
	duration := time.Duration(seconds) * time.Second
	var sent int64

	if direction == "down" {
		// 先经隧道发一个包，让发包端学到 pdnd 上游 socket 的地址。
		if _, err := tunnel.WriteTo([]byte("hello"), peer.LocalAddr()); err != nil {
			t.Fatal(err)
		}
		_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
		buffer := make([]byte, 2048)
		_, upstream, err := peer.ReadFrom(buffer)
		if err != nil {
			t.Fatal(err)
		}
		go countPackets(sink, &received)
		fmt.Println("HY2LOAD START")
		sent = pace(pps, duration, func() error { _, err := peer.WriteTo(payload, upstream); return err })
	} else {
		go countPackets(sink, &received)
		target := M.SocksaddrFromNet(peer.LocalAddr())
		fmt.Println("HY2LOAD START")
		sent = pace(pps, duration, func() error { _, err := tunnel.WriteTo(payload, target); return err })
	}
	fmt.Println("HY2LOAD SENT")
	time.Sleep(time.Second)
	out, _ := json.Marshal(hy2LoadResult{Direction: direction, TargetMbps: mbps, Size: size, Seconds: duration.Seconds(), Sent: sent, Received: received.Load()})
	fmt.Println("HY2LOAD RESULT " + string(out))
}

func countPackets(conn net.PacketConn, counter *atomic.Int64) {
	buffer := make([]byte, 64<<10)
	_ = conn.SetReadDeadline(time.Time{})
	for {
		if _, _, err := conn.ReadFrom(buffer); err != nil {
			return
		}
		counter.Add(1)
	}
}

// pace 按 pps 匀速发包 duration 时长，每毫秒补发欠下的包；返回实际发出的包数。
func pace(pps float64, duration time.Duration, send func() error) int64 {
	start := time.Now()
	var sent int64
	for {
		elapsed := time.Since(start)
		if elapsed >= duration {
			return sent
		}
		owed := int64(elapsed.Seconds()*pps) - sent
		for ; owed > 0; owed-- {
			if send() != nil {
				return sent
			}
			sent++
		}
		time.Sleep(200 * time.Microsecond)
	}
}
