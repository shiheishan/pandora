// Package core 定义内核抽象。
//
// 为什么要这层抽象：sing-box、xray-core、mihomo 三者的用户管理与统计 API
// 形态完全不同，但上层「同步用户、上报流量」的逻辑是一样的。把差异关在这层，
// 换内核或同时跑多个内核时，节点管理逻辑一行都不用改。
package core

import "context"

// User 是从面板下发的一个用户。
//
// 字段刻意做成协议无关：UUID 在 VLESS/VMess 里是用户 ID，在 Trojan 里是密码，
// 在 Shadowsocks 里是 PSK 的来源。由各内核实现自行解释，上层只管传递。
type User struct {
	// ID 是面板侧的整数用户号，流量上报时要按它回传
	ID          int64
	UUID        string
	SpeedLimit  int // kbps，0 表示不限
	DeviceLimit int
}

// UserTraffic 是一个用户在一个统计周期内的用量。
// 上报后计数器清零，因此这里的值是增量而非累计。
type UserTraffic struct {
	ID       int64
	Upload   int64
	Download int64
}

// InboundConfig 描述一个待启动的入站。
//
// Protocol 决定用哪种 inbound；Raw 是面板下发的协议原始配置，
// 各内核实现自己解析 —— 平台不解释协议细节，新增协议不必改这层。
type InboundConfig struct {
	Tag      string
	Protocol string // vless / vmess / trojan / shadowsocks / hysteria2 / tuic / anytls
	Listen   string
	Port     int

	// Kernel 是面板指定的承载内核：auto / sing-box / xray-core。
	// 空值等同 auto。只对两个内核都支持的协议有意义 ——
	// mieru、juicity 各自只有一种实现，这个字段对它们无效。
	Kernel string

	Raw map[string]any
}

// Outbound 是一条出站描述。
//
// Settings 是协议特有参数（server / port / uuid / password / tls …），
// 键名沿用 sing-box 的命名。这是个有意的选择：sing-box 是默认内核，
// 让它这条路径零翻译；xray 那边做一次映射即可。反过来的话两边都要翻译。
type Outbound struct {
	Tag      string
	Type     string
	Settings map[string]any
}

// Route 是一条分流规则。
//
// Matcher 的键同样用 sing-box 的命名：domain / domain_suffix /
// domain_keyword / domain_regex / ip_cidr / geoip / port / network / protocol。
// 空 Matcher 表示无条件匹配，也就是兜底出口。
type Route struct {
	Matcher     map[string]any
	OutboundTag string
}

// Routing 是一个节点完整的出站与分流配置。
type Routing struct {
	Outbounds []Outbound
	Routes    []Route
}

// Core 是内核必须提供的能力。
//
// 所有方法都要求并发安全：用户同步与流量上报跑在不同的 goroutine 里。
type Core interface {
	// Type 返回内核标识，用于日志与多内核共存时的区分
	Type() string

	Start(ctx context.Context) error
	Close() error

	// AddInbound 启动一个入站。重复调用同一 Tag 应当先移除旧的。
	AddInbound(cfg *InboundConfig) error
	DelInbound(tag string) error

	// AddUsers 增量添加。已存在的 UUID 应当被忽略而不是报错 ——
	// 面板每次下发全量列表，重复是常态而非异常。
	AddUsers(tag string, users []User) error

	// DelUsers 按 UUID 移除。不存在的 UUID 同样静默忽略。
	DelUsers(tag string, uuids []string) error

	// GetTraffic 取出并清零该入站上所有用户的流量增量。
	//
	// 取出即清零是刻意的：若分成「读」和「清零」两步，
	// 两步之间产生的流量会被永久丢失，长期运行下累积的误差可观。
	GetTraffic(tag string) ([]UserTraffic, error)

	// OnlineIPs 返回各用户当前的活跃来源 IP，用于设备数限制。
	OnlineIPs(tag string) map[int64][]string

	// SetRouting 设置某个入站的出站与分流。传 nil 表示该入站恢复直出。
	//
	// 按入站而不是按内核设置，是因为一个节点端进程会同时服务多个节点，
	// 而每个节点有自己的分流策略 —— 节点 A 要 Netflix 走中转甲、
	// 节点 B 要走中转乙是完全正常的需求。内核内部只有一张路由表，
	// 实现时必须把各节点的规则用「入站限定」隔开，否则会互相污染。
	SetRouting(inboundTag string, r *Routing) error
}
