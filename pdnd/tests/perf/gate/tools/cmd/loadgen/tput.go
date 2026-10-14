package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	tputWriteChunk = 32 << 10
	tputBucketCap  = 64 << 10
	tputTick       = time.Millisecond
)

var tputFill = make([]byte, tputWriteChunk)

// pacedSend 在时长 d 内向 w 写入数据；bps 为每秒比特，0 表示不限速。
func pacedSend(ctx context.Context, w io.Writer, bps uint64, d time.Duration) (int64, error) {
	deadline := time.Now().Add(d)
	var total int64

	if bps == 0 {
		for time.Now().Before(deadline) {
			if ctx.Err() != nil {
				return total, ctx.Err()
			}
			n, err := w.Write(tputFill)
			if n > 0 {
				total += int64(n)
			}
			if err != nil {
				return total, err
			}
		}
		return total, nil
	}

	bytesPerTick := float64(bps) / 8.0 / 1000.0
	bucketCap := float64(tputBucketCap)
	if 4*bytesPerTick > bucketCap {
		bucketCap = 4 * bytesPerTick
	}
	var bucket float64
	nextTick := time.Now()

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		now := time.Now()
		for !now.Before(nextTick) && now.Before(deadline) {
			bucket += bytesPerTick
			if bucket > bucketCap {
				bucket = bucketCap
			}
			nextTick = nextTick.Add(tputTick)
			now = time.Now()
		}
		if bucket >= float64(tputWriteChunk) {
			n, err := w.Write(tputFill)
			if n > 0 {
				total += int64(n)
				bucket -= float64(n)
			}
			if err != nil {
				return total, err
			}
		} else {
			sleep := nextTick.Sub(time.Now())
			if sleep > 0 {
				t := time.NewTimer(sleep)
				select {
				case <-ctx.Done():
					t.Stop()
					return total, ctx.Err()
				case <-t.C:
				}
			}
		}
	}
	return total, nil
}

func runTput(args []string) int {
	fs := flag.NewFlagSet("tput", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var dir string
	var rateMbps float64
	var streams int
	var duration time.Duration
	var common commonFlags
	common.register(fs)
	fs.StringVar(&dir, "dir", "down", "down 或 up")
	fs.Float64Var(&rateMbps, "rate-mbps", 300, "总速率 Mbps，0=满速")
	fs.IntVar(&streams, "streams", 4, "并行流数")
	fs.DurationVar(&duration, "duration", 30*time.Second, "每流时长")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := common.load(); err != nil {
		fmt.Fprintf(os.Stderr, "tput: %v\n", err)
		return 2
	}
	if common.sink == "" {
		fmt.Fprintf(os.Stderr, "tput: --sink 必填\n")
		return 2
	}
	if common.node == "" {
		fmt.Fprintf(os.Stderr, "tput: --node 必填\n")
		return 2
	}
	if common.proto == "" || common.proto == "direct" {
		fmt.Fprintf(os.Stderr, "tput: --proto 必填\n")
		return 2
	}
	if dir != "down" && dir != "up" {
		fmt.Fprintf(os.Stderr, "tput: --dir 必须是 down 或 up\n")
		return 2
	}
	if streams < 1 {
		fmt.Fprintf(os.Stderr, "tput: --streams 必须 ≥ 1\n")
		return 2
	}
	if rateMbps < 0 {
		fmt.Fprintf(os.Stderr, "tput: --rate-mbps 不能为负\n")
		return 2
	}

	users := pickUsers(common.prof, common.clients)
	dialers := make([]Dialer, common.clients)
	for k := 0; k < common.clients; k++ {
		d, err := NewDialer(common.prof, common.proto, common.node, users[k])
		if err != nil {
			fmt.Fprintf(os.Stderr, "tput: 创建 Dialer: %v\n", err)
			return 1
		}
		dialers[k] = d
	}
	defer func() {
		for _, d := range dialers {
			_ = d.Close()
		}
	}()

	ctx, cancel := signalContext(nil, duration+30*time.Second)
	defer cancel()

	var bpsPerStream uint64
	if rateMbps > 0 {
		bpsPerStream = uint64(rateMbps * 1e6 / float64(streams))
	}
	downSecs := int(math.Ceil(duration.Seconds()))
	if downSecs < 1 {
		downSecs = 1
	}

	var fail atomic.Int32
	var totalBytes atomic.Int64
	var t0Ms atomic.Int64
	var t1Ms atomic.Int64
	var mu sync.Mutex
	setBounds := func(first, last int64) {
		mu.Lock()
		if t0Ms.Load() == 0 || first < t0Ms.Load() {
			t0Ms.Store(first)
		}
		if last > t1Ms.Load() {
			t1Ms.Store(last)
		}
		mu.Unlock()
	}

	var wg sync.WaitGroup
	for s := 0; s < streams; s++ {
		wg.Add(1)
		go func(streamID int) {
			defer wg.Done()
			d := dialers[streamID%len(dialers)]
			streamCtx, streamCancel := context.WithTimeout(ctx, duration+10*time.Second)
			defer streamCancel()

			dialCtx, dialCancel := context.WithTimeout(streamCtx, 10*time.Second)
			conn, err := d.DialTCP(dialCtx, common.sink)
			dialCancel()
			if err != nil {
				fail.Add(1)
				return
			}
			defer conn.Close()

			if dir == "down" {
				req := fmt.Sprintf("DOWN %d %d\n", bpsPerStream, downSecs)
				if _, err := conn.Write([]byte(req)); err != nil {
					fail.Add(1)
					return
				}
				_ = conn.SetDeadline(time.Now().Add(duration + 10*time.Second))
				var n int64
				var first, last int64
				buf := make([]byte, tputWriteChunk)
				for {
					if streamCtx.Err() != nil {
						break
					}
					nr, err := conn.Read(buf)
					if nr > 0 {
						// secs 按第一个字节到最后一个字节算：代理不一定把 sink 的半关传过来，
						// 读到 EOF 的时刻可能晚很多（一直等到期限），不能用它当结束时刻
						last = nowMs()
						if first == 0 {
							first = last
						}
						n += int64(nr)
					}
					if err != nil {
						break
					}
				}
				_ = conn.SetDeadline(time.Time{})
				if n > 0 {
					totalBytes.Add(n)
					setBounds(first, last)
				} else {
					fail.Add(1)
				}
				return
			}

			// up
			if _, err := conn.Write([]byte("UP\n")); err != nil {
				fail.Add(1)
				return
			}
			sendStart := nowMs()
			n, err := pacedSend(streamCtx, conn, bpsPerStream, duration)
			sendEnd := nowMs()
			if err != nil && streamCtx.Err() == nil && n == 0 {
				fail.Add(1)
				return
			}
			closeWrite(conn)
			_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
			line, err := bufio.NewReader(conn).ReadString('\n')
			_ = conn.SetReadDeadline(time.Time{})
			if err != nil {
				fail.Add(1)
				return
			}
			line = strings.TrimSpace(line)
			reported, err := strconv.ParseInt(line, 10, 64)
			if err != nil || reported <= 0 {
				fail.Add(1)
				return
			}
			totalBytes.Add(reported)
			setBounds(sendStart, sendEnd)
		}(s)
	}
	wg.Wait()

	t0 := t0Ms.Load()
	t1 := t1Ms.Load()
	secs := 0.0
	if t0 > 0 && t1 > 0 {
		secs = float64(t1-t0) / 1000.0
	}
	bytes := totalBytes.Load()
	mbps := 0.0
	if secs > 0 {
		mbps = float64(bytes) * 8.0 / secs / 1e6
	}

	emit(map[string]any{
		"cmd":              "tput",
		"name":             common.name,
		"proto":            common.proto,
		"dir":              dir,
		"rate_mbps_target": rateMbps,
		"streams":          streams,
		"bytes":            bytes,
		"secs":             secs,
		"mbps":             mbps,
		"fail":             int(fail.Load()),
		"t0_unix_ms":       t0,
		"t1_unix_ms":       t1,
	})
	return 0
}

func closeWrite(conn net.Conn) {
	type halfCloser interface {
		CloseWrite() error
	}
	if c, ok := conn.(halfCloser); ok {
		_ = c.CloseWrite()
	}
}
