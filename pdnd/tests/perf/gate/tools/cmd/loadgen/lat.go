package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/aegispanel/nodeagent/perfgate/internal/stats"
)

func runLat(args []string) int {
	fs := flag.NewFlagSet("lat", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var count int
	var interval time.Duration
	var common commonFlags
	common.register(fs)
	fs.IntVar(&count, "count", 2000, "往返次数")
	fs.DurationVar(&interval, "interval", 5*time.Millisecond, "两次往返间隔")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := common.load(); err != nil {
		fmt.Fprintf(os.Stderr, "lat: %v\n", err)
		return 2
	}
	if common.echo == "" {
		fmt.Fprintf(os.Stderr, "lat: --echo 必填\n")
		return 2
	}
	if common.proto == "" {
		fmt.Fprintf(os.Stderr, "lat: --proto 必填\n")
		return 2
	}
	if common.proto != "direct" && common.node == "" {
		fmt.Fprintf(os.Stderr, "lat: --node 必填\n")
		return 2
	}
	if count < 1 {
		fmt.Fprintf(os.Stderr, "lat: --count 必须 ≥ 1\n")
		return 2
	}

	ctx, cancel := signalContext(nil, 0)
	defer cancel()

	var d Dialer
	if common.proto == "direct" {
		d = NewDirectDialer()
	} else {
		users := pickUsers(common.prof, 1)
		nd, err := NewDialer(common.prof, common.proto, common.node, users[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "lat: 创建 Dialer: %v\n", err)
			return 1
		}
		d = nd
	}
	defer func() { _ = d.Close() }()

	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := d.DialTCP(dialCtx, common.echo)
	dialCancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lat: 拨号: %v\n", err)
		return 1
	}
	defer conn.Close()

	rttRec := stats.NewRecorder()
	var fail int64
	t0Ms := nowMs()

	for i := 0; i < count; i++ {
		if ctx.Err() != nil {
			break
		}
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			fail++
			break
		}
		start := time.Now()
		if err := roundTrip(conn, buf, 5*time.Second); err != nil {
			fail++
			_ = conn.Close()
			dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
			conn, err = d.DialTCP(dialCtx, common.echo)
			dialCancel()
			if err != nil {
				break
			}
			if interval > 0 {
				if !sleepCtx(ctx, interval) {
					break
				}
			}
			continue
		}
		rttRec.Add(float64(time.Since(start).Microseconds()) / 1000.0)
		if i+1 < count && interval > 0 {
			if !sleepCtx(ctx, interval) {
				break
			}
		}
	}
	t1Ms := nowMs()

	emit(map[string]any{
		"cmd":        "lat",
		"name":       common.name,
		"proto":      common.proto,
		"rtt_ms":     rttRec.Summary(),
		"fail":       fail,
		"t0_unix_ms": t0Ms,
		"t1_unix_ms": t1Ms,
	})
	return 0
}
