package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/internal/confnum"
	"github.com/aegispanel/nodeagent/panel"
)

// applyRetryMax 是装失败重试的退避上限。
const applyRetryMax = 5 * time.Minute

// applyRetryDelay 是第 attempts 次装失败之后、下一次重试之前的等待：1、2、4 分钟，
// 之后封顶 5 分钟，各带 ±10% 抖动。只用于「旧配置仍在服务」：这时每次重试都可能
// 重建入站、断一次在线连接，不能频繁试；节点已停时没有可保的东西，按 stoppedRetryInterval 试。
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

// registerInboundOwner 告诉内核这个入站属于哪台服务器的哪个节点。
// 只影响端口冲突时能不能写出对方节点 id：同一台服务器（同一 scope）的节点之间才写，
// 别的一律「本机其他服务」。
func (n *Node) registerInboundOwner() {
	n.kernel.SetInboundOwner(n.tag, n.scope, n.id)
}

// applyConfig 装一份配置并记下结果：失败原因留给心跳上报，成功即清空。
// 主循环按 ServerConfig 的顺序逐个装，同机端口天然先到先得，不用另外排队。
func (n *Node) applyConfig(cfg map[string]any, nodeType string) error {
	err := n.applyConfigOnce(cfg, nodeType)
	n.lastApplyErr = err
	return err
}

func (n *Node) applyConfigOnce(cfg map[string]any, nodeType string) error {
	snapshot, err := cloneConfigMap(cfg)
	if err != nil {
		return fmt.Errorf("snapshot config: %w", err)
	}
	previous := n.activeConfig
	previousType := n.nodeType
	previousUsers := make([]core.User, 0, len(n.known))
	for _, user := range n.known {
		previousUsers = append(previousUsers, user)
	}

	prepared := n.prepareInstallUsers()
	if err := n.installConfig(cfg, nodeType, prepared); err != nil {
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
		if restoreErr := n.installConfig(previous, previousType, nil); restoreErr != nil {
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
		// 恢复 started，名单就一直不装，下一次重试还会把同一份坏配置当成「节点
		// 已停」再重建两次入站。用户镜像若已作废，面板收到 resync 回执会发全量。
		n.started = true
		n.log.Warn("新配置应用失败，已恢复上一版本", "err", err)
		return err
	}

	n.activeConfig = snapshot
	n.nodeType = strings.TrimSpace(nodeType)
	// 入站重建会丢掉内核里的用户表：本地镜像对齐到随入站一起装的那份名单。
	n.adoptInstalledUsers(prepared)
	n.started = true
	return nil
}

// installConfig 装入站。prepared 为 nil 只出现在回滚装回旧配置时，旧名单由 applyConfigOnce 随后补回。
func (n *Node) installConfig(cfg map[string]any, nodeType string, prepared *preparedUsers) error {
	protocol := strings.TrimSpace(nodeType)
	if protocol == "" {
		return errors.New("面板没给 node_type")
	}
	port := intFrom(cfg, "server_port")
	if port <= 0 || port > 65535 {
		return errInvalidPort(port)
	}

	inbound := &core.InboundConfig{
		Tag:      n.tag,
		Protocol: protocol,
		Port:     port,
		Raw:      cfg,
	}
	routing, err := parseRouting(cfg)
	if err != nil {
		return err
	}
	n.registerInboundOwner()
	if prepared != nil {
		if err := n.kernel.ApplyInboundWithUsers(inbound, routing, prepared.users); err != nil {
			if !isUsersPreloadError(err) {
				return err
			}
			// 入站已起、名单没装上：作废镜像，面板收到 resync 回执会发全量。
			n.log.Warn("入站已就绪，随入站一起装的用户名单失败，等面板发全量", "err", err)
			prepared.failed = true
		}
	} else if err := n.kernel.ApplyInbound(inbound, routing); err != nil {
		return err
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
	n, _ := confnum.Int(m[key])
	return n
}
