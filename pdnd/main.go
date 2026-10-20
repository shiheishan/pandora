// pandora-native 是 Pandora 的自研节点端（module 沿用旧名 aegispanel/nodeagent）。
//
//   - Pandora NativeCore 负责协议、传输、路由和统计，是唯一的数据面
//   - 流量按用户在连接层统计，TCP 与 UDP 都计入
//   - 一台服务器一个进程、一个身份、一条到面板的 gRPC 会话（mTLS），名下多个节点（入站），
//     节点列表与配置都由面板下发
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aegispanel/nodeagent/core"
	nativekernel "github.com/aegispanel/nodeagent/kernel"
	"github.com/aegispanel/nodeagent/node"
	"github.com/aegispanel/nodeagent/panel"
)

type config struct {
	// LogLevel: debug / info / warn / error
	LogLevel string `json:"log_level"`
	// CacheDir 是状态目录：落盘缓存（各节点最后一份装上的配置与名单）放在
	// <cache_dir>/nodes/ 下。缺省 /var/lib/pandora-native，与 systemd 单元的 StateDirectory 一致。
	CacheDir string `json:"cache_dir"`
	Panel    struct {
		// Address 是面板节点通道的 host:port（只给 host 时补缺省端口 9443）。
		Address string `json:"address"`
		// IdentityDir 是 enroll 写下的身份目录（ca.pem、cert.pem、key.pem、server_id）。
		// 续期要原地换证书，所以必须在服务可写的路径里（缺省在 StateDirectory 下）。
		IdentityDir string `json:"identity_dir"`
	} `json:"panel"`
	// Runtime 是可选的连接回收、停机排空与内存上限调优，见 runtime_tuning.go。
	Runtime runtimeTuning `json:"runtime"`
	// Upgrade 是在线升级的可选设置，见 upgrade_cmd.go。
	Upgrade upgradeConfig `json:"upgrade"`
}

// buildVersion is injected by the release builder and intentionally defaults
// to dev for local builds.
var buildVersion = "dev"

// enrollCommand 是首次接入：一次性令牌 + 本机生成的 CSR 换客户端证书，写进身份目录。
//
// 令牌只从文件或标准输入读，不收命令行参数：argv 对本机所有用户可见（ps、/proc），
// 也会留在 shell 历史里。
func enrollCommand(args []string, stdin io.Reader) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	panelAddr := fs.String("panel", "", "面板节点通道 host:port")
	caSHA := fs.String("ca-sha256", "", "面板 CA 指纹（sha256:<hex>，安装命令给的）")
	tokenFile := fs.String("token-file", "", "从这个文件读一次性接入令牌")
	tokenStdin := fs.Bool("token-stdin", false, "从标准输入读一次性接入令牌")
	identityDir := fs.String("identity-dir", panel.DefaultIdentityDir, "身份目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("多余的参数：%v", fs.Args())
	}
	if *panelAddr == "" || *caSHA == "" {
		return errors.New("--panel 与 --ca-sha256 必填")
	}
	token, err := readToken(*tokenFile, *tokenStdin, stdin)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	id, err := panel.Enroll(ctx, panel.EnrollOptions{
		Panel: *panelAddr, CAFingerprint: *caSHA, Token: token, Dir: *identityDir,
		AgentVersion: buildVersion, Host: panel.CollectHostInfo(),
	})
	if err != nil {
		return err
	}
	fmt.Printf("enrolled\n  server id: %s\n  identity: %s\n  certificate expires: %s\n",
		id.ServerID(), id.Dir(), id.NotAfter().UTC().Format(time.RFC3339))
	return nil
}

// readToken 从 --token-file 或 --token-stdin 取令牌，二者必须且只能给一个。
func readToken(file string, fromStdin bool, stdin io.Reader) (string, error) {
	switch {
	case file != "" && fromStdin:
		return "", errors.New("--token-file 与 --token-stdin 只能给一个")
	case file != "":
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return nonEmptyToken(string(raw))
	case fromStdin:
		line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return nonEmptyToken(line)
	default:
		return "", errors.New("需要 --token-file 或 --token-stdin（令牌不收命令行参数）")
	}
}

func nonEmptyToken(raw string) (string, error) {
	token := strings.TrimSpace(raw)
	if token == "" {
		return "", errors.New("接入令牌为空")
	}
	return token, nil
}

func validateInstallCommand(args []string) error {
	fs := flag.NewFlagSet("validate-install", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath, "config file")
	identityDir := fs.String("identity-dir", "", "身份目录（缺省取配置里的 panel.identity_dir）")
	unitPath := fs.String("unit", "", "systemd unit file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *unitPath == "" {
		return fmt.Errorf("unit is required")
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}
	dir := *identityDir
	if dir == "" {
		dir = cfg.identityDir()
	}
	if _, err := panel.LoadIdentity(dir); err != nil {
		return fmt.Errorf("identity validation failed: %w", err)
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
	if strings.Contains(unitText, "--token") {
		return fmt.Errorf("unit validation failed: token in ExecStart")
	}
	fmt.Println("install validation passed: config identity unit")
	return nil
}

const defaultConfigPath = "/etc/pandora-native/config.json"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "enroll" {
		if err := enrollCommand(os.Args[2:], os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, "enroll failed:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "upgrade-apply" {
		if err := upgradeApplyCommand(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "upgrade-apply failed:", err)
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
		if err := enc.Encode(nativekernel.BuildNativeCapabilityReport()); err != nil {
			fmt.Fprintln(os.Stderr, "打印能力矩阵失败:", err)
			os.Exit(1)
		}
		return
	}

	if showSelfCheck {
		report := nativekernel.NativeSelfCheck()
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
	log := installLogger(cfg.LogLevel, os.Stdout)
	cfg.Runtime.apply(log)
	if err := run(cfg, log); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

// run 是守护进程：读身份、起内核、按落盘缓存冷启动、连面板，收到 SIGTERM 后按序收尾。
func run(cfg *config, log *slog.Logger) error {
	id, err := panel.LoadIdentity(cfg.identityDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("还没接入面板：先运行 pandora-native enroll --panel <host:port> --ca-sha256 <指纹> --token-file <文件>（身份目录 %s）", cfg.identityDir())
		}
		return fmt.Errorf("身份目录不可用: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 进程日志交给 NativeCore：逐连接的失败（限频、脱敏）与节点其余日志落在同一条流里。
	kernel := nativekernel.NewNativeCoreWithLogger(nil, log)
	if err := kernel.Start(ctx); err != nil {
		return fmt.Errorf("启动内核失败: %w", err)
	}
	defer kernel.Close()
	log.Info("内核已启动", "kernel", kernel.Type(), "version", buildVersion, "server", id.ServerID())

	binSHA := binarySHA256()
	env := currentUpgradeEnv()
	upgrader := newUpgrader(cfg, env, binSHA, log)
	srv := node.New(node.Options{ServerID: id.ServerID(), Kernel: kernel, Logger: log, CacheDir: cfg.stateDir(), Upgrader: upgrader})
	// 先用落盘缓存把上次的节点装起来：面板不可达也照常服务，第一条 Hello 就带着这些版本。
	srv.Start()

	sess, err := panel.NewSession(panel.SessionOptions{
		Address: cfg.Panel.Address, Identity: id, AgentVersion: buildVersion,
		BinarySHA256: binSHA, BuildSerial: env.serial, LastUpgrade: srv.LastUpgrade,
		Features: sessionFeatures(upgrader), Applied: srv.Applied, Logger: log,
	})
	if err != nil {
		return err
	}
	// 会话比主循环活得久：关停时主循环先停，最后一轮流量还要经会话交给面板。
	sessCtx, sessCancel := context.WithCancel(context.Background())
	sessDone := make(chan struct{})
	go func() {
		defer close(sessDone)
		// Tombstone 之后不再重连；节点端已停掉全部代理服务并清掉缓存（Server.stopAllForTombstone）。
		_ = sess.Run(sessCtx)
	}()
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		srv.Run(ctx, sess)
	}()

	<-ctx.Done()
	log.Info("收到退出信号，正在收尾")
	shutdown(log, kernel, srv, loopDone)
	sessCancel()
	select {
	case <-sessDone:
	case <-time.After(2 * time.Second):
	}
	return nil
}

// binarySHA256 是本进程二进制的 SHA-256（Hello 里自报，面板据此提示版本不一致）；读不到返回空串。
func binarySHA256() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// trafficDrainer 是内核「先关停、再交出最后一轮流量」的可选契约（NativeCore 实现）。
type trafficDrainer interface {
	CloseAndDrainTraffic() (map[string][]core.UserTraffic, error)
}

// shutdown 是退出顺序：等主循环停下 → 关内核（在途 TCP 连接此时把流量入账）→ 把最后一轮
// 流量与积压的待报流量经会话交给面板、等它确认。
//
// 原先是先报、再关内核，在途连接的流量每次重启都丢。给足时限但不无限等：面板不可达时
// 死等会让 systemd 最终强杀（TimeoutStopSec=30s：内核排空 5 秒 + 强制关闭至多 3 秒 +
// 最后一次上报至多 10 秒，留出余量）。
func shutdown(log *slog.Logger, kernel core.Core, srv *node.Server, loopDone <-chan struct{}) {
	// 内核关停（停 accept → 排空 → 强制关）与等主循环停下并行：排空要等几秒，没必要排在
	// 主循环后面。内核关停不碰节点状态；主循环此刻若还在调内核，只会拿到「已关闭」错误。
	drained := map[string][]core.UserTraffic{}
	haveDrained := false
	drainDone := make(chan struct{})
	if d, ok := kernel.(trafficDrainer); ok {
		haveDrained = true
		go func() {
			defer close(drainDone)
			out, err := d.CloseAndDrainTraffic()
			if err != nil {
				log.Warn("关停内核时有入站没关干净", "err", err)
			}
			drained = out
		}()
	} else {
		close(drainDone)
	}

	stopped := false
	select {
	case <-loopDone:
		stopped = true
	case <-time.After(5 * time.Second):
	}
	<-drainDone

	if stopped {
		finalCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		srv.Shutdown(finalCtx, drained, haveDrained)
		cancel()
	} else {
		// 主循环还没停（卡在某个内核调用里），不能和它并发动节点状态。
		log.Warn("主循环未在时限内停下，跳过最后一次上报")
	}
	if !haveDrained {
		_ = kernel.Close()
	}
}

func (c *config) stateDir() string {
	if c == nil || strings.TrimSpace(c.CacheDir) == "" {
		return node.DefaultCacheDir
	}
	return c.CacheDir
}

func (c *config) identityDir() string {
	if c == nil || strings.TrimSpace(c.Panel.IdentityDir) == "" {
		return panel.DefaultIdentityDir
	}
	return c.Panel.IdentityDir
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
	if _, _, err := panel.PanelHost(cfg.Panel.Address); err != nil {
		return nil, fmt.Errorf("panel.address: %w", err)
	}
	if err := cfg.Runtime.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// installLogger 建进程日志并设为 slog 的默认 logger。
//
// 设默认不能省：内核里有几处不经注入 logger、直接用包级 slog 的日志（QUIC 缓冲偏小、
// 入站关闭超时、内核关停、转发 panic）。默认 logger 还是标准库那个时，它们走 Go 默认
// log 的「2026/10/09 00:16:51 WARN ...」格式、也不受 log_level 约束，按 level= 采集
// 会漏掉（10-09 VPC 复测）。设了之后标准库 log 包的输出也一并转进同一个 handler，按 WARN 记。
func installLogger(level string, w io.Writer) *slog.Logger {
	lv := slog.LevelInfo
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	}
	log := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lv}))
	slog.SetDefault(log)
	// 标准库 log 包转进来的输出（依赖库与 net/http 的「http: panic serving …」）缺省按
	// INFO 记，log_level=warn 时会被整条吞掉；按 WARN 记才和其他告警一起留下。
	slog.SetLogLoggerLevel(slog.LevelWarn)
	return log
}
