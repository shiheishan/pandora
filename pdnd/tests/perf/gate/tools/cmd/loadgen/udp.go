package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/nodeagent/perfgate/internal/profile"
	"github.com/aegispanel/nodeagent/perfgate/internal/stats"
)

// encodeUDPHeader 写入 8 字节大端序号与 8 字节发送时刻纳秒。
func encodeUDPHeader(b []byte, seq uint64, sentNs int64) {
	binary.BigEndian.PutUint64(b[0:8], seq)
	binary.BigEndian.PutUint64(b[8:16], uint64(sentNs))
}

// decodeUDPHeader 解析 UDP 包头；长度不足 16 时 ok=false。
func decodeUDPHeader(b []byte) (seq uint64, sentNs int64, ok bool) {
	if len(b) < 16 {
		return 0, 0, false
	}
	seq = binary.BigEndian.Uint64(b[0:8])
	sentNs = int64(binary.BigEndian.Uint64(b[8:16]))
	return seq, sentNs, true
}

func runUDP(args []string) int {
	fs := flag.NewFlagSet("udp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var rateMbps float64
	var duration time.Duration
	var size int
	var sessions int
	var common commonFlags
	common.register(fs)
	fs.Float64Var(&rateMbps, "rate-mbps", 100, "总发送速率 Mbps")
	fs.DurationVar(&duration, "duration", 20*time.Second, "发送时长")
	fs.IntVar(&size, "size", 1200, "每包字节数")
	fs.IntVar(&sessions, "sessions", 1, "UDP 会话数")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := common.load(); err != nil {
		fmt.Fprintf(os.Stderr, "udp: %v\n", err)
		return 2
	}
	if common.udpEcho == "" {
		fmt.Fprintf(os.Stderr, "udp: --udp-echo 必填\n")
		return 2
	}
	if common.node == "" {
		fmt.Fprintf(os.Stderr, "udp: --node 必填\n")
		return 2
	}
	if common.proto != profile.Hy2 && common.proto != profile.TUIC {
		fmt.Fprintf(os.Stderr, "udp: --proto 必须是 hy2 或 tuic\n")
		return 2
	}
	if sessions < 1 || size < 16 {
		fmt.Fprintf(os.Stderr, "udp: --sessions≥1 且 --size≥16\n")
		return 2
	}
	if rateMbps < 0 {
		return 2
	}

	users := pickUsers(common.prof, common.clients)
	dialers := make([]Dialer, common.clients)
	for k := 0; k < common.clients; k++ {
		d, err := NewDialer(common.prof, common.proto, common.node, users[k])
		if err != nil {
			fmt.Fprintf(os.Stderr, "udp: 创建 Dialer: %v\n", err)
			return 1
		}
		dialers[k] = d
	}
	defer func() {
		for _, d := range dialers {
			_ = d.Close()
		}
	}()

	dest, err := net.ResolveUDPAddr("udp", common.udpEcho)
	if err != nil {
		fmt.Fprintf(os.Stderr, "udp: 解析 udp-echo: %v\n", err)
		return 2
	}

	ctx, cancel := signalContext(nil, duration+10*time.Second)
	defer cancel()

	var totalSent atomic.Int64
	var totalRecv atomic.Int64
	rttRec := stats.NewRecorder()
	var t0Ms atomic.Int64
	var sendDoneAt atomic.Int64

	ppsPerSession := 0.0
	if rateMbps > 0 && sessions > 0 {
		// 总比特率 / sessions / 每包比特
		ppsPerSession = (rateMbps * 1e6 / 8.0) / float64(sessions) / float64(size)
	}

	var wg sync.WaitGroup
	for s := 0; s < sessions; s++ {
		pd, ok := dialers[s%len(dialers)].(PacketDialer)
		if !ok {
			fmt.Fprintf(os.Stderr, "udp: 协议不支持 PacketDialer\n")
			return 2
		}
		listenCtx, listenCancel := context.WithTimeout(ctx, 15*time.Second)
		pc, err := pd.ListenUDP(listenCtx)
		listenCancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "udp: ListenUDP: %v\n", err)
			return 1
		}

		wg.Add(2)
		go func(pc net.PacketConn) {
			defer wg.Done()
			sendUDP(ctx, pc, dest, size, duration, ppsPerSession, &totalSent, &t0Ms, &sendDoneAt)
			// 发完再等 2 秒收尾，然后关掉 pc，让收方的 ReadFrom 返回（不依赖读期限：
			// sing-quic 的 UDP 连接不保证支持 SetReadDeadline）
			sleepCtx(ctx, 2*time.Second)
			_ = pc.Close()
		}(pc)
		go func(pc net.PacketConn) {
			defer wg.Done()
			recvUDP(pc, &totalRecv, rttRec)
		}(pc)
	}
	wg.Wait()

	sent := totalSent.Load()
	recv := totalRecv.Load()
	t0 := t0Ms.Load()
	t1 := nowMs()
	secs := 0.0
	if t0 > 0 {
		secs = float64(sendDoneAt.Load()-t0) / 1000.0
	}
	if secs <= 0 && duration > 0 {
		secs = duration.Seconds()
	}
	loss := 0.0
	if sent > 0 {
		loss = float64(sent-recv) / float64(sent) * 100.0
	}
	mbps := 0.0
	if secs > 0 {
		mbps = float64(recv) * float64(size) * 8.0 / secs / 1e6
	}

	emit(map[string]any{
		"cmd":              "udp",
		"name":             common.name,
		"proto":            common.proto,
		"rate_mbps_target": rateMbps,
		"size":             size,
		"sent":             sent,
		"recv":             recv,
		"loss_pct":         loss,
		"mbps":             mbps,
		"secs":             secs,
		"rtt_ms":           rttRec.Summary(),
		"t0_unix_ms":       t0,
		"t1_unix_ms":       t1,
	})
	return 0
}

func sendUDP(
	ctx context.Context,
	pc net.PacketConn,
	dest *net.UDPAddr,
	size int,
	duration time.Duration,
	pps float64,
	totalSent *atomic.Int64,
	t0Ms *atomic.Int64,
	sendDoneAt *atomic.Int64,
) {
	buf := make([]byte, size)
	deadline := time.Now().Add(duration)
	if t0Ms.Load() == 0 {
		t0Ms.Store(nowMs())
	}
	var seq uint64
	var frac float64
	nextTick := time.Now()

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		allowed := 0
		if pps <= 0 {
			allowed = 1
		} else {
			now := time.Now()
			for !now.Before(nextTick) && now.Before(deadline) {
				frac += pps / 1000.0
				nextTick = nextTick.Add(time.Millisecond)
				now = time.Now()
			}
			allowed = int(frac)
			if allowed > 0 {
				frac -= float64(allowed)
			}
		}
		for i := 0; i < allowed; i++ {
			if time.Now().After(deadline) {
				break
			}
			sentNs := time.Now().UnixNano()
			encodeUDPHeader(buf, seq, sentNs)
			seq++
			if _, err := pc.WriteTo(buf, dest); err != nil {
				return
			}
			totalSent.Add(1)
		}
		if pps > 0 && allowed == 0 {
			sleep := nextTick.Sub(time.Now())
			if sleep > 0 {
				if !sleepCtx(ctx, sleep) {
					break
				}
			}
		}
	}
	sendDoneAt.Store(nowMs())
}

// recvUDP 一直收回显，直到 pc 被发送方关掉。
func recvUDP(pc net.PacketConn, totalRecv *atomic.Int64, rttRec *stats.Recorder) {
	buf := make([]byte, 2048)
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		_, sentNs, ok := decodeUDPHeader(buf[:n])
		if !ok {
			continue
		}
		totalRecv.Add(1)
		rttMs := float64(time.Now().UnixNano()-sentNs) / 1e6
		if rttMs >= 0 {
			rttRec.Add(rttMs)
		}
	}
}
