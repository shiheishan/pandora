// pandora-native 是 AegisPanel 的自研节点端（module 沿用旧名 aegispanel/nodeagent）。
//
// 与 XrayR / V2bX 的接入面相同，区别在于：
//   - Pandora NativeCore 负责协议、传输、路由和统计；兼容层只按需承载尚未接管的组合
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
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	nativekernel "github.com/aegispanel/nodeagent/kernel"
	"github.com/aegispanel/nodeagent/node"
	"github.com/aegispanel/nodeagent/panel"
)

type config struct {
	// LogLevel: debug / info / warn / error
	LogLevel string `json:"log_level"`
	// NativeOnly rejects configurations outside the verified Pandora matrix.
	// A pointer preserves the distinction between an omitted field (the safe
	// NativeCore-only default) and an explicit false migration override in a
	// separately built compat binary.
	NativeOnly *bool `json:"native_only"`
	Panel      struct {
		URL string `json:"url"`
		// IdentityPath is the Ed25519 node identity created by bootstrap.
		// Keep it configurable so packaged installations and custom layouts
		// use the exact same path at bootstrap and runtime.
		IdentityPath   string `json:"identity_path"`
		SignedRequired bool   `json:"signed_required"`
		// TimeoutSeconds 是单次面板请求的上限。
		// 比拉取间隔小才有意义，否则请求会互相堆叠。
		TimeoutSeconds int `json:"timeout_seconds"`
	} `json:"panel"`
	Nodes []struct {
		NodeID       string `json:"node_id"`
		NodeType     string `json:"node_type"`
		Token        string `json:"token"`
		IdentityPath string `json:"identity_path"`
	} `json:"nodes"`
}

// buildVersion is injected by the release builder and intentionally defaults
// to dev for local builds.
var buildVersion = "dev"

func bootstrapCommand(args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	server := fs.String("server", "", "控制面地址")
	token := fs.String("token", "", "一次性引导令牌")
	name := fs.String("name", "", "节点名称")
	path := fs.String("identity", panel.DefaultIdentityPath, "身份文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// 单阶段引导已经下线：服务端的 POST /v1/nodes/bootstrap 不看内容一律返回
	// 426，要求走 /v1/nodes/enrollments。这个子命令的参数和 `enrollment begin`
	// 逐个对应，所以保留命令名、把语义改成两阶段接入的第一步——照旧习惯敲
	// bootstrap 的人不会再撞在一个光秃秃的 HTTP 426 上。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	j, err := panel.BeginEnrollment(ctx, panel.BootstrapOptions{Server: *server, Token: *token, Name: *name, Path: *path})
	if err != nil {
		return err
	}
	fmt.Printf("enrollment pending\n  enrollment id: %s\n  node id: %s\n  serial: %d\n  journal: %s\n",
		j.EnrollmentID, j.NodeID, j.Serial, panel.EnrollmentJournalPath(*path))
	fmt.Printf("下一步：装好配置与 unit 之后执行\n"+
		"  %s enrollment commit --identity %s \\\n"+
		"    --binary-sha256 <二进制> --config-sha256 <配置> \\\n"+
		"    --unit-sha256 <unit> --preflight-sha256 <预检证据>\n",
		os.Args[0], *path)
	return nil
}

func enrollmentCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("enrollment subcommand is required: begin, status, commit, abort")
	}
	subcommand := args[0]
	fs := flag.NewFlagSet("enrollment "+subcommand, flag.ContinueOnError)
	identityPath := fs.String("identity", panel.DefaultIdentityPath, "final identity file path")
	server := fs.String("server", "", "control-plane URL")
	token := fs.String("token", "", "one-time bootstrap token")
	tokenFile := fs.String("token-file", "", "read one-time bootstrap token from a protected file")
	name := fs.String("name", "", "node name")
	reason := fs.String("reason", "local installation failed before commit", "abort reason")
	binarySHA := fs.String("binary-sha256", "", "installed binary SHA-256")
	configSHA := fs.String("config-sha256", "", "installed config SHA-256")
	unitSHA := fs.String("unit-sha256", "", "installed unit SHA-256")
	preflightSHA := fs.String("preflight-sha256", "", "preflight evidence SHA-256")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch subcommand {
	case "begin":
		if *tokenFile != "" {
			if *token != "" {
				return fmt.Errorf("token and token-file are mutually exclusive")
			}
			raw, readErr := os.ReadFile(*tokenFile)
			if readErr != nil {
				return readErr
			}
			*token = strings.TrimSpace(string(raw))
		}
		j, err := panel.BeginEnrollment(ctx, panel.BootstrapOptions{Server: *server, Token: *token, Name: *name, Path: *identityPath})
		if err != nil {
			return err
		}
		fmt.Printf("enrollment pending\n  enrollment id: %s\n  node id: %s\n  serial: %d\n  journal: %s\n", j.EnrollmentID, j.NodeID, j.Serial, panel.EnrollmentJournalPath(*identityPath))
		return nil
	case "status":
		j, err := panel.LoadEnrollmentJournal(*identityPath)
		if err != nil {
			return err
		}
		if err := panel.EnrollmentStatus(ctx, *identityPath, j); err != nil {
			return err
		}
		fmt.Printf("enrollment state: %s\n", j.State)
		if j.State == "committed" {
			if _, err := panel.PromoteCommittedEnrollment(*identityPath, j); err != nil {
				return err
			}
		}
		return nil
	case "commit":
		j, err := panel.LoadEnrollmentJournal(*identityPath)
		if err != nil {
			return err
		}
		_, err = panel.CommitEnrollment(ctx, *identityPath, j, panel.EnrollmentEvidence{AgentVersion: buildVersion,
			Architecture: runtime.GOARCH, BinarySHA256: *binarySHA, ConfigSHA256: *configSHA, UnitSHA256: *unitSHA, PreflightSHA256: *preflightSHA})
		return err
	case "abort":
		j, err := panel.LoadEnrollmentJournal(*identityPath)
		if err != nil {
			return err
		}
		return panel.AbortEnrollment(ctx, *identityPath, j, *reason)
	default:
		return fmt.Errorf("unknown enrollment subcommand %q", subcommand)
	}
}

func verifyIdentityCommand(args []string) error {
	fs := flag.NewFlagSet("verify-identity", flag.ContinueOnError)
	path := fs.String("identity", panel.DefaultIdentityPath, "identity file path")
	nodeID := fs.String("node-id", "", "expected node ID")
	expectedServer := fs.String("expected-server", "", "expected control-plane URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *nodeID == "" {
		return fmt.Errorf("node-id is required")
	}
	identity, err := panel.LoadIdentity(*path)
	if err != nil {
		return err
	}
	if identity.NodeID != *nodeID {
		return fmt.Errorf("identity node %s does not match configured node %s", identity.NodeID, *nodeID)
	}
	if *expectedServer == "" {
		return fmt.Errorf("expected-server is required")
	}
	expected, err := panel.CanonicalSignedServer(*expectedServer)
	if err != nil {
		return err
	}
	actual, err := panel.CanonicalSignedServer(identity.Server)
	if err != nil || actual != expected {
		return fmt.Errorf("identity control plane does not match expected server")
	}
	client, err := panel.NewSignedClientAt(identity, *path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := client.Heartbeat(ctx, panel.HeartbeatInput{
		AgentVersion:  buildVersion,
		RuntimeStatus: "installing",
	})
	if err != nil {
		return fmt.Errorf("signed heartbeat failed: %w", err)
	}
	if out == nil || out.NodeStatus == "" {
		return fmt.Errorf("signed heartbeat response is incomplete")
	}
	return nil
}

func validateInstallCommand(args []string) error {
	fs := flag.NewFlagSet("validate-install", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath, "config file")
	identityPath := fs.String("identity", panel.DefaultIdentityPath, "final or pending identity file")
	unitPath := fs.String("unit", "", "systemd unit file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *unitPath == "" {
		return fmt.Errorf("unit is required")
	}
	if _, err := loadConfig(*configPath); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}
	if _, err := panel.LoadIdentity(*identityPath); err != nil {
		if _, journalErr := panel.LoadEnrollmentJournal(*identityPath); journalErr != nil {
			return fmt.Errorf("identity validation failed: %w", err)
		}
	}
	unit, err := os.ReadFile(*unitPath)
	if err != nil {
		return fmt.Errorf("unit validation failed: %w", err)
	}
	unitText := string(unit)
	directives := map[string]string{}
	for _, line := range strings.Split(unitText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		// systemd 的后出现者胜；这里保留最后一次赋值。
		directives[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	for key, want := range map[string]string{"User": "pandora", "Group": "pandora"} {
		if directives[key] != want {
			return fmt.Errorf("unit validation failed: missing %s=%s", key, want)
		}
	}
	// systemd 的布尔指令接受 1/yes/true/on，行为完全一样，别只认其中一种写法。
	switch strings.ToLower(directives["NoNewPrivileges"]) {
	case "1", "yes", "true", "on":
	default:
		return fmt.Errorf("unit validation failed: missing NoNewPrivileges=true")
	}
	if directives["ExecStart"] == "" {
		return fmt.Errorf("unit validation failed: missing ExecStart=")
	}
	if strings.Contains(unitText, "--token ") || strings.Contains(unitText, "--token=") {
		return fmt.Errorf("unit validation failed: runtime token in ExecStart")
	}
	fmt.Println("install validation passed: config identity unit")
	return nil
}

const defaultConfigPath = "/etc/pandora-native/config.json"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "bootstrap" {
		if err := bootstrapCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "bootstrap failed:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "verify-identity" {
		if err := verifyIdentityCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "identity verification failed:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "validate-install" {
		if err := validateInstallCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "install validation failed:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "enrollment" {
		if err := enrollmentCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "enrollment failed:", err)
			os.Exit(1)
		}
		return
	}

	var path string
	var showVersion bool
	var showCapabilities bool
	var showSelfCheck bool
	flag.StringVar(&path, "c", defaultConfigPath, "配置文件路径")
	flag.BoolVar(&showVersion, "version", false, "打印版本并退出")
	flag.BoolVar(&showCapabilities, "capabilities", false, "打印 NativeCore 能力矩阵并退出")
	flag.BoolVar(&showSelfCheck, "self-check", false, "check native capability registry")
	flag.Parse()
	if showVersion {
		fmt.Println(buildVersion)
		return
	}
	if showCapabilities {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(nativekernel.NativeCapabilityReportFor(runtimeNativeOnly)); err != nil {
			fmt.Fprintln(os.Stderr, "打印能力矩阵失败:", err)
			os.Exit(1)
		}
		return
	}

	if showSelfCheck {
		report := nativekernel.NativeSelfCheckFor(runtimeNativeOnly)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintln(os.Stderr, "self-check output failed:", err)
			os.Exit(1)
		}
		if report.Status != "ok" {
			os.Exit(1)
		}
		return
	}

	cfg, err := loadConfig(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取配置失败:", err)
		os.Exit(1)
	}

	log := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	kernel, err := newRuntime(log, cfg.nativeOnly())
	if err != nil {
		log.Error("鍒涘缓鍐呮牳澶辫触", "err", err)
		os.Exit(1)
	}
	if err := kernel.Start(ctx); err != nil {
		log.Error("启动内核失败", "err", err)
		os.Exit(1)
	}
	defer kernel.Close()
	log.Info("内核已启动", "kernel", kernel.Type(), "version", buildVersion, "节点数", len(cfg.Nodes))

	var wg sync.WaitGroup
	for _, nc := range cfg.Nodes {
		client := panel.New(panel.Options{
			BaseURL:  cfg.Panel.URL,
			NodeID:   nc.NodeID,
			NodeType: nc.NodeType,
			Token:    nc.Token,
			Timeout:  time.Duration(cfg.Panel.TimeoutSeconds) * time.Second,
		})
		var signed *panel.SignedClient
		identityPath := cfg.identityPath(nc.IdentityPath)
		identity, identityErr := panel.LoadIdentity(identityPath)
		if identityErr == nil && identity.NodeID != nc.NodeID {
			identityErr = fmt.Errorf("identity node %s does not match configured node %s", identity.NodeID, nc.NodeID)
		}
		if identityErr == nil {
			signed, identityErr = panel.NewSignedClientAt(identity, identityPath)
		}
		if identityErr != nil {
			if cfg.Panel.SignedRequired {
				log.Error("签名身份不可用，已拒绝启动该节点", "node", nc.NodeID, "identity", identityPath, "err", identityErr)
				continue
			}
			log.Warn("签名客户端不可用，使用兼容通道", "node", nc.NodeID, "identity", identityPath, "err", identityErr)
		}
		n := node.NewWithSignedClient(client, kernel, log, signed)
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

func (c *config) nativeOnly() bool {
	if c == nil || c.NativeOnly == nil {
		return true
	}
	return *c.NativeOnly
}

func (c *config) identityPath(nodePath string) string {
	if nodePath != "" {
		return nodePath
	}
	if c == nil || c.Panel.IdentityPath == "" {
		return panel.DefaultIdentityPath
	}
	return c.Panel.IdentityPath
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
