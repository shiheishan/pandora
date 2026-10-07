package kernel

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
	"github.com/gofrs/uuid/v5"
	tuic "github.com/sagernet/sing-quic/tuic"
	M "github.com/sagernet/sing/common/metadata"
)

// 单条 QUIC 连接的 TUIC UDP 转发压测，与 TestHysteria2UDPSingleConnLoad 同一套
// 口径（同一个上游 socket 夹具、匀速发包、getrusage 量服务端 CPU、MemStats 量
// 每包分配），好让两种协议的逐包开销直接对比。默认跳过，设 PDND_TUIC_LOAD=1：
//
//	PDND_TUIC_LOAD=1 PDND_HY2_LOAD_MBPS=200 PDND_HY2_LOAD_DIR=up \
//	  go test -run '^TestTUICUDPSingleConnLoad$' -v ./kernel/
//
// 速率、时长、包长、方向沿用 PDND_HY2_LOAD_* 那组环境变量。
func TestTUICUDPSingleConnLoad(t *testing.T) {
	if os.Getenv("PDND_TUIC_LOAD_ROLE") == "client" {
		runTUICLoadClient(t)
		return
	}
	if os.Getenv("PDND_TUIC_LOAD") == "" {
		t.Skip("设 PDND_TUIC_LOAD=1 运行单连接 UDP 压测")
	}
	certPath, keyPath := testXHTTPServerCertFiles(t)
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "tuic", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"cert_path": certPath, "key_path": keyPath, "network": "udp",
	}}}
	adapter, err := newTUICAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: hy2LoadPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 1, UUID: tuicLoadUUID}}); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run", "^TestTUICUDPSingleConnLoad$", "-test.v")
	cmd.Env = append(os.Environ(), "GOMAXPROCS=8", "PDND_TUIC_LOAD_ROLE=client", "PDND_TUIC_LOAD_SERVER=127.0.0.1:"+strconv.Itoa(port))
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
		case line == "HY2LOAD SENT":
			_ = syscall.Getrusage(syscall.RUSAGE_SELF, &endUsage)
			runtime.ReadMemStats(&endMem)
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
	perPacket := func(v float64) float64 {
		if result.Received == 0 {
			return 0
		}
		return v / float64(result.Received)
	}
	deliveredGbit := float64(result.Received) * float64(result.Size) * 8 / 1e9
	cpuPerGbps := 0.0
	if deliveredGbit > 0 {
		cpuPerGbps = cpu / deliveredGbit
	}
	t.Logf("TUIC UDP %s 目标 %dMbps：发出 %d 包，收到 %d 包（%.0fMbps），丢包 %.2f%%；服务端 CPU %.2f 核·秒，%.2f 核/Gbps，%.0f ns/包；每包分配 %.1f 次 / %.0f 字节，GC %d 次",
		result.Direction, result.TargetMbps, result.Sent, result.Received, deliveredGbit*1e3/result.Seconds, loss,
		cpu, cpuPerGbps, perPacket(cpu*1e9),
		perPacket(float64(endMem.Mallocs-startMem.Mallocs)), perPacket(float64(endMem.TotalAlloc-startMem.TotalAlloc)), endMem.NumGC-startMem.NumGC)
}

const tuicLoadUUID = "5d6f0a52-6ae1-4e8e-91b3-1f269c8f5c7b"

func runTUICLoadClient(t *testing.T) {
	mbps := hy2LoadEnvInt("PDND_HY2_LOAD_MBPS", 200)
	seconds := hy2LoadEnvInt("PDND_HY2_LOAD_SECONDS", 10)
	size := hy2LoadEnvInt("PDND_HY2_LOAD_SIZE", 1200)
	direction := os.Getenv("PDND_HY2_LOAD_DIR")
	if direction != "down" {
		direction = "up"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	parsed := uuid.Must(uuid.FromString(tuicLoadUUID))
	client, err := tuic.NewClient(tuic.ClientOptions{
		Context: ctx, Dialer: outbound.NewDirect("load", outbound.StrategyPreferIPv4, nil),
		ServerAddress: M.ParseSocksaddr(os.Getenv("PDND_TUIC_LOAD_SERVER")), UUID: [16]byte(parsed), Password: tuicLoadUUID,
		CongestionControl: "bbr",
		TLSConfig:         &hysteria2TLSConfig{std: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost", NextProtos: []string{"h3"}}},
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
	sink := peer
	if direction == "down" {
		sink = tunnel
	}
	pps := float64(mbps) * 1e6 / 8 / float64(size)
	payload := make([]byte, size)
	duration := time.Duration(seconds) * time.Second
	var sent int64
	if direction == "down" {
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
