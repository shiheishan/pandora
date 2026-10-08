package nodesim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
)

// pdnd reportStatus 写死的两个字符串：AgentVersion 与 NativeCore.Type()。
const pdndAgentVersion = "pandora-native"

const (
	flagSigFail  = "sig_fail"
	flagAuthFail = "auth_fail"
	flagETag304  = "etag_304"
	flagStream   = "trigger:stream"
	// flagUnchanged 标生效配置的 204：节点报的已应用版本仍是当前版（current 才会出现）
	flagUnchanged = "unchanged_204"
)

type simNode struct {
	id     string
	index  int
	opt    *Options
	uni    *uniClient
	signed *signedClient // nil 表示兼容通道，与 pdnd 没有身份时一样
	obs    *observer
	work   *workload
	host   *hostState
	rng    *rand.Rand // 只在主循环里用
	jit    jitterSource

	pullInterval   time.Duration
	pushInterval   time.Duration
	statusInterval time.Duration
	userVersion    string
	events         chan streamEvent

	// userIDs 对应 pdnd 的 n.known（内核里已下发的用户），只留排好序的 id，并且只留
	// 「可能连在本节点上」的那一部分（workload.candidate）：流量与在线上报只会选中这些人，
	// 上千个节点各存一份上万人的名单要吃掉近百 MB，而发给面板的请求一字不差。
	userIDs []int64
	// started 跨 goroutine 读（结束时统计），用原子值。
	started              atomic.Bool
	appliedConfigVersion int
	appliedConfigHash    string
	appliedReleaseID     string
	appliedGeneration    uint64
	appliedAt            time.Time
	// 以下三项只在 current 用（pdnd 的 appliedSigned / switchedSettled / healthSettled）：
	// 已应用的那份签名配置，与它的两个阶段回执是否已被面板收下（或明确拒收）。
	appliedSigned   *nodefabric.SignedConfig
	switchedSettled bool
	healthSettled   bool
	// pushInflight / pushPending 是 pdnd 的待报缓冲（current）：没送到的那份原样
	// 重发、带同一个 report_id，期间的新流量按 uid 合并，见 pdnd node/report.go。
	pushInflight   map[string][2]int64
	pushInflightID string
	pushPending    map[string][2]int64
}

func newSimNode(id string, index int, opt *Options, uni *uniClient, signed *signedClient, obs *observer, work *workload,
	rng *rand.Rand, jit jitterSource) *simNode {
	if signed != nil && !opt.legacy() {
		// pdnd 的换钥检查节拍在 SignedClient 里，间隔每次带抖动
		signed.keyCheckEvery = opt.KeyCheckInterval
		signed.keyJitter = func(d time.Duration) time.Duration { return jitter(jit.key, d) }
	}
	return &simNode{
		id: id, index: index, opt: opt, uni: uni, signed: signed, obs: obs, work: work,
		host: newHostState(rng), rng: rng, jit: jit,
		// pdnd 的兜底节拍：面板下发 base_config 之前用它
		pullInterval:   opt.PullInterval,
		pushInterval:   opt.PushInterval,
		statusInterval: opt.StatusInterval,
		events:         make(chan streamEvent, 32),
	}
}

// run 是 pdnd Node.Run 的逐段复刻，跑到 ctx 取消。
func (n *simNode) run(ctx context.Context) {
	// 先同步一次再进循环
	n.syncOnce(ctx, "")

	// current 三条节拍都是带抖动的定时器，legacy 是 ticker（beat.go）
	legacy := n.opt.legacy()
	pull := newBeat(n.pullInterval, legacy, n.jit.pull)
	push := newBeat(n.pushInterval, legacy, n.jit.push)
	status := newBeat(n.statusInterval, legacy, n.jit.status)
	defer pull.stop()
	defer push.stop()
	defer status.stop()
	curPull, curPush := n.pullInterval, n.pushInterval

	// 同步之后立刻报一次状态
	n.reportStatus(ctx)

	// 事件流是加速通路，轮询不停
	if n.opt.Stream {
		go n.uni.streamLoop(ctx, n.events, !legacy, func(err error) { n.obs.sample("stream", n.id, err) })
	}

	for {
		select {
		case <-ctx.Done():
			// 退出前把最后一段流量交上去
			n.report(context.WithoutCancel(ctx))
			return
		case <-pull.C():
			n.syncOnce(ctx, "")
			pull.fired(n.pullInterval)
		case <-push.C():
			n.report(ctx)
			push.fired(n.pushInterval)
		case <-status.C():
			n.reportStatus(ctx)
			status.fired(n.statusInterval)
		case ev := <-n.events:
			n.applyStreamEvent(ctx, ev)
		}
		// 面板在 base_config 里改了节拍就重排（status 节拍 pdnd 写死，不跟）
		if n.pullInterval != curPull && n.pullInterval > 0 {
			pull.reset(n.pullInterval)
			curPull = n.pullInterval
		}
		if n.pushInterval != curPush && n.pushInterval > 0 {
			push.reset(n.pushInterval)
			curPush = n.pushInterval
		}
	}
}

func (n *simNode) syncOnce(ctx context.Context, flag string) {
	if err := n.syncConfig(ctx, flag); err != nil {
		n.obs.sample("sync config", n.id, err)
		// legacy：配置一出错就不拉用户；current 照 pdnd，入站在服务就照常拉。
		if n.opt.legacy() {
			return
		}
	}
	if !n.started.Load() {
		return
	}
	if err := n.syncUsers(ctx, flag); err != nil {
		n.obs.sample("sync users", n.id, err)
	}
}

// syncConfig 是一轮配置同步。兼容通道走 /config 的 ETag；签名通道 current 见
// signed_sync.go，legacy 是改版前的 syncSignedLegacy。
func (n *simNode) syncConfig(ctx context.Context, flag string) error {
	if n.signed == nil {
		cfg, changed, err := n.uni.config(ctx)
		if err != nil || !changed {
			return err
		}
		return n.applyConfig(cfg)
	}
	if n.opt.legacy() {
		return n.syncSignedLegacy(ctx, flag)
	}
	return n.syncSigned(ctx, flag)
}

// syncSignedLegacy 复刻老 pdnd 的签名配置流程：每轮先问换钥 → 拉全量 → 验签
// （失败回报 failed）→ 已应用则重放 switched、稳定窗口过后再报 health_passed →
// 否则解码、应用、记账、报 switched。
func (n *simNode) syncSignedLegacy(ctx context.Context, flag string) error {
	cfg, err := n.signed.config(ctx, flag)
	if err != nil {
		return err
	}
	if err := n.signed.verifyConfig(cfg); err != nil {
		n.obs.fleet.verifyFailures.Add(1)
		_ = n.signed.reportPhase(ctx, cfg, "failed", err.Error())
		return err
	}
	if n.started.Load() && n.alreadyApplied(cfg) {
		if err := n.signed.reportPhase(ctx, cfg, "switched", ""); err != nil {
			return fmt.Errorf("retry effective config switched report: %w", err)
		}
		if !n.healthReady(time.Now()) {
			return nil
		}
		if err := n.signed.reportPhase(ctx, cfg, "health_passed", ""); err != nil {
			return fmt.Errorf("retry effective config health report: %w", err)
		}
		return nil
	}
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(cfg.Payload))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		_ = n.signed.reportPhase(ctx, cfg, "failed", err.Error())
		return err
	}
	if err := n.applyConfig(raw); err != nil {
		_ = n.signed.reportPhase(ctx, cfg, "failed", err.Error())
		return err
	}
	n.recordApplied(cfg)
	if err := n.signed.reportPhase(ctx, cfg, "switched", ""); err != nil {
		n.obs.sample("switched report", n.id, err)
	}
	return nil
}

func (n *simNode) alreadyApplied(cfg *nodefabric.SignedConfig) bool {
	if cfg.ConfigContract != "" {
		return cfg.ReleaseID == n.appliedReleaseID && cfg.Generation == n.appliedGeneration &&
			cfg.ContentSHA256 == n.appliedConfigHash
	}
	return cfg.Version == n.appliedConfigVersion && cfg.Hash == n.appliedConfigHash
}

func (n *simNode) recordApplied(cfg *nodefabric.SignedConfig) {
	n.appliedSigned = cfg
	n.switchedSettled, n.healthSettled = false, false
	n.appliedConfigHash = cfg.Hash
	if cfg.ConfigContract != "" {
		n.appliedReleaseID, n.appliedGeneration = cfg.ReleaseID, cfg.Generation
		n.appliedConfigVersion, n.appliedAt = 0, time.Now()
		return
	}
	n.appliedConfigVersion = cfg.Version
	n.appliedReleaseID, n.appliedGeneration, n.appliedAt = "", 0, time.Time{}
}

// healthReady 是 pdnd effectiveHealthReady：应用满稳定窗口（5 秒）且内核报
// 入站就绪。模拟器没有内核，就绪恒为真。
func (n *simNode) healthReady(now time.Time) bool {
	return n.started.Load() && !n.appliedAt.IsZero() && now.Sub(n.appliedAt) >= n.opt.HealthWindow
}

// applyConfig 复刻 pdnd applyConfig + installConfig 里面板看得见的部分：
// 端口合法性、协议跟随下发、base_config 的两个节拍；成功后清空已下发用户
// （pdnd 重建入站会丢掉内核用户表，n.known 随之清空）。current 另照 pdnd
// resetUserMirror 一并作废用户版本与 ETag，下一次拉用户拿全量而不是 304；
// legacy 保留改版前只清 id 的做法。
// 分流的形状校验不做：它只影响 pdnd 本地，不产生面板请求。
func (n *simNode) applyConfig(cfg map[string]any) error {
	port := intFrom(cfg, "server_port")
	if port <= 0 || port > 65535 {
		return fmt.Errorf("面板下发的 server_port 非法: %d", port)
	}
	if p, _ := cfg["protocol"].(string); strings.TrimSpace(p) != "" {
		n.uni.setNodeType(p)
	}
	if base, ok := cfg["base_config"].(map[string]any); ok {
		if v := intFrom(base, "pull_interval"); v > 0 {
			n.pullInterval = time.Duration(v) * time.Second
		}
		if v := intFrom(base, "push_interval"); v > 0 {
			n.pushInterval = time.Duration(v) * time.Second
		}
	}
	n.userIDs = nil
	if !n.opt.legacy() {
		n.userVersion = ""
		n.uni.forgetUsersVersion()
	}
	if !n.started.Swap(true) {
		n.obs.fleet.started.Add(1)
	}
	n.obs.fleet.configApplied.Add(1)
	return nil
}

func (n *simNode) syncUsers(ctx context.Context, flag string) error {
	users, changed, err := n.uni.users(ctx, flag)
	if err != nil {
		return err
	}
	if !changed {
		// 304：不能当成「一个用户都没有」
		return nil
	}
	n.applyUsers(users)
	return nil
}

// applyUsers 把完整列表落到本地（pdnd 落到内核），跳过没有 UUID 的条目。
func (n *simNode) applyUsers(users []nodefabric.ProxyUser) {
	ids := make([]int64, 0, len(users)/max(n.work.total, 1)+8)
	for _, u := range users {
		if u.UUID != "" && n.work.candidate(u.ID, n.index) {
			ids = append(ids, u.ID)
		}
	}
	slices.Sort(ids)
	n.userIDs = slices.Compact(ids)
	n.obs.fleet.userListsApplied.Add(1)
}

// applyStreamEvent 复刻 pdnd 同名函数，与轮询在同一个 goroutine 里串行。
func (n *simNode) applyStreamEvent(ctx context.Context, ev streamEvent) {
	switch ev.Type {
	case nodefabric.EventSyncConfig:
		// 事件只当信号，回头走一遍完整的签名拉取。current 照 pdnd 走 syncOnce
		// （配置之后紧接着拉用户）；legacy 保留改版前只拉配置的做法。
		if !n.opt.legacy() {
			n.syncOnce(ctx, flagStream)
			return
		}
		if err := n.syncConfig(ctx, flagStream); err != nil {
			n.obs.sample("stream sync config", n.id, err)
		}
	case nodefabric.EventSyncUsers:
		n.applyUsers(ev.Users)
		n.userVersion = ev.Version
		n.uni.setUsersVersion(ev.Version)
	case nodefabric.EventSyncUserDelta:
		if ev.FromVersion != n.userVersion {
			// 基准对不上，改拉全量
			n.obs.fleet.deltaMismatches.Add(1)
			if err := n.syncUsers(ctx, flagStream); err != nil {
				n.obs.sample("delta full pull", n.id, err)
			}
			return
		}
		n.applyUserDelta(ev)
		n.userVersion = ev.ToVersion
		n.uni.setUsersVersion(ev.ToVersion)
	}
}

func (n *simNode) applyUserDelta(ev streamEvent) {
	ids := slices.Clone(n.userIDs)
	for _, u := range ev.Added {
		if u.UUID != "" && n.work.candidate(u.ID, n.index) {
			ids = append(ids, u.ID)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ev.Removed) > 0 {
		drop := make(map[int64]bool, len(ev.Removed))
		for _, id := range ev.Removed {
			drop[id] = true
		}
		ids = slices.DeleteFunc(ids, func(id int64) bool { return drop[id] })
	}
	n.userIDs = ids
}

// report 复刻 pdnd report：先 push 后 alive，各自为空就不发。legacy 失败只记一笔；
// current 照 pdnd 留待报缓冲（flushPush）。
func (n *simNode) report(ctx context.Context) {
	if !n.started.Load() {
		return
	}
	traffic, total := n.work.trafficFor(n.userIDs, n.index, n.rng)
	if len(traffic) > 0 {
		n.host.addTraffic(total)
	}
	if n.opt.legacy() {
		if len(traffic) > 0 {
			if err := n.uni.push(ctx, traffic, ""); err != nil {
				n.obs.sample("push", n.id, err)
			}
		}
	} else {
		n.flushPush(ctx, traffic)
	}
	if online := n.work.aliveFor(n.userIDs, n.index); len(online) > 0 {
		if err := n.uni.alive(ctx, online); err != nil {
			n.obs.sample("alive", n.id, err)
		}
	}
}

// reportStatus 复刻 pdnd reportStatus：签名通道发心跳（带 metrics 与已应用
// 发布物），兼容通道发 /status。
func (n *simNode) reportStatus(ctx context.Context) {
	if n.signed == nil {
		if err := n.uni.status(ctx, n.host.status(n.rng)); err != nil {
			n.obs.sample("status", n.id, err)
		}
		return
	}
	in := heartbeatBody{
		AgentVersion: pdndAgentVersion, RuntimeVersion: pdndAgentVersion, RuntimeStatus: "running",
		ConfigVersion: n.appliedConfigVersion, ConfigSigningKeyID: n.signed.configKeyID,
		CPUCores: hostCPUCores, MemoryMB: hostMemoryMB, DiskGB: hostDiskGB,
	}
	if n.appliedReleaseID != "" {
		in.AppliedReleaseID = n.appliedReleaseID
		in.AppliedGeneration = n.appliedGeneration
		in.AppliedContentSHA256 = n.appliedConfigHash
	} else {
		in.ConfigHash = n.appliedConfigHash
	}
	online := len(n.work.aliveFor(n.userIDs, n.index))
	in.Metrics = n.host.metrics(online, n.rng)
	if err := n.signed.heartbeat(ctx, in); err != nil {
		n.obs.sample("heartbeat", n.id, err)
	}
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

// flushPush 是 pdnd flushTraffic 的模拟：新流量按 uid 并入 pending；先原样重发
// 没送到的 inflight（同一个 report_id），收下后再把 pending 封成新的一份发出，
// 一轮最多两次请求。面板明确拒收（4xx，408 / 429 除外）的那份丢弃。
func (n *simNode) flushPush(ctx context.Context, traffic map[string][2]int64) {
	if len(traffic) > 0 && n.pushPending == nil {
		n.pushPending = make(map[string][2]int64, len(traffic))
	}
	for uid, v := range traffic {
		cur := n.pushPending[uid]
		n.pushPending[uid] = [2]int64{cur[0] + v[0], cur[1] + v[1]}
	}
	for i := 0; i < 2; i++ {
		if n.pushInflight == nil {
			if len(n.pushPending) == 0 {
				return
			}
			n.pushInflight, n.pushInflightID, n.pushPending = n.pushPending, uuid.NewString(), nil
		}
		err := n.uni.push(ctx, n.pushInflight, n.pushInflightID)
		if err != nil {
			n.obs.sample("push", n.id, err)
			if !reportSettled(err) {
				return
			}
		}
		n.pushInflight, n.pushInflightID = nil, ""
	}
}
