package service

import (
	"context"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/shopspring/decimal"
)

// 本文件由块负责人定稿：批量运营（预览、导出、群发收件人、批量生成）、用户画像、访问明细、
// 同源 IP 风控用到的对外类型，以及这些功能要的端口。改这里要同时改实现方（subscription 的 ops_reads.go、
// notify 的 bulk_mail.go、app 装配），先和块负责人对齐。

// ---- 人群筛选 ----

// BulkFilter 圈定一批用户；零值不限。预览、导出、群发（notify 经 BulkRecipients）共用同一套条件，
// 「预览说 300 人」与「实际发给 300 人」永远是同一批。
type BulkFilter struct {
	// Status 是账号状态：active / suspended / banned
	Status string
	// GroupID 是用户组
	GroupID *uuid.UUID
	// Query 是邮箱包含（不分大小写）
	Query string
	// HasActiveSub 是有无生效订阅（true 与 SubState=active 同口径）
	HasActiveSub *bool
	// PlanID 是当前订阅的套餐
	PlanID *uuid.UUID
	// ExpiresWithinDays 是当前订阅在 N 天内到期（1–365，0 不限）
	ExpiresWithinDays int
	// SubState 是订阅情况：active 有生效订阅；expired 有过订阅但没有生效的；none 从没有过订阅
	SubState string
}

// SubFilter 是交给 subscription 的订阅条件（BulkFilter 与用户列表 sub_state 里跟订阅有关的几项）。
type SubFilter struct {
	// State 是 ""、active、expired、none
	State string
	// HasLive 非 nil 时：true 要有生效订阅，false 要没有生效订阅
	HasLive *bool
	// PlanID 是当前订阅的套餐
	PlanID *uuid.UUID
	// ExpiresWithinDays 大于 0 时：当前订阅的到期时间在 [now, now+N 天) 内
	ExpiresWithinDays int
}

// Empty 报告是否没有任何订阅条件。
func (f SubFilter) Empty() bool {
	return f.State == "" && f.HasLive == nil && f.PlanID == nil && f.ExpiresWithinDays <= 0
}

// CurrentSub 是一个用户的「当前订阅」摘要，口径与 subscription 的 UserSubscriptions.Current 相同
// （生效的优先，再到期晚的优先，再创建晚的优先）。
type CurrentSub struct {
	PlanName  string
	PeriodEnd time.Time
	// Live 是当前订阅是否生效
	Live bool
	// LiveCount 是名下生效订阅的份数
	LiveCount int
}

// BulkPreview 是预览的结果：命中人数与前 10 个样本（注册时间倒序）。
type BulkPreview struct {
	Total   int64
	Samples []BulkSample
}

// BulkSample 是预览里的一个样本；没有订阅时 PlanName、PeriodEnd 为 nil。
type BulkSample struct {
	Email     string
	PlanName  *string
	PeriodEnd *time.Time
}

// ExportRow 是导出 CSV 的一行。
type ExportRow struct {
	Email       string
	Status      string
	GroupName   string
	ActiveSubs  int
	Orders      int64
	Paid        string
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

// BulkRecipient 是群发邮件的一个收件人；当前订阅的套餐与到期给正文里的 $plan、$expire 用，没有订阅时为空。
type BulkRecipient struct {
	UserID    uuid.UUID
	Email     string
	PlanName  string
	PeriodEnd *time.Time
}

// ---- 批量生成账号 ----

// 批量生成任务的状态（与迁移 00033 的 CHECK 一致）。
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
)

// GenerateInput 是提交批量生成任务的输入。
type GenerateInput struct {
	Count       int
	EmailPrefix string
	EmailDomain string
	GroupID     *uuid.UUID
	Reason      string
}

// GenerationJob 是批量生成任务（不含结果）。
type GenerationJob struct {
	ID              uuid.UUID
	ActorID         uuid.UUID
	Status          string
	Total           int
	Completed       int
	Failed          int
	EmailPrefix     string
	EmailDomain     string
	GroupID         *uuid.UUID
	Reason          string
	Error           *string
	ResultAvailable bool
	ResultExpiresAt *time.Time
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

// GeneratedUser 是生成出来的一个账号与初始口令；结果密文里是它的 JSON 数组。
type GeneratedUser struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// GenerationPayload 是 identity:generate_users 任务的载荷。
type GenerationPayload struct {
	JobID uuid.UUID `json:"job_id"`
}

// ---- 画像、访问明细、同源 IP ----

// UserProfile 是后台用户画像；IP 都是解封后的明文（解不开为空串），列表永不为 nil。
type UserProfile struct {
	Events         []ProfileEvent
	IPs            []ProfileIP
	Related        []UserRef
	Fetches        []FetchRecord
	FetchSources7d int
	RegisteredIP   string
	// Risk 各 IP 风险里最高的一档；没有共用 IP 为空串。users.risk_level 列没有任何代码写，后台不再用它判断风险
	Risk string
}

// ProfileEvent 是画像里的一条行为（本人作为操作者的审计）。
type ProfileEvent struct {
	Action    string
	Outcome   string
	IP        string
	UserAgent string
	// Domain 是 public / admin（审计的 actor_audience），没有为空串
	Domain string
	At     time.Time
}

// ProfileIP 是按来源 IP 归并的统计；Accounts 是近 90 天同一 IP 上的账号数（含本人，至少 1）。
type ProfileIP struct {
	IP       string
	Count    int
	First    time.Time
	Last     time.Time
	Accounts int
	// Geo 读时按 IP 库现算，同访问日志
	Geo string
	// NetworkKind 读时按 IP 库现算，同访问日志
	NetworkKind string
	// Risk 近 90 天和别的账号共用这个 IP 时按 clusterRisk 定的 high / mid / low，与风控聚类同一条规则；没共用为空串
	Risk string
}

// FetchQuery 是访问明细里订阅拉取的筛选。
type FetchQuery struct {
	// IP 非空时按来源 IP 精确匹配（subscription 自己算哈希）
	IP string
	// FilterUsers 为 true 时只看 UserIDs 这些用户的订阅（UserIDs 为空即一条都不要）
	FilterUsers bool
	UserIDs     []uuid.UUID
	// Outcome 是 ""（全部）、success（只要 ok）、error（只要非 ok）
	Outcome string
	// Limit 是最多取几条（按拉取时间倒序）
	Limit int
}

// FetchRecord 是一次订阅拉取；IP、UA 是解封后的明文（解不开为空串）。未认证失败的拉取没有 UserID。
type FetchRecord struct {
	UserID    *uuid.UUID
	IP        string
	UserAgent string
	Family    string
	Result    string
	Format    string
	At        time.Time
}

// AccessLogQuery 是访问明细的筛选（参数已由 handler 绑定，service 校验取值）。
type AccessLogQuery struct {
	// Category 是 ""、login、register、reset_password、order、payment、ticket、admin、other、subscribe
	Category string
	// Outcome 是 ""、success、failure、denied、partial、error（= 非 success）
	Outcome string
	// IP 是来源 IP 精确匹配
	IP string
	// User 是用户 id，或邮箱片段
	User   string
	Limit  int
	Offset int
}

// AccessItem 是访问明细的一行（审计与订阅拉取两路归并）。
type AccessItem struct {
	Category        string
	Action          string
	UserID          *uuid.UUID
	UserEmail       string
	TargetUserID    *uuid.UUID
	TargetUserEmail string
	IP              string
	Geo             string
	NetworkKind     string
	UserAgent       string
	Outcome         string
	At              time.Time
}

// IPCluster 是一个同源 IP 聚类（近 90 天同一来源 IP 关联到多个用户账号）。
type IPCluster struct {
	// Key 是来源 IP 哈希的 hex，处置接口用它定位
	Key         string
	IP          string
	Geo         string
	NetworkKind string
	// Risk 是 high / mid / low
	Risk     string
	Accounts int
	Events   int
	First    time.Time
	Last     time.Time
	// Emails、Users 按邮箱升序，永不为 nil
	Emails []string
	Users  []ClusterUser
	Review *ClusterReview
}

// ClusterUser 是聚类里的一个账号；ActivePlan 是当前生效订阅的套餐名，没有为 nil。
type ClusterUser struct {
	ID         uuid.UUID
	Email      string
	Status     string
	ActivePlan *string
}

// ClusterReview 是聚类当前的处置结论。
type ClusterReview struct {
	// Decision 是 normal / disabled
	Decision  string
	DecidedAt time.Time
	// ExpiresAt 只有 normal 有（30 天后重新提示）
	ExpiresAt *time.Time
}

// 批量停用时逐个账号的跳过原因（与前端 SKIP_REASONS 一致）。
const (
	SkipSelf            = "self"
	SkipAdministrator   = "administrator"
	SkipNotMember       = "not_member"
	SkipAlreadyDisabled = "already_disabled"
)

// ClusterSkip 是批量停用里没停的一个账号与原因。
type ClusterSkip struct {
	UserID uuid.UUID
	Reason string
}

// DisableClusterResult 是批量停用的结果；Skipped 永不为 nil。
type DisableClusterResult struct {
	Disabled int
	Skipped  []ClusterSkip
}

// ---- 端口 ----

// OpsSubscriptions 由 subscription 实现（subscription/service/ops_reads.go），启动装配时经 UseOpsSubscriptions 注入。
type OpsSubscriptions interface {
	// UserIDsBySubFilter 按订阅条件给出用户 id：exclude 为 false 时只保留 ids 里的用户，为 true 时排除 ids 里的用户。
	// 调用方只在 f 非空时调用。
	UserIDsBySubFilter(ctx context.Context, f SubFilter) (ids []uuid.UUID, exclude bool, err error)
	// CurrentSubs 批量取用户的当前订阅；没有订阅的用户不在结果里。
	CurrentSubs(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]CurrentSub, error)
	// Fetches 是访问明细里的订阅拉取，按拉取时间倒序。
	Fetches(ctx context.Context, q FetchQuery) ([]FetchRecord, error)
	// UserFetches 是画像里一个用户最近 50 次拉取，与近 7 天成功拉取的不同来源数。
	UserFetches(ctx context.Context, userID uuid.UUID) ([]FetchRecord, int, error)
	// AdminSubs 批量取用户的全部订阅（后台用户列表、详情）；每个入参用户都在结果里（没有订阅的是空列表）。
	// 查询条数与用户数无关。
	AdminSubs(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]AdminUserSubs, error)
}

// OrderStat 是一个用户的订单总数与实付金额汇总。
type OrderStat struct {
	Orders int64
	Paid   decimal.Decimal
}

// OpsOrders 由 order 模块经适配器实现（app/adapters.go），启动装配时经 UseOpsOrders 注入。
type OpsOrders interface {
	UserOrderStats(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]OrderStat, error)
	// RecentOrders 是一个用户最近 limit 单（下单时间倒序），行与后台订单列表同形；ctx 须是后台登录主体。
	RecentOrders(ctx context.Context, userID uuid.UUID, limit int) ([]AdminOrderRow, error)
}

// JobQueue 入队一次性任务；*asynq.Client 实现。
type JobQueue interface {
	EnqueueContext(ctx context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// auditOps 是 identity 用到的 audit 读与复核写；*auditsvc.Service 实现（audit/service/ops.go）。
type auditOps interface {
	UserActivity(ctx context.Context, userID uuid.UUID) (auditsvc.Activity, error)
	AccessEvents(ctx context.Context, q auditsvc.AccessQuery) ([]auditsvc.AccessEvent, error)
	IPClusters(ctx context.Context, includeReviewed bool) ([]auditsvc.Cluster, error)
	ClusterMembers(ctx context.Context, key string) ([]uuid.UUID, error)
	MarkClusterNormal(ctx context.Context, key, note string) (time.Time, error)
	MarkClusterDisabled(ctx context.Context, key, note string) (uuid.UUID, error)
	RecordSourceIPView(ctx context.Context, view string, userID *uuid.UUID, filter map[string]any) error
	// 溯源总览的两个聚合读（audit/service/trace_reads.go）
	TraceIPs(ctx context.Context, q auditsvc.TraceIPQuery) (auditsvc.TraceIPSet, error)
	TraceUsers(ctx context.Context, q auditsvc.TraceUserQuery) ([]auditsvc.TraceUser, error)
}

var _ auditOps = (*auditsvc.Service)(nil)

// UseOpsSubscriptions 只在启动装配时调用（不并发安全）。
func (s *Service) UseOpsSubscriptions(o OpsSubscriptions) { s.subs = o }

// UseOpsOrders 只在启动装配时调用（不并发安全）。
func (s *Service) UseOpsOrders(o OpsOrders) { s.orders = o }

// UseJobQueue 只在启动装配时调用（不并发安全）。没注入时批量生成任务只靠 reconcile:user_generation 每分钟补排。
func (s *Service) UseJobQueue(q JobQueue) { s.queue = q }

func (s *Service) needSubs() (OpsSubscriptions, error) {
	if s.subs == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.subs, nil
}

func (s *Service) needOrders() (OpsOrders, error) {
	if s.orders == nil {
		return nil, errs.Wrap(errs.Unavailable, errNotWired)
	}
	return s.orders, nil
}
