package service

import (
	"context"
	"time"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/module/notify/repo"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/google/uuid"
)

// ---- 跨模块端口（启动装配时注入，见 app/wire.go） ----

// Directory 是 notify 要的用户读取面；*identitysvc.Service 满足。
type Directory interface {
	// Emails 批量取用户邮箱（一条 SQL）。
	Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
	// UserGroupID 取用户所在的用户组；没有分组返回 nil。
	UserGroupID(ctx context.Context, userID uuid.UUID) (*uuid.UUID, error)
	// ListGroups 列出全部用户组。
	ListGroups(ctx context.Context) ([]identitysvc.Group, error)
	// BulkRecipients 按后台批量筛选条件取收件人（与预览、导出同一套条件）；total 为命中人数，total 为 0 或超过 max 时不返回名单。
	BulkRecipients(ctx context.Context, f identitysvc.BulkFilter, max int) ([]identitysvc.BulkRecipient, int64, error)
}

// Plans 是 notify 要的套餐读取面；*catalogsvc.Service 满足。
type Plans interface {
	// ListPlans 后台套餐列表（含已归档）。
	ListPlans(ctx context.Context) ([]catalogsvc.PlanSummary, error)
	// PlansByIDs 批量取套餐（含已归档）。
	PlansByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Plan, error)
}

// Subscriptions 是 notify 要的订阅读取面；*subsvc.Service 满足（subscription/service/notify_reads.go）。
type Subscriptions interface {
	// ExpiringForNotify 取 status=active、到期时间在 (after, until] 内的订阅。
	ExpiringForNotify(ctx context.Context, after, until time.Time) ([]subsvc.NotifyCandidate, error)
	// ExpiredForNotify 取 status=expired、续费窗口没关、到期时间在 (endedAfter, endedUntil] 内的订阅；
	// onlyWithoutLive 为 true 时排除名下另有生效订阅的用户。
	ExpiredForNotify(ctx context.Context, endedAfter, endedUntil time.Time, onlyWithoutLive bool) ([]subsvc.NotifyCandidate, error)
	// QuotaUsageForNotify 取生效订阅本期流量用量（含挂在这一份上的流量包余量）达到 minPercent% 的配额行。
	QuotaUsageForNotify(ctx context.Context, minPercent int) ([]subsvc.QuotaNotice, error)
	// LivePlanIDs 取用户名下生效订阅的套餐 id（去重）。
	LivePlanIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
	// SubscriptionsByIDs 按 id 批量读订阅；支付成功通知取套餐与到期时间用。
	SubscriptionsByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]subsvc.Subscription, error)
}

// Orders 是 notify 读订单的端口（order 实现）：扫描给最近履约的订单补写支付成功通知。
type Orders interface {
	PaidForNotify(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int) ([]ordersvc.PaidNotice, error)
}

var (
	_ Directory     = (*identitysvc.Service)(nil)
	_ Plans         = (*catalogsvc.Service)(nil)
	_ Subscriptions = (*subsvc.Service)(nil)
	_ Orders        = (*ordersvc.Service)(nil)
)

// UseOrders 只在启动装配时调用（不并发安全）。
func (s *Service) UseOrders(o Orders) { s.orders = o }

// UseDirectory 只在启动装配时调用（不并发安全）。
func (s *Service) UseDirectory(d Directory) { s.dir = d }

// UsePlans 只在启动装配时调用（不并发安全）。
func (s *Service) UsePlans(p Plans) { s.plans = p }

// UseSubscriptions 只在启动装配时调用（不并发安全）。
func (s *Service) UseSubscriptions(x Subscriptions) { s.subs = x }

// UsePublicBaseURL 设置面板对外地址（Telegram webhook 地址由它拼出）；只在启动装配时调用。
func (s *Service) UsePublicBaseURL(u string) { s.publicBaseURL = u }

// MissingPorts 返回没装配的必需端口名（装配完成后由 internal/app 统一检查）。
// 不算必需：publicBaseURL（UsePublicBaseURL 是配置值，不是端口）。
func (s *Service) MissingPorts() []string {
	var out []string
	if s.dir == nil {
		out = append(out, "Directory")
	}
	if s.plans == nil {
		out = append(out, "Plans")
	}
	if s.subs == nil {
		out = append(out, "Subscriptions")
	}
	if s.orders == nil {
		out = append(out, "Orders")
	}
	if s.events == nil {
		out = append(out, "PluginEvents")
	}
	if s.rt == nil {
		out = append(out, "Realtime")
	}
	return append(out, s.missingXBPorts()...)
}

// telegramReady 读 Telegram 设置并判断能否发送：已启用、有 Bot 用户名、Token 解得开。
func (s *Service) telegramReady(ctx context.Context) (settingssvc.Telegram, bool, error) {
	t, err := s.settings.Telegram(ctx)
	if err != nil {
		return settingssvc.Telegram{}, false, err
	}
	return t, t.Enabled && t.BotUsername != "" && t.BotToken != "", nil
}

// ---- 通知、偏好、公告、Telegram 的 repo 方法（*repo.Repo 实现，见 repo/inbox.go、preferences.go、announcements.go、telegram.go） ----

type p6Store interface {
	// ScanCursor 读扫描游标；没有时 ok 为 false。
	ScanCursor(ctx context.Context, name string) (repo.ScanCursor, bool, error)
	// SetScanCursor 写扫描游标（没有就建）。
	SetScanCursor(ctx context.Context, name string, c repo.ScanCursor) error
	// InsertInbox 写一条站内信。
	InsertInbox(ctx context.Context, it *model.InboxItem) error
	// ListInbox 按 created_at、id 倒序取本人最近 limit 条站内信；onlyUnread 只取未读。
	ListInbox(ctx context.Context, userID uuid.UUID, onlyUnread bool, limit int) ([]model.InboxItem, error)
	// CountUnread 本人未读站内信数。
	CountUnread(ctx context.Context, userID uuid.UUID) (int64, error)
	// MarkInboxRead 把本人的一条未读站内信标为已读；不存在、不属于本人或已读都不算错。
	MarkInboxRead(ctx context.Context, userID, id uuid.UUID) error
	// MarkAllInboxRead 把本人全部未读站内信标为已读，返回行数。
	MarkAllInboxRead(ctx context.Context, userID uuid.UUID) (int64, error)
	// PurgeInbox 删 created_at < before 的站内信，每次至多 batch 行。
	PurgeInbox(ctx context.Context, before time.Time, batch int) (int, error)

	// ExistingEventKeys 返回 keys 里已存在的去重键。
	ExistingEventKeys(ctx context.Context, keys []string) (map[string]bool, error)
	// InsertEvent 写一个去重键；已存在返回 false（不报错）。
	InsertEvent(ctx context.Context, e *model.Event) (bool, error)
	// PurgeEvents 删 created_at < before 的去重键，每次至多 batch 行。
	PurgeEvents(ctx context.Context, before time.Time, batch int) (int, error)

	// Preferences 读本人改过的偏好项。
	Preferences(ctx context.Context, userID uuid.UUID) ([]model.Preference, error)
	// PreferencesForUsers 批量读一批用户改过的偏好项（一条 SQL）。
	PreferencesForUsers(ctx context.Context, userIDs []uuid.UUID) ([]model.Preference, error)
	// UpsertPreference 按主键写一条偏好。
	UpsertPreference(ctx context.Context, p *model.Preference) error

	// ListAnnouncements 后台列表：未撤回/未下线的在前，再按 pinned 降序、发布时间降序取前 limit 条。
	ListAnnouncements(ctx context.Context, limit int) ([]repo.AnnouncementRow, error)
	// LockAnnouncement 读一条公告并加 FOR UPDATE；不存在 ok 为 false。
	LockAnnouncement(ctx context.Context, id uuid.UUID) (repo.AnnouncementRow, bool, error)
	// InsertAnnouncement 新建公告，回填 ID、Version、CreatedAt、UpdatedAt。
	InsertAnnouncement(ctx context.Context, a *repo.AnnouncementRow) error
	// UpdateAnnouncement 按版本 CAS 改内容、定向、状态与时间，version 加 1；版本不符 ok 为 false。
	UpdateAnnouncement(ctx context.Context, a repo.AnnouncementRow, expectedVersion int) (newVersion int, ok bool, err error)
	// WithdrawAnnouncement 按版本 CAS 置 withdrawn（记撤回人与时间），version 加 1；版本不符或已撤回 ok 为 false。
	WithdrawAnnouncement(ctx context.Context, id, by uuid.UUID, expectedVersion int) (newVersion int, ok bool, err error)
	// PublishDueAnnouncements 把 publish_at <= now 的 scheduled 公告转为 published（published_at = now，version 加 1）。
	PublishDueAnnouncements(ctx context.Context, now time.Time) ([]repo.PublishedAnnouncement, error)
	// VisibleAnnouncements 门户可见公告：已发布、在时间窗内、定向命中（groupID 为 nil 表示没有分组），
	// 按 pinned 降序、COALESCE(published_at, created_at) 降序取前 limit 条。
	VisibleAnnouncements(ctx context.Context, now time.Time, groupID *uuid.UUID, planIDs []uuid.UUID, limit int) ([]model.Announcement, error)

	// Binding 读本人的 Telegram 绑定；没有 ok 为 false。
	Binding(ctx context.Context, userID uuid.UUID) (model.TelegramBinding, bool, error)
	// BindingsForUsers 批量读一批用户的绑定（一条 SQL）。
	BindingsForUsers(ctx context.Context, userIDs []uuid.UUID) ([]model.TelegramBinding, error)
	// InsertBinding 写一条绑定；用户或 chat_id 已绑定返回 repo.ErrDuplicate。
	InsertBinding(ctx context.Context, b *model.TelegramBinding) error
	// DeleteBinding 删本人的绑定；没有返回 false。
	DeleteBinding(ctx context.Context, userID uuid.UUID) (bool, error)
	// DeleteUnusedBindCodes 删本人全部未用的绑定码。
	DeleteUnusedBindCodes(ctx context.Context, userID uuid.UUID) error
	// InsertBindCode 写一个绑定码；哈希撞了返回 repo.ErrDuplicate。
	InsertBindCode(ctx context.Context, c *model.TelegramBindCode) error
	// LockBindCode 按哈希取未用、未过期（expires_at > now）的绑定码并加 FOR UPDATE；没有 ok 为 false。
	LockBindCode(ctx context.Context, hash []byte, now time.Time) (model.TelegramBindCode, bool, error)
	// MarkBindCodeUsed 把绑定码标为已用；已用过返回 false。
	MarkBindCodeUsed(ctx context.Context, id uuid.UUID) (bool, error)
	// PurgeBindCodes 删 expires_at < before 的绑定码，每次至多 batch 行。
	PurgeBindCodes(ctx context.Context, before time.Time, batch int) (int, error)

	// InsertDeliveries 批量写投递（ID 由调用方生成）。
	InsertDeliveries(ctx context.Context, ds []model.Delivery) error
	// MarketingEmailOptOuts 在 userIDs 里查出关闭了营销邮件偏好的用户（一条 SQL）。
	MarketingEmailOptOuts(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}

// ---- 站内信与偏好（inbox.go） ----

// InboxEntry 是收件箱里的一条站内信。SentAt 就是写入时间。
type InboxEntry struct {
	ID      uuid.UUID
	Code    string
	Subject string
	Body    string
	SentAt  time.Time
	ReadAt  *time.Time
}

// Preference 是一项通知偏好（类别 × 渠道）；Locked 的项恒开、不能改。
type Preference struct {
	Category string
	Channel  string
	Enabled  bool
	Locked   bool
}

// ---- 公告（announce.go、announce_admin.go） ----

// PlanRef 是公告定向的套餐；套餐已不存在时 Name 为「不可用套餐」、Status 为 missing。
type PlanRef struct {
	ID     uuid.UUID
	Name   string
	Status string
}

// GroupRef 是公告定向的用户组。
type GroupRef struct {
	ID   uuid.UUID
	Name string
}

// AnnouncementView 是后台公告列表的一行。
type AnnouncementView struct {
	ID           uuid.UUID
	Title        string
	Body         string
	Severity     string
	Pinned       bool
	Status       string
	Version      int
	PlanIDs      []uuid.UUID // 永不为 nil
	PlanTargets  []PlanRef   // 与 PlanIDs 同序；永不为 nil
	GroupIDs     []uuid.UUID // 永不为 nil
	GroupTargets []GroupRef  // 只含还存在的组，按名称升序；永不为 nil
	PublishAt    *time.Time
	ExpiresAt    *time.Time
	CreatedAt    time.Time
}

// AdminAnnouncements 是后台公告页一次读到的全部内容：公告、可定向的套餐与用户组。
type AdminAnnouncements struct {
	Announcements []AnnouncementView
	Plans         []PlanRef
	UserGroups    []GroupRef
}

// AnnouncementInput 是一次公告保存：ID 为 nil 即新建。Title、Body 已去首尾空白；
// PlanIDs、GroupIDs 已去重；时间已转 UTC。Publish 为 true 表示发布（PublishAt 在未来则定时）。
type AnnouncementInput struct {
	ID              *uuid.UUID
	Title           string
	Body            string
	Severity        string
	Pinned          bool
	PlanIDs         []uuid.UUID
	GroupIDs        []uuid.UUID
	PublishAt       *time.Time
	ExpiresAt       *time.Time
	Publish         bool
	ExpectedVersion int
}

// AnnouncementSaved 是保存结果。
type AnnouncementSaved struct {
	ID      uuid.UUID
	Status  string
	Version int
}

// PortalAnnouncement 是门户看到的一条公告；PublishedAt 为 COALESCE(published_at, created_at)。
type PortalAnnouncement struct {
	ID          uuid.UUID
	Title       string
	Body        string
	Severity    string
	Pinned      bool
	PublishedAt time.Time
}

// ---- Telegram（telegram.go、telegram_settings.go） ----

// TelegramSettingsView 是后台 Telegram 设置；Bot Token 只给「是否已设置」。
type TelegramSettingsView struct {
	Enabled     bool
	BotUsername string
	HasToken    bool
	AdminChatID *int64
}

// TelegramSettingsInput 是后台 Telegram 设置的保存输入。BotToken 为空串表示不改；
// AdminChatSet 为 false 时不动管理员群组，为 true 时写 AdminChatID（nil 即清除）。
type TelegramSettingsInput struct {
	Enabled      bool
	BotUsername  string
	BotToken     string
	AdminChatSet bool
	AdminChatID  *int64
}

// TelegramBindingInfo 是门户看到的绑定状态；Enabled 表示站点的 Telegram 通知可用（已启用、有 Bot 用户名与 Token）。
type TelegramBindingInfo struct {
	Bound       bool
	Username    string
	BotUsername string
	Enabled     bool
	DiscussLink string // 讨论群链接，后台没设或 Telegram 不可用时为空串
}

// BindCode 是一次性绑定码。
type BindCode struct {
	Code        string
	BotUsername string
	ExpiresAt   time.Time
}
