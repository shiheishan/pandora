package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/aegispanel/nodeagent/perfgate/internal/profile"
)

func runProbe(args []string) int {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var protosFlag string
	var timeout time.Duration
	var sinceUnixMs int64
	var common commonFlags
	common.register(fs)
	fs.StringVar(&protosFlag, "protos", "all", "协议列表：all 或逗号分隔")
	fs.DurationVar(&timeout, "timeout", 60*time.Second, "单协议探测总时长")
	fs.Int64Var(&sinceUnixMs, "since-unix-ms", 0, "计时起点 Unix 毫秒；0 表示用 start")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := common.load(); err != nil {
		fmt.Fprintf(os.Stderr, "probe: %v\n", err)
		return 2
	}
	if common.echo == "" {
		fmt.Fprintf(os.Stderr, "probe: --echo 必填\n")
		return 2
	}
	if common.node == "" {
		fmt.Fprintf(os.Stderr, "probe: --node 必填\n")
		return 2
	}

	want, err := profileParseProtocolsFiltered(common.prof, protosFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: %v\n", err)
		return 2
	}
	if len(want) == 0 {
		fmt.Fprintf(os.Stderr, "probe: 没有可探测的协议\n")
		return 2
	}

	startMs := nowMs()
	sinceMs := sinceUnixMs
	if sinceMs == 0 {
		sinceMs = startMs
	}
	since := time.UnixMilli(sinceMs)

	ctx, cancel := signalContext(nil, timeout)
	defer cancel()

	users := pickUsers(common.prof, 1)
	user0 := users[0]

	type protoResult struct {
		OK        bool
		TS        float64
		Attempts  int
		LastError string
	}

	per := make(map[string]protoResult)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, proto := range want {
		wg.Add(1)
		go func(proto string) {
			defer wg.Done()
			attempts := 0
			var lastErr string
			var ok bool
			var tS float64

			for {
				if ctx.Err() != nil {
					break
				}
				d, err := NewDialer(common.prof, proto, common.node, user0)
				if err != nil {
					attempts++
					lastErr = truncateErr(err.Error(), 120)
					if !sleepCtx(ctx, 100*time.Millisecond) {
						break
					}
					continue
				}
				_, err = echoOnce(ctx, d, common.echo, 3*time.Second)
				_ = d.Close()
				attempts++
				if err != nil {
					lastErr = truncateErr(err.Error(), 120)
					if !sleepCtx(ctx, 100*time.Millisecond) {
						break
					}
					continue
				}
				ok = true
				tS = time.Since(since).Seconds()
				break
			}

			mu.Lock()
			per[proto] = protoResult{OK: ok, TS: tS, Attempts: attempts, LastError: lastErr}
			mu.Unlock()
		}(proto)
	}
	wg.Wait()

	allOK := true
	var tAll float64
	for _, proto := range want {
		r := per[proto]
		if !r.OK {
			allOK = false
		}
		if r.OK && r.TS > tAll {
			tAll = r.TS
		}
	}

	perOut := make(map[string]map[string]any, len(want))
	for _, proto := range want {
		r := per[proto]
		perOut[proto] = map[string]any{
			"ok":         r.OK,
			"t_s":        r.TS,
			"attempts":   r.Attempts,
			"last_error": r.LastError,
		}
	}

	emit(map[string]any{
		"cmd":           "probe",
		"name":          common.name,
		"ok":            allOK,
		"start_unix_ms": startMs,
		"since_unix_ms": sinceMs,
		"t_all_s":       tAll,
		"per_proto":     perOut,
	})
	return 0
}

func profileParseProtocolsFiltered(p *profile.Profile, s string) ([]string, error) {
	parsed, err := profile.ParseProtocols(s)
	if err != nil {
		return nil, err
	}
	enabled := make(map[string]bool, len(p.Protocols))
	for _, x := range p.Protocols {
		enabled[x] = true
	}
	out := make([]string, 0, len(parsed))
	for _, x := range parsed {
		if enabled[x] {
			out = append(out, x)
		}
	}
	return out, nil
}

func truncateErr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
