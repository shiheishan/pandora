// Package service 是 plugin 模块的业务层：插件钩子的后台管理、事件出站（Emit 在调用方事务里写 queued 投递，
// 提交后入队 plugin:dispatch）、派发（认领、签名 POST、退避重试）与保留期清理。
//
// 出站 webhook 而不是在面板进程内加载插件代码：插件崩了面板不崩，插件被攻破了也拿不到数据库。
package service

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/plugin/model"
	"github.com/aegispanel/aegis/internal/module/plugin/repo"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// TaskDispatch 是投递任务类型：Emit 提交后入队（TaskID 按秒分桶去重），另有每分钟的周期兜底。
const TaskDispatch = "plugin:dispatch"

const (
	DispatchTimeout    = 4 * time.Minute    // plugin:dispatch 的任务超时
	dispatchLease      = 10 * time.Minute   // 认领租约，必须长于任务超时
	dispatchBatch      = 10                 // 每批认领条数
	dispatchMaxBatches = 50                 // 一次 Dispatch 最多批数
	RetentionDays      = 90                 // 投递记录保留期（产品定）
	statsWindow        = 7 * 24 * time.Hour // 列表里失败数、送达数的统计窗口
	defaultTimeoutMS   = 5000
	defaultMaxAttempts = 5
	defaultDeliveries  = 50  // 投递记录缺省条数
	maxDeliveries      = 200 // 投递记录条数上限
)

// 审计（动作名沿用旧代码）。
const (
	ActionHookSave   = "plugin.hook.save"
	ActionHookDelete = "plugin.hook.delete"
	ActionTestSent   = "notify.test_sent"
	ResourceHook     = "plugin_hook"
	ResourceTest     = "notification_test"
	testKind         = "plugin_hook"
)

// EventInfo 是一个可订阅的事件。
type EventInfo struct {
	Name string
	Desc string
}

// Events 是可订阅事件的白名单（放在代码里：打错一个字母的钩子永远不会触发，而且没有地方会提示）。
var Events = []EventInfo{
	{"user.registered", "用户完成注册"},
	{"order.created", "订单创建"},
	{"order.paid", "订单支付成功"},
	{"order.cancelled", "订单取消"},
	{"subscription.provisioned", "订阅开通或续期"},
	{"subscription.expiring", "订阅即将到期"},
	{"subscription.expired", "订阅已过期"},
	{"traffic.exhausted", "流量用尽"},
	{"ticket.created", "用户提交工单"},
	{"giftcard.redeemed", "礼品卡兑换"},
}

// KnownEvent 报告事件名是否在白名单里。
func KnownEvent(name string) bool {
	for _, e := range Events {
		if e.Name == name {
			return true
		}
	}
	return false
}

// store 是 service 对 repo 的全部要求；*repo.Repo 实现它。
type store interface {
	ListHooks(ctx context.Context) ([]model.Hook, error)
	HookByCode(ctx context.Context, code string, lock bool) (model.Hook, bool, error)
	HooksByIDs(ctx context.Context, ids []uuid.UUID) ([]model.Hook, error)
	InsertHook(ctx context.Context, h *model.Hook) error
	UpdateHook(ctx context.Context, h *model.Hook, withSecret bool) error
	DeleteHook(ctx context.Context, id uuid.UUID) error
	HookStats(ctx context.Context, since time.Time) ([]repo.HookStat, error)

	EnqueueEvent(ctx context.Context, event, dedupeKey, payload string) (int64, error)
	ClaimDue(ctx context.Context, limit int, lease time.Duration) ([]model.Delivery, error)
	FailExhausted(ctx context.Context) (int, error)
	MarkSent(ctx context.Context, id uuid.UUID, code int, durationMS *int) (bool, error)
	MarkRetry(ctx context.Context, id uuid.UUID, delay time.Duration, code *int, msg string, durationMS *int) (bool, error)
	MarkFailed(ctx context.Context, id uuid.UUID, code *int, msg string, durationMS *int) (bool, error)
	DeliveriesByHook(ctx context.Context, hookID uuid.UUID, limit int) ([]model.Delivery, error)
	PurgeDeliveries(ctx context.Context, before time.Time, batch int) (int, error)
	WithDispatchLock(ctx context.Context, fn func(ctx context.Context) error) (bool, error)
}

var _ store = (*repo.Repo)(nil)

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// sealer 加解密落库的秘密；*crypto.Sealer 实现它。
type sealer interface {
	Seal(plain, aad []byte) ([]byte, error)
	Open(sealed, aad []byte) ([]byte, error)
}

var _ sealer = (*crypto.Sealer)(nil)

// idempotency 是幂等 service 在业务事务内完成认领的方法；*idemsvc.Service 直接满足。
type idempotency interface {
	CompleteUnreplayable(ctx context.Context, c *idemsvc.Claim, status int) error
}

var _ idempotency = (*idemsvc.Service)(nil)

// Queue 是入队任务的接口；*asynq.Client 实现它。
type Queue interface {
	EnqueueContext(ctx context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// Options 是装配项。
type Options struct {
	// AllowPrivate 允许 http 与内网地址：只在开发环境为 true。生产必须为 false，
	// 否则能改钩子的账号就能把面板当成内网扫描器（SSRF）。
	AllowPrivate bool
}

// Service 是 plugin 模块对外的唯一入口。
type Service struct {
	store  store
	tx     txRunner
	audit  auditWriter
	idem   idempotency
	sealer sealer
	queue  Queue
	opts   Options
	client *http.Client
	now    func() time.Time
}

// New 构造 plugin service。queue 可以为 nil（不入队，只靠周期任务）。
func New(r *repo.Repo, txm *tx.Manager, audit *auditsvc.Service, idem *idemsvc.Service, sl *crypto.Sealer, queue Queue, opts Options) *Service {
	return newService(r, txm, audit, idem, sl, queue, opts)
}

func newService(st store, txr txRunner, audit auditWriter, idem idempotency, sl sealer, queue Queue, opts Options) *Service {
	return &Service{
		store:  st,
		tx:     txr,
		audit:  audit,
		idem:   idem,
		sealer: sl,
		queue:  queue,
		opts:   opts,
		client: newHTTPClient(opts.AllowPrivate),
		now:    time.Now,
	}
}

// Hook 是后台列表里的一个钩子。LastSentAt 为空串表示从没送达过。
type Hook struct {
	ID          uuid.UUID
	Code        string
	Name        string
	Description string
	Enabled     bool
	Events      []string // 永不为 nil
	EndpointURL string
	HasSecret   bool
	TimeoutMS   int
	MaxAttempts int
	QueuedCount int
	FailedCount int    // 近 7 天
	SentCount7d int    // 近 7 天
	LastSentAt  string // 北京时间 2006-01-02 15:04
}

// SaveHookInput 是新建或修改钩子的输入（按 code upsert）。Secret 为空表示不修改（新建时由面板生成）。
type SaveHookInput struct {
	Code        string
	Name        string
	Description string
	Enabled     bool
	Events      []string
	EndpointURL string
	Secret      string
	TimeoutMS   int // 0 取缺省 5000
	MaxAttempts int // 0 取缺省 5
}

// Delivery 是后台投递记录的一行；时刻是北京时间的 2006-01-02 15:04:05，SentAt 为空串表示未送达；
// ResponseCode 0 表示没有响应；DurationMS 为 nil 表示请求没发出去。
type Delivery struct {
	Event        string
	Status       string
	Attempts     int
	ResponseCode int
	ErrorMessage string
	CreatedAt    string
	SentAt       string
	DurationMS   *int
}

// TestResult 是测试投递的结果。
type TestResult struct {
	ResponseCode int
	DurationMS   int
}

// secretAAD 绑定钩子 code，封存密钥的 AAD。
func secretAAD(code string) []byte { return []byte("plugin_hook:" + code) }

// displayLoc 是后台显示投递时刻用的时区：固定北京时间（产品定，不随站点时区）。
// 主二进制内嵌了 time/tzdata；万一仍取不到，退回固定的 UTC+8。
var displayLoc = beijing()

func beijing() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*60*60)
}
