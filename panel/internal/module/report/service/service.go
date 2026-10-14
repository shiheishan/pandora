// Package service 是 report 模块的业务层：后台看板（概览、营收按日、流量排行、通知积压、需要处理、系统状态、行为趋势）、
// 营收报表调整与冲正、行为趋势的按天汇总。
//
// report 只拥有两张表（revenue_report_adjustments、activity_daily）；其余数字一律经属主模块 service 的聚合读（端口见下），
// 不读别人的表。切日一律按站点时区（settings 的 site.timezone）。
//
// 营收口径：实际收入 = 平台收入科目（platform_revenue）全部分录的净额（贷 − 借），与旧口径相同；
// 报表调整来自本模块的只追加表，只影响展示值（displayed = actual + adjustment），不回写账本。
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	commissionsvc "github.com/aegispanel/aegis/internal/module/commission/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	nodesvc "github.com/aegispanel/aegis/internal/module/node/service"
	notifysvc "github.com/aegispanel/aegis/internal/module/notify/service"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	"github.com/aegispanel/aegis/internal/module/report/model"
	"github.com/aegispanel/aegis/internal/module/report/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	supportsvc "github.com/aegispanel/aegis/internal/module/support/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/redis/go-redis/v9"
)

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// store 是 service 对本模块 repo 的全部要求；*repo.Repo 实现它。
type store interface {
	// InsertAdjustment 插入一行调整或冲正（ID、CreatedAt 由库生成并回填）。
	InsertAdjustment(ctx context.Context, a *model.Adjustment) error
	// GetAdjustment 按 id 读一行；不存在回 false。
	GetAdjustment(ctx context.Context, id uuid.UUID) (model.Adjustment, bool, error)
	// LockAdjustment 取这一行的事务级咨询锁，串行化对同一行的冲正（只追加的表不能 FOR UPDATE）。
	LockAdjustment(ctx context.Context, id uuid.UUID) error
	// HasReversal 报告这一行是否已被冲正。
	HasReversal(ctx context.Context, id uuid.UUID) (bool, error)
	// ListAdjustments 按 created_at、id 倒序取至多 limit 行；currency 为空取全部币种。Reversed 表示该行已被冲正。
	ListAdjustments(ctx context.Context, currency string, limit int) ([]repo.AdjustmentRow, error)
	// AdjustmentSumsByDay 按（币种, 生效日）汇总生效日在 [fromDay, toDay) 的调整金额（含冲正行）。
	AdjustmentSumsByDay(ctx context.Context, fromDay, toDay string) ([]repo.AdjustmentDayRow, error)
	// AdjustmentTotals 按币种汇总全部调整金额（含冲正行）。
	AdjustmentTotals(ctx context.Context) ([]repo.AdjustmentTotalRow, error)
	// UpsertActivity 按 (tz, day) 覆盖写一批按天汇总。
	UpsertActivity(ctx context.Context, rows []model.ActivityDaily) error
	// ActivityRows 读 tz 下日子在 [fromDay, toDay) 的汇总行，按日子升序。
	ActivityRows(ctx context.Context, tz, fromDay, toDay string) ([]model.ActivityDaily, error)
	// PurgeActivity 删日子早于 beforeDay 的汇总行，一次至多 batch 行，返回删除行数。
	PurgeActivity(ctx context.Context, beforeDay string, batch int) (int, error)
	// Ping 做一次 SELECT 1 往返。
	Ping(ctx context.Context) error
	// DatabaseStats 一条语句读库大小、当前连接数与连接上限。
	DatabaseStats(ctx context.Context) (repo.DatabaseStatsRow, error)
}

var _ store = (*repo.Repo)(nil)

// Settings 给站点时区；*settingssvc.Service 直接满足。
type Settings interface {
	Site(ctx context.Context) (settingssvc.Site, error)
}

// Users 是 identity 的聚合读；*identitysvc.Service 直接满足。
type Users interface {
	ReportUserCounts(ctx context.Context, since, recentSince time.Time) (identitysvc.ReportUserCounts, error)
	// Emails 批量取邮箱（调整列表的登记人、流量排行的脱敏邮箱）。
	Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
}

// Subscriptions 是 subscription 的聚合读；*subsvc.Service 直接满足。
type Subscriptions interface {
	ReportSubscriptionCounts(ctx context.Context, now, expiringBefore, createdSince time.Time) (subsvc.ReportSubscriptionCounts, error)
	OwnersBySubscription(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]uuid.UUID, error)
	ActiveUsersByDay(ctx context.Context, from, to time.Time) ([]subsvc.ActiveUsersDay, error)
}

// Nodes 是 node 的聚合读；*nodesvc.Service 直接满足。
type Nodes interface {
	ReportNodeCounts(ctx context.Context, now time.Time) (nodesvc.ReportNodeCounts, error)
	OfflineNodes(ctx context.Context, now time.Time, sample int) (nodesvc.OfflineNodes, error)
	ConfigLaggingCount(ctx context.Context) (int64, error)
	TrafficByNode(ctx context.Context, from, to time.Time) ([]nodesvc.NodeTraffic, error)
	TrafficBySubscription(ctx context.Context, from, to time.Time) ([]nodesvc.SubscriptionTraffic, error)
	TrafficQuality(ctx context.Context, from, to time.Time) (nodesvc.TrafficQuality, error)
}

// Ledger 是 ledger 的聚合读；*ledgersvc.Service 直接满足。
type Ledger interface {
	RevenueByDay(ctx context.Context, from, to time.Time) ([]ledgersvc.RevenueDay, error)
	RevenueTotals(ctx context.Context) ([]ledgersvc.RevenueTotal, error)
	DriftAccounts(ctx context.Context) (int64, error)
}

// Orders 是 order 的聚合读；*ordersvc.Service 直接满足。
type Orders interface {
	ReportOrderCounts(ctx context.Context, since, staleBefore time.Time) (ordersvc.ReportOrderCounts, error)
	PendingStaleCount(ctx context.Context, staleBefore time.Time) (int64, error)
}

// Payments 是 payment 的聚合读；*paymentsvc.Service 直接满足。
type Payments interface {
	StuckCallbackCount(ctx context.Context, receivedBefore time.Time) (int64, error)
}

// Commissions 是 commission 的聚合读；*commissionsvc.Service 直接满足。
type Commissions interface {
	PendingWithdrawalSums(ctx context.Context) ([]commissionsvc.PendingWithdrawalSum, error)
}

// Tickets 是 support 的聚合读；*supportsvc.Service 直接满足。
type Tickets interface {
	OpenTicketStats(ctx context.Context, now time.Time) (supportsvc.OpenTicketStats, error)
}

// Notices 是 notify 的聚合读；*notifysvc.Service 直接满足。
type Notices interface {
	DeliveryBacklog(ctx context.Context, now time.Time) (notifysvc.DeliveryBacklog, error)
	DeliveryCountsByChannel(ctx context.Context) (map[string]notifysvc.ChannelDeliveryCounts, error)
}

// Activity 是 audit 的按天行为统计；*auditsvc.Service 直接满足。
type Activity interface {
	ActivityByDay(ctx context.Context, from, to time.Time, a auditsvc.ActivityActions) ([]auditsvc.ActivityDay, error)
}

// Pinger 探测 Redis 是否可达（系统状态页的 redis 组件），由 app 装配适配器实现。
type Pinger interface {
	Ping(ctx context.Context) error
}

// StreamCounter 报告本机 SSE 连接数与广播订阅是否正常（系统状态页的 sse 组件），由 app 装配 Hub 实现。
type StreamCounter interface {
	Count() int
	Live() bool
}

// Deps 是 report 依赖的各属主模块端口，app 装配时一次给全；缺哪个 MissingPorts 报哪个。
type Deps struct {
	Settings      Settings
	Users         Users
	Subscriptions Subscriptions
	Nodes         Nodes
	Ledger        Ledger
	Orders        Orders
	Payments      Payments
	Commissions   Commissions
	Tickets       Tickets
	Notices       Notices
	Activity      Activity
	Redis         Pinger
}

// Options 是 report 的可配项。
type Options struct {
	// BackupDir 是加密备份所在目录（config 的 backup.dir）；为空表示没配置
	BackupDir string
}

// Service 是 report 模块对外的唯一入口。
type Service struct {
	repo    store
	tx      txRunner
	audit   auditWriter
	deps    Deps
	opt     Options
	now     func() time.Time
	rdb     *redis.Client
	streams StreamCounter
}

// New 构造 report service。
func New(r *repo.Repo, txm *tx.Manager, audit *auditsvc.Service, deps Deps, opt Options) *Service {
	return newService(r, txm, audit, deps, opt)
}

// newService 是 New 的接口版本，单测用假实现。
func newService(r store, txm txRunner, audit auditWriter, deps Deps, opt Options) *Service {
	return &Service{
		repo:  r,
		tx:    txm,
		audit: audit,
		deps:  deps,
		opt:   opt,
		now:   func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) },
	}
}

// MissingPorts 返回没装配的必需端口名（装配完成后由 internal/app 统一检查）。
// 不算必需：streams（UseStreams 只在 serve 里注入 SSE hub，worker 与 admin 子命令没有）。
func (s *Service) MissingPorts() []string {
	d := s.deps
	var out []string
	for _, p := range []struct {
		name string
		ok   bool
	}{
		{"Settings", d.Settings != nil},
		{"Users", d.Users != nil},
		{"Subscriptions", d.Subscriptions != nil},
		{"Nodes", d.Nodes != nil},
		{"Ledger", d.Ledger != nil},
		{"Orders", d.Orders != nil},
		{"Payments", d.Payments != nil},
		{"Commissions", d.Commissions != nil},
		{"Tickets", d.Tickets != nil},
		{"Notices", d.Notices != nil},
		{"Activity", d.Activity != nil},
		{"Redis", d.Redis != nil},
		{"Cache", s.rdb != nil},
	} {
		if !p.ok {
			out = append(out, p.name)
		}
	}
	return out
}

// location 读站点时区；时区名解析失败（tzdata 缺失、设置被手改坏）回 500，不悄悄退回 UTC 把钱算到错的日子里。
func (s *Service) location(ctx context.Context) (*time.Location, error) {
	site, err := s.deps.Settings.Site(ctx)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(site.Timezone)
	if err != nil {
		return nil, errs.Wrap(errs.Internal, fmt.Errorf("站点时区 %q 无法解析: %w", site.Timezone, err))
	}
	return loc, nil
}

// midnight 返回 t 在 loc 里那一天的零点（带 loc）。
func midnight(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// dayString 把站点时区的零点格式化成 YYYY-MM-DD。
func dayString(t time.Time) string { return t.Format(time.DateOnly) }
