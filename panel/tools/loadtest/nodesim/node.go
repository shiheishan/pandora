// [INPUT]: 依赖同包 signed.go 的 signedClient、uniproxy.go 的 uniClient 与 streamEvent、workload.go 的虚构负载，依赖 domain/nodefabric 的 SignedConfig / ProxyUser 与事件常量
// [OUTPUT]: 对外提供 包内 simNode（newSimNode、run）
// [POS]: tools/loadtest/nodesim 的单节点循环，逐段复刻 pdnd node/node.go 的 Run：启动顺序、三条节拍与按 base_config 重置、签名配置的 switched / health_passed 回报、用户全量与增量、退出前最后一次上报；内核换成 workload 的虚构负载

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

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
)

// pdnd reportStatus 写死的两个字符串：AgentVersion 与 NativeCore.Type()。
const pdndAgentVersion = "pandora-native"

const (
	flagSigFail  = "sig_fail"
	flagAuthFail = "auth_fail"
	flagETag304  = "etag_304"
	flagStream   = "trigger:stream"
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

	pullInterval   time.Duration
	pushInterval   time.Duration
	statusInterval time.Duration
	userVersion    string
	events         chan streamEvent

	// userIDs 对应 pdnd 的 n.known（内核里已下发的用户），只留排好序的 id：
	// 两百个节点各存一份上万人的完整用户表太占压测机内存，而流量与在线
	// 上报只用得到 id。
	userIDs []int64
	// started 跨 goroutine 读（结束时统计），用原子值。
	started              atomic.Bool
	appliedConfigVersion int
	appliedConfigHash    string
	appliedReleaseID     string
	appliedGeneration    uint64
	appliedAt            time.Time
}

func newSimNode(id string, index int, opt *Options, uni *uniClient, signed *signedClient, obs *observer, work *workload, rng *rand.Rand) *simNode {
	return &simNode{
		id: id, index: index, opt: opt, uni: uni, signed: signed, obs: obs, work: work,
		host: newHostState(rng), rng: rng,
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

	pull := time.NewTicker(n.pullInterval)
	push := time.NewTicker(n.pushInterval)
	status := time.NewTicker(n.statusInterval)
	defer pull.Stop()
	defer push.Stop()
	defer status.Stop()
	curPull, curPush := n.pullInterval, n.pushInterval

	// 同步之后立刻报一次状态
	n.reportStatus(ctx)

	// 事件流是加速通路，轮询不停
	if n.opt.Stream {
		go n.uni.streamLoop(ctx, n.events, func(err error) { n.obs.sample("stream", n.id, err) })
	}

	for {
		select {
		case <-ctx.Done():
			// 退出前把最后一段流量交上去
			n.report(context.WithoutCancel(ctx))
			return
		case <-pull.C:
			n.syncOnce(ctx, "")
		case <-push.C:
			n.report(ctx)
		case <-status.C:
			n.reportStatus(ctx)
		case ev := <-n.events:
			n.applyStreamEvent(ctx, ev)
		}
		// 面板在 base_config 里改了节拍就重置 ticker（status 节拍 pdnd 写死，不跟）
		if n.pullInterval != curPull && n.pullInterval > 0 {
			pull.Reset(n.pullInterval)
			curPull = n.pullInterval
		}
		if n.pushInterval != curPush && n.pushInterval > 0 {
			push.Reset(n.pushInterval)
			curPush = n.pushInterval
		}
	}
}

func (n *simNode) syncOnce(ctx context.Context, flag string) {
	if err := n.syncConfig(ctx, flag); err != nil {
		n.obs.sample("sync config", n.id, err)
		return
	}
	if !n.started.Load() {
		return
	}
	if err := n.syncUsers(ctx, ""); err != nil {
		n.obs.sample("sync users", n.id, err)
	}
}

// syncConfig 复刻 pdnd 的签名配置流程：拉取 → 验签（失败回报 failed）→
// 已应用则重放 switched、稳定窗口过后再报 health_passed → 否则解码、应用、
// 记账、报 switched。兼容通道走 /config 的 ETag。
func (n *simNode) syncConfig(ctx context.Context, flag string) error {
	if n.signed == nil {
		cfg, changed, err := n.uni.config(ctx)
		if err != nil || !changed {
			return err
		}
		return n.applyConfig(cfg)
	}
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
// （pdnd 重建入站会丢掉内核用户表，n.known 随之清空）。
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
	ids := make([]int64, 0, len(users))
	for _, u := range users {
		if u.UUID != "" {
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
		// 事件只当信号，回头走一遍完整的签名拉取
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
		if u.UUID != "" {
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

// report 复刻 pdnd report：先 push 后 alive，各自为空就不发，失败只记一笔。
func (n *simNode) report(ctx context.Context) {
	if !n.started.Load() {
		return
	}
	traffic, total := n.work.trafficFor(n.userIDs, n.index, n.rng)
	if len(traffic) > 0 {
		n.host.addTraffic(total)
		if err := n.uni.push(ctx, traffic); err != nil {
			n.obs.sample("push", n.id, err)
		}
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
