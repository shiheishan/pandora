// 路由合并口径：节点私有层 → 所在各路由组（按组 sort_order、id）→ 全局层。
// 出站同 tag 按层从低到高覆盖，保留首次出现顺序；规则按层顺序拼接，只含启用项。
package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

const (
	routingScopeNode   = "node"
	routingScopeGroup  = "group"
	routingScopeGlobal = "global"
)

const (
	maxScopeOutbounds = 200
	maxScopeRoutes    = 1000
	maxRoutingTag     = 64
	maxRouteNote      = 500
)

// RoutingOutbound 是一条出站。
type RoutingOutbound struct {
	Tag      string
	Type     string
	Settings json.RawMessage
}

// RoutingRule 是一条分流规则（含停用的）。
type RoutingRule struct {
	Priority    int
	Matcher     json.RawMessage
	OutboundTag string
	Enabled     bool
	Note        string
}

// RoutingInput 是一个范围（全局、节点私有或路由组）整体替换的出站与规则。
type RoutingInput struct {
	Outbounds []RoutingOutbound
	Routes    []RoutingRule
}

// RouteGroupRef 是节点所在的一个组（按生效顺序）。
type RouteGroupRef struct {
	ID        uuid.UUID
	Name      string
	SortOrder int
}

// RoutingSource 标注一条生效出站或规则来自哪一层：Scope 是 "node"、"group" 或 "global"；组层才有 GroupID、GroupName。
type RoutingSource struct {
	Scope     string
	GroupID   *uuid.UUID
	GroupName string
}

// EffectiveOutbound 是一条生效出站与来源（同 tag 被覆盖时只剩胜出的那条）。
type EffectiveOutbound struct {
	RoutingOutbound
	Source RoutingSource
}

// EffectiveRoute 是一条生效规则（只含启用的）与来源，顺序即匹配顺序。
type EffectiveRoute struct {
	Matcher  json.RawMessage
	Outbound string
	Source   RoutingSource
}

var routingOutboundTypes = map[string]bool{
	"direct": true, "block": true,
	"socks": true, "http": true, "shadowsocks": true, "vmess": true, "vless": true, "trojan": true,
	"hysteria": true, "hysteria2": true, "tuic": true, "anytls": true, "shadowtls": true, "wireguard": true,
}

// routingLayer 是一个范围的出站与启用中的规则（已按存储顺序排好）。
type routingLayer struct {
	source    RoutingSource
	outbounds []RoutingOutbound
	routes    []RoutingRule // 只含启用的
}

func canonicalRouteTag(tag string) string {
	t := strings.TrimSpace(tag)
	low := strings.ToLower(t)
	if low == "direct" || low == "block" {
		return low
	}
	return t
}

func isBuiltinOutbound(tag string) bool {
	c := canonicalRouteTag(tag)
	return c == "direct" || c == "block"
}

func tagLenOK(tag string) bool {
	if strings.ContainsRune(tag, '\x00') {
		return false
	}
	return utf8.RuneCountInString(tag) <= maxRoutingTag
}

func noteOK(note string) bool {
	if strings.ContainsRune(note, '\x00') {
		return false
	}
	return utf8.RuneCountInString(note) <= maxRouteNote
}

func settingsObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return json.RawMessage("{}"), nil
	}
	if bytes.Contains(raw, []byte(`\u0000`)) {
		return nil, fmt.Errorf("settings")
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, fmt.Errorf("settings")
	}
	return raw, nil
}

func matcherObject(raw json.RawMessage) (json.RawMessage, bool, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return json.RawMessage("{}"), true, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, false, fmt.Errorf("匹配条件必须是 JSON 对象")
	}
	if len(m) == 0 {
		return raw, true, nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !matcherKeyAllowed(key) {
			return nil, false, fmt.Errorf("不支持的匹配条件 %s", key)
		}
		if err := validateMatcherValue(key, m[key]); err != nil {
			return nil, false, err
		}
	}
	return raw, false, nil
}

func matcherKeyAllowed(key string) bool {
	switch key {
	case "domain", "domains",
		"domain_suffix", "domain_suffixes",
		"ip", "ip_cidr", "ip_cidrs",
		"port", "ports",
		"network", "networks",
		"source", "source_ip_cidr", "source_cidrs",
		"source_port", "source_ports":
		return true
	default:
		return false
	}
}

func isIPMatcherKey(key string) bool {
	switch key {
	case "ip", "ip_cidr", "ip_cidrs", "source", "source_ip_cidr", "source_cidrs":
		return true
	default:
		return false
	}
}

func isPortMatcherKey(key string) bool {
	switch key {
	case "port", "ports", "source_port", "source_ports":
		return true
	default:
		return false
	}
}

func isNetworkMatcherKey(key string) bool {
	switch key {
	case "network", "networks":
		return true
	default:
		return false
	}
}

func validateMatcherValue(key string, val any) error {
	list, err := matcherToStrings(val)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return fmt.Errorf("匹配器 %q 至少需要一个有效值", key)
	}
	for _, v := range list {
		if isIPMatcherKey(key) {
			if err := validateIPOrCIDR(v); err != nil {
				return fmt.Errorf("%s 不是合法的 IP 或 CIDR：%s", key, v)
			}
			continue
		}
		if isPortMatcherKey(key) {
			if err := validatePortSpec(v); err != nil {
				return fmt.Errorf("%s 不是合法的端口或端口区间：%s", key, v)
			}
			continue
		}
		if isNetworkMatcherKey(key) {
			low := strings.ToLower(v)
			if low != "tcp" && low != "udp" {
				return fmt.Errorf("network %q 无效", v)
			}
		}
	}
	return nil
}

func validateIPOrCIDR(s string) error {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		if _, err := netip.ParsePrefix(s); err != nil {
			return err
		}
		return nil
	}
	if _, err := netip.ParseAddr(s); err != nil {
		return err
	}
	return nil
}

func validatePortSpec(s string) error {
	s = strings.TrimSpace(s)
	if lo, hi, ok := strings.Cut(s, "-"); ok {
		a, err1 := strconv.ParseUint(strings.TrimSpace(lo), 10, 16)
		b, err2 := strconv.ParseUint(strings.TrimSpace(hi), 10, 16)
		if err1 != nil || err2 != nil || a > b {
			return fmt.Errorf("invalid")
		}
		return nil
	}
	v, err := strconv.ParseUint(s, 10, 16)
	if err != nil {
		return fmt.Errorf("invalid")
	}
	_ = v
	return nil
}

func matcherToStrings(v any) ([]string, error) {
	switch x := v.(type) {
	case string:
		x = strings.TrimSpace(x)
		if x == "" || strings.ContainsRune(x, '\x00') {
			return nil, fmt.Errorf("值不能为空")
		}
		return []string{x}, nil
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			return nil, fmt.Errorf("数值必须是整数")
		}
		return []string{strconv.FormatInt(n, 10)}, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			ss, err := matcherToStrings(e)
			if err != nil {
				return nil, err
			}
			out = append(out, ss...)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("不支持的值类型")
	}
}

// validateMatcher 校验 matcher JSON；empty 表示空对象无条件匹配。
func validateMatcher(raw json.RawMessage) (empty bool, err error) {
	_, empty, err = matcherObject(raw)
	return empty, err
}

// validateRoutingInput 就地规范化并校验，返回本次提交的出站 tag 集合（原样，不含内置两个）。
func validateRoutingInput(in *RoutingInput) (map[string]bool, error) {
	if len(in.Outbounds) > maxScopeOutbounds {
		return nil, errs.Invalid(map[string]string{"outbounds": fmt.Sprintf("最多 %d 条", maxScopeOutbounds)})
	}
	if len(in.Routes) > maxScopeRoutes {
		return nil, errs.Invalid(map[string]string{"routes": fmt.Sprintf("最多 %d 条", maxScopeRoutes)})
	}

	seenLower := map[string]string{}
	tags := map[string]bool{}

	for i := range in.Outbounds {
		o := &in.Outbounds[i]
		o.Tag = strings.TrimSpace(o.Tag)
		o.Type = strings.ToLower(strings.TrimSpace(o.Type))
		if o.Tag == "" || o.Type == "" {
			return nil, errs.Invalid(map[string]string{"outbounds": fmt.Sprintf("第 %d 条出站要有 tag 和 type", i+1)})
		}
		if !tagLenOK(o.Tag) {
			return nil, errs.Invalid(map[string]string{"outbounds": fmt.Sprintf("第 %d 条出站的 tag 为 1 到 64 个字符", i+1)})
		}
		if !routingOutboundTypes[o.Type] {
			return nil, errs.Invalid(map[string]string{"outbounds": fmt.Sprintf("第 %d 条出站的类型不支持：%s", i+1, o.Type)})
		}
		low := strings.ToLower(o.Tag)
		if low == "direct" || low == "block" {
			return nil, errs.Invalid(map[string]string{"outbounds": fmt.Sprintf("出站 tag 重复或占用内置名称：%s", o.Tag)})
		}
		if prev, ok := seenLower[low]; ok {
			return nil, errs.Invalid(map[string]string{"outbounds": fmt.Sprintf("出站 tag 重复或占用内置名称：%s", prev)})
		}
		seenLower[low] = o.Tag
		tags[o.Tag] = true

		settings, err := settingsObject(o.Settings)
		if err != nil {
			return nil, errs.Invalid(map[string]string{"outbounds": fmt.Sprintf("第 %d 条出站的 settings 必须是 JSON 对象", i+1)})
		}
		o.Settings = settings
	}

	for i := range in.Routes {
		r := &in.Routes[i]
		if r.Priority < 0 {
			return nil, errs.Invalid(map[string]string{"routes": fmt.Sprintf("第 %d 条规则的 priority 不能为负", i+1)})
		}
		if r.Priority == 0 {
			r.Priority = (i + 1) * 10
		}
		matcher, empty, err := matcherObject(r.Matcher)
		if err != nil {
			return nil, errs.Invalid(map[string]string{"routes": fmt.Sprintf("第 %d 条规则：%v", i+1, err)})
		}
		r.Matcher = matcher

		r.OutboundTag = canonicalRouteTag(r.OutboundTag)
		if r.OutboundTag == "" {
			return nil, errs.Invalid(map[string]string{"routes": fmt.Sprintf("第 %d 条规则要指定出站", i+1)})
		}
		if !tagLenOK(r.OutboundTag) {
			return nil, errs.Invalid(map[string]string{"routes": fmt.Sprintf("第 %d 条规则的出站 tag 过长", i+1)})
		}
		if !noteOK(r.Note) {
			return nil, errs.Invalid(map[string]string{"routes": fmt.Sprintf("第 %d 条规则的备注最多 500 个字符", i+1)})
		}
		_ = empty
	}

	sort.SliceStable(in.Routes, func(i, j int) bool {
		return in.Routes[i].Priority < in.Routes[j].Priority
	})

	lastEnabled := -1
	for i, r := range in.Routes {
		if r.Enabled {
			lastEnabled = i
		}
	}
	for i, r := range in.Routes {
		empty, err := validateMatcher(r.Matcher)
		if err != nil {
			return nil, errs.Invalid(map[string]string{"routes": fmt.Sprintf("第 %d 条规则：%v", i+1, err)})
		}
		if empty && r.Enabled && i != lastEnabled {
			return nil, errs.Invalid(map[string]string{"routes": fmt.Sprintf("第 %d 条空匹配兜底规则必须放在最后", i+1)})
		}
	}

	return tags, nil
}

// checkRouteRefs 检查规则引用的出站是否存在。
func checkRouteRefs(routes []RoutingRule, visible ...map[string]bool) error {
	for i, r := range routes {
		if isBuiltinOutbound(r.OutboundTag) {
			continue
		}
		found := false
		for _, m := range visible {
			if m != nil && m[r.OutboundTag] {
				found = true
				break
			}
		}
		if !found {
			return errs.Invalid(map[string]string{
				"routes": fmt.Sprintf("第 %d 条规则指向不存在的出站 %q", i+1, r.OutboundTag),
			})
		}
	}
	return nil
}

// mergeRouting 把按优先级从高到低排好的层（节点私有 → 各组 → 全局）合成生效的出站与规则。
func mergeRouting(layers []routingLayer) ([]EffectiveOutbound, []EffectiveRoute) {
	idx := map[string]int{}
	outs := make([]EffectiveOutbound, 0)
	for li := len(layers) - 1; li >= 0; li-- {
		layer := layers[li]
		for _, o := range layer.outbounds {
			if j, ok := idx[o.Tag]; ok {
				outs[j] = EffectiveOutbound{RoutingOutbound: o, Source: layer.source}
			} else {
				idx[o.Tag] = len(outs)
				outs = append(outs, EffectiveOutbound{RoutingOutbound: o, Source: layer.source})
			}
		}
	}

	routes := make([]EffectiveRoute, 0)
	for _, layer := range layers {
		for _, r := range layer.routes {
			if !r.Enabled {
				continue
			}
			routes = append(routes, EffectiveRoute{
				Matcher:  r.Matcher,
				Outbound: r.OutboundTag,
				Source:   layer.source,
			})
		}
	}
	return outs, routes
}

func outboundFromModel(m model.NodeOutbound) RoutingOutbound {
	return RoutingOutbound{
		Tag:      m.Tag,
		Type:     m.Type,
		Settings: json.RawMessage(m.Settings),
	}
}

func ruleFromModel(m model.NodeRoute) RoutingRule {
	return RoutingRule{
		Priority:    m.Priority,
		Matcher:     json.RawMessage(m.Matcher),
		OutboundTag: m.OutboundTag,
		Enabled:     m.Enabled,
		Note:        m.Note,
	}
}

func outboundsToModel(outs []RoutingOutbound) []model.NodeOutbound {
	out := make([]model.NodeOutbound, len(outs))
	for i, o := range outs {
		out[i] = model.NodeOutbound{
			Tag:       o.Tag,
			Type:      o.Type,
			Settings:  string(o.Settings),
			SortOrder: i * 10,
		}
	}
	return out
}

func rulesToModel(routes []RoutingRule) []model.NodeRoute {
	out := make([]model.NodeRoute, len(routes))
	for i, r := range routes {
		out[i] = model.NodeRoute{
			Priority:    r.Priority,
			SortOrder:   i,
			Matcher:     string(r.Matcher),
			OutboundTag: r.OutboundTag,
			Enabled:     r.Enabled,
			Note:        r.Note,
		}
	}
	return out
}
