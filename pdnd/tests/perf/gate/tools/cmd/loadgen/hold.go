package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/nodeagent/perfgate/internal/stats"
)

// staggerDelay 计算 hold 心跳的错开延迟。
func staggerDelay(heartbeat time.Duration, i int) time.Duration {
	if heartbeat <= 0 {
		return 0
	}
	return heartbeat * time.Duration(i%1000) / 1000
}

const holdOpenWorkers = 64

func runHold(args []string) int {
	fs := flag.NewFlagSet("hold", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var conns int
	var ramp int
	var duration time.Duration
	var heartbeat time.Duration
	var statusFile string
	var common commonFlags
	common.register(fs)
	fs.IntVar(&conns, "conns", 0, "目标长连接数")
	fs.IntVar(&ramp, "ramp", 1000, "每秒新开连接上限")
	fs.DurationVar(&duration, "duration", 600*time.Second, "保持时长")
	fs.DurationVar(&heartbeat, "heartbeat", 30*time.Second, "心跳间隔")
	fs.StringVar(&statusFile, "status-file", "", "状态 JSON 路径")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := common.load(); err != nil {
		fmt.Fprintf(os.Stderr, "hold: %v\n", err)
		return 2
	}
	if common.echo == "" {
		fmt.Fprintf(os.Stderr, "hold: --echo 必填\n")
		return 2
	}
	if common.node == "" {
		fmt.Fprintf(os.Stderr, "hold: --node 必填\n")
		return 2
	}
	if common.proto == "" || common.proto == "direct" {
		fmt.Fprintf(os.Stderr, "hold: --proto 必填\n")
		return 2
	}
	if conns < 1 {
		fmt.Fprintf(os.Stderr, "hold: --conns 必须 ≥ 1\n")
		return 2
	}
	if ramp < 1 {
		fmt.Fprintf(os.Stderr, "hold: --ramp 必须 ≥ 1\n")
		return 2
	}

	users := pickUsers(common.prof, common.clients)
	dialers := make([]Dialer, common.clients)
	for k := 0; k < common.clients; k++ {
		d, err := NewDialer(common.prof, common.proto, common.node, users[k])
		if err != nil {
			fmt.Fprintf(os.Stderr, "hold: 创建 Dialer: %v\n", err)
			return 1
		}
		dialers[k] = d
	}
	defer func() {
		for _, d := range dialers {
			_ = d.Close()
		}
	}()

	ctx, cancel := signalContext(nil, duration)
	defer cancel()

	var stopping atomic.Bool
	var opened atomic.Int32
	var openFail atomic.Int32
	var dropped atomic.Int32
	var heartbeatFail atomic.Int64
	var readyUnixMs atomic.Int64
	connectRec := stats.NewRecorder()
	rttRec := stats.NewRecorder()

	var dropsMu sync.Mutex
	dropsUnixMs := make([]int64, 0, 32)

	var statusMu sync.Mutex
	statusOpened := 0
	statusOpenFail := 0
	statusDropped := 0

	updateStatus := func() {
		if statusFile == "" {
			return
		}
		statusMu.Lock()
		op := statusOpened
		of := statusOpenFail
		dr := statusDropped
		statusMu.Unlock()
		ready := op+of >= conns
		if ready && readyUnixMs.Load() == 0 {
			readyUnixMs.Store(nowMs())
		}
		body, _ := json.Marshal(map[string]any{
			"opened":    op,
			"open_fail": of,
			"dropped":   dr,
			"ready":     ready,
		})
		tmp := statusFile + ".tmp"
		if err := os.WriteFile(tmp, body, 0644); err == nil {
			_ = os.Rename(tmp, statusFile)
		}
	}

	statusStop := make(chan struct{})
	if statusFile != "" {
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-statusStop:
					return
				case <-ticker.C:
					updateStatus()
				}
			}
		}()
	}

	recordDrop := func() {
		dropsMu.Lock()
		if len(dropsUnixMs) < 1000 {
			dropsUnixMs = append(dropsUnixMs, nowMs())
		}
		dropsMu.Unlock()
	}

	tasks := make(chan int, conns)
	for i := 0; i < conns; i++ {
		tasks <- i
	}
	close(tasks)

	rampInterval := time.Second / time.Duration(ramp)
	if rampInterval <= 0 {
		rampInterval = time.Nanosecond
	}
	rampTicker := time.NewTicker(rampInterval)
	defer rampTicker.Stop()

	var connWG sync.WaitGroup
	var liveConns []net.Conn
	var liveMu sync.Mutex

	openOne := func(idx int) {
		d := dialers[idx%len(dialers)]
		start := time.Now()
		dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
		conn, err := d.DialTCP(dialCtx, common.echo)
		dialCancel()
		if err != nil {
			openFail.Add(1)
			statusMu.Lock()
			statusOpenFail++
			statusMu.Unlock()
			updateStatus()
			return
		}
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			conn.Close()
			openFail.Add(1)
			statusMu.Lock()
			statusOpenFail++
			statusMu.Unlock()
			updateStatus()
			return
		}
		if err := roundTrip(conn, buf, 10*time.Second); err != nil {
			conn.Close()
			openFail.Add(1)
			statusMu.Lock()
			statusOpenFail++
			statusMu.Unlock()
			updateStatus()
			return
		}
		connectRec.Add(float64(time.Since(start).Microseconds()) / 1000.0)
		opened.Add(1)
		statusMu.Lock()
		statusOpened++
		statusMu.Unlock()
		updateStatus()

		liveMu.Lock()
		liveConns = append(liveConns, conn)
		liveMu.Unlock()

		connWG.Add(1)
		go holdConnLoop(ctx, conn, idx, heartbeat, &stopping, rttRec, &heartbeatFail, &dropped, &statusDropped, &statusMu, recordDrop, connWG.Done)
	}

	var openWG sync.WaitGroup
	for w := 0; w < holdOpenWorkers; w++ {
		openWG.Add(1)
		go func() {
			defer openWG.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case idx, ok := <-tasks:
					if !ok {
						return
					}
					select {
					case <-ctx.Done():
						return
					case <-rampTicker.C:
					}
					openOne(idx)
				}
			}
		}()
	}
	openWG.Wait()

	// 等到时长或信号
	<-ctx.Done()
	stopping.Store(true)
	close(statusStop)
	updateStatus()

	liveMu.Lock()
	for _, c := range liveConns {
		_ = c.Close()
	}
	liveMu.Unlock()

	done := make(chan struct{})
	go func() {
		connWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}

	dropsMu.Lock()
	dropsCopy := append([]int64(nil), dropsUnixMs...)
	dropsMu.Unlock()

	emit(map[string]any{
		"cmd":            "hold",
		"name":           common.name,
		"proto":          common.proto,
		"conns":          conns,
		"opened":         int(opened.Load()),
		"open_fail":      int(openFail.Load()),
		"dropped":        int(dropped.Load()),
		"drops_unix_ms":  dropsCopy,
		"connect_ms":     connectRec.Summary(),
		"rtt_ms":         rttRec.Summary(),
		"heartbeat_fail": heartbeatFail.Load(),
		"ready_unix_ms":  readyUnixMs.Load(),
		"end_unix_ms":    nowMs(),
	})
	return 0
}

func holdConnLoop(
	ctx context.Context,
	conn net.Conn,
	idx int,
	heartbeat time.Duration,
	stopping *atomic.Bool,
	rttRec *stats.Recorder,
	heartbeatFail *atomic.Int64,
	dropped *atomic.Int32,
	statusDropped *int,
	statusMu *sync.Mutex,
	recordDrop func(),
	done func(),
) {
	defer done()
	defer conn.Close()

	delay := staggerDelay(heartbeat, idx)
	if delay > 0 {
		if !sleepCtx(ctx, delay) {
			return
		}
	}

	var markedDrop bool
	markDrop := func() {
		if markedDrop || stopping.Load() {
			return
		}
		markedDrop = true
		dropped.Add(1)
		statusMu.Lock()
		*statusDropped++
		statusMu.Unlock()
		recordDrop()
	}

	// 后台一直读：对端（节点重启、删人、断流）关掉连接时立刻发现并记下断开时刻，
	// 不等下一次心跳（否则断开时刻最多晚一个心跳间隔，failover 的时间窗统计会错位）。
	echoCh := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		for {
			got := make([]byte, 32)
			if _, err := io.ReadFull(conn, got); err != nil {
				errCh <- err
				return
			}
			select {
			case echoCh <- got:
			case <-ctx.Done():
				return
			}
		}
	}()

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-errCh:
			markDrop()
			return
		case <-ticker.C:
			buf := make([]byte, 32)
			if _, err := rand.Read(buf); err != nil {
				return
			}
			start := time.Now()
			if _, err := conn.Write(buf); err != nil {
				heartbeatFail.Add(1)
				markDrop()
				return
			}
			timer := time.NewTimer(10 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-errCh:
				timer.Stop()
				heartbeatFail.Add(1)
				markDrop()
				return
			case got := <-echoCh:
				timer.Stop()
				if !bytes.Equal(got, buf) {
					heartbeatFail.Add(1)
					markDrop()
					return
				}
				rttRec.Add(float64(time.Since(start).Microseconds()) / 1000.0)
			case <-timer.C:
				// 心跳超时 10 秒算断开
				heartbeatFail.Add(1)
				markDrop()
				return
			}
		}
	}
}
