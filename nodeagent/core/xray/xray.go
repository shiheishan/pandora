// Package xray 基于 xray-core 实现内核层。
//
// 与 sing-box 那边最大的不同：这里几乎不用改上游代码。
// xray 继承了 v2ray 的设计，用户管理（proxy.UserManager）和按用户统计
// （stats.Manager 的 counter / onlinemap）本来就是公开接口，
// 加人删人取流量全都是现成的 —— sing-box 那边要自己重写 inbound 才能拿到的东西，
// 在这里是开箱即用。
//
// 取舍也很清楚：xray 的配置是 protobuf，构造起来比 JSON 繁琐得多，
// 而且协议集合比 sing-box 小（没有 Naive、AnyTLS、Mieru）。
// 两个内核并存的价值在于，同一个协议可以换一种实现 ——
// 比如 XHTTP 传输只有 xray 有，某些客户端也只认 xray 的实现细节。
//
// 许可证：xray-core 是 MPL-2.0，与 GPL-3.0 兼容，不改变本模块的分发条件。
package xray

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	xcore "github.com/xtls/xray-core/core"

	"github.com/xtls/xray-core/app/dispatcher"
	xlog "github.com/xtls/xray-core/app/log"
	"github.com/xtls/xray-core/app/policy"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/app/stats"
	clog "github.com/xtls/xray-core/common/log"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	xinbound "github.com/xtls/xray-core/features/inbound"
	xstats "github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/proxy/blackhole"
	"github.com/xtls/xray-core/proxy/freedom"
	"github.com/xtls/xray-core/transport/internet"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/core/counter"
)

// Core 是 xray-core 内核的封装。
type Core struct {
	log      *slog.Logger
	mu       sync.RWMutex
	instance *xcore.Instance
	inbounds map[string]*core.InboundConfig
	// 每个入站的用户表。xray 按 email 索引用户，而面板要的是整数 ID，
	// 这张表负责在两者之间换算。
	users   map[string]*counter.Table
	routing map[string]*core.Routing
	ctx     context.Context
}

func New(log *slog.Logger) *Core {
	return &Core{
		log:      log.With("kernel", "xray-core"),
		inbounds: make(map[string]*core.InboundConfig),
		users:    make(map[string]*counter.Table),
		routing:  make(map[string]*core.Routing),
	}
}

func (c *Core) Type() string { return "xray-core" }

// Start 起一个空实例。入站随后通过 AddInbound 挂上去 ——
// 这是 xray 相对 sing-box 的一个实打实的优势：加减入站不必重建实例。
func (c *Core) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ctx = ctx

	inst, err := xcore.New(&xcore.Config{
		App: []*serial.TypedMessage{
			// 没有 log 模块时 xray 会把内部错误直接丢掉 ——
			// 出问题时表现为「连接超时」而没有任何线索，极难排查。
			// 输出到 stderr，由 systemd 收进 journal，与我们自己的日志并排。
			serial.ToTypedMessage(&xlog.Config{
				ErrorLogType:  xlog.LogType_Console,
				ErrorLogLevel: clog.Severity_Debug,
			}),
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
			serial.ToTypedMessage(&stats.Config{}),
			// 路由模块。不加的话 GetFeature(RouterType) 拿不到东西，
			// 分流会以「xray 缺少 router」失败 —— 而入站照常工作，
			// 所以这个疏漏只在配了分流之后才暴露。
			serial.ToTypedMessage(&router.Config{}),
			// 用户级统计默认是关的，必须在 policy 里显式打开，
			// 否则 counter 根本不会被创建，取流量永远是 0
			serial.ToTypedMessage(&policy.Config{
				Level: map[uint32]*policy.Policy{
					0: {Stats: &policy.Policy_Stats{
						UserUplink:   true,
						UserDownlink: true,
						UserOnline:   true,
					}},
				},
			}),
		},
		// 两个内建出站：direct 是默认出口（也是 xray 的第一个出站，
		// 没有规则命中时走它），block 让「拒绝」类规则不必额外配出站
		Outbound: []*xcore.OutboundHandlerConfig{
			{Tag: outboundDirect, ProxySettings: serial.ToTypedMessage(&freedom.Config{
				// 兜底直出同样优先 IPv4，理由见 routing.go 里 direct 那段。
				// 绝大多数流量走的是这一条，只改分流里显式配的那个不够。
				DomainStrategy: internet.DomainStrategy_USE_IP46,
			})},
			{Tag: outboundBlock, ProxySettings: serial.ToTypedMessage(&blackhole.Config{})},
		},
	})
	if err != nil {
		return fmt.Errorf("创建 xray 实例: %w", err)
	}
	if err := inst.Start(); err != nil {
		return fmt.Errorf("启动 xray: %w", err)
	}
	c.instance = inst

	// 实例起来之前记下的分流配置在这里补上
	if len(c.routing) > 0 {
		snapshot := make(map[string]*core.Routing, len(c.routing))
		for k, v := range c.routing {
			snapshot[k] = v
		}
		if err := c.applyRouting(inst, snapshot); err != nil {
			c.log.Error("重放分流配置失败", "err", err)
		}
	}
	return nil
}

func (c *Core) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.instance == nil {
		return nil
	}
	err := c.instance.Close()
	c.instance = nil
	return err
}

//------------------------------------------------------------------------------
// 入站
//------------------------------------------------------------------------------

func (c *Core) AddInbound(cfg *core.InboundConfig) (err error) {
	// 一个节点的配置不该能打挂整台机器。
	//
	// xray 内部有若干处是直接把切片转成定长数组的（例如 REALITY 的
	// short id 要求正好 8 字节），面板下发的数据只要差一个字节就是 panic。
	// 没有这层 recover 的话，一个配错的节点会带走 agent 进程，
	// 而这台机器上其它本来好好的节点跟着一起下线。
	//
	// 捕获后把它变成这个入站的错误：调用方会记日志、跳过它、继续下一个。
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("入站 %s 启动时崩溃（配置多半有问题）: %v", cfg.Tag, r)
		}
	}()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.instance == nil {
		return fmt.Errorf("xray 实例尚未启动")
	}

	handlerCfg, err := buildInbound(cfg)
	if err != nil {
		return fmt.Errorf("入站 %s 配置无效: %w", cfg.Tag, err)
	}

	mgr, err := c.inboundManager()
	if err != nil {
		return err
	}
	// 同 tag 重复添加会被拒，先摘掉旧的。忽略错误：不存在是正常情况
	_ = mgr.RemoveHandler(c.ctx, cfg.Tag)

	handler, err := xcore.CreateObject(c.instance, handlerCfg)
	if err != nil {
		return fmt.Errorf("构建入站 %s: %w", cfg.Tag, err)
	}
	ih, ok := handler.(xinbound.Handler)
	if !ok {
		return fmt.Errorf("入站 %s 不是合法的 handler", cfg.Tag)
	}
	if err := mgr.AddHandler(c.ctx, ih); err != nil {
		return fmt.Errorf("挂载入站 %s: %w", cfg.Tag, err)
	}

	c.inbounds[cfg.Tag] = cfg
	table, exists := c.users[cfg.Tag]
	if !exists {
		table = counter.NewTable()
		c.users[cfg.Tag] = table
	}

	// 把用户重新注入。
	//
	// RemoveHandler 会连同入站里的用户一起丢掉，而上层的同步循环
	// 是按增量工作的 —— 它记得「这些人已经下发过」，不会再发一次。
	// 结果就是入站还在、端口还通，但没有一个用户能认证成功。
	// sing-box 那边踩过同样的坑（那边是重建 box），这里是重建入站。
	if n := table.Count(); n > 0 {
		um, err := c.userManagerLocked(cfg.Tag)
		if err != nil {
			return fmt.Errorf("入站 %s 重建后取用户管理器: %w", cfg.Tag, err)
		}
		var failed int
		for _, u := range table.Snapshot() {
			account, err := buildAccount(cfg, u)
			if err != nil {
				failed++
				continue
			}
			if err := um.AddUser(c.ctx,
				&protocol.MemoryUser{Email: u.UUID, Level: 0, Account: account}); err != nil {
				failed++
			}
		}
		if failed > 0 {
			c.log.Error("入站重建后重放用户失败", "入站", cfg.Tag, "失败数", failed, "共", n)
		}
	}
	return nil
}

func (c *Core) DelInbound(tag string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.inbounds, tag)
	delete(c.users, tag)
	if c.instance == nil {
		return nil
	}
	mgr, err := c.inboundManager()
	if err != nil {
		return err
	}
	return mgr.RemoveHandler(c.ctx, tag)
}

//------------------------------------------------------------------------------
// 用户
//------------------------------------------------------------------------------

func (c *Core) AddUsers(tag string, users []core.User) error {
	c.mu.RLock()
	cfg, okCfg := c.inbounds[tag]
	table, okTable := c.users[tag]
	c.mu.RUnlock()
	if !okCfg || !okTable {
		return fmt.Errorf("入站 %s 不存在", tag)
	}

	um, err := c.userManager(tag)
	if err != nil {
		return err
	}
	added := table.Add(users)
	var failed int
	var firstErr error
	for _, u := range added {
		account, err := buildAccount(cfg, u)
		if err != nil {
			return err
		}
		// email 就用 UUID：统计 counter 的名字由 email 拼出来，
		// 让它等于 UUID 才能在取流量时一步对上号
		mu := &protocol.MemoryUser{Email: u.UUID, Level: 0, Account: account}
		if err := um.AddUser(c.ctx, mu); err != nil {
			// 单个用户失败不该拖垮整批 —— 常见原因是 UUID 格式非法，
			// 那是这一个人的问题，其余人照常服务。
			// 但绝不能静默：本地表要同步摘掉（否则下一轮 diff 认为已下发，
			// 这个人就永远连不上且无人知晓），并且必须留下痕迹。
			table.Del([]string{u.UUID})
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
	}
	if failed > 0 {
		c.log.Error("部分用户添加失败", "入站", tag, "失败数", failed,
			"共", len(added), "首个错误", firstErr)
	}
	return nil
}

func (c *Core) DelUsers(tag string, uuids []string) error {
	c.mu.RLock()
	table, ok := c.users[tag]
	c.mu.RUnlock()
	if !ok {
		return nil
	}
	um, err := c.userManager(tag)
	if err != nil {
		return err
	}
	for _, id := range table.Del(uuids) {
		_ = um.RemoveUser(c.ctx, id)
	}
	return nil
}

//------------------------------------------------------------------------------
// 统计
//------------------------------------------------------------------------------

// GetTraffic 取出并清零。
//
// Counter.Set(0) 返回旧值，正好是「读取并清零」的原子操作 ——
// 与我们在 sing-box 那边手写的 drain 语义一致。
func (c *Core) GetTraffic(tag string) ([]core.UserTraffic, error) {
	c.mu.RLock()
	table, ok := c.users[tag]
	inst := c.instance
	c.mu.RUnlock()
	if !ok || inst == nil {
		return nil, nil
	}
	sm, err := statsManager(inst)
	if err != nil {
		return nil, err
	}

	out := make([]core.UserTraffic, 0, table.Count())
	for _, u := range table.Snapshot() {
		var up, down int64
		if ctr := sm.GetCounter("user>>>" + u.UUID + ">>>traffic>>>uplink"); ctr != nil {
			up = ctr.Set(0)
		}
		if ctr := sm.GetCounter("user>>>" + u.UUID + ">>>traffic>>>downlink"); ctr != nil {
			down = ctr.Set(0)
		}
		if up == 0 && down == 0 {
			continue
		}
		out = append(out, core.UserTraffic{ID: u.ID, Upload: up, Download: down})
	}
	return out, nil
}

// OnlineIPs 直接用 xray 的 onlinemap。
// 这一项 sing-box 那边要自己记，xray 原生就有。
func (c *Core) OnlineIPs(tag string) map[int64][]string {
	c.mu.RLock()
	table, ok := c.users[tag]
	inst := c.instance
	c.mu.RUnlock()
	if !ok || inst == nil {
		return nil
	}
	sm, err := statsManager(inst)
	if err != nil {
		return nil
	}

	out := make(map[int64][]string)
	for _, u := range table.Snapshot() {
		om := sm.GetOnlineMap("user>>>" + u.UUID + ">>>online")
		if om == nil {
			continue
		}
		if ips := om.List(); len(ips) > 0 {
			out[u.ID] = ips
		}
	}
	return out
}

//------------------------------------------------------------------------------
// 内部工具
//------------------------------------------------------------------------------

func (c *Core) inboundManager() (xinbound.Manager, error) {
	if c.instance == nil {
		return nil, fmt.Errorf("xray 实例尚未启动")
	}
	mgr, ok := c.instance.GetFeature(xinbound.ManagerType()).(xinbound.Manager)
	if !ok {
		return nil, fmt.Errorf("xray 缺少 inbound manager")
	}
	return mgr, nil
}

func statsManager(inst *xcore.Instance) (xstats.Manager, error) {
	sm, ok := inst.GetFeature(xstats.ManagerType()).(xstats.Manager)
	if !ok {
		return nil, fmt.Errorf("xray 缺少 stats manager")
	}
	return sm, nil
}

// userManager 取出某个入站的用户管理器。
func (c *Core) userManager(tag string) (proxy.UserManager, error) {
	return c.userManagerLocked(tag)
}

// userManagerLocked 与 userManager 相同，但不碰锁 ——
// 供已经持有 c.mu 的调用方使用（如 AddInbound 里的用户重放）。
func (c *Core) userManagerLocked(tag string) (proxy.UserManager, error) {
	mgr, err := c.inboundManager()
	if err != nil {
		return nil, err
	}
	handler, err := mgr.GetHandler(c.ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("取入站 %s: %w", tag, err)
	}
	gi, ok := handler.(proxy.GetInbound)
	if !ok {
		return nil, fmt.Errorf("入站 %s 不支持用户管理", tag)
	}
	um, ok := gi.GetInbound().(proxy.UserManager)
	if !ok {
		return nil, fmt.Errorf("入站 %s 的协议不支持用户管理", tag)
	}
	return um, nil
}

// listenAddr 把配置里的监听地址翻译成 xray 的形式。
func listenAddr(listen string) *xnet.IPOrDomain {
	if listen == "" || listen == "::" || listen == "0.0.0.0" {
		return xnet.NewIPOrDomain(xnet.AnyIP)
	}
	return xnet.NewIPOrDomain(xnet.ParseAddress(listen))
}

func receiverConfig(cfg *core.InboundConfig, stream *internet.StreamConfig) *proxyman.ReceiverConfig {
	return &proxyman.ReceiverConfig{
		PortList: &xnet.PortList{Range: []*xnet.PortRange{{
			From: uint32(cfg.Port), To: uint32(cfg.Port),
		}}},
		Listen:         listenAddr(cfg.Listen),
		StreamSettings: stream,
	}
}

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

var _ core.Core = (*Core)(nil)
