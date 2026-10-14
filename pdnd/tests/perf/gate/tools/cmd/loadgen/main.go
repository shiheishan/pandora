// loadgen：节点端性能闸门压测客户端。
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aegispanel/nodeagent/perfgate/internal/profile"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var code int
	switch cmd {
	case "probe":
		code = runProbe(args)
	case "hold":
		code = runHold(args)
	case "connrate":
		code = runConnrate(args)
	case "tput":
		code = runTput(args)
	case "lat":
		code = runLat(args)
	case "udp":
		code = runUDP(args)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n", cmd)
		usage()
		code = 2
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprintf(os.Stderr, "用法: loadgen <probe|hold|connrate|tput|lat|udp> [参数...]\n")
}

// 后五个子命令由同包其它文件实现；此处仅占位以便编译。
func runHold(args []string) int     { return stubSubcommand("hold") }
func runConnrate(args []string) int { return stubSubcommand("connrate") }
func runTput(args []string) int     { return stubSubcommand("tput") }
func runLat(args []string) int      { return stubSubcommand("lat") }
func runUDP(args []string) int      { return stubSubcommand("udp") }

func stubSubcommand(name string) int {
	fmt.Fprintf(os.Stderr, "%s: 尚未实现\n", name)
	return 2
}

type commonFlags struct {
	profilePath, node, proto, echo, udpEcho, sink, name string
	clients                                             int
	prof                                                *profile.Profile
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.profilePath, "profile", "", "profile.json 路径")
	fs.StringVar(&c.node, "node", "", "节点地址（IP）")
	fs.StringVar(&c.proto, "proto", "", "协议 id 或 direct（仅 lat）")
	fs.StringVar(&c.echo, "echo", "", "target tcp-echo 地址")
	fs.StringVar(&c.udpEcho, "udp-echo", "", "target udp-echo 地址")
	fs.StringVar(&c.sink, "sink", "", "target sink 地址")
	fs.IntVar(&c.clients, "clients", 16, "客户端实例数（分散用户）")
	fs.StringVar(&c.name, "name", "", "结果 JSON 的 name 字段")
}

func (c *commonFlags) load() error {
	if c.profilePath == "" {
		return fmt.Errorf("--profile 必填")
	}
	p, err := profile.Load(c.profilePath)
	if err != nil {
		return err
	}
	c.prof = p
	if c.clients < 1 {
		return fmt.Errorf("--clients 必须 ≥ 1")
	}
	if c.proto != "" && c.proto != "direct" && profile.NodeType(c.proto) == "" {
		return fmt.Errorf("未知协议: %s", c.proto)
	}
	return nil
}

func pickUsers(p *profile.Profile, clients int) []profile.User {
	stable := p.StableUsers()
	out := make([]profile.User, clients)
	for k := 0; k < clients; k++ {
		idx := k * stable / clients
		out[k] = profile.User{
			Index: idx,
			UID:   int64(idx + 1),
			UUID:  profile.UserUUID(p.Seed, idx),
		}
	}
	return out
}

func emit(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "json: %v\n", err)
		return
	}
	_, _ = os.Stdout.Write(append(b, '\n'))
}

func signalContext(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	var cancelTime context.CancelFunc
	ctx := parent
	if d > 0 {
		ctx, cancelTime = context.WithTimeout(parent, d)
	}
	ctx, cancelSig := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	return ctx, func() {
		cancelSig()
		if cancelTime != nil {
			cancelTime()
		}
	}
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}

func echoOnce(ctx context.Context, d Dialer, dest string, timeout time.Duration) (connectMs float64, err error) {
	start := time.Now()
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if timeout > 0 && timeout < 10*time.Second {
		dialCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	conn, err := d.DialTCP(dialCtx, dest)
	cancel()
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return 0, err
	}
	if err := roundTrip(conn, buf, timeout); err != nil {
		return 0, err
	}
	return float64(time.Since(start).Microseconds()) / 1000.0, nil
}

// roundTrip 发 buf、收齐同样字节并比对。timeout>0 时只在这一次往返期间设期限，结束后清掉
// （deadlineConn 的期限到了会关连接，长连接在心跳之间不能留着旧期限）。
func roundTrip(conn net.Conn, buf []byte, timeout time.Duration) error {
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
		defer conn.SetDeadline(time.Time{})
	}
	if _, err := conn.Write(buf); err != nil {
		return err
	}
	got := make([]byte, len(buf))
	if _, err := io.ReadFull(conn, got); err != nil {
		return err
	}
	for i := range buf {
		if buf[i] != got[i] {
			return fmt.Errorf("回显不匹配")
		}
	}
	return nil
}
