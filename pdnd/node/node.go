// Package node 把面板与内核粘起来：拉配置、同步用户、上报流量。
package node

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// Node 是一个受面板管理的入站。
type Node struct {
	client *panel.Client
	signed *panel.SignedClient
	kernel core.Core
	log    *slog.Logger
	tag    string

	pullInterval   time.Duration
	pushInterval   time.Duration
	statusInterval time.Duration
	// userVersion 是当前用户列表的版本，用来判断收到的增量能不能打。
	userVersion string
	// events 收面板推来的事件。带缓冲：推送方（Stream 那个 goroutine）
	// 不该因为主循环正忙着同步配置而阻塞。
	events chan panel.StreamEvent

	// 已下发给内核的用户，用于算增量。
	// 面板每次返回全量列表，本地存一份才能知道该加谁、该删谁 ——
	// 每次全量重推会让所有在线用户的连接被打断。
	known map[string]core.User
	// 入站是否已建立。配置拉到之前不能同步用户。
	started              bool
	activeConfig         map[string]any
	appliedConfigVersion int
	appliedConfigHash    string
	appliedReleaseID     string
	appliedGeneration    uint64
	appliedAt            time.Time
	// failedSigned 记下最近一个装不上的签名配置版本与重试节拍，见 signed_config.go。
	failedSigned *signedApplyFailure
	// compatFailure 是兼容通道的同一件事：旧配置仍在服务、新配置装不上，按退避重试。
	compatFailure *compatApplyFailure
	// lastApplyErr 是最近一次配置应用失败的原因（成功即清空），degraded 上报用。
	lastApplyErr error
	// appliedSigned 是已应用的那份签名配置；switchedSettled / healthSettled 记它的
	// 两个阶段是否已被面板收下（或明确拒收），收下之后不再重报。
	appliedSigned   *panel.SignedConfig
	switchedSettled bool
	healthSettled   bool
	// tenantID 是签名配置里的租户，与面板地址一起组成端口登记的归属范围。
	tenantID string

	// cache 是本节点的落盘缓存（nil 即不落盘），见 cache.go。fromCache 表示正在用
	// 缓存服务、面板还没确认过；usersDirty 表示用户名单有了还没落盘的变化。
	cache      *nodeCache
	fromCache  bool
	usersDirty bool
	// offlineStart 只在冷启动走落盘缓存装入站的那一刻为真（install_users.go）。
	offlineStart bool

	// traffic 是还没被面板收下的流量，见 report.go。
	traffic trafficBuffer

	// order / orderIndex 决定冷启动时谁先装入站（同机端口先到先得），见 startup.go。
	order      *StartupOrder
	orderIndex int
	// lifeCtx 是 Run 的 ctx，冷启动排队等待时用来响应退出。
	lifeCtx context.Context

	// now 只给测试替换（装失败的退避重试按它算），nil 即 time.Now。
	now func() time.Time
}

func New(client *panel.Client, kernel core.Core, log *slog.Logger) *Node {
	return NewWithSignedClient(client, kernel, log, nil)
}

func NewWithSignedClient(client *panel.Client, kernel core.Core, log *slog.Logger, signed *panel.SignedClient) *Node {
	n := &Node{
		client: client,
		signed: signed,
		kernel: kernel,
		// type 不在构造时定死：面板下发的协议会经 SetNodeType 改掉它（protocolFrom），
		// 日志要跟着变；按条从 client 现取，事件流 goroutine 读也不竞争（原子值）
		log: slog.New(nodeTypeHandler{inner: log.With("node", client.NodeID()).Handler(), client: client}),
		tag: client.NodeType() + "-" + client.NodeID(),
		// 面板会在 base_config 里下发真实间隔，这里只是拿不到时的兜底
		pullInterval: 60 * time.Second,
		pushInterval: 60 * time.Second,
		// 状态上报比流量上报更该准时：后台判断节点死活就看这个时间戳。
		// 30 秒是权衡——再长了挂掉之后要等很久才在面板上变色，再短了
		// 每次都要采一轮 CPU（含 100ms 采样），纯属浪费。
		statusInterval: 30 * time.Second,
		events:         make(chan panel.StreamEvent, 32),
		known:          make(map[string]core.User),
	}
	if signed != nil {
		signed.OnKeyCheckError(func(err error) {
			n.log.Warn("例行换钥检查失败，本轮照常拉配置与用户", "err", err)
		})
	}
	return n
}

func (n *Node) Tag() string { return n.tag }

func (n *Node) clock() time.Time {
	if n.now != nil {
		return n.now()
	}
	return time.Now()
}

// Run 一直跑到 ctx 取消。退出时不在这里做最后一次上报：要先关内核让在途连接
// 把流量入账，由调用方随后调 Shutdown（见 main）。
//
// 三条节拍——拉取（配置 + 用户）、上报（流量 + 在线）、状态——与事件流在同一个
// select 里串行处理，用户镜像因此不用加锁。
func (n *Node) Run(ctx context.Context) {
	n.lifeCtx = ctx
	// 先同步一次再进循环，否则节点要等一个完整周期才开始服务。面板不可达时
	// 先用落盘缓存起服务（startup.go）。
	n.startup(ctx)
	if ctx.Err() != nil {
		return
	}

	// 三条节拍都用定时器而不是 ticker：每轮各自带 ±10% 抖动（panel.Jitter），
	// 同时装好、同时启动的一批节点几轮之后就不再踩同一秒打面板。
	pull := time.NewTimer(panel.Jitter(n.pullInterval))
	push := time.NewTimer(panel.Jitter(n.pushInterval))
	status := time.NewTimer(panel.Jitter(n.statusInterval))
	defer pull.Stop()
	defer push.Stop()
	defer status.Stop()
	curPull, curPush := n.pullInterval, n.pushInterval

	// 先报一次，别让面板等满一个周期才知道这个节点起来了（没起来就如实报 degraded）。
	n.reportStatus(ctx)

	// 事件流：面板有变更时立刻推下来，省掉轮询那一个周期的等待。
	//
	// 轮询不停。流是加速通路不是替代：它断了、丢消息了、面板那边没启用，
	// 轮询都还在按原节奏走。停掉轮询的话，流一断节点就彻底聋了，而 SSE
	// 断连未必有明显信号。
	go n.client.Stream(ctx, n.events, func(err error) {
		if errors.Is(err, panel.ErrStreamUnsupported) {
			n.log.Info("面板不支持事件流，只走轮询")
			return
		}
		// 连不上很常见（面板重启、网络抖动），记 Info 不记 Error——
		// 记成 Error 会让日志里全是它，真正的问题反而被埋掉。
		n.log.Info("事件流断开，将退避重连", "err", err)
	})

	for {
		select {
		case <-ctx.Done():
			return
		case <-pull.C:
			n.syncOnce(ctx)
			pull.Reset(panel.Jitter(n.pullInterval))
		case <-push.C:
			n.report(ctx)
			push.Reset(panel.Jitter(n.pushInterval))
		case <-status.C:
			n.reportStatus(ctx)
			status.Reset(panel.Jitter(n.statusInterval))
		case ev := <-n.events:
			n.applyStreamEvent(ctx, ev)
		}

		// 面板可以在 base_config 里改这两个节拍，applyConfig 会写进字段，
		// 但定时器是按旧值排的 —— 不在这里重排，改下来的值就只是
		// 存了个变量，行为一点没变。面板把拉取间隔从 60 秒调到 15 秒之后
		// 实测节点仍然 60 秒一次，就是栽在这一步。
		if n.pullInterval != curPull && n.pullInterval > 0 {
			pull.Reset(panel.Jitter(n.pullInterval))
			curPull = n.pullInterval
			n.log.Info("拉取间隔已调整", "秒", int(curPull.Seconds()))
		}
		if n.pushInterval != curPush && n.pushInterval > 0 {
			push.Reset(panel.Jitter(n.pushInterval))
			curPush = n.pushInterval
			n.log.Info("上报间隔已调整", "秒", int(curPush.Seconds()))
		}
	}
}

// applyStreamEvent 处理一条面板推来的事件。
//
// 在主循环的 select 里调用，和轮询是同一个 goroutine——这一点是有意的：
// 两条路都会改 n.known，让它们串行执行就不需要为这份状态加锁，也不会
// 出现「轮询拉到的旧列表覆盖掉刚推下来的新列表」。
func (n *Node) applyStreamEvent(ctx context.Context, ev panel.StreamEvent) {
	switch ev.Type {
	case panel.EventSyncConfig:
		// 配置内容不从事件里取，而是回头拉一次 REST。
		//
		// 那条路上有签名校验、分流解析、端口合法性检查一整套，复制到
		// 这里迟早会和 REST 那份走样。事件在这里只当一个「有变化了，
		// 现在就去拉」的信号——省掉的是等待，不是那些校验。
		//
		// 拉完配置紧接着拉用户，和轮询走同一个 syncOnce：配置变了就意味着
		// 入站重建、内核用户表已清空，等下一个轮询节拍再补，中间这段时间
		// 谁也连不上。配置没变时这一下只是一次 304，不值得为省它另开分支。
		n.syncOnce(ctx)

	case panel.EventSyncUsers:
		if err := n.applyUsers(ev.Users); err != nil {
			n.log.Error("按事件同步用户失败", "err", err)
			return
		}
		n.userVersion = ev.Version
		// 同步给客户端，让下一轮轮询带上这个版本换 304，不用重复拉
		n.client.SetUsersVersion(ev.Version)

	case panel.EventSyncUserDelta:
		if ev.FromVersion != n.userVersion {
			// 基准对不上：这条增量是基于我们没有的那一版算出来的。
			// 硬打上去会留下一批本该删掉的用户还在放行——比不打更糟。
			n.log.Info("增量基准版本对不上，改拉全量",
				"本地", n.userVersion, "增量基于", ev.FromVersion)
			if err := n.syncUsers(ctx); err != nil {
				n.log.Error("拉全量用户失败", "err", err)
			}
			return
		}
		if err := n.applyUserDelta(ev); err != nil {
			n.log.Error("应用用户增量失败", "err", err)
			return
		}
		n.userVersion = ev.ToVersion
		n.client.SetUsersVersion(ev.ToVersion)
	}
}

// protocolFrom 取这一轮该用哪个协议。
//
// 优先用面板下发的。原先这里写死用本地 config.json 里的 node_type，
// 于是在面板上把节点从 shadowsocks 改成 vless，端口和参数都跟着变了、
// 协议却没变——节点端拿 ss 的适配器去解析 vless 的配置，要么起不来，
// 要么起来了但行为对不上。运维只能上服务器改 config.json 再重启，
// 而「不用手动碰节点端」正是这套下发机制存在的理由。
//
// 面板的认证只看 node_id + token，不校验 URL 上的 node_type，所以换了
// 协议之后节点端沿用旧参数请求也照样能拉到配置，不需要同步改本地文件。
//
// 面板没给就回落到本地值：老版本面板不下发 protocol 字段，回落让节点
// 至少还能按原协议服务，而不是因为读不到就整个起不来。
func (n *Node) protocolFrom(cfg map[string]any) string {
	p, _ := cfg["protocol"].(string)
	p = strings.TrimSpace(p)
	if p == "" {
		return n.client.NodeType()
	}
	if p != n.client.NodeType() {
		n.log.Info("协议已按面板下发切换", "从", n.client.NodeType(), "到", p)
		// 记回客户端，之后的请求都带新协议。不记的话每一轮都会重新
		// 打这条日志，面板那边也会一直记「协议不一致」。
		n.client.SetNodeType(p)
	}
	return p
}

// nodeTypeHandler 给每条日志补上节点当前的协议（type）。
//
// 原先构造时 log.With("type", …) 把协议写死，面板下发换了协议之后日志还报旧协议，
// 排查「这台到底在跑什么」时只会误导。每条现取 client.NodeType()（原子读），既跟得上
// SetNodeType，也不用在主循环里换 logger（事件流 goroutine 同时在用它）。
type nodeTypeHandler struct {
	inner  slog.Handler
	client *panel.Client
}

func (h nodeTypeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h nodeTypeHandler) Handle(ctx context.Context, r slog.Record) error {
	r = r.Clone()
	r.AddAttrs(slog.String("type", h.client.NodeType()))
	return h.inner.Handle(ctx, r)
}

func (h nodeTypeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return nodeTypeHandler{inner: h.inner.WithAttrs(attrs), client: h.client}
}

func (h nodeTypeHandler) WithGroup(name string) slog.Handler {
	return nodeTypeHandler{inner: h.inner.WithGroup(name), client: h.client}
}
