// [INPUT]: 依赖 core 的 Core 接口与 core/counter 的用户表，依赖 os/exec 拉起 juicity-server
// [OUTPUT]: 对外提供 Juicity、JuicityOptions、NewJuicity、DefaultJuicityWorkDir
// [POS]: pdnd/core/external 的唯一成员，只在 compat 构建里经 core/multi 调用；配置文件缺省写进 pandora-native 唯一可写的状态目录
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package external 托管以独立进程运行的协议实现。
//
// 为什么不把它们链接进来：许可证。Juicity 是 AGPL-3.0，一旦链接，
// 整个节点端二进制（pandora-native）就要在「对外提供网络服务」时向使用者
// 开放源码。以独立进程运行，juicity 按它自己的许可存在，节点端不受它的
// 网络条款约束。
//
// 代价必须说清楚：进程外的连接我们碰不到，因此这类协议
// 无法按用户计流量。协议能用，计费不能用。
package external

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/core/counter"
)

// Juicity 托管一个 juicity-server 进程。
type Juicity struct {
	tag      string
	port     int
	binary   string
	workDir  string
	certPath string
	keyPath  string
	congest  string
	log      *slog.Logger

	users *counter.Table

	mu   sync.Mutex
	cmd  *exec.Cmd
	stop context.CancelFunc
}

type JuicityOptions struct {
	Tag        string
	Port       int
	Binary     string // juicity-server 可执行文件路径
	WorkDir    string // 配置文件存放目录，缺省 DefaultJuicityWorkDir
	CertPath   string
	KeyPath    string
	Congestion string
	Log        *slog.Logger
}

// DefaultJuicityWorkDir 是配置里没写 work_dir 时的配置目录。
//
// pandora-native 以 pandora 用户、ProtectSystem=strict 运行，只有状态目录
// /var/lib/pandora-native 可写（release/pandora-native.service 的
// ReadWritePaths）；旧缺省 /etc/aegis-nodeagent/juicity 写不进去，juicity 起不来。
const DefaultJuicityWorkDir = "/var/lib/pandora-native/juicity"

func NewJuicity(o JuicityOptions) *Juicity {
	binary := o.Binary
	if binary == "" {
		binary = "juicity-server"
	}
	workDir := o.WorkDir
	if workDir == "" {
		workDir = DefaultJuicityWorkDir
	}
	congest := o.Congestion
	if congest == "" {
		congest = "bbr"
	}
	return &Juicity{
		tag: o.Tag, port: o.Port, binary: binary, workDir: workDir,
		certPath: o.CertPath, keyPath: o.KeyPath, congest: congest,
		log:   o.Log.With("inbound", o.Tag, "protocol", "juicity"),
		users: counter.NewTable(),
	}
}

// Traffic 恒为空。
//
// 不是「还没实现」，而是这个架构下做不到：流量在 juicity 进程内部，
// 我们只能看到一个 UDP 端口。返回空而不是返回节点级总量 ——
// 把整机流量摊到某个用户头上，比不计费错得更离谱。
func (j *Juicity) Traffic() []core.UserTraffic { return nil }

// Online 同理为空：在线判定需要看连接的来源 IP 与用户身份的对应关系，
// 那同样在进程内部。
func (j *Juicity) Online() map[int64][]string { return nil }

func (j *Juicity) AddUsers(users []core.User) error {
	if added := j.users.Add(users); len(added) == 0 {
		return nil
	}
	return j.restart()
}

func (j *Juicity) UpsertUsers(users []core.User) error {
	if updated := j.users.Upsert(users); len(updated) == 0 {
		return nil
	}
	return j.restart()
}

func (j *Juicity) DelUsers(uuids []string) error {
	if gone := j.users.Del(uuids); len(gone) == 0 {
		return nil
	}
	return j.restart()
}

func (j *Juicity) Start() error {
	if _, err := exec.LookPath(j.binary); err != nil {
		return fmt.Errorf("找不到 juicity-server（%s）：%w", j.binary, err)
	}
	if j.certPath == "" || j.keyPath == "" {
		return fmt.Errorf("juicity 需要 cert_path 与 key_path")
	}
	j.log.Warn("juicity 以独立进程运行，该节点无法按用户计流量")
	return j.restart()
}

func (j *Juicity) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stopLocked()
}

func (j *Juicity) stopLocked() error {
	if j.stop != nil {
		j.stop()
		j.stop = nil
	}
	if j.cmd == nil || j.cmd.Process == nil {
		j.cmd = nil
		return nil
	}
	proc := j.cmd.Process
	j.cmd = nil

	// 先请它自己退出，给一小段时间收尾；不听话再强杀。
	// 直接 SIGKILL 会让已建立的连接毫无征兆地断开。
	_ = proc.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = proc.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = proc.Kill()
	}
	return nil
}

func (j *Juicity) restart() error {
	j.mu.Lock()
	defer j.mu.Unlock()

	_ = j.stopLocked()

	list := j.users.Snapshot()
	if len(list) == 0 {
		return nil
	}
	cfgPath, err := j.writeConfig(list)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, j.binary, "run", "-c", cfgPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("启动 juicity-server: %w", err)
	}
	j.cmd = cmd
	j.stop = cancel

	go func() {
		err := cmd.Wait()
		select {
		case <-ctx.Done():
			// 我们主动停的，正常
			return
		default:
		}
		// 进程自己挂了。不在这里自动重启：紧接着的下一次用户同步
		// 会调用 restart，那时重来一次即可；在这里再加一个重启循环
		// 只会让两条路径打架。
		j.log.Error("juicity-server 意外退出", "err", err)
	}()

	j.log.Info("juicity 已启动", "port", j.port, "用户数", len(list))
	return nil
}

// writeConfig 生成 juicity-server 的配置文件。
func (j *Juicity) writeConfig(users []core.User) (string, error) {
	if err := os.MkdirAll(j.workDir, 0o700); err != nil {
		return "", err
	}
	userMap := make(map[string]string, len(users))
	for _, u := range users {
		// 密码沿用 UUID，与其它协议保持一致
		userMap[u.UUID] = u.UUID
	}
	cfg := map[string]any{
		"listen":             fmt.Sprintf(":%d", j.port),
		"users":              userMap,
		"certificate":        j.certPath,
		"private_key":        j.keyPath,
		"congestion_control": j.congest,
		"log_level":          "warn",
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}

	path := filepath.Join(j.workDir, j.tag+".json")
	// 配置里是全部用户的凭据，权限必须收紧
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
