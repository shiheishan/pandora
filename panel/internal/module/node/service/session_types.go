package service

// 会话层与传输层之间的消息类型（与 proto 同义，传输层负责互转）。

import (
	"time"

	"github.com/google/uuid"
)

// HostInfo 是节点自报的机器信息；采不到的项为零，落库时存「未知」（NULL）。
type HostInfo struct {
	Hostname, OS, Arch     string
	CPUCores               uint32
	MemoryBytes, DiskBytes uint64
}

// Hello 是流上的第一条消息。
type Hello struct {
	AgentVersion string
	// BinarySHA256 是节点自报的二进制摘要（hex），只用来在后台提示版本不一致
	BinarySHA256 string
	Features     []string
	Host         HostInfo
	// AppliedConfigs 是各节点已应用的配置摘要（节点 id → content_sha256）
	AppliedConfigs map[uuid.UUID]string
	// AppliedUsers 是各节点内核里的名单版本（节点 id → u1-…）
	AppliedUsers map[uuid.UUID]string
	// BuildSerial 是节点二进制的发版序号（开发构建为 0），在线升级判断目标是否更新用
	BuildSerial uint64
	// LastUpgrade 是上一次升级的结局（节点本机结果文件）；没有为 nil
	LastUpgrade *UpgradeReport
}

// 节点运行状态（nodes.runtime_status 的取值）。
const (
	RuntimeRunning  = "running"
	RuntimeDegraded = "degraded"
	RuntimeDown     = "down"
)

// NodeCert 是节点端报的入站证书状态（心跳 NodeRuntime.cert）。
type NodeCert struct {
	Identifier  string     // acme 证书的名字（域名或 IP）；file 为空
	NotAfter    *time.Time // 当前证书到期时间；nil = 还没有证书
	ErrorCode   string     // 最近一次申请 / 续期失败的原因码；成功后为空
	ErrorDetail string
	NextAttempt *time.Time // 下次申请或续期的计划时间；nil = 没有
}

// NodeRuntime 是心跳里一个节点的运行状态。
type NodeRuntime struct {
	NodeID uuid.UUID
	// Status 是 RuntimeRunning / RuntimeDegraded / RuntimeDown
	Status string
	// Reason 是降级原因码，running 时为空
	Reason string
	// ReasonNodeID 是端口被同机另一节点占用时的占用者
	ReasonNodeID *uuid.UUID
	// Cert 是这拍心跳带的入站证书状态；nil = 这拍没带（不用证书的协议，或旧节点端）
	Cert *NodeCert
}

// Heartbeat 是一拍心跳。
type Heartbeat struct {
	// Metrics 为 nil 表示这拍没带指标
	Metrics *HostMetrics
	Nodes   []NodeRuntime
}

// ConfigPhase 是配置回执的阶段。
type ConfigPhase int

// 配置回执阶段（与 proto ConfigPhase 同义）。
const (
	ConfigSwitched ConfigPhase = iota + 1
	ConfigHealthPassed
	ConfigFailed
)

// ConfigAck 是一个节点应用某版配置的回执。
type ConfigAck struct {
	NodeID     uuid.UUID
	SHA256     string
	Phase      ConfigPhase
	ReasonCode string
	Detail     string
}

// UsersAck 是一个节点应用某版名单的回执。
type UsersAck struct {
	NodeID     uuid.UUID
	Version    string
	OK         bool
	ReasonCode string
}

// OutKind 是面板发往节点的消息种类。
type OutKind int

// 发往节点的消息种类（与 proto SessionResponse.body 一一对应；OutReconnect、OutTombstone、OutUpgrade 是 Command）。
const (
	OutWelcome OutKind = iota + 1
	OutConfig
	OutUsersFull
	OutUsersDelta
	OutTrafficAck
	OutCertificate
	OutReconnect
	OutTombstone
	OutUpgrade
)

// Welcome 是对 Hello 的应答。
type Welcome struct {
	SessionID                       string
	Features                        []string
	PushInterval, HeartbeatInterval time.Duration
	ServerTime                      time.Time
}

// ServerConfigOut 是这台服务器全部节点的全量配置。
type ServerConfigOut struct {
	// ReleaseID 是这一份全量的内容哈希（日志与排障用）
	ReleaseID string
	Nodes     []NodeConfigDoc
}

// Reconnect 让节点在 In 加上 [0, Jitter) 后重连。
type Reconnect struct {
	In, Jitter time.Duration
}

// Outbound 是发往节点的一条逻辑消息（按 Kind 读对应字段）。UsersFull 由传输层按 UsersChunkSize 分块。
type Outbound struct {
	Kind    OutKind
	Welcome Welcome
	Config  ServerConfigOut
	// Roster：OutUsersFull 是整份名单，OutUsersDelta 是目标名单（NodeID、Version 取它的）
	Roster *NodeRoster
	// OutUsersDelta 的起点版本、新增或属性有变的用户（UID 升序）、移除的 UID（升序）
	DeltaFrom string
	Upserted  []RosterUser
	Removed   []int64
	// OutTrafficAck
	ReportID string
	// OutCertificate
	CertChainPEM, CAPEM []byte
	// OutReconnect
	Reconnect Reconnect
	// OutTombstone
	Reason string
	// OutUpgrade
	Upgrade UpgradeCommand
}
