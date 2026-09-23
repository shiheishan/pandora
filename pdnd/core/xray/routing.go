package xray

// 出站与分流的翻译。
//
// 与 sing-box 那边不同，这里必须逐字段映射：xray 的配置是 protobuf，
// 没有「拼个 JSON 交给它自己解析」这条捷径。好在两者的路由语义同源，
// 对应关系是清楚的：
//
//	中立格式（sing-box 命名）   xray
//	domain                     Domain{Type: Full}     精确匹配
//	domain_suffix              Domain{Type: Domain}   子域匹配
//	domain_keyword             Domain{Type: Plain}    子串匹配
//	domain_regex               Domain{Type: Regex}
//	ip_cidr                    GeoIP{Cidr: [...]}
//	geoip                      GeoIP{CountryCode}
//	port                       PortList
//	network                    Networks
//	inbound（隔离用）           InboundTag
//
// 一个实打实的优势：xray 的路由和出站都能热更新
// （Router.ReloadRules + OutboundManager.AddHandler），
// 改分流不必重建实例，也就不会波及同实例的其它节点 ——
// sing-box 那边只能整体重建。

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	xcore "github.com/xtls/xray-core/core"

	"github.com/xtls/xray-core/app/router"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	xoutbound "github.com/xtls/xray-core/features/outbound"
	xrouting "github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/proxy/blackhole"
	"github.com/xtls/xray-core/proxy/freedom"
	"github.com/xtls/xray-core/transport/internet"
	xhttp "github.com/xtls/xray-core/proxy/http"
	"github.com/xtls/xray-core/proxy/shadowsocks"
	"github.com/xtls/xray-core/proxy/socks"
	"github.com/xtls/xray-core/proxy/trojan"
	"github.com/xtls/xray-core/proxy/vless"
	vlessout "github.com/xtls/xray-core/proxy/vless/outbound"
	"github.com/xtls/xray-core/proxy/vmess"
	vmessout "github.com/xtls/xray-core/proxy/vmess/outbound"

	"github.com/aegispanel/nodeagent/core"
)

// scopedTag 与 sing-box 侧同样的隔离约定：一个进程服务多个节点，
// 出站名必须带上节点前缀，否则两个节点各配一条同名出站就会互相接管流量。
func scopedTag(inboundTag, outboundTag string) string {
	return inboundTag + "::" + outboundTag
}

// SetRouting 设置某个入站的出站与分流，全程热更新。
func (c *Core) SetRouting(inboundTag string, r *core.Routing) error {
	c.mu.Lock()
	if r == nil {
		delete(c.routing, inboundTag)
	} else {
		c.routing[inboundTag] = r
	}
	inst := c.instance
	snapshot := make(map[string]*core.Routing, len(c.routing))
	for k, v := range c.routing {
		snapshot[k] = v
	}
	c.mu.Unlock()

	if inst == nil {
		// 实例还没起来，配置已经记下了，Start 之后会重放
		return nil
	}
	return c.applyRouting(inst, snapshot)
}

func (c *Core) applyRouting(inst *xcore.Instance, routing map[string]*core.Routing) error {
	om, ok := inst.GetFeature(xoutbound.ManagerType()).(xoutbound.Manager)
	if !ok {
		return fmt.Errorf("xray 缺少 outbound manager")
	}
	badRouting := func(err error) { c.log.Error("分流配置有误", "err", err) }

	// 先把出站挂上，再装规则。反过来的话，规则会短暂指向还不存在的出站，
	// 那段时间内命中的连接会被直接丢弃。
	wanted := make(map[string]bool)
	for _, inbound := range sortedRoutingKeys(routing) {
		r := routing[inbound]
		if r == nil {
			continue
		}
		for _, o := range r.Outbounds {
			if o.Tag == "" || o.Type == "" {
				continue
			}
			tag := scopedTag(inbound, o.Tag)
			wanted[tag] = true

			cfg, err := buildOutboundHandler(tag, o)
			if err != nil {
				badRouting(err)
				delete(wanted, tag)
				continue
			}
			// 已存在的先摘掉：AddHandler 对重复 tag 会报错，
			// 而配置改了之后必须换成新的那份
			_ = om.RemoveHandler(c.ctx, tag)
			if err := xcore.AddOutboundHandler(inst, cfg); err != nil {
				return fmt.Errorf("挂载出站 %s: %w", o.Tag, err)
			}
		}
	}

	// 摘掉本轮不再需要的出站。留着不管的话，删掉的中转线路
	// 会一直保持连接池，而且下次同名新建时会撞 tag。
	for _, h := range om.ListHandlers(c.ctx) {
		tag := h.Tag()
		if tag == outboundDirect || tag == outboundBlock || wanted[tag] {
			continue
		}
		if strings.Contains(tag, "::") {
			_ = om.RemoveHandler(c.ctx, tag)
		}
	}

	rules, err := buildRoutingRules(routing, wanted, badRouting)
	if err != nil {
		return err
	}
	rt, ok := inst.GetFeature(xrouting.RouterType()).(*router.Router)
	if !ok {
		return fmt.Errorf("xray 缺少 router")
	}
	// shouldAppend=false：整表替换。追加的话删掉的规则会留在表里，
	// 面板上看到的和实际生效的会逐渐对不上。
	if err := rt.ReloadRules(&router.Config{Rule: rules}, false); err != nil {
		return fmt.Errorf("重载路由规则: %w", err)
	}
	return nil
}

const (
	outboundDirect = "direct"
	outboundBlock  = "block"
)

// buildOutboundHandler 把中立出站翻译成 xray 的出站描述。
func buildOutboundHandler(tag string, o core.Outbound) (*xcore.OutboundHandlerConfig, error) {
	var settings *serial.TypedMessage

	switch strings.ToLower(o.Type) {
	case "direct":
		// 优先 IPv4 出站。
		//
		// 不设的话（AS_IS）由系统解析顺序决定，而多数 VPS 在有 v6 的机器上
		// 会优先走 v6 —— 结果是节点的入口是 v4、出口却是 v6，两者对不上。
		// 有些站点对 v6 段的判定和 v4 不同（风控、地区、直接拒绝），
		// 用户会遇到「这个节点某些网站打不开」而我们这边看不出任何异常。
		//
		// USE_IP46 是「先试 v4，没有再用 v6」，纯 v6 站点仍然可达；
		// 用 USE_IP4 会把它们变成打不开。
		settings = serial.ToTypedMessage(&freedom.Config{
			DomainStrategy: internet.DomainStrategy_USE_IP46,
		})
	case "block":
		settings = serial.ToTypedMessage(&blackhole.Config{})

	case "socks":
		ep, err := endpointOf(o, func(u core.Outbound) (*protocol.User, error) {
			user, _ := u.Settings["username"].(string)
			pass, _ := u.Settings["password"].(string)
			if user == "" {
				return nil, nil
			}
			return &protocol.User{Account: serial.ToTypedMessage(
				&socks.Account{Username: user, Password: pass})}, nil
		})
		if err != nil {
			return nil, err
		}
		settings = serial.ToTypedMessage(&socks.ClientConfig{Server: ep})

	case "http":
		ep, err := endpointOf(o, func(u core.Outbound) (*protocol.User, error) {
			user, _ := u.Settings["username"].(string)
			pass, _ := u.Settings["password"].(string)
			if user == "" {
				return nil, nil
			}
			return &protocol.User{Account: serial.ToTypedMessage(
				&xhttp.Account{Username: user, Password: pass})}, nil
		})
		if err != nil {
			return nil, err
		}
		settings = serial.ToTypedMessage(&xhttp.ClientConfig{Server: ep})

	case "shadowsocks":
		ep, err := endpointOf(o, func(u core.Outbound) (*protocol.User, error) {
			pass, _ := u.Settings["password"].(string)
			method, _ := u.Settings["method"].(string)
			ct, err := ssCipher(method)
			if err != nil {
				return nil, err
			}
			return &protocol.User{Account: serial.ToTypedMessage(
				&shadowsocks.Account{Password: pass, CipherType: ct})}, nil
		})
		if err != nil {
			return nil, err
		}
		settings = serial.ToTypedMessage(&shadowsocks.ClientConfig{Server: ep})

	case "trojan":
		ep, err := endpointOf(o, func(u core.Outbound) (*protocol.User, error) {
			pass, _ := u.Settings["password"].(string)
			return &protocol.User{Account: serial.ToTypedMessage(
				&trojan.Account{Password: pass})}, nil
		})
		if err != nil {
			return nil, err
		}
		settings = serial.ToTypedMessage(&trojan.ClientConfig{Server: ep})

	case "vless":
		ep, err := endpointOf(o, func(u core.Outbound) (*protocol.User, error) {
			id, _ := u.Settings["uuid"].(string)
			flow, _ := u.Settings["flow"].(string)
			return &protocol.User{Account: serial.ToTypedMessage(
				&vless.Account{Id: id, Flow: flow})}, nil
		})
		if err != nil {
			return nil, err
		}
		settings = serial.ToTypedMessage(&vlessout.Config{Vnext: ep})

	case "vmess":
		ep, err := endpointOf(o, func(u core.Outbound) (*protocol.User, error) {
			id, _ := u.Settings["uuid"].(string)
			return &protocol.User{Account: serial.ToTypedMessage(
				&vmess.Account{Id: id})}, nil
		})
		if err != nil {
			return nil, err
		}
		settings = serial.ToTypedMessage(&vmessout.Config{Receiver: ep})

	default:
		return nil, fmt.Errorf("xray 内核暂不支持出站类型 %q", o.Type)
	}

	return &xcore.OutboundHandlerConfig{Tag: tag, ProxySettings: settings}, nil
}

// endpointOf 从 settings 里取出 server / port 并构造凭据。
func endpointOf(o core.Outbound, mkUser func(core.Outbound) (*protocol.User, error)) (*protocol.ServerEndpoint, error) {
	host, _ := o.Settings["server"].(string)
	if host == "" {
		return nil, fmt.Errorf("出站 %s 缺少 server", o.Tag)
	}
	port := 0
	switch v := o.Settings["server_port"].(type) {
	case float64:
		port = int(v)
	case string:
		port, _ = strconv.Atoi(v)
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("出站 %s 的 server_port 非法", o.Tag)
	}

	user, err := mkUser(o)
	if err != nil {
		return nil, fmt.Errorf("出站 %s: %w", o.Tag, err)
	}
	return &protocol.ServerEndpoint{
		Address: xnet.NewIPOrDomain(xnet.ParseAddress(host)),
		Port:    uint32(port),
		User:    user,
	}, nil
}

// buildRoutingRules 把中立规则翻译成 xray 的路由表。
func buildRoutingRules(routing map[string]*core.Routing, known map[string]bool,
	onErr func(error)) ([]*router.RoutingRule, error) {
	var rules []*router.RoutingRule

	for _, inbound := range sortedRoutingKeys(routing) {
		r := routing[inbound]
		if r == nil {
			continue
		}
		for i, rule := range r.Routes {
			if rule.OutboundTag == "" {
				continue
			}
			target := scopedTag(inbound, rule.OutboundTag)
			if !known[target] {
				switch rule.OutboundTag {
				case outboundDirect, outboundBlock:
					// 允许直接引用内建出站，这是最常见的「其余直连」写法
					target = rule.OutboundTag
				default:
					// 与 sing-box 侧同样的取舍：坏规则只让它自己那个节点降级，
					// 不能连累同实例的其它节点
					onErr(fmt.Errorf("入站 %s 的第 %d 条规则指向未定义的出站 %q，已跳过",
						inbound, i+1, rule.OutboundTag))
					continue
				}
			}

			rr := &router.RoutingRule{
				// 入站限定：多节点共用一张路由表时不互相污染的关键
				InboundTag: []string{inbound},
				TargetTag:  &router.RoutingRule_Tag{Tag: target},
			}
			if err := applyMatcher(rr, rule.Matcher); err != nil {
				onErr(fmt.Errorf("入站 %s 的第 %d 条规则: %w，已跳过", inbound, i+1, err))
				continue
			}
			rules = append(rules, rr)
		}
	}
	return rules, nil
}

// applyMatcher 把中立匹配条件填进 xray 的规则。
func applyMatcher(rr *router.RoutingRule, m map[string]any) error {
	domainKinds := []struct {
		key string
		typ router.Domain_Type
	}{
		{"domain", router.Domain_Full},
		{"domain_suffix", router.Domain_Domain},
		{"domain_keyword", router.Domain_Plain},
		{"domain_regex", router.Domain_Regex},
	}
	for _, dk := range domainKinds {
		for _, v := range strList(m[dk.key]) {
			rr.Domain = append(rr.Domain, &router.Domain{Type: dk.typ, Value: v})
		}
	}

	var cidrs []*router.CIDR
	for _, v := range strList(m["ip_cidr"]) {
		c, err := parseCIDR(v)
		if err != nil {
			return err
		}
		cidrs = append(cidrs, c)
	}
	if len(cidrs) > 0 {
		rr.Geoip = append(rr.Geoip, &router.GeoIP{Cidr: cidrs})
	}
	for _, v := range strList(m["geoip"]) {
		rr.Geoip = append(rr.Geoip, &router.GeoIP{CountryCode: strings.ToUpper(v)})
	}

	if ports := intList(m["port"]); len(ports) > 0 {
		pl := &xnet.PortList{}
		for _, p := range ports {
			pl.Range = append(pl.Range, &xnet.PortRange{From: uint32(p), To: uint32(p)})
		}
		rr.PortList = pl
	}

	for _, v := range strList(m["network"]) {
		switch strings.ToLower(v) {
		case "tcp":
			rr.Networks = append(rr.Networks, xnet.Network_TCP)
		case "udp":
			rr.Networks = append(rr.Networks, xnet.Network_UDP)
		}
	}
	return nil
}

// parseCIDR 接受 1.2.3.4/24，也接受不带掩码的裸 IP。
func parseCIDR(s string) (*router.CIDR, error) {
	if !strings.Contains(s, "/") {
		ip := net.ParseIP(s)
		if ip == nil {
			return nil, fmt.Errorf("非法 IP %q", s)
		}
		bits := 32
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		} else {
			bits = 128
		}
		return &router.CIDR{Ip: ip, Prefix: uint32(bits)}, nil
	}
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return nil, fmt.Errorf("非法 CIDR %q", s)
	}
	ones, _ := n.Mask.Size()
	ip := n.IP
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return &router.CIDR{Ip: ip, Prefix: uint32(ones)}, nil
}

// ssCipher 把方法名翻译成 xray 的枚举。
// 只列 2022 之外的经典方法：2022 系列在 xray 里是另一套实现（shadowsocks_2022），
// 出站侧用得极少，先不支持而不是给一个会静默失败的映射。
func ssCipher(method string) (shadowsocks.CipherType, error) {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "aes-128-gcm":
		return shadowsocks.CipherType_AES_128_GCM, nil
	case "aes-256-gcm", "":
		return shadowsocks.CipherType_AES_256_GCM, nil
	case "chacha20-poly1305", "chacha20-ietf-poly1305":
		return shadowsocks.CipherType_CHACHA20_POLY1305, nil
	case "none", "plain":
		return shadowsocks.CipherType_NONE, nil
	default:
		return shadowsocks.CipherType_UNKNOWN, fmt.Errorf("不支持的加密方式 %q", method)
	}
}

func strList(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, it := range x {
			if s, ok := it.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	}
	return nil
}

func intList(v any) []int {
	switch x := v.(type) {
	case float64:
		return []int{int(x)}
	case []any:
		out := make([]int, 0, len(x))
		for _, it := range x {
			switch n := it.(type) {
			case float64:
				out = append(out, int(n))
			case string:
				if p, err := strconv.Atoi(n); err == nil {
					out = append(out, p)
				}
			}
		}
		return out
	}
	return nil
}

// sortedRoutingKeys 让规则顺序稳定 —— map 遍历是随机的，
// 而规则有先后，顺序漂移就是行为漂移。
func sortedRoutingKeys(m map[string]*core.Routing) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
