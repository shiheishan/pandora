// Package panel 是节点端与面板之间的通道：接入（Enroll）、身份目录、控制会话（Session）。
//
// 只管协议与线格式，不做编排：会话收到的配置、名单、流量回执经 Events 交给 node 包的
// 主循环，主循环要发的心跳、流量、在线、回执经 Send* 交回来。契约见
// docs/architecture.md 第 11 节与 proto/pandora/node/v1/node.proto。
package panel

import (
	"context"
	"errors"
	"net"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
)

// DefaultIdentityDir 是身份目录的缺省位置（与 systemd 单元的 StateDirectory 同在 /var/lib/pandora-native 下）。
const DefaultIdentityDir = "/var/lib/pandora-native/identity"

// DefaultPanelPort 是面板节点通道的缺省端口；--panel 只给主机时补上它。
const DefaultPanelPort = "9443"

// RenewBefore 是证书剩余有效期不到多久时在会话里续期。
const RenewBefore = 10 * 24 * time.Hour

// FeatureUsersDelta 是 Hello 里声明「能收名单增量」的能力名。
const FeatureUsersDelta = "users_delta"

// 身份目录里的文件。
const (
	caFile       = "ca.pem"
	certFile     = "cert.pem"
	keyFile      = "key.pem"
	serverIDFile = "server_id"
	// journalFile 是接入日志：request_id 与私钥在发请求之前落盘，崩溃重跑用同一个 request_id。
	journalFile = "enroll.json"
	// nextDir 是待换上的一套身份文件（接入成功、续期成功时先整套写到这里）。
	nextDir = "next"
	// readyMarker 写在 nextDir 里，表示整套已落盘、可以逐个换上；没有它的 nextDir 是半截，丢弃。
	readyMarker = "ready"
)

// UsersAck 里 ok=false 的原因码。面板对任何 ok=false 都应随后发这个节点的全量名单。
const (
	// UsersReasonVersionMismatch：增量的 from_version 不是本地当前版本，已丢弃。
	UsersReasonVersionMismatch = "version_mismatch"
	// UsersReasonApplyFailed：名单没能装进内核。
	UsersReasonApplyFailed = "apply_failed"
	// UsersReasonResync：内核用户表被清空（入站重建失败、回滚失败等），需要全量重来。
	UsersReasonResync = "resync"
	// UsersReasonIncomplete：全量名单的分块不完整（缺块、乱序、版本中途变了）。
	UsersReasonIncomplete = "incomplete_chunks"
)

// ErrTombstoned 表示面板发来 Tombstone：身份已吊销或服务器已删除，会话不再重连。
var ErrTombstoned = errors.New("面板已吊销本机身份或删除了这台服务器：停止重连，继续用磁盘缓存服务；需要在后台生成新令牌重新接入")

// Dialer 拨号函数（测试注入 bufconn 用），签名与 grpc.WithContextDialer 一致。
type Dialer func(ctx context.Context, addr string) (net.Conn, error)

// EventKind 是会话交给编排层的事件种类。
type EventKind int

const (
	// EventWelcome：流已建立、面板回了 Welcome（每条新流一次）。编排层此时重发未确认的流量、复位回执台账。
	EventWelcome EventKind = iota + 1
	// EventConfig：面板下发这台服务器全部节点的全量配置。
	EventConfig
	// EventUsersFull：某节点的全量名单（各块已收齐）。
	EventUsersFull
	// EventUsersDelta：某节点的名单增量。
	EventUsersDelta
	// EventTrafficAck：面板已落库某份流量上报。
	EventTrafficAck
	// EventDisconnected：当前流断了（之后会按退避重连，除非已 Tombstone）。
	EventDisconnected
	// EventTombstone：面板吊销了身份；会话随后停止重连。
	EventTombstone
)

// Event 是会话交给编排层的一条消息，按 Kind 只有对应字段有值。
type Event struct {
	Kind       EventKind
	Welcome    *nodev1.Welcome
	Config     *nodev1.ServerConfig
	UsersFull  *UsersFull
	UsersDelta *nodev1.UsersDelta
	// ReportID 是 EventTrafficAck 确认的 report_id。
	ReportID string
	// Err 是 EventDisconnected 的原因。
	Err error
	// Reason 是 EventTombstone 的原因。
	Reason string
}

// UsersFull 是收齐各块之后的一个节点的全量名单。
type UsersFull struct {
	NodeID  string
	Version string
	Users   []*nodev1.User
}

// AppliedFunc 返回 Hello 里要报的已应用版本（各节点的配置摘要与名单版本）。
// 会话在自己的 goroutine 里调它，实现必须并发安全。
type AppliedFunc func() (configs []*nodev1.AppliedConfig, users []*nodev1.AppliedUsers)
