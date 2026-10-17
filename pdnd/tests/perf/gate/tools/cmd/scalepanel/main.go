package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/aegispanel/nodeagent/perfgate/internal/profile"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags)

	if len(os.Args) < 2 {
		log.Print("用法: scalepanel init|serve ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init":
		os.Exit(runInit(os.Args[2:]))
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	default:
		log.Printf("未知子命令: %s", os.Args[1])
		os.Exit(2)
	}
}

func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	out := fs.String("out", "", "输出目录（必填）")
	seed := fs.String("seed", "nodegate", "种子")
	users := fs.Int("users", 5000, "名单人数")
	deltaSize := fs.Int("delta-size", 500, "每代轮换人数")
	portBase := fs.Int("port-base", 20000, "端口基址")
	protocols := fs.String("protocols", "all", "协议子集或 all")
	realityDest := fs.String("reality-dest", "", "REALITY dest（必填）")
	nodeCertDir := fs.String("node-cert-dir", "", "节点读证书目录")
	if fs.Parse(args) != nil {
		return 2
	}
	if *out == "" || *realityDest == "" {
		log.Print("--out 与 --reality-dest 必填")
		return 2
	}
	protos, err := profile.ParseProtocols(*protocols)
	if err != nil {
		log.Print(err)
		return 2
	}
	prof, err := profile.Init(profile.InitOptions{
		Dir: *out, Seed: *seed, Users: *users, DeltaSize: *deltaSize, PortBase: *portBase,
		Protocols: protos, RealityDest: *realityDest, NodeCertDir: *nodeCertDir,
	})
	if err != nil {
		log.Print(err)
		return 1
	}
	_, _ = os.Stdout.WriteString("profile: " + filepath.Join(prof.Dir, "profile.json") + "\n")
	return 0
}

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	profilePath := fs.String("profile", "", "profile.json 路径（必填）")
	grpcListen := fs.String("grpc-listen", "127.0.0.1:9443", "gRPC 监听")
	serverName := fs.String("server-name", "127.0.0.1", "服务端证书 SAN")
	controlListen := fs.String("control-listen", "127.0.0.1:9180", "HTTP 控制口")
	tokenFile := fs.String("token-file", "", "令牌文件（必填）")
	caFPOut := fs.String("ca-fingerprint-out", "", "CA 指纹输出（必填）")
	pushInterval := fs.Duration("push-interval", 60*time.Second, "推送间隔")
	heartbeatInterval := fs.Duration("heartbeat-interval", 30*time.Second, "心跳间隔")
	if fs.Parse(args) != nil {
		return 2
	}
	if *profilePath == "" || *tokenFile == "" || *caFPOut == "" {
		log.Print("--profile、--token-file、--ca-fingerprint-out 必填")
		return 2
	}
	prof, err := profile.Load(*profilePath)
	if err != nil {
		log.Print(err)
		return 1
	}
	rawToken, err := os.ReadFile(*tokenFile)
	if err != nil {
		log.Print(err)
		return 1
	}
	token := stringsTrim(string(rawToken))
	if token == "" {
		log.Print("令牌文件为空")
		return 2
	}
	pan, err := newPanel(prof, token, *serverName)
	if err != nil {
		log.Print(err)
		return 1
	}
	pan.setIntervals(*pushInterval, *heartbeatInterval)
	pan.grpcListen = *grpcListen

	if err := pan.startGRPC(); err != nil {
		log.Printf("gRPC 启动失败: %v", err)
		return 1
	}
	log.Printf("scalepanel gRPC 监听 %s", *grpcListen)

	ctrl, err := pan.runControlServer(*controlListen)
	if err != nil {
		log.Printf("控制口监听失败: %v", err)
		pan.stopGRPC()
		return 1
	}
	log.Printf("scalepanel 控制口监听 %s", *controlListen)

	// 两个口都在听了才写 CA 指纹：编排看到这个文件就开始 enroll。先写临时文件再改名，不会读到半个。
	tmp := *caFPOut + ".tmp"
	if err := os.WriteFile(tmp, []byte(pan.caFingerprint()+"\n"), 0o600); err != nil {
		log.Print(err)
		return 1
	}
	if err := os.Rename(tmp, *caFPOut); err != nil {
		log.Print(err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Print("收到退出信号，正在关闭")
	pan.stopGRPC()
	shutdownControl(context.Background(), ctrl)
	return 0
}
