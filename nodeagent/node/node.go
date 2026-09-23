// Package node 把面板与内核粘起来：拉配置、同步用户、上报流量。
package node

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// Node 是一个受面板管理的入站。
type Node struct {
	client *panel.Client
	kernel core.Core
	log    *slog.Logger
	tag    string

	pullInterval time.Duration
	pushInterval time.Duration

	// 已下发给内核的用户，用于算增量。
	// 面板每次返回全量列表，本地存一份才能知道该加谁、该删谁 ——
	// 每次全量重推会让所有在线用户的连接被打断。
	known map[string]core.User
	// 入站是否已建立。配置拉到之前不能同步用户。
	started bool

	// host 采集宿主机资源，供状态上报。带状态（要记住上次 CPU 读数），
	// 所以存在 Node 上而不是每次现建。
	host *core.HostStat
}

func New(client *panel.Client, kernel core.Core, log *slog.Logger) *Node {
	return &Node{
		client: client,
		kernel: kernel,
		log:    log.With("node", client.NodeID(), "type", client.NodeType()),
		tag:    client.NodeType() + "-" + client.NodeID(),
		// 面板会在 base_config 里下发真实间隔，这里只是拿不到时的兜底
		pullInterval: 60 * time.Second,
		pushInterval: 60 * time.Second,
		known:        make(map[string]core.User),
		host:         core.NewHostStat(),
	}
}

func (n *Node) Tag() string { return n.tag }

// Run 一直跑到 ctx 取消。
//
// 两条独立的节拍：拉取（配置 + 用户）和上报（流量 + 在线）。
// 分开是因为它们的失败后果完全不同 —— 拉取失败只是配置滞后，
// 上报失败会丢流量数据。混在一个循环里，一方超时会拖累另一方。
func (n *Node) Run(ctx context.Context) {
	// 先同步一次再进循环，否则节点要等一个完整周期才开始服务
	n.syncOnce(ctx)

	pull := time.NewTicker(n.pullInterval)
	push := time.NewTicker(n.pushInterval)
	defer pull.Stop()
	defer push.Stop()
	curPull, curPush := n.pullInterval, n.pushInterval

	for {
		select {
		case <-ctx.Done():
			// 退出前把最后一段流量交上去。这几秒的数据同样是钱，
			// 进程重启（升级、改配置）时丢掉它是没必要的损失。
			n.report(context.WithoutCancel(ctx))
			return
		case <-pull.C:
			n.syncOnce(ctx)
		case <-push.C:
			n.report(ctx)
		}

		// 面板可以在 base_config 里改这两个节拍，applyConfig 会写进字段，
		// 但 ticker 是启动时按旧值建的 —— 不在这里重置，改下来的值就只是
		// 存了个变量，行为一点没变。面板把拉取间隔从 60 秒调到 15 秒之后
		// 实测节点仍然 60 秒一次，就是栽在这一步。
		if n.pullInterval != curPull && n.pullInterval > 0 {
			pull.Reset(n.pullInterval)
			curPull = n.pullInterval
			n.log.Info("拉取间隔已调整", "秒", int(curPull.Seconds()))
		}
		if n.pushInterval != curPush && n.pushInterval > 0 {
			push.Reset(n.pushInterval)
			curPush = n.pushInterval
			n.log.Info("上报间隔已调整", "秒", int(curPush.Seconds()))
		}
	}
}

func (n *Node) syncOnce(ctx context.Context) {
	if err := n.syncConfig(ctx); err != nil {
		n.log.Error("同步配置失败", "err", err)
		return
	}
	if !n.started {
		return
	}
	if err := n.syncUsers(ctx); err != nil {
		n.log.Error("同步用户失败", "err", err)
	}
}

func (n *Node) syncConfig(ctx context.Context) error {
	cfg, changed, err := n.client.Config(ctx)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}

	port := intFrom(cfg, "server_port")
	if port <= 0 || port > 65535 {
		return errInvalidPort(port)
	}

	if base, ok := cfg["base_config"].(map[string]any); ok {
		if v := intFrom(base, "pull_interval"); v > 0 {
			n.pullInterval = time.Duration(v) * time.Second
		}
		if v := intFrom(base, "push_interval"); v > 0 {
			n.pushInterval = time.Duration(v) * time.Second
		}
	}

	kernel, _ := cfg["kernel"].(string)
	inbound := &core.InboundConfig{
		Tag:      n.tag,
		Protocol: n.client.NodeType(),
		Port:     port,
		Kernel:   kernel,
		Raw:      cfg,
	}
	if err := n.kernel.AddInbound(inbound); err != nil {
		return err
	}

	// 分流跟着配置一起下发。放在 AddInbound 之后：规则要限定到入站，
	// 入站还不存在时下发规则没有意义。
	if err := n.kernel.SetRouting(n.tag, parseRouting(cfg)); err != nil {
		// 分流配错不该让节点彻底不可用 —— 入站已经起来了，
		// 用户还能连上，只是没有分流（全部直出）。记录下来等人处理。
		n.log.Error("下发分流配置失败，该节点暂时全部直出", "err", err)
	}

	// 入站重建会丢掉内核里的用户表，本地记录必须一并清空，
	// 否则下一轮 diff 会认为「都已下发」，结果谁也连不上。
	n.known = make(map[string]core.User)
	n.started = true
	n.log.Info("入站已就绪", "port", port)
	return nil
}

// parseRouting 从面板下发的配置里取出出站与分流。
//
// 面板不下发这两个键时返回 nil，表示「这个节点没配分流」，
// 与「配了一份空的」不同：前者保持默认直出，后者会清掉已有规则。
func parseRouting(cfg map[string]any) *core.Routing {
	outs, _ := cfg["outbounds"].([]any)
	routes, _ := cfg["routes"].([]any)
	if len(outs) == 0 && len(routes) == 0 {
		return nil
	}

	r := &core.Routing{}
	for _, item := range outs {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		o := core.Outbound{}
		o.Tag, _ = m["tag"].(string)
		o.Type, _ = m["type"].(string)
		if s, ok := m["settings"].(map[string]any); ok {
			o.Settings = s
		}
		if o.Tag != "" && o.Type != "" {
			r.Outbounds = append(r.Outbounds, o)
		}
	}
	for _, item := range routes {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rt := core.Route{}
		rt.OutboundTag, _ = m["outbound"].(string)
		if mm, ok := m["matcher"].(map[string]any); ok {
			rt.Matcher = mm
		}
		if rt.OutboundTag != "" {
			r.Routes = append(r.Routes, rt)
		}
	}
	if len(r.Outbounds) == 0 && len(r.Routes) == 0 {
		return nil
	}
	return r
}

func (n *Node) syncUsers(ctx context.Context) error {
	users, err := n.client.Users(ctx)
	if err != nil {
		return err
	}

	want := make(map[string]core.User, len(users))
	var added []core.User
	for _, u := range users {
		if u.UUID == "" {
			continue
		}
		want[u.UUID] = u
		if _, ok := n.known[u.UUID]; !ok {
			added = append(added, u)
		}
	}
	var removed []string
	for uuid := range n.known {
		if _, ok := want[uuid]; !ok {
			removed = append(removed, uuid)
		}
	}

	if len(added) > 0 {
		if err := n.kernel.AddUsers(n.tag, added); err != nil {
			return err
		}
	}
	if len(removed) > 0 {
		if err := n.kernel.DelUsers(n.tag, removed); err != nil {
			return err
		}
	}
	n.known = want

	if len(added) > 0 || len(removed) > 0 {
		n.log.Info("用户已同步", "总数", len(want), "新增", len(added), "移除", len(removed))
	}
	return nil
}

// report 上报流量与在线 IP。
func (n *Node) report(ctx context.Context) {
	if !n.started {
		return
	}

	// 状态上报放在最前面，而且不受后面任何一步的成败影响。
	//
	// 它是面板判断这个节点「还活着」的唯一依据 —— nodes.last_heartbeat_at
	// 只由这个接口写入，而订阅下发会跳过从未上报过心跳的节点。所以哪怕
	// 取流量失败提前 return 了，心跳也得先发出去；否则一个内核出问题的
	// 节点会连同心跳一起消失，面板既看不到它异常，也说不清它是不是没装。
	if err := n.client.Status(ctx, n.host.Read()); err != nil {
		n.log.Warn("上报运行状态失败", "err", err)
	}

	traffic, err := n.kernel.GetTraffic(n.tag)
	if err != nil {
		n.log.Error("读取流量失败", "err", err)
		return
	}
	if len(traffic) > 0 {
		if err := n.client.Push(ctx, traffic); err != nil {
			// 流量已经从内核取出并清零，上报失败就真的丢了。
			// 这里不重试：重试要么阻塞下一轮统计，要么需要一个
			// 持久化队列 —— 后者才是正解，但属于下一步的事，
			// 现在至少要把丢失量明确记下来，而不是静默吞掉。
			var lost int64
			for _, t := range traffic {
				lost += t.Upload + t.Download
			}
			n.log.Error("上报流量失败，本轮数据已丢失", "err", err, "字节", lost)
		}
	}

	if online := n.kernel.OnlineIPs(n.tag); len(online) > 0 {
		if err := n.client.Alive(ctx, online); err != nil {
			n.log.Warn("上报在线 IP 失败", "err", err)
		}
	}
}

func errInvalidPort(port int) error {
	return fmt.Errorf("面板下发的 server_port 非法: %d", port)
}

func intFrom(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	}
	return 0
}
