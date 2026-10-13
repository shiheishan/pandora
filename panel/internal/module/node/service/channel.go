package service

// 节点通道（第二波）各块之间的类型与调用口，由主 Agent 定，签名不改；要改写进交回说明。
//
// 分工：p5-enroll（CA、身份、接入令牌、gRPC 服务端）、p5-session（会话管理：对齐、下发、回执、心跳、续期）、
// p5-delivery（名单与订阅线路）、p5-routing（出站、分流、路由组与节点配置文档）、p5-traffic（流量入账、在线、指标）。
// 会话层只经下面这些方法拿名单与配置、交上报；各方法在各块自己的文件里实现（骨架先放 panic 占位）。

import (
	"net/netip"

	"github.com/google/uuid"
)

// ServedOnServerCond 在 repo 包（repo.ServedOnServerCond）：一台服务器「该跑哪些节点」的唯一口径，
// 名单（Rosters）与配置（ServerConfigs）按同一个节点集合给。

// RosterUser 是名单里的一个用户。
type RosterUser struct {
	// UID 是 subscriptions.node_uid
	UID int64
	// UUID 是 subscriptions.proxy_uuid
	UUID uuid.UUID
	// SpeedLimitKbps 为 0 表示不限速
	SpeedLimitKbps uint32
	// DeviceLimit 为 0 表示不限设备数
	DeviceLimit uint32
}

// NodeRoster 是一个节点的完整名单。
type NodeRoster struct {
	NodeID uuid.UUID
	// Version 是内容哈希 "u1-" + 小写 hex：同样的用户集合（含限速、设备数）得到同样的版本，与节点、服务器无关
	Version string
	// Users 按 UID 升序，UID 不重复
	Users []RosterUser
}

// NodeConfigDoc 是下发给节点的一份生效配置文档（proto NodeConfig）。
type NodeConfigDoc struct {
	NodeID   uuid.UUID
	NodeType string
	// JSON 是配置文档（JSON 对象，键见 .claude/rules/pdnd-node-panel.md 的配置文档一节）
	JSON []byte
	// SHA256 是 JSON 的 SHA-256 小写 hex；节点按它判断是否已应用
	SHA256 string
}

// TrafficReport 是节点的一份流量上报（这台服务器各节点的增量）。
type TrafficReport struct {
	// ReportID 是节点给的幂等键：同一服务器同一 id 只入账一次，内容不同是错误（ErrTrafficReportConflict）
	ReportID string
	Entries  []TrafficEntry
}

// TrafficEntry 是一个节点上一个用户的增量字节数（实际字节，未乘倍率）。
type TrafficEntry struct {
	NodeID           uuid.UUID
	UID              int64
	Upload, Download uint64
}

// OnlineEntry 是一个节点上一个用户当前在线的来源 IP。
type OnlineEntry struct {
	NodeID uuid.UUID
	UID    int64
	IPs    []netip.Addr
}

// HostMetrics 是机器指标（整数口径，与 proto HostMetrics 同义）。
type HostMetrics struct {
	CPUBp                         uint32 // CPU 使用率，万分之一
	MemUsedBytes, MemTotalBytes   uint64
	DiskUsedBytes, DiskTotalBytes uint64
	Load1Cp, Load5Cp, Load15Cp    uint32 // 负载 × 100
	NetRxBytes, NetTxBytes        uint64 // 开机以来累计
	TCPConns                      uint32
	UptimeSec                     uint64
	// Partial 为真表示有的项没采到（取 0）
	Partial bool
}

// RosterMembership 回答「最近真正发给这台服务器的两版名单（当前与上一版）里，节点 nodeID 有没有 uid」，由会话层按自己已下发的名单实现。
// 看两版是为了刚被摘掉的用户在摘掉前那个上报周期里用的流量照样扣（10-13 定）。
// 流量只扣名单内的 uid、在线 IP 只收名单内的 uid；名单外的条目照样留档，不扣费。
type RosterMembership interface {
	Has(nodeID uuid.UUID, uid int64) bool
}

// SessionControl 由会话管理（p5-session）实现：身份吊销、服务器删除后调，事务提交之后才调。
type SessionControl interface {
	// Tombstone 先给这台服务器的在途流发 Command.tombstone，再关闭它；没有在途流时什么也不做。
	Tombstone(serverID uuid.UUID, reason string)
}

// UseSessions 只在启动装配时调用（不并发安全）。没装配时吊销只改库，在途流要等下一次建流校验才断。
func (s *Service) UseSessions(c SessionControl) { s.sessions = c }
