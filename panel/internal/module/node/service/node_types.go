package service

// 服务器与节点后台编排的对外类型与共用的错误构造。
// 对外方法的参数与返回值只用这里的类型，不暴露 model。

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// ---------------------------------------------------------------------------
// 服务器
// ---------------------------------------------------------------------------

// Server 是后台读到的一台服务器（含名下节点计数）。
type Server struct {
	ID              uuid.UUID
	Name            string
	Status          string
	StatusReason    *string
	RowVersion      int64
	Region          *string
	Hostname        *string
	PublicIPv4      *string
	PublicIPv6      *string
	PrivateIPv4     *string
	Architecture    *string
	OSName          *string
	AgentVersion    *string
	LastHeartbeatAt *time.Time
	// HeartbeatOnline：status = ready 且最近心跳在 NodeStaleAfter 之内
	HeartbeatOnline bool
	CPUCores        *int
	MemoryMB        *int
	DiskGB          *int
	CapacityNodes   int
	Notes           *string
	// NodeCount 是名下未删除的节点数（含已退役）
	NodeCount int
	// ActiveNodeCount 是名下 serving_status = active 的节点数
	ActiveNodeCount int
	// ServingNodeCount 是在役且服务器见过心跳的节点数；NeverSeenNodeCount 是在役但服务器从没心跳的节点数
	ServingNodeCount   int
	NeverSeenNodeCount int
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// 以下取自最新一个指标点（node_metrics）；没有点时全为 nil。MB、GB 按 1024 进制整除
	CPUBp       *int
	MemUsedMB   *int64
	MemTotalMB  *int64
	DiskUsedGB  *int64
	DiskTotalGB *int64
	MetricsAt   *time.Time
}

// ServerFilter 是服务器列表的筛选：Status 为空不筛；Query 按名称、主机名子串（不分大小写）。
type ServerFilter struct {
	Status string
	Query  string
}

// ServerInput 是新建服务器的输入；空串表示不填。CapacityNodes 为 0 取缺省 32。
type ServerInput struct {
	Name          string
	Region        string
	Hostname      string
	PublicIPv4    string
	PublicIPv6    string
	PrivateIPv4   string
	Architecture  string
	OSName        string
	CapacityNodes int
	Notes         string
}

// ServerPatch 是改服务器的输入：nil = 不改，空串 = 清空（Name 不能清空）。
type ServerPatch struct {
	RowVersion    int64
	Name          *string
	Region        *string
	Hostname      *string
	PublicIPv4    *string
	PublicIPv6    *string
	PrivateIPv4   *string
	Architecture  *string
	OSName        *string
	CapacityNodes *int
	Notes         *string
}

// ServerNode 是服务器名下的一个节点（GET /servers/:id/nodes，含已退役）。
type ServerNode struct {
	ID                uuid.UUID
	Name              string
	ServingStatus     string
	NodeType          *string
	DisplayName       *string
	ServerHost        *string
	ServerPort        *int
	TrafficRate       decimal.Decimal
	ConfigValidatedAt *time.Time
	// LastHeartbeatAt 取所在服务器的心跳（心跳是服务器级的）
	LastHeartbeatAt *time.Time
	CreatedAt       time.Time
}

// ---------------------------------------------------------------------------
// 节点：写接口
// ---------------------------------------------------------------------------

// AdminNode 是节点写接口（建、改、复制、迁移、上线、退役）回的节点；ProtocolConfig 已抹掉敏感值。
type AdminNode struct {
	ID                    uuid.UUID
	NodeNo                int64
	RowVersion            int64
	Name                  string
	ServerID              *uuid.UUID
	PoolID                *uuid.UUID
	ServingStatus         string
	NodeType              *string
	ServerHost            *string
	ServerPort            *int
	TrafficRate           decimal.Decimal
	DisplayName           *string
	CountryCode           *string
	ProtocolConfig        json.RawMessage
	ProtocolSchemaVersion int
	ConfigValidatedAt     *time.Time
	SortOrder             int
	CreatedAt             time.Time
	UpdatedAt             time.Time
	// Tags 是规范化后的标签，永不为 nil
	Tags []string
	// Traffic 是每周期流量上限与本周期用量
	Traffic NodeTrafficLimit
	// RateSchedule 是时段倍率（按开始时刻升序），永不为 nil
	RateSchedule []RateRangeInput
	// Warnings 是只提示、不拒绝的说明（保留端口、存量协议不满足现行规则、上线后不服务任何人）；没有时为 nil
	Warnings []string
}

// NodeTrafficLimit 是节点的每周期流量上限与本周期用量（迁移 00045）。LimitBytes 为 nil 不限；ResetDay 为 nil 不自动重置；
// UsedBytes 是本周期实际字节（不乘倍率）；ResetAt 是本周期用量起算时刻；ExceededAt 非空 = 已超限、停止下发。
type NodeTrafficLimit struct {
	LimitBytes *int64
	ResetDay   *int
	UsedBytes  int64
	ResetAt    time.Time
	ExceededAt *time.Time
}

// NodeInput 是新建节点的输入。TrafficRate 为 nil 取 1.00；PoolID 为 nil 不划池。
// Tags、TrafficLimitBytes、TrafficResetDay、TrafficRateSchedule 都可不给（没有标签、不限、不自动重置、没有时段倍率）。
type NodeInput struct {
	Name           string
	ServerID       uuid.UUID
	PoolID         *uuid.UUID
	NodeType       string
	ServerHost     string
	ServerPort     int
	TrafficRate    *decimal.Decimal
	DisplayName    string
	CountryCode    string
	ProtocolConfig json.RawMessage
	SortOrder      int

	Tags                []string
	TrafficLimitBytes   *int64
	TrafficResetDay     *int
	TrafficRateSchedule []RateRangeInput
}

// OptionalInt64 区分「没给」与「给了 null」：Set 为 false 不改；Set 且 Value 为 nil 表示清空。
type OptionalInt64 struct {
	Set   bool
	Value *int64
}

// OptionalInt 区分「没给」与「给了 null」：Set 为 false 不改；Set 且 Value 为 nil 表示清空。
type OptionalInt struct {
	Set   bool
	Value *int
}

// OptionalUUID 区分「没给」与「给了 null」：Set 为 false 不改；Set 且 Value 为 nil 表示清空。
type OptionalUUID struct {
	Set   bool
	Value *uuid.UUID
}

// OptionalString 区分「没给」与「给了 null」：Set 为 false 不改；Set 且 Value 为 nil 或空串表示清空。
type OptionalString struct {
	Set   bool
	Value *string
}

// NodePatch 是改节点的输入：指针为 nil = 不改；ProtocolConfig 为 nil = 不改（给了就整体替换）。
type NodePatch struct {
	RowVersion     int64
	Name           *string
	PoolID         OptionalUUID
	NodeType       *string
	ServerHost     *string
	ServerPort     *int
	TrafficRate    *decimal.Decimal
	DisplayName    *string
	CountryCode    OptionalString
	ProtocolConfig json.RawMessage

	// Tags 为 nil 不改，给了就整体替换（空切片 = 清空）
	Tags *[]string
	// TrafficLimitBytes / TrafficResetDay：Set 且 Value 为 nil = 不限 / 不自动重置
	TrafficLimitBytes OptionalInt64
	TrafficResetDay   OptionalInt
	// TrafficRateSchedule 为 nil 不改，给了就整体替换（空切片 = 清空）
	TrafficRateSchedule *[]RateRangeInput
}

// NodeCopyInput 是复制节点的输入：TargetServerID、PoolID 为 nil 沿用原节点；Name 为空取「原名-copy」；
// SortOrder 为 nil 取原节点 +1；ServerPort 为 nil 沿用原端口。CopyRouting 时连节点私有路由与所在路由组一起复制。
type NodeCopyInput struct {
	RowVersion     int64
	Name           string
	TargetServerID *uuid.UUID
	PoolID         *uuid.UUID
	SortOrder      *int
	ServerPort     *int
	CopyRouting    bool
}

// NodeMoveInput 是把草稿节点迁到另一台服务器的输入。
type NodeMoveInput struct {
	RowVersion int64
	ServerID   uuid.UUID
	Reason     string
}

// NodeOrderItem 是排序的一项。
type NodeOrderItem struct {
	ID         uuid.UUID
	RowVersion int64
	SortOrder  int
}

// NodeVersionRef 是带乐观锁版本的节点引用。
type NodeVersionRef struct {
	ID         uuid.UUID
	RowVersion int64
}

// NodeStatusBatch 是批量改服务状态的输入：整批有一条不合法就整批 409，不做部分更新。
type NodeStatusBatch struct {
	Items         []NodeVersionRef
	ServingStatus string
	Reason        string
}

// NodeBatchEdit 是批量修改节点（#50）：Items 带乐观锁版本；Set 里给了的字段才改。整批一个事务，
// 对每个节点走与单个 PATCH 相同的校验与纪元（patchNodeTx）；ServingStatus 非空时先按 BatchNodeStatus 的口径改状态。
// 任一节点不合法整批回滚，错误里带是哪个节点、哪个字段。
type NodeBatchEdit struct {
	Items  []NodeVersionRef
	Set    NodeBatchFields
	Reason string
}

// NodeBatchFields 是批量修改可改的字段（与 Xboard batchUpdate 取交集后再加本项目的上限、标签、时段倍率）：
// 池、倍率、标签、服务状态、流量上限与重置日、时段倍率。语义与 NodePatch 的同名字段相同；全都没给回 422。
type NodeBatchFields struct {
	PoolID              OptionalUUID
	TrafficRate         *decimal.Decimal
	Tags                *[]string
	ServingStatus       string
	TrafficLimitBytes   OptionalInt64
	TrafficResetDay     OptionalInt
	TrafficRateSchedule *[]RateRangeInput
}

// RealityKeypair 是新生成的一对 REALITY 密钥与一个随机 short_id（私钥只在这一次返回）。
type RealityKeypair struct {
	PrivateKey string
	PublicKey  string
	ShortID    string
}

// ---------------------------------------------------------------------------
// 节点：列表与详情
// ---------------------------------------------------------------------------

// NodeQuery 是后台节点列表的筛选与分页。
type NodeQuery struct {
	// IncludeRetired 只在 State 为空时生效：State 给了就由它决定含不含已退役
	IncludeRetired bool
	// State：all（不含已退役）/ online / offline / disabled / retired；空串不筛
	State string
	// Search 在名称、展示名、服务器名、国家、协议、地址、编号里按子串找（不分大小写），最长 120 字
	Search string
	// Tag 非空时只列带这个标签的节点（精确匹配规范化后的标签）
	Tag string
	// ID 非空时只取这一个节点，并带上 Detail
	ID     *uuid.UUID
	Limit  int
	Offset int
}

// ApplyFailure 是最近一次配置应用失败。
type ApplyFailure struct {
	Phase  string
	Detail string
	At     time.Time
}

// NodeRow 是后台节点列表的一行。在线、流量、指标、身份序号、授权套餐按当前页批量取，不逐行查。
type NodeRow struct {
	ID            uuid.UUID
	NodeNo        int64
	RowVersion    int64
	Name          string
	ServingStatus string
	ServerID      *uuid.UUID
	ServerName    *string
	PoolID        *uuid.UUID
	PoolName      *string
	// 以下取自所在服务器（心跳与主机信息是服务器级的）
	AgentVersion    *string
	Hostname        *string
	PublicIPv4      *string
	CPUCores        *int
	MemoryMB        *int
	DiskGB          *int
	LastHeartbeatAt *time.Time
	// Stale：服务器从没心跳或心跳超过 NodeStaleAfter
	Stale bool
	// Delivered / DeliveryNote：这个节点现在会不会出现在用户订阅里、为什么
	Delivered    bool
	DeliveryNote string
	CreatedAt    time.Time
	NodeType     *string
	ServerHost   *string
	ServerPort   *int
	DisplayName  *string
	CountryCode  *string
	SortOrder    int
	// EffectiveState：'' / applied / pending / failed。pending = 节点已应用的配置摘要与面板现在为它算出的文档摘要不同；
	// 新设计没有代数（generation），只比 SHA-256
	EffectiveState    string
	LastApplyFailure  *ApplyFailure
	RuntimeStatus     *string
	RuntimeReason     *string
	RuntimeStateAt    *time.Time
	RuntimeReasonNode *string
	DeliveryDegraded  bool
	// OnlineUsers、OnlineIPs、TrafficBytes24h 来自 NodeLiveStats；没有数据为 0
	OnlineUsers     int
	OnlineIPs       int
	TrafficBytes24h int64
	// CPUPercent、MemPercent、MetricsAt 取所在服务器最新一个指标点；没有点为 nil
	CPUPercent *float64
	MemPercent *float64
	MetricsAt  *time.Time
	// IdentitySerial 是所在服务器当前 active 身份的 seq；没有为 nil
	IdentitySerial *int64
	// Movable：能不能迁移到别的服务器（nodeMovableState，只看节点自己）
	Movable bool
	// GrantedPlans 是绑定了节点所在池的套餐名（去重、升序），永不为 nil
	GrantedPlans []string
	// Tags 是节点标签，永不为 nil
	Tags []string
	// Traffic 是每周期流量上限与本周期用量
	Traffic NodeTrafficLimit
	// RateSchedule 是时段倍率（按开始时刻升序），永不为 nil
	RateSchedule []RateRangeInput
	// RelayCount 是挂在这个节点下的中转条目数（含停用的）
	RelayCount int
	// ECHEnabled 是节点有当前 ECH 密钥（是否真的生效还要看协议，见节点的 ECH 详情）
	ECHEnabled bool
	// Detail 只在 NodeQuery.ID 非空（单取）时填
	Detail *NodeDetail
}

// NodeDetail 是单取时才带的编辑字段；ProtocolConfig 已抹掉敏感值。
type NodeDetail struct {
	TrafficRate           decimal.Decimal
	ProtocolConfig        json.RawMessage
	ProtocolSchemaVersion int
	ConfigValidatedAt     *time.Time
	// Warnings 是存量协议配置不满足现行规则的提示；没有时为 nil
	Warnings []string
}

// NodeStaleAfter 是后台「失联」的判定：服务器心跳超过它即视为失联（列表的 stale、筛选 online / offline）。
const NodeStaleAfter = 90 * time.Second

// ---------------------------------------------------------------------------
// 共用的错误
// ---------------------------------------------------------------------------

// nodeVersionConflict 是节点乐观锁冲突。
func nodeVersionConflict(current int64) error {
	return &errs.Error{Code: errs.Conflict, Message: "节点已被其他管理员修改，请刷新后重试",
		Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", current)}}
}

// serverVersionConflict 是服务器乐观锁冲突。
func serverVersionConflict(current int64) error {
	return &errs.Error{Code: errs.Conflict, Message: "服务器已被其他管理员修改，请刷新后重试",
		Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", current)}}
}

// inventoryConflict 是带自定义中文文案的 409。
func inventoryConflict(message string) error { return errs.Newf(errs.Conflict, message) }

// inventoryInvalid 是单个字段的 422。
func inventoryInvalid(field, message string) error {
	return errs.Invalid(map[string]string{field: message})
}
