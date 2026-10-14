// Command nodesim 是节点模拟器：用真实的 pdnd/panel 客户端连新面板 gRPC 节点通道，
// 模拟多台服务器做接入、会话、配置与名单回执、心跳、流量与在线上报；不起内核、不监听代理端口。
// 状态文件 JSON 格式见 status.go。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/aegispanel/nodeagent/panel"
	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
)

type serverOutcome int

const (
	serverOutcomeOK         serverOutcome = 0
	serverOutcomeEnrollFail serverOutcome = 1
	serverOutcomeTombstone  serverOutcome = 2
)

// serverParams 是各台模拟服务器共用的参数。
type serverParams struct {
	panelAddr, caSHA, stateDir string
	reportInterval             time.Duration
	trafficUsers               int
	omitStatusUsers            bool
	spread                     bool
	enrollAttempts             int // 0 = 不限
	enrollRetryWait            time.Duration
	enrollSem                  chan struct{} // nil = 不限并发
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run 解析参数并跑全部模拟服务器，返回进程退出码。
func run(args []string) int {
	if len(args) > 0 && args[0] == "summary" {
		return runSummary(args[1:], os.Stdout, os.Stderr)
	}

	fs := flag.NewFlagSet("nodesim", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	panelAddr := fs.String("panel", "", "面板节点通道 host:port（必填）")
	caSHA := fs.String("ca-sha256", "", "接入用 CA 指纹 sha256:…（无身份时必填）")
	tokensFile := fs.String("tokens-file", "", "服务器令牌文件（必填）")
	stateDir := fs.String("state-dir", "", "身份目录根（必填），每台服务器 D/<名字>")
	reportInterval := fs.Duration("report-interval", 10*time.Second, "流量与在线上报间隔")
	trafficUsers := fs.Int("traffic-users", 5, "每节点每轮上报用户数")
	statusFile := fs.String("status-file", "", "状态 JSON 路径（可空）")
	duration := fs.Duration("duration", 0, "运行时长，0 表示直到 SIGINT/SIGTERM")
	logLevel := fs.String("log-level", "info", "日志级别 debug|info|warn|error")
	statusUsers := fs.Bool("status-users", true, "状态文件里写各节点的用户 uuid 名单；200 台、每台几千人时设 false")
	enrollAttempts := fs.Int("enroll-attempts", 5, "无身份时接入的最多尝试次数，0 表示一直重试到退出")
	enrollRetryWait := fs.Duration("enroll-retry-wait", 2*time.Second, "接入失败后的等待")
	spread := fs.Bool("spread", false, "按真实节点错开：首轮上报随机、每轮 ±10% 抖动、各服务器从不同用户开始轮报")
	enrollConcurrency := fs.Int("enroll-concurrency", 0, "同时进行接入的服务器数，0 不限（面板按来源 IP 每分钟限 10 次接入）")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *enrollAttempts < 0 {
		fmt.Fprintln(os.Stderr, "--enroll-attempts 不能为负数")
		return 2
	}
	if *enrollRetryWait <= 0 {
		fmt.Fprintln(os.Stderr, "--enroll-retry-wait 必须大于 0")
		return 2
	}
	if *enrollConcurrency < 0 {
		fmt.Fprintln(os.Stderr, "--enroll-concurrency 不能为负数")
		return 2
	}
	if *panelAddr == "" || *tokensFile == "" || *stateDir == "" {
		fmt.Fprintln(os.Stderr, "用法: nodesim --panel host:port --tokens-file F --state-dir D [--ca-sha256 sha256:…] …；汇总: nodesim summary --status-file F [--version V] [--since RFC3339]")
		return 2
	}

	logger := newLogger(*logLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	tokens, err := LoadTokens(*tokensFile)
	if err != nil {
		logger.Error("读取令牌文件失败", "err", err)
		return 2
	}

	board := NewBoard(*statusFile, nil)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := board.Write(); err != nil {
					logger.Warn("写状态文件失败", "err", err)
				}
			}
		}
	}()

	var enrollSem chan struct{}
	if *enrollConcurrency > 0 {
		enrollSem = make(chan struct{}, *enrollConcurrency)
	}
	params := serverParams{
		panelAddr:       *panelAddr,
		caSHA:           *caSHA,
		stateDir:        *stateDir,
		reportInterval:  *reportInterval,
		trafficUsers:    *trafficUsers,
		omitStatusUsers: !*statusUsers,
		spread:          *spread,
		enrollAttempts:  *enrollAttempts,
		enrollRetryWait: *enrollRetryWait,
		enrollSem:       enrollSem,
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var enrollFail, tombstone int
	for _, tok := range tokens {
		wg.Add(1)
		go func(name, token string) {
			defer wg.Done()
			r := runServer(ctx, logger, board, params, name, token)
			mu.Lock()
			switch r {
			case serverOutcomeEnrollFail:
				enrollFail++
			case serverOutcomeTombstone:
				tombstone++
			}
			mu.Unlock()
		}(tok.Name, tok.Token)
	}
	wg.Wait()

	if err := board.Write(); err != nil {
		logger.Warn("写状态文件失败", "err", err)
	}

	snap := board.Snapshot()
	fmt.Fprintf(os.Stderr, "汇总: 服务器 %d, 接入失败 %d, Tombstone %d, 错误计数 %d\n",
		len(tokens), enrollFail, tombstone, snap.Errors)

	if enrollFail > 0 || tombstone > 0 {
		return 1
	}
	return 0
}

// runServer 跑一台模拟服务器并返回结束原因。
func runServer(ctx context.Context, logger *slog.Logger, board *Board, p serverParams, name, token string) serverOutcome {
	dir := filepath.Join(p.stateDir, name)
	id, err := panel.LoadIdentity(dir)
	if errors.Is(err, os.ErrNotExist) {
		if p.caSHA == "" {
			board.RecordError(name, errors.New("身份不存在且未提供 --ca-sha256"))
			return serverOutcomeEnrollFail
		}
		id, err = enrollWithRetry(ctx, logger, p, name, token, dir)
		if err != nil {
			board.RecordError(name, err)
			return serverOutcomeEnrollFail
		}
	} else if err != nil {
		board.RecordError(name, err)
		return serverOutcomeEnrollFail
	}

	board.Update(name, func(s *ServerStatus) {
		s.Enrolled = true
		s.ServerID = id.ServerID()
	})

	sim := NewSim(SimOptions{
		Name:            name,
		Board:           board,
		ReportInterval:  p.reportInterval,
		TrafficUsers:    p.trafficUsers,
		OmitStatusUsers: p.omitStatusUsers,
		Spread:          p.spread,
		Logger:          logger.With("sim", name),
	})

	sess, err := panel.NewSession(panel.SessionOptions{
		Address:      p.panelAddr,
		Identity:     id,
		AgentVersion: "nodesim",
		Host:         func() *nodev1.HostInfo { return hostInfo(name) },
		Applied:      sim.Applied,
		Logger:       logger.With("sim", name),
	})
	if err != nil {
		board.RecordError(name, err)
		return serverOutcomeEnrollFail
	}

	runCtx, cancel := context.WithCancel(ctx)
	simDone := make(chan struct{})
	go func() {
		sim.Run(runCtx, sess)
		close(simDone)
	}()

	sessErr := sess.Run(runCtx)
	cancel()
	<-simDone

	if errors.Is(sessErr, panel.ErrTombstoned) {
		board.Update(name, func(s *ServerStatus) { s.Tombstoned = true })
		return serverOutcomeTombstone
	}
	return serverOutcomeOK
}

// enrollWithRetry 用一次性令牌接入，失败按 --enroll-retry-wait 重试；有并发上限时只在接入期间占位，接入完就释放。
func enrollWithRetry(ctx context.Context, logger *slog.Logger, p serverParams, name, token, dir string) (*panel.Identity, error) {
	if p.enrollSem != nil {
		select {
		case p.enrollSem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-p.enrollSem }()
	}
	var lastErr error
	for attempt := 0; p.enrollAttempts == 0 || attempt < p.enrollAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		id, err := panel.Enroll(ctx, panel.EnrollOptions{
			Panel:         p.panelAddr,
			CAFingerprint: p.caSHA,
			Token:         token,
			Dir:           dir,
			AgentVersion:  "nodesim",
			Host:          hostInfo(name),
		})
		if err == nil {
			return id, nil
		}
		lastErr = err
		logger.Warn("接入失败，稍后重试", "sim", name, "attempt", attempt+1, "err", err)
		if p.enrollAttempts == 0 || attempt+1 < p.enrollAttempts {
			t := time.NewTimer(p.enrollRetryWait)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}
	}
	return nil, lastErr
}

func hostInfo(name string) *nodev1.HostInfo {
	return &nodev1.HostInfo{
		Hostname:    "nodesim-" + name,
		Os:          "linux",
		Arch:        "amd64",
		CpuCores:    1,
		MemoryBytes: 1 << 30,
		DiskBytes:   20 << 30,
	}
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}
