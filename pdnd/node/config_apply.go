package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// syncOnce 是一轮轮询：先配置、后用户。
func (n *Node) syncOnce(ctx context.Context) {
	cfgErr, usersErr := n.syncOnceErr(ctx)
	if cfgErr != nil {
		n.log.Error("同步配置失败", "err", cfgErr)
	}
	if usersErr != nil {
		n.log.Error("同步用户失败", "err", usersErr)
	}
}

// syncOnceErr 是 syncOnce 的本体，把两段错误交给调用方（冷启动要据此决定能不能
// 用落盘缓存）。
//
// 配置同步失败不挡用户同步：签名回执补报失败、换钥检查失败、新发布验签不过，
// 都与「该放行谁」无关。原先这里配置一出错就 return，回执接口持续 5xx 时用户
// 名单整个冻结（该删的人不删、新用户连不上）。只要入站在服务就照常拉用户。
func (n *Node) syncOnceErr(ctx context.Context) (cfgErr, usersErr error) {
	cfgErr = n.syncConfig(ctx)
	if cfgErr == nil {
		// 面板确认过配置（204 / 304 / 新版本已装上），不再是「只靠缓存在服务」。
		n.fromCache = false
	}
	if n.started {
		usersErr = n.syncUsers(ctx)
	}
	n.flushUsersCache()
	return cfgErr, usersErr
}

// compatApplyFailure 是兼容通道「新配置装不上、旧配置仍在服务」的重试台账。
//
// 配置 ETag 在解析成功时就记下了，不作废的话每轮都换回 304、永不再试（审计 E2：
// 端口一度被占、随后释放，节点仍停在旧端口，面板却以为它在新端口）。作废得
// 太勤又会每轮重建入站。所以按 applyRetryDelay 退避：到点才作废 ETag 再拉一次。
type compatApplyFailure struct {
	etag      string
	attempts  int
	nextRetry time.Time
}

func (n *Node) syncConfig(ctx context.Context) error {
	if n.signed != nil {
		return n.syncSignedConfig(ctx)
	}
	if f := n.compatFailure; f != nil && n.started && !n.clock().Before(f.nextRetry) {
		n.log.Info("重试之前装不上的配置", "第几次", f.attempts+1)
		n.client.ForgetConfigVersion()
	}
	cfg, changed, err := n.client.Config(ctx)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if err := n.applyConfig(cfg); err != nil {
		if !n.started {
			// 节点已不在服务（首次就没装上，或回滚也失败了）：作废配置
			// ETag，下一轮重拉重试——没有旧配置可保，重试没有代价。
			n.client.ForgetConfigVersion()
			return err
		}
		etag := n.client.ConfigVersion()
		f := n.compatFailure
		if f == nil || f.etag != etag {
			f = &compatApplyFailure{etag: etag}
			n.compatFailure = f
		}
		f.attempts++
		f.nextRetry = n.clock().Add(applyRetryDelay(f.attempts))
		return err
	}
	n.compatFailure = nil
	n.saveCompatConfigCache(cfg)
	return nil
}

// applyRetryMax 是装失败重试的退避上限。
const applyRetryMax = 5 * time.Minute

// applyRetryDelay 是第 attempts 次装失败之后、下一次重试之前的等待：1、2、4 分钟，
// 之后封顶 5 分钟，各带 ±10% 抖动。只用于「旧配置仍在服务」：这时每次重试都可能
// 重建入站、断一次在线连接，不能每轮都试；节点已停时没有可保的东西，每轮都试。
func applyRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := applyRetryMax
	if attempts <= 3 {
		d = time.Minute << (attempts - 1)
	}
	return panel.Jitter(d)
}

// registerInboundOwner 告诉内核这个入站属于哪个面板的哪个节点（可选契约）。
// 只影响端口冲突时能不能写出对方节点 ID：同一面板（同一 panel URL 加同一租户）
// 才写，别的面板一律「本机其他服务」。租户不明（兼容通道）时范围为空，不透露。
func (n *Node) registerInboundOwner() {
	registrar, ok := n.kernel.(interface {
		SetInboundOwner(tag, scope, nodeID string)
	})
	if !ok {
		return
	}
	scope := ""
	if n.tenantID != "" {
		sum := sha256.Sum256([]byte(panelKey(n.client.BaseURL()) + "\n" + n.tenantID))
		scope = hex.EncodeToString(sum[:16])
	}
	registrar.SetInboundOwner(n.tag, scope, n.client.NodeID())
}

// panelKey 把面板地址规整成比较与哈希用的形式。
func panelKey(base string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(base)), "/")
}

// applyConfig 装一份配置并记下结果：失败原因留给 degraded 上报，成功即清空。
// 冷启动时先排队（StartupOrder），保证同机端口按 nodes[] 顺序先到先得。
func (n *Node) applyConfig(cfg map[string]any) error {
	n.awaitStartupTurn()
	err := n.applyConfigOnce(cfg)
	n.lastApplyErr = err
	return err
}

func (n *Node) applyConfigOnce(cfg map[string]any) error {
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
					n.markInboundLost()
					return errors.Join(err, fmt.Errorf("restore previous users: %w", restoreErr))
				}
			}
			return err
		}
		if previous == nil {
			return err
		}
		if restoreErr := n.installConfig(previous); restoreErr != nil {
			n.markInboundLost()
			return errors.Join(err, fmt.Errorf("restore previous config: %w", restoreErr))
		}
		if len(previousUsers) > 0 {
			if restoreErr := n.kernel.AddUsers(n.tag, previousUsers); restoreErr != nil {
				n.markInboundLost()
				return errors.Join(err, fmt.Errorf("restore previous users: %w", restoreErr))
			}
		}
		// 旧入站已装回、正在服务。节点此前可能因回滚失败被标成已停：不在这里
		// 恢复 started，用户就一直不同步，下一轮还会把同一份坏配置当成「节点
		// 已停、该重试」再重建两次入站。用户镜像若已作废，下一轮自会全量补回。
		n.started = true
		n.log.Warn("新配置应用失败，已恢复上一版本", "err", err)
		return err
	}

	n.activeConfig = snapshot
	// 入站重建会丢掉内核里的用户表，本地镜像与用户版本必须一并作废，
	// 否则下一轮要么 diff 认为「都已下发」，要么拿旧 ETag 换回 304——
	// 两种都是谁也连不上。
	n.resetUserMirror()
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
	n.registerInboundOwner()
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
