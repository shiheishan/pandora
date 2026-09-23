// Package node 把面板与内核粘起来：拉配置、同步用户、上报流量。
package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

const effectiveHealthStabilityWindow = 5 * time.Second

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
}

func New(client *panel.Client, kernel core.Core, log *slog.Logger) *Node {
	return NewWithSignedClient(client, kernel, log, nil)
}

func NewWithSignedClient(client *panel.Client, kernel core.Core, log *slog.Logger, signed *panel.SignedClient) *Node {
	return &Node{
		client: client,
		signed: signed,
		kernel: kernel,
		log:    log.With("node", client.NodeID(), "type", client.NodeType()),
		tag:    client.NodeType() + "-" + client.NodeID(),
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
	status := time.NewTicker(n.statusInterval)
	defer pull.Stop()
	defer push.Stop()
	defer status.Stop()
	curPull, curPush := n.pullInterval, n.pushInterval

	// 先报一次，别让面板等满一个周期才知道这个节点起来了。
	// 放在 syncOnce 之后：入站没起来就报「活着」是在骗人。
	n.reportStatus(ctx)

	// 事件流：面板有变更时立刻推下来，省掉轮询那一个周期的等待。
	//
	// 轮询不停。流是加速通路不是替代：它断了、丢消息了、面板那边没启用，
	// 轮询都还在按原节奏走。停掉轮询的话，流一断节点就彻底聋了，而 SSE
	// 断连未必有明显信号。
	go n.client.Stream(ctx, n.events, func(err error) {
		// 连不上很常见（面板重启、网络抖动），记 Info 不记 Error——
		// 记成 Error 会让日志里全是它，真正的问题反而被埋掉。
		n.log.Info("事件流断开，将退避重连", "err", err)
	})

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
		case <-status.C:
			n.reportStatus(ctx)
		case ev := <-n.events:
			n.applyStreamEvent(ctx, ev)
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

// applyUsers 把一份完整用户列表落到内核上。
//
// 轮询和事件流两条路都走这里——所有对 n.known 的改动集中在一个地方，
// 才好保证它和内核里的实际状态一致。
func (n *Node) applyUsers(users []core.User) error {
	want := make(map[string]core.User, len(users))
	var added, updated []core.User
	for _, u := range users {
		if u.UUID == "" {
			continue
		}
		want[u.UUID] = u
		before, exists := n.known[u.UUID]
		switch {
		case !exists:
			added = append(added, u)
		case before != u:
			updated = append(updated, u)
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
	if len(updated) > 0 {
		if err := n.kernel.UpsertUsers(n.tag, updated); err != nil {
			return err
		}
	}
	if len(removed) > 0 {
		if err := n.kernel.DelUsers(n.tag, removed); err != nil {
			return err
		}
	}
	n.known = want

	if len(added) > 0 || len(updated) > 0 || len(removed) > 0 {
		n.log.Info("用户已同步", "总数", len(want), "新增", len(added), "更新", len(updated), "移除", len(removed))
	}
	return nil
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
		if err := n.syncConfig(ctx); err != nil {
			n.log.Error("按事件同步配置失败", "err", err)
		}

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

// applyUserDelta 在现有列表上打补丁。
func (n *Node) applyUserDelta(ev panel.StreamEvent) error {
	var added, updated []core.User
	for _, u := range ev.Added {
		if u.UUID == "" {
			continue
		}
		if before, exists := n.known[u.UUID]; exists && before != u {
			updated = append(updated, u)
		} else if !exists {
			added = append(added, u)
		}
	}
	if len(added) > 0 {
		if err := n.kernel.AddUsers(n.tag, added); err != nil {
			return err
		}
	}
	if len(updated) > 0 {
		if err := n.kernel.UpsertUsers(n.tag, updated); err != nil {
			return err
		}
	}
	if len(ev.Removed) > 0 {
		// 增量里的 Removed 是用户 ID，内核按 UUID 删，要先换算。
		byID := make(map[int64]string, len(n.known))
		for uuid, u := range n.known {
			byID[u.ID] = uuid
		}
		uuids := make([]string, 0, len(ev.Removed))
		for _, id := range ev.Removed {
			if uuid, ok := byID[id]; ok {
				uuids = append(uuids, uuid)
			}
		}
		if len(uuids) > 0 {
			if err := n.kernel.DelUsers(n.tag, uuids); err != nil {
				return err
			}
		}
	}
	for _, u := range added {
		n.known[u.UUID] = u
	}
	for _, u := range updated {
		n.known[u.UUID] = u
	}
	for _, id := range ev.Removed {
		for uuid, user := range n.known {
			if user.ID == id {
				delete(n.known, uuid)
			}
		}
	}
	n.log.Info("用户增量已应用",
		"总数", len(n.known), "新增", len(added), "更新", len(updated), "移除", len(ev.Removed))
	return nil
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

// reportStatus 把本机资源占用报给面板。
//
// 面板拿它更新 last_heartbeat_at 和 health_score——也就是后台节点列表上
// 「这个节点还活着吗」的唯一依据。不报的话那几列一直空着，服务挂了后台
// 也不变色，只能等用户报障。
//
// 失败只记日志不重试：下一个节拍会再来一次，而卡在这里重试会挤掉同一个
// 循环里的配置同步和流量上报——那两件比状态上报重要。
func (n *Node) reportStatus(ctx context.Context) {
	if n.signed != nil {
		input := panel.HeartbeatInput{
			AgentVersion: "pandora-native", RuntimeVersion: n.kernel.Type(), RuntimeStatus: "running",
			ConfigVersion: n.appliedConfigVersion, ConfigSigningKeyID: n.signed.ConfigSigningKeyID(),
		}
		if n.appliedReleaseID != "" {
			input.AppliedReleaseID = n.appliedReleaseID
			input.AppliedGeneration = n.appliedGeneration
			input.AppliedContentSHA256 = n.appliedConfigHash
		} else {
			input.ConfigHash = n.appliedConfigHash
		}
		if _, err := n.signed.Heartbeat(ctx, input); err != nil {
			n.log.Error("签名上报运行状态失败", "err", err)
		}
		return
	}
	if err := n.client.Status(ctx, panel.CollectRuntimeStatus()); err != nil {
		n.log.Error("上报运行状态失败", "err", err)
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
	if n.signed != nil {
		cfg, err := n.signed.Config(ctx)
		if err != nil {
			return err
		}
		if err := n.signed.VerifyConfig(cfg); err != nil {
			n.reportSignedConfigPhase(ctx, cfg, "failed", err.Error())
			return err
		}
		if n.started && n.signedConfigAlreadyApplied(cfg) {
			// Reports use a deterministic id per release and phase, so replaying
			// both phases safely repairs a response lost after the local switch.
			if err := n.reportSignedConfigPhase(ctx, cfg, "switched", ""); err != nil {
				return fmt.Errorf("retry effective config switched report: %w", err)
			}
			if !n.effectiveHealthReady(time.Now()) {
				return nil
			}
			if err := n.reportSignedConfigPhase(ctx, cfg, "health_passed", ""); err != nil {
				return fmt.Errorf("retry effective config health report: %w", err)
			}
			return nil
		}
		var raw map[string]any
		decoder := json.NewDecoder(bytes.NewReader(cfg.Payload))
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			n.reportSignedConfigPhase(ctx, cfg, "failed", err.Error())
			return err
		}
		if err := n.applyConfig(raw); err != nil {
			n.reportSignedConfigPhase(ctx, cfg, "failed", err.Error())
			return err
		}
		n.recordAppliedSignedConfig(cfg)
		if err := n.reportSignedConfigPhase(ctx, cfg, "switched", ""); err != nil {
			n.log.Warn("配置已应用但切换上报失败", "release_id", cfg.ReleaseID, "generation", cfg.Generation, "err", err)
		}
		return nil
	}
	cfg, changed, err := n.client.Config(ctx)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return n.applyConfig(cfg)
}

func (n *Node) signedConfigAlreadyApplied(cfg *panel.SignedConfig) bool {
	if cfg.ConfigContract != "" {
		return cfg.ReleaseID == n.appliedReleaseID && cfg.Generation == n.appliedGeneration &&
			cfg.ContentSHA256 == n.appliedConfigHash
	}
	return cfg.Version == n.appliedConfigVersion && cfg.Hash == n.appliedConfigHash
}

func (n *Node) recordAppliedSignedConfig(cfg *panel.SignedConfig) {
	n.appliedConfigHash = cfg.Hash
	if cfg.ConfigContract != "" {
		n.appliedReleaseID = cfg.ReleaseID
		n.appliedGeneration = cfg.Generation
		n.appliedConfigVersion = 0
		n.appliedAt = time.Now()
		return
	}
	n.appliedConfigVersion = cfg.Version
	n.appliedReleaseID = ""
	n.appliedGeneration = 0
	n.appliedAt = time.Time{}
}

func (n *Node) effectiveHealthReady(now time.Time) bool {
	if !n.started || n.appliedAt.IsZero() || now.Sub(n.appliedAt) < effectiveHealthStabilityWindow {
		return false
	}
	probe, ok := n.kernel.(core.InboundReadiness)
	return ok && probe.InboundReady(n.tag) == nil
}

func (n *Node) reportSignedConfigPhase(ctx context.Context, cfg *panel.SignedConfig, phase, detail string) error {
	if cfg != nil && cfg.ConfigContract != "" {
		return n.signed.ReportEffectiveConfig(ctx, cfg, phase, detail)
	}
	if cfg == nil {
		return fmt.Errorf("signed config is required")
	}
	return n.signed.ReportConfig(ctx, cfg.Version, phase, detail)
}

func (n *Node) applyConfig(cfg map[string]any) error {
	snapshot, err := cloneConfigMap(cfg)
	if err != nil {
		return fmt.Errorf("snapshot config: %w", err)
	}
	previous := n.activeConfig
	previousPull, previousPush := n.pullInterval, n.pushInterval
	previousUsers := make([]core.User, 0, len(n.known))
	for _, user := range n.known {
		previousUsers = append(previousUsers, user)
	}

	if err := n.installConfig(cfg); err != nil {
		n.pullInterval, n.pushInterval = previousPull, previousPush
		var applyErr *core.ConfigApplyError
		if errors.As(err, &applyErr) && applyErr.PreviousPreserved {
			if len(previousUsers) > 0 {
				if restoreErr := n.kernel.AddUsers(n.tag, previousUsers); restoreErr != nil {
					n.started = false
					return errors.Join(err, fmt.Errorf("restore previous users: %w", restoreErr))
				}
			}
			return err
		}
		if previous == nil {
			return err
		}
		if restoreErr := n.installConfig(previous); restoreErr != nil {
			n.started = false
			return errors.Join(err, fmt.Errorf("restore previous config: %w", restoreErr))
		}
		if len(previousUsers) > 0 {
			if restoreErr := n.kernel.AddUsers(n.tag, previousUsers); restoreErr != nil {
				n.started = false
				return errors.Join(err, fmt.Errorf("restore previous users: %w", restoreErr))
			}
		}
		n.log.Warn("新配置应用失败，已恢复上一版本", "err", err)
		return err
	}

	n.activeConfig = snapshot
	// 入站重建会丢掉内核里的用户表，本地记录必须一并清空，
	// 否则下一轮 diff 会认为「都已下发」，结果谁也连不上。
	n.known = make(map[string]core.User)
	n.started = true
	n.log.Info("入站已就绪", "port", intFrom(cfg, "server_port"))
	return nil
}

func (n *Node) installConfig(cfg map[string]any) error {
	port := intFrom(cfg, "server_port")
	if port <= 0 || port > 65535 {
		return errInvalidPort(port)
	}

	kernel, _ := cfg["kernel"].(string)
	inbound := &core.InboundConfig{
		Tag:      n.tag,
		Protocol: n.protocolFrom(cfg),
		Port:     port,
		Kernel:   kernel,
		Raw:      cfg,
	}
	routing, err := parseRouting(cfg)
	if err != nil {
		return err
	}
	if applier, ok := n.kernel.(core.ConfigApplier); ok {
		if err := applier.ApplyInbound(inbound, routing); err != nil {
			return err
		}
	} else {
		if err := n.kernel.AddInbound(inbound); err != nil {
			return err
		}
		// Compatibility cores do not expose a generation-level apply contract.
		if err := n.kernel.SetRouting(n.tag, routing); err != nil {
			_ = n.kernel.DelInbound(n.tag)
			return err
		}
	}
	if base, ok := cfg["base_config"].(map[string]any); ok {
		if v := intFrom(base, "pull_interval"); v > 0 {
			n.pullInterval = time.Duration(v) * time.Second
		}
		if v := intFrom(base, "push_interval"); v > 0 {
			n.pushInterval = time.Duration(v) * time.Second
		}
	}
	return nil
}

func cloneConfigMap(cfg map[string]any) (map[string]any, error) {
	body, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// parseRouting 从面板下发的配置里取出出站与分流。
//
// 面板不下发这两个键时返回 nil，表示「这个节点没配分流」，
// 与「配了一份空的」不同：前者保持默认直出，后者会清掉已有规则。
func parseRouting(cfg map[string]any) (*core.Routing, error) {
	outsRaw, outsPresent := cfg["outbounds"]
	routesRaw, routesPresent := cfg["routes"]
	_, finalPresent := cfg["final"]
	if !outsPresent && !routesPresent && !finalPresent {
		return nil, nil
	}
	var outs, routes []any
	if outsPresent {
		var ok bool
		outs, ok = outsRaw.([]any)
		if !ok {
			return nil, fmt.Errorf("outbounds 必须是数组")
		}
	}
	if routesPresent {
		var ok bool
		routes, ok = routesRaw.([]any)
		if !ok {
			return nil, fmt.Errorf("routes 必须是数组")
		}
	}

	r := &core.Routing{}
	if finalValue, present := cfg["final"]; present {
		final, ok := finalValue.(string)
		if !ok {
			return nil, fmt.Errorf("final 必须是字符串")
		}
		r.Final = strings.TrimSpace(final)
	}
	for index, item := range outs {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("outbounds[%d] 必须是对象", index)
		}
		o := core.Outbound{}
		var okTag, okType bool
		o.Tag, okTag = m["tag"].(string)
		o.Type, okType = m["type"].(string)
		if !okTag || strings.TrimSpace(o.Tag) == "" || !okType || strings.TrimSpace(o.Type) == "" {
			return nil, fmt.Errorf("outbounds[%d] 缺少 tag/type", index)
		}
		if rawSettings, present := m["settings"]; present {
			var okSettings bool
			o.Settings, okSettings = rawSettings.(map[string]any)
			if !okSettings {
				return nil, fmt.Errorf("outbounds[%d].settings 必须是对象", index)
			}
		}
		r.Outbounds = append(r.Outbounds, o)
	}
	for index, item := range routes {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("routes[%d] 必须是对象", index)
		}
		rt := core.Route{}
		var okOutbound bool
		rt.OutboundTag, okOutbound = m["outbound"].(string)
		if !okOutbound || strings.TrimSpace(rt.OutboundTag) == "" {
			return nil, fmt.Errorf("routes[%d] 缺少 outbound", index)
		}
		if rawMatcher, present := m["matcher"]; present {
			var okMatcher bool
			rt.Matcher, okMatcher = rawMatcher.(map[string]any)
			if !okMatcher {
				return nil, fmt.Errorf("routes[%d].matcher 必须是对象", index)
			}
		}
		r.Routes = append(r.Routes, rt)
	}
	if len(r.Outbounds) == 0 && len(r.Routes) == 0 {
		return r, nil
	}
	return r, nil
}

func (n *Node) syncUsers(ctx context.Context) error {
	users, changed, err := n.client.Users(ctx)
	if err != nil {
		return err
	}
	if !changed {
		// 面板回了 304，列表和上一轮一样。直接返回——不能往下走：
		// changed 为 false 时 users 是 nil，下面那段会把它当成「一个用户
		// 都没有」，然后把所有人从内核里删掉。
		return nil
	}

	return n.applyUsers(users)
}

// report 上报流量与在线 IP。
func (n *Node) report(ctx context.Context) {
	if !n.started {
		return
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
