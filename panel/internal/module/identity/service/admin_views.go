package service

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 本文件由块负责人定稿：后台用户列表、用户详情、用户组列表要的跨模块数据（视图类型与端口）。
// 实现方：subscription（OpsSubscriptions.AdminSubs，subscription/service/admin_reads.go）、
// app 适配器（OpsOrders.RecentOrders、OpsWallets、OpsGroupUsage，app/adapters.go）、notify（OpsTelegram）。
// 改这里要同时改实现方。

// ---- 订阅 ----

// AdminQuota 是一行配额（与门户「我的套餐」同口径）；Limit 为 nil 表示不限，Remaining 同理。
type AdminQuota struct {
	Metric string
	// Period 配额周期 total / cycle / day / month；后台改上限要按指标 + 周期定位一行
	Period    string
	Limit     *int64
	Consumed  int64
	Remaining *int64
}

// AdminSub 是后台看到的一份订阅。
type AdminSub struct {
	ID          uuid.UUID
	Label       *string
	PlanName    string
	PlanVersion int
	// Status 是库里的 status（active / expired）
	Status string
	// Live 是此刻是否生效（subscription 的状态口径 live）
	Live        bool
	PeriodStart time.Time
	PeriodEnd   time.Time
	// Amount、Currency 是订阅上的价格快照
	Amount   decimal.Decimal
	Currency string
	// Quotas 按 metric 升序、同一指标内周期起点倒序；永不为 nil
	Quotas []AdminQuota
	// DeviceLimitOverride 是管理员覆盖（nil 沿用套餐版本，0 不限）；PlanMaxDevices 是锁定版本的设备数（nil 不限）
	DeviceLimitOverride *int
	PlanMaxDevices      *int
	// OnlineDevices 是在线设备数（计数口读失败时按 0）
	OnlineDevices int
	// PackRemainingBytes 是挂在这一份上的流量包余量
	PackRemainingBytes int64
}

// AdminUserSubs 是一个用户的全部订阅。
type AdminUserSubs struct {
	// Subscriptions 按创建时间倒序；永不为 nil
	Subscriptions []AdminSub
	// Current 是「当前订阅」（生效的优先，再到期晚的，再创建晚的），是 Subscriptions 里那一项的副本；没有订阅为 nil
	Current *AdminSub
	// UnattachedPackBytes 是还没加到任何一份的流量包余量
	UnattachedPackBytes int64
}

// ---- 订单 ----

// AdminOrderRow 是后台订单列表的一行（与 GET v1/orders 同口径，金额是 decimal，handler 转分）。
type AdminOrderRow struct {
	ID             uuid.UUID
	OrderNo        string
	UserEmail      string
	Kind           string
	Status         string
	Currency       string
	Total          decimal.Decimal
	Payable        decimal.Decimal
	Paid           decimal.Decimal
	BalanceApplied decimal.Decimal
	CreatedAt      time.Time
	PaidAt         *time.Time
	ProviderCode   *string
	ProviderName   *string
	// PlanName 是首项为套餐时的快照名，否则空串；Interval、IntervalCount 是首项的周期，没有为空串、0
	PlanName      string
	Interval      string
	IntervalCount int
	ItemCount     int
	Manual        bool
}

// CurrencyAmount 是某币种下的一笔金额。
type CurrencyAmount struct {
	Currency string
	Amount   decimal.Decimal
}

// ---- Telegram ----

// TelegramRef 是用户的 Telegram 绑定。
type TelegramRef struct {
	Username string
	BoundAt  time.Time
}

// ---- 用户组引用 ----

// PoolRef 是一个节点池的 id 与名称。
type PoolRef struct {
	ID   uuid.UUID
	Name string
}

// GroupUsage 是一个用户组被别处引用的情况。
type GroupUsage struct {
	// Plans 是把该组列入可见名单的套餐数（含已归档）
	Plans int64
	// Prices 是该组的专属价格数（不分状态）
	Prices int64
	// Coupons 是限定该组的优惠券数（不分状态）
	Coupons int64
	// ExclusivePools 是把该组列入「仅限用户组」名单的节点池，按名称、id 升序
	ExclusivePools []PoolRef
}

// ---- 端口 ----

// OpsWallets 由 app 适配器包 ledger 实现。
type OpsWallets interface {
	// UserBalances 返回站点币种，与这批用户在该币种下的可用余额（每个入参都有值，没有科目的为 0）。
	UserBalances(ctx context.Context, userIDs []uuid.UUID) (currency string, balances map[uuid.UUID]decimal.Decimal, err error)
}

// OpsTelegram 由 notify 实现。
type OpsTelegram interface {
	// AdminTelegramBinding 返回用户的 Telegram 绑定；没绑定为 nil。
	AdminTelegramBinding(ctx context.Context, userID uuid.UUID) (*TelegramRef, error)
}

// OpsGroupUsage 由 app 适配器组合 catalog、promo、node 实现。
type OpsGroupUsage interface {
	// GroupUsage 返回各用户组被引用的情况；没有任何引用的组不在结果里。
	GroupUsage(ctx context.Context) (map[uuid.UUID]GroupUsage, error)
}

// UseOpsWallets 只在启动装配时调用（不并发安全）。
func (s *Service) UseOpsWallets(w OpsWallets) { s.wallets = w }

// UseOpsTelegram 只在启动装配时调用（不并发安全）。
func (s *Service) UseOpsTelegram(t OpsTelegram) { s.telegram = t }

// UseOpsGroupUsage 只在启动装配时调用（不并发安全）。
func (s *Service) UseOpsGroupUsage(g OpsGroupUsage) { s.groupUsage = g }

func (s *Service) needWallets() (OpsWallets, error) {
	if s.wallets == nil {
		return nil, errs.Wrap(errs.Unavailable, errNotWired)
	}
	return s.wallets, nil
}

func (s *Service) needTelegram() (OpsTelegram, error) {
	if s.telegram == nil {
		return nil, errs.Wrap(errs.Unavailable, errNotWired)
	}
	return s.telegram, nil
}

func (s *Service) needGroupUsage() (OpsGroupUsage, error) {
	if s.groupUsage == nil {
		return nil, errs.Wrap(errs.Unavailable, errNotWired)
	}
	return s.groupUsage, nil
}
