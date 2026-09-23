// aegis-nodeagent 是 AegisPanel 的自研节点端。
//
// 与 XrayR / V2bX 的定位相同，区别在于：
//   - 不 fork 内核，只用 sing-box 的注册表覆盖需要用户管理的入站
//   - 流量按用户在连接层统计，TCP 与 UDP 都计入
//   - 一个进程可带多个节点（不同协议、不同端口），互不影响
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aegispanel/nodeagent/core/multi"
	"github.com/aegispanel/nodeagent/node"
	"github.com/aegispanel/nodeagent/panel"
)

type config struct {
	// LogLevel: debug / info / warn / error
	LogLevel string `json:"log_level"`
	Panel    struct {
		URL string `json:"url"`
		// TimeoutSeconds 是单次面板请求的上限。
		// 比拉取间隔小才有意义，否则请求会互相堆叠。
		TimeoutSeconds int `json:"timeout_seconds"`
	} `json:"panel"`
	Nodes []struct {
		NodeID   string `json:"node_id"`
		NodeType string `json:"node_type"`
		Token    string `json:"token"`
	} `json:"nodes"`
}

func main() {
	var path string
	flag.StringVar(&path, "c", "/etc/aegis-nodeagent/config.json", "配置文件路径")
	flag.Parse()

	cfg, err := loadConfig(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取配置失败:", err)
		os.Exit(1)
	}

	log := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	kernel := multi.New(log)
	if err := kernel.Start(ctx); err != nil {
		log.Error("启动内核失败", "err", err)
		os.Exit(1)
	}
	defer kernel.Close()
	log.Info("内核已启动", "kernel", kernel.Type(), "节点数", len(cfg.Nodes))

	var wg sync.WaitGroup
	for _, nc := range cfg.Nodes {
		client := panel.New(panel.Options{
			BaseURL:  cfg.Panel.URL,
			NodeID:   nc.NodeID,
			NodeType: nc.NodeType,
			Token:    nc.Token,
			Timeout:  time.Duration(cfg.Panel.TimeoutSeconds) * time.Second,
		})
		n := node.New(client, kernel, log)
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.Run(ctx)
		}()
	}

	<-ctx.Done()
	log.Info("收到退出信号，正在收尾")

	// 等各节点上报完最后一轮流量再退出。给足时限但不无限等 ——
	// 面板不可达时死等会让 systemd 最终强杀，反而更糟。
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Warn("收尾超时，强制退出")
	}
}

func loadConfig(path string) (*config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields() // 配置项拼错要立刻报错，而不是静默用默认值
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if cfg.Panel.URL == "" {
		return nil, fmt.Errorf("panel.url 不能为空")
	}
	if len(cfg.Nodes) == 0 {
		return nil, fmt.Errorf("至少要配置一个节点")
	}
	for i, n := range cfg.Nodes {
		if n.NodeID == "" || n.NodeType == "" || n.Token == "" {
			return nil, fmt.Errorf("节点 #%d 缺少 node_id / node_type / token", i+1)
		}
	}
	return &cfg, nil
}

func newLogger(level string) *slog.Logger {
	lv := slog.LevelInfo
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}
