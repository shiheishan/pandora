// [INPUT]: 依赖 tools/loadtest/ltkit 的 Manifest 与 Recorder，依赖同包 node.go 的 simNode、signed.go / uniproxy.go 的两个客户端、workload.go 的虚构负载、fleet.go 的计量接线
// [OUTPUT]: 对外提供 Main（nodes 子命令入口）、Options、Run；包内 staggerOffsets、strictProblems
// [POS]: tools/loadtest/nodesim 的入口与编排：解析 flag、按清单装出 M 个模拟 pdnd、在错开窗口内逐个起跑、每分钟打进度、到时或收到 SIGINT/SIGTERM 收尾写 nodes.json / nodes.txt，-strict 时按 5xx、验签失败与未起来的节点判退出码
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package nodesim 是 nodes 子命令：M 个模拟节点对着面板 node 网关跑，
// 请求序列、节拍与失败处理逐段对齐 pdnd（pdnd/node/node.go 与 pdnd/panel/*），
// 只把内核换成虚构负载。
package nodesim

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// Options 是一次运行的全部参数；Main 从 flag 填，测试直接构造。
type Options struct {
	NodeURL  string
	Nodes    int
	Duration time.Duration
	Stagger  time.Duration
	// Timeout 是 UniProxy 单次请求上限，对应 pdnd 的 panel.timeout_seconds；
	// 签名通道 pdnd 写死 15 秒，不跟这个值。
	Timeout      time.Duration
	Stream       bool
	VerifyConfig bool
	OnlineRatio  float64
	TrafficMiB   float64
	Progress     time.Duration
	Seed         uint64
	// 以下四个是 pdnd 写死的值，暴露出来只为测试缩短节拍。
	PullInterval   time.Duration
	PushInterval   time.Duration
	StatusInterval time.Duration
	HealthWindow   time.Duration

	ErrorSamples int64
	Stdout       io.Writer
	Stderr       io.Writer
}

// DefaultOptions 是 pdnd 的缺省节拍与一组适合 Vultr 实测的负载参数。
func DefaultOptions() Options {
	return Options{
		Nodes: 0, Duration: 10 * time.Minute, Stagger: time.Minute, Timeout: 15 * time.Second,
		Stream: true, VerifyConfig: true, OnlineRatio: 0.3, TrafficMiB: 8, Progress: time.Minute,
		PullInterval: 60 * time.Second, PushInterval: 60 * time.Second, StatusInterval: 30 * time.Second,
		HealthWindow: 5 * time.Second, ErrorSamples: 20,
	}
}

// Main 是 nodes 子命令的入口。
func Main(args []string) error {
	opt := DefaultOptions()
	fs := flag.NewFlagSet("nodes", flag.ContinueOnError)
	manifest := fs.String("manifest", "", "seed 写出的造数清单（必填）")
	out := fs.String("out", "", "结果目录，写 nodes.json 与 nodes.txt（必填）")
	strict := fs.Bool("strict", false, "出现 5xx、签名或令牌 401、配置验签失败、起跑后迟迟没拿到配置的节点即退出码非 0（CI 用）")
	fs.StringVar(&opt.NodeURL, "node-url", "", "node 网关基址：https 公网域名，或回环上的 http（必填）")
	fs.IntVar(&opt.Nodes, "nodes", opt.Nodes, "取清单前 N 个节点，0 表示全部")
	fs.DurationVar(&opt.Duration, "duration", opt.Duration, "运行时长，0 表示一直跑到 SIGINT/SIGTERM")
	fs.DurationVar(&opt.Stagger, "stagger", opt.Stagger, "节点起跑在这个窗口内随机错开（纳秒级，不整秒齐射）")
	fs.DurationVar(&opt.Timeout, "timeout", opt.Timeout, "UniProxy 单次请求超时（pdnd panel.timeout_seconds）；签名通道固定 15 秒")
	fs.BoolVar(&opt.Stream, "stream", opt.Stream, "挂 SSE 事件流（pdnd 总是挂）")
	fs.BoolVar(&opt.VerifyConfig, "verify-config", opt.VerifyConfig, "按 pdnd VerifyConfig 验配置签名")
	fs.Float64Var(&opt.OnlineRatio, "online-ratio", opt.OnlineRatio, "全体用户中同时在线的比例，每个在线用户落在一个模拟节点上")
	fs.Float64Var(&opt.TrafficMiB, "traffic-mib", opt.TrafficMiB, "每个在线用户每个上报周期的下行均值（MiB），上行取八分之一")
	fs.DurationVar(&opt.Progress, "progress", opt.Progress, "进度行间隔")
	fs.Uint64Var(&opt.Seed, "seed", 0, "随机种子，0 取当前时间")
	fs.DurationVar(&opt.PullInterval, "pull-default", opt.PullInterval, "拿到 base_config 前的拉取间隔（pdnd 写死，只为测试改）")
	fs.DurationVar(&opt.PushInterval, "push-default", opt.PushInterval, "拿到 base_config 前的上报间隔（pdnd 写死，只为测试改）")
	fs.DurationVar(&opt.StatusInterval, "status-interval", opt.StatusInterval, "心跳间隔（pdnd 写死 30 秒，只为测试改）")
	fs.DurationVar(&opt.HealthWindow, "health-window", opt.HealthWindow, "switched 之后多久报 health_passed（pdnd 写死 5 秒）")
	fs.Int64Var(&opt.ErrorSamples, "error-samples", opt.ErrorSamples, "节点侧错误最多往 stderr 打几条样本")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *manifest == "" || *out == "" || opt.NodeURL == "" {
		fs.Usage()
		return errors.New("-manifest, -out and -node-url are required")
	}
	m, err := ltkit.LoadManifest(*manifest)
	if err != nil {
		return err
	}
	opt.Stdout, opt.Stderr = os.Stdout, os.Stderr

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if opt.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.Duration)
		defer cancel()
	}
	rec := ltkit.NewRecorder("nodes", 10*time.Second)
	summary, err := Run(ctx, m, opt, rec)
	if err != nil {
		return err
	}
	rep, err := rec.WriteFiles(*out)
	if err != nil {
		return fmt.Errorf("write results: %w", err)
	}
	ltkit.WriteSummary(os.Stdout, rep)
	if *strict {
		if problems := strictProblems(rep, summary); len(problems) > 0 {
			return fmt.Errorf("strict: %v", problems)
		}
	}
	return nil
}

// Run 起跑全部模拟节点，跑到 ctx 结束，等各节点交完最后一轮上报后返回整机
// 状态；请求计量都在 rec 里，fleet 摘要也已写进 rec 的 meta。
func Run(ctx context.Context, m *ltkit.Manifest, opt Options, rec *ltkit.Recorder) (fleetSummary, error) {
	base, err := canonicalServer(opt.NodeURL)
	if err != nil {
		return fleetSummary{}, err
	}
	nodes := m.Nodes
	if opt.Nodes > 0 {
		if opt.Nodes > len(nodes) {
			return fleetSummary{}, fmt.Errorf("-nodes %d exceeds the %d nodes in the manifest", opt.Nodes, len(nodes))
		}
		nodes = nodes[:opt.Nodes]
	}
	if len(nodes) == 0 {
		return fleetSummary{}, errors.New("manifest has no nodes")
	}
	if opt.Duration > 0 && opt.Stagger >= opt.Duration {
		return fleetSummary{}, errors.New("-stagger must be shorter than -duration")
	}
	if opt.Seed == 0 {
		opt.Seed = uint64(time.Now().UnixNano())
	}
	stdout, stderr := orDiscard(opt.Stdout), orDiscard(opt.Stderr)

	fleet := newFleetStats()
	obs := &observer{rec: rec, fleet: fleet, log: stderr, maxSamples: opt.ErrorSamples}
	work := newWorkload(m, len(nodes), opt.OnlineRatio, opt.TrafficMiB)
	sims := make([]*simNode, len(nodes))
	var transports []func()
	for i, n := range nodes {
		uni := newUniClient(base, n.ID, n.NodeType, n.RuntimeToken, opt.Timeout, obs)
		transports = append(transports, uni.http.CloseIdleConnections)
		var signed *signedClient
		if n.PrivateKey != "" {
			if signed, err = newSignedClient(base, n, opt.VerifyConfig, obs); err != nil {
				return fleetSummary{}, err
			}
			transports = append(transports, signed.http.CloseIdleConnections)
		}
		rng := rand.New(rand.NewPCG(opt.Seed, uint64(i)+1))
		sims[i] = newSimNode(n.ID, i, &opt, uni, signed, obs, work, rng)
	}
	offsets := staggerOffsets(len(nodes), opt.Stagger, rand.New(rand.NewPCG(opt.Seed, 0)))

	rec.SetMeta("label", m.Label)
	rec.SetMeta("nodes", len(nodes))
	rec.SetMeta("manifest_users", len(m.Users))
	rec.SetMeta("stagger_s", opt.Stagger.Seconds())
	rec.SetMeta("stream", opt.Stream)
	rec.SetMeta("verify_config", opt.VerifyConfig)
	rec.SetMeta("online_ratio", opt.OnlineRatio)
	rec.SetMeta("traffic_mib", opt.TrafficMiB)
	rec.SetMeta("timeout_s", opt.Timeout.Seconds())
	rec.SetMeta("seed", opt.Seed)

	start := time.Now()
	launchedAt := make([]atomic.Int64, len(sims))
	var wg sync.WaitGroup
	for i, sim := range sims {
		wg.Add(1)
		go func() {
			defer wg.Done()
			timer := time.NewTimer(offsets[i])
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			launchedAt[i].Store(time.Now().UnixNano())
			fleet.launched.Add(1)
			sim.run(ctx)
		}()
	}

	if opt.Progress > 0 {
		go func() {
			tick := time.NewTicker(opt.Progress)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					fmt.Fprintln(stdout, progressLine(time.Since(start), len(sims), rec.Snapshot(), fleet.snapshot()))
				}
			}
		}()
	}

	<-ctx.Done()
	end := time.Now()
	rec.Stop()
	// 和 pdnd 一样等各节点交完最后一轮流量，但不无限等
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(opt.Timeout + 5*time.Second):
		fmt.Fprintln(stderr, "nodes: final reports did not finish in time")
	}
	for _, closeIdle := range transports {
		closeIdle()
	}

	summary := fleet.snapshot()
	// 起跑后超过一个请求超时加 5 秒还没拿到配置的节点算「没起来」；
	// 收尾前一刻才起跑的节点不算，否则 -strict 会被时序误伤。
	grace := opt.Timeout + 5*time.Second
	for i, sim := range sims {
		at := launchedAt[i].Load()
		if at != 0 && !sim.started.Load() && end.Sub(time.Unix(0, at)) > grace {
			summary.NotStarted = append(summary.NotStarted, sim.id)
		}
	}
	rec.SetMeta("fleet", summary)
	return summary, nil
}

// staggerOffsets 给每个节点抽一个 [0, window) 内的起跑偏移，纳秒精度。
func staggerOffsets(n int, window time.Duration, rng *rand.Rand) []time.Duration {
	out := make([]time.Duration, n)
	if window <= 0 {
		return out
	}
	for i := range out {
		out[i] = time.Duration(rng.Int64N(int64(window)))
	}
	return out
}

// strictProblems 列出 -strict 判失败的理由，空表示通过。
func strictProblems(rep ltkit.Report, s fleetSummary) []string {
	var out []string
	t := rep.Totals
	if t.Count == 0 {
		out = append(out, "no requests were made")
	}
	if t.Server5x > 0 {
		out = append(out, fmt.Sprintf("%d responses were 5xx", t.Server5x))
	}
	if n := t.Flags[flagSigFail]; n > 0 {
		out = append(out, fmt.Sprintf("%d signed requests were rejected with 401", n))
	}
	if n := t.Flags[flagAuthFail]; n > 0 {
		out = append(out, fmt.Sprintf("%d UniProxy requests were rejected with 401", n))
	}
	if s.VerifyFailures > 0 {
		out = append(out, fmt.Sprintf("%d signed configs failed verification", s.VerifyFailures))
	}
	if len(s.NotStarted) > 0 {
		out = append(out, fmt.Sprintf("%d nodes never applied a config: %v", len(s.NotStarted), s.NotStarted))
	}
	return out
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
