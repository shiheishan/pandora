package sing

// 出站与分流的翻译。
//
// 这里几乎不做翻译 —— 中立格式的键名就是按 sing-box 的命名定的，
// 拼成 JSON 再交给 sing-box 自己的反序列化即可。
//
// 为什么走 JSON 而不是直接构造 option 结构体：sing-box 的出站选项有十几种，
// 每种都是独立类型，手工构造要写十几段映射，上游每加一个字段还得跟。
// 它的 UnmarshalJSONContext 本来就会按 type 分派到正确的类型，不用白不用。
//
// 需要留意的核心约束：一个节点端进程服务多个节点，而 sing-box 只有
// 一张路由表。因此每个节点的出站都会被加上前缀隔离，每条规则也会被
// 限定到该节点自己的入站上 —— 否则 A 节点的分流会作用到 B 节点的流量上。

import (
	"context"
	"fmt"
	"sort"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"

	"github.com/aegispanel/nodeagent/core"
)

const (
	// 内建出站。direct 是最终兜底，block 让「拒绝」类规则不必额外配出站 ——
	// 两个内核都提供同名的这两个，中立格式才能在内核间通用。
	outboundDirect = "direct"
	outboundBlock  = "block"
)

// scopedTag 把某个节点的出站名映射成全局唯一的名字。
//
// 不做隔离的话，两个节点各自配一条叫 "relay" 的出站就会撞车，
// 后加载的那条会悄悄接管前一个节点的流量 —— 这种错误在面板上完全看不出来。
func scopedTag(inboundTag, outboundTag string) string {
	return inboundTag + "::" + outboundTag
}

// SetRouting 保存某个入站的出站与分流，并重建实例使其生效。
func (c *Core) SetRouting(inboundTag string, r *core.Routing) error {
	c.mu.Lock()
	if r == nil {
		delete(c.routing, inboundTag)
	} else {
		c.routing[inboundTag] = r
	}
	c.mu.Unlock()
	return c.rebuild()
}

// buildOutbounds 汇总所有入站的出站列表。
//
// ctx 必须带着出站注册表 —— 那正是 UnmarshalJSONContext 用来按 type
// 找到对应选项类型的东西，缺了它会直接报 missing outbound options registry。
func buildOutbounds(ctx context.Context, routing map[string]*core.Routing,
	onErr func(error)) ([]option.Outbound, error) {
	// direct 永远存在且排在最前：没配分流的节点靠它直出，
	// 配了分流的节点也总要有个地方兜底
	out := []option.Outbound{
		{Type: "direct", Tag: outboundDirect},
		{Type: "block", Tag: outboundBlock},
	}

	for _, inbound := range sortedKeys(routing) {
		r := routing[inbound]
		if r == nil {
			continue
		}
		for _, o := range r.Outbounds {
			if o.Tag == "" || o.Type == "" {
				continue
			}
			raw := make(map[string]any, len(o.Settings)+2)
			for k, v := range o.Settings {
				raw[k] = v
			}
			raw["type"] = o.Type
			raw["tag"] = scopedTag(inbound, o.Tag)

			body, err := json.Marshal(raw)
			if err != nil {
				onErr(fmt.Errorf("出站 %s 序列化失败: %w，已跳过", o.Tag, err))
				continue
			}
			var ob option.Outbound
			if err := json.UnmarshalContext(ctx, body, &ob); err != nil {
				onErr(fmt.Errorf("出站 %s 配置无效: %w，已跳过", o.Tag, err))
				continue
			}
			out = append(out, ob)
		}
	}
	return out, nil
}

// buildRoute 汇总所有入站的分流规则。
//
// 每条规则都会被限定到它所属的入站，因此多个节点的规则可以安全地
// 共存在同一张表里。规则顺序：按入站分组，组内保持面板给出的优先级。
func buildRoute(ctx context.Context, routing map[string]*core.Routing,
	known map[string]bool, onErr func(error)) (*option.RouteOptions, error) {

	route := &option.RouteOptions{Final: outboundDirect}

	for _, inbound := range sortedKeys(routing) {
		r := routing[inbound]
		if r == nil || len(r.Routes) == 0 {
			continue
		}
		for i, rule := range r.Routes {
			if rule.OutboundTag == "" {
				continue
			}
			target := scopedTag(inbound, rule.OutboundTag)
			// 允许直接引用内建出站，那是最常见的「其余直连」「拒绝广告」写法
			if !known[target] &&
				(rule.OutboundTag == outboundDirect || rule.OutboundTag == outboundBlock) {
				target = rule.OutboundTag
			}
			if !known[target] {
				// 指向不存在的出站会让 sing-box 拒绝整份配置。
				//
				// 这里刻意跳过而不是返回错误：这张路由表是全机器共用的，
				// 中断整个构建意味着一个节点写错一条规则，同机器上其它节点的
				// 配置同步全部失败 —— 实测踩过这个坑。
				// 坏规则只该让它自己那个节点降级为直出。
				onErr(fmt.Errorf("入站 %s 的第 %d 条规则指向未定义的出站 %q，已跳过",
					inbound, i+1, rule.OutboundTag))
				continue
			}

			raw := make(map[string]any, len(rule.Matcher)+2)
			for k, v := range rule.Matcher {
				raw[k] = v
			}
			// 入站限定：这是多节点共表不互相污染的关键。
			// 即便面板下发的 matcher 里已经写了 inbound，也以这里为准 ——
			// 一个节点不该有能力把规则作用到别的节点上。
			raw["inbound"] = []string{inbound}
			raw["outbound"] = target

			// 空匹配条件的规则语义是「该节点的兜底出口」。
			// 加了 inbound 限定之后它不再是无条件规则，可以正常作为一条规则存在，
			// 放在该组最后即可 —— 这也正是它被放在循环里而非改 Final 的原因：
			// Final 是全局的，改它会影响别的节点。
			body, err := json.Marshal(raw)
			if err != nil {
				onErr(fmt.Errorf("入站 %s 的第 %d 条规则序列化失败: %w，已跳过", inbound, i+1, err))
				continue
			}
			var pr option.Rule
			if err := json.UnmarshalContext(ctx, body, &pr); err != nil {
				onErr(fmt.Errorf("入站 %s 的第 %d 条规则无效: %w，已跳过", inbound, i+1, err))
				continue
			}
			route.Rules = append(route.Rules, pr)
		}
	}
	return route, nil
}

// sortedKeys 让规则顺序稳定。
// map 遍历是随机的，不排序的话每次重建生成的规则顺序都不同 ——
// 规则是有先后的，顺序漂移意味着行为漂移。
func sortedKeys(m map[string]*core.Routing) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
