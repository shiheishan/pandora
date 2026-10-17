package service

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// addDays 返回零点 t（带站点时区）往后 n 天的零点；按日历加，夏令时那天也落在零点。
func addDays(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d+n, 0, 0, 0, 0, t.Location())
}

// ---------------------------------------------------------------------------
// 概览（GET /overview）
// ---------------------------------------------------------------------------

// Overview 是看板概览。
type Overview struct {
	Users               UserCounts
	Subscriptions       SubscriptionCounts
	Nodes               NodeCounts
	Revenue             RevenueRow
	Orders              OrderCounts
	LedgerDriftAccounts int64
}

// UserCounts 是用户计数；Today 按站点时区的今天零点。
type UserCounts struct {
	Total, Active, Today, Last7Days int64
}

// SubscriptionCounts 是订阅计数；Active 即「生效」（subscription 的 StateLive 口径）。
type SubscriptionCounts struct {
	Active, Expiring7Days, Expired, New7Days int64
}

// NodeCounts 是节点计数。
type NodeCounts struct {
	Total, Online int64
}

// OrderCounts 是订单计数；「今天」按站点时区。
type OrderCounts struct {
	PaidToday, Pending, FailedToday int64
}

// RevenueRow 是全站收入。Actual* 是账本口径（平台收入科目净额），Adjustment* 是报表调整，
// 无前缀的是展示值 = 实际 + 调整。窗口都按站点时区：今天、昨天、近 7 天（含今天）、近 30 天（含今天）、全部。
type RevenueRow struct {
	Today, Yesterday, Last7Days, Last30Days, Total                       decimal.Decimal
	ActualToday, ActualYesterday, Actual7Days, Actual30Days, ActualTotal decimal.Decimal
	AdjustmentToday, Adjustment7Days, Adjustment30Days, AdjustmentTotal  decimal.Decimal
}

// ---------------------------------------------------------------------------
// 营收按日（GET /revenue/timeseries）
// ---------------------------------------------------------------------------

// RevenueSeries 是最近 Days 天（含今天，站点时区）按日补齐的营收，以及紧邻的前一个等长区间的展示净额合计。
type RevenueSeries struct {
	Days          int
	Points        []RevenuePoint
	PreviousTotal decimal.Decimal
}

// RevenuePoint 是一天的营收：DisplayedNet = ActualCredit − ActualDebit + Adjustment。
type RevenuePoint struct {
	Date         string // YYYY-MM-DD
	ActualCredit decimal.Decimal
	ActualDebit  decimal.Decimal
	Adjustment   decimal.Decimal
	DisplayedNet decimal.Decimal
}

// ---------------------------------------------------------------------------
// 报表调整（/revenue/adjustments）
// ---------------------------------------------------------------------------

// Adjustment 是一条报表调整或冲正。
type Adjustment struct {
	ID          uuid.UUID
	Amount      decimal.Decimal
	Reason      string
	EffectiveOn string // YYYY-MM-DD（站点时区的自然日）
	ReversalOf  *uuid.UUID
	CreatedBy   uuid.UUID
	// CreatedByEmail 是登记人邮箱；读不到时为 nil
	CreatedByEmail *string
	CreatedAt      time.Time
	// Reversed 表示这一条已被冲正
	Reversed bool
}

// CreateAdjustmentInput 是登记一条报表调整的输入；Amount 由 handler 从整数分换成元。
type CreateAdjustmentInput struct {
	Amount decimal.Decimal
	Reason string
	// EffectiveOn 为空表示站点时区的今天
	EffectiveOn string
}

// ---------------------------------------------------------------------------
// 流量排行（/dashboard/traffic/*）
// ---------------------------------------------------------------------------

// TrafficQuery 是流量排行的查询：Range 24h / 7d / 30d（空为 7d），Limit 5 / 10 / 20（0 为 10），
// SnapshotAt 为空表示现在，否则是 RFC3339 UTC 时间（不晚于现在、不早于 31 天前）。
type TrafficQuery struct {
	Range      string
	Limit      int
	SnapshotAt string
}

// TrafficWindow 是排行实际计入的窗口：小时桶起点落在 [From, To) 的整桶。From 是 SnapshotAt − 区间后向上取整到整点。
type TrafficWindow struct {
	Range      string
	SnapshotAt time.Time
	From       time.Time
	To         time.Time
}

// TrafficTotals 是窗口内的合计：Reported 是节点上报的实际字节（含名单外），Attributed 是入账到订阅的部分。
type TrafficTotals struct {
	Reported, Attributed, Unattributed int64
}

// TrafficQuality 是窗口内的上报质量计数（新通道同一编号的重复上报不入账、不留行，所以没有重复计数）。
type TrafficQuality struct {
	InvalidReports, InvalidEntries int64
}

// NodeTrafficItem 是节点排行的一行。
type NodeTrafficItem struct {
	NodeID      uuid.UUID
	Name        string
	DisplayName *string
	Upload      int64
	Download    int64
	Total       int64
	LastAt      time.Time
}

// NodeTraffic 是节点流量排行。Returned 是排行里各行之和，Other = Reported − Returned。
type NodeTraffic struct {
	Window   TrafficWindow
	Items    []NodeTrafficItem
	Totals   TrafficTotals
	Returned int64
	Other    int64
	Quality  TrafficQuality
}

// UserTrafficItem 是用户排行的一行；邮箱只给脱敏形式。
type UserTrafficItem struct {
	UserID            uuid.UUID
	EmailMasked       string
	Upload            int64
	Download          int64
	Total             int64
	SubscriptionCount int64
	LastAt            time.Time
}

// UserTraffic 是用户流量排行。Returned 是排行里各行之和，Other = Attributed − Returned。
type UserTraffic struct {
	Window   TrafficWindow
	Items    []UserTrafficItem
	Totals   TrafficTotals
	Returned int64
	Other    int64
	Quality  TrafficQuality
}

// ---------------------------------------------------------------------------
// 通知积压（/dashboard/backlog/notifications）
// ---------------------------------------------------------------------------

// BacklogThreshold 是积压判定：到期未发的最早一条等待超过它即 backlogged。
const BacklogThreshold = 600 * time.Second

// ScannerInterval 是 notify:dispatch 兜底扫描的间隔（展示用）。
const ScannerInterval = 30 * time.Second

// NotificationBacklog 是通知投递积压。State 为 clear / backlogged；Reason 为 no_due_backlog / within_threshold / lag_exceeded。
type NotificationBacklog struct {
	AsOf           time.Time
	State          string
	Ready          int64
	ReadyRetry     int64
	Scheduled      int64
	ScheduledRetry int64
	Failed         int64
	Suppressed     int64
	OldestReadyAt  *time.Time
	MaxReadyLag    int64 // 秒；Ready 为 0 时为 0
	LastSentAt     *time.Time
	Reason         string
}

// ---------------------------------------------------------------------------
// 需要处理（/dashboard/tasks）
// ---------------------------------------------------------------------------

// StaleOrderAfter 是「超时未支付」的门槛：下单这么久还挂着，说明过期任务没跑或卡住。
const StaleOrderAfter = 30 * time.Minute

// Tasks 是「需要处理」：后台不分权限，六项全部给。
type Tasks struct {
	AsOf                 time.Time
	TicketsOpen          TicketsOpenTask
	WithdrawalsPending   WithdrawalsPendingTask
	NodesOffline         NodesOfflineTask
	OrdersPendingStale   OrdersPendingStaleTask
	NotificationsBacklog NotificationsBacklogTask
	LedgerDrift          int64
}

// TicketsOpenTask 是待客服处理的工单。
type TicketsOpenTask struct {
	Count             int64
	HighPriority      int64
	OldestWaitSeconds *int64
}

// WithdrawalsPendingTask 是待审核提现。
type WithdrawalsPendingTask struct {
	Count  int64
	Amount decimal.Decimal
}

// NodeRef 是失联节点样例。
type NodeRef struct {
	ID   uuid.UUID
	Name string
}

// NodesOfflineTask 是失联节点。
type NodesOfflineTask struct {
	Count                 int64
	Sample                []NodeRef
	LongestOfflineSeconds *int64
}

// OrdersPendingStaleTask 是超时未支付的订单。
type OrdersPendingStaleTask struct {
	Count            int64
	ThresholdSeconds int64
}

// NotificationsBacklogTask 是通知积压摘要。
type NotificationsBacklogTask struct {
	Queued       int64
	FailedTotal  int64
	BacklogState string
}

// ---------------------------------------------------------------------------
// 系统状态（/system/status）
// ---------------------------------------------------------------------------

// 组件状态。
const (
	StateOK      = "ok"
	StateWarn    = "warn"
	StateDown    = "down"
	StateUnknown = "unknown"
)

// SystemStatus 是系统状态页：备份、数据库、各组件，State 为 ok / degraded（任一组件 warn 或 down）。
type SystemStatus struct {
	Backup   BackupStatus
	Database *DatabaseStats // 读失败为 nil
	// DatabaseError 是 Database 为 nil 时给页面的一句话（不含内部错误细节）
	DatabaseError string
	State         string
	Components    []Component
}

// DatabaseStats 是数据库体积与连接数（三个数一起读到，或一个都没有）。
type DatabaseStats struct {
	SizeBytes      int64
	Connections    int64
	MaxConnections int64
}

// Component 是一个组件的探测结果。Key 是 postgres、redis、node_fabric、payment_callbacks、mail、telegram、sse、backup 之一（顺序固定）。
// Metrics 的键与值由 handler 原样输出；LatencyMS 只在探测成功时有。
type Component struct {
	Key       string
	State     string
	LatencyMS *float64
	Metrics   map[string]any
	Message   string
}

// BackupFile 是一份加密备份（*.dump.age）。
type BackupFile struct {
	Name        string
	Size        int64
	ModTime     time.Time
	HasChecksum bool // 同名 .sha256 在不在
	HasSeal     bool // 同名 .seal 在不在
}

// BackupStatus 是备份目录的探测结果。Readable 为假时只有 Dir 与 Message；为真时 Message 为空，
// Latest / LatestAgeHours 只在有「有校验、有封条」的备份时有，MissingChecksum / MissingSeal 只在有备份文件时有。
type BackupStatus struct {
	Dir             string
	Readable        bool
	Message         string
	Count           int
	TotalBytes      int64
	Latest          *BackupFile
	LatestAgeHours  *int
	Stale           bool
	MissingChecksum *int
	MissingSeal     *int
	Recent          []BackupFile // 最新的至多 5 份
	// OffsiteConfigured 表示配了异地备份（WebDAV 配置文件在不在）；只在 Readable 时有意义
	OffsiteConfigured bool
}

// ---------------------------------------------------------------------------
// 行为趋势（GET /stats/timeseries）
// ---------------------------------------------------------------------------

// ActivityPoint 是行为趋势的一天。Day 形如 MM-DD（站点时区）。
type ActivityPoint struct {
	Day         string
	Registered  int64
	Logins      int64
	Orders      int64
	UniqueIPs   int64
	ActiveUsers int64
}
