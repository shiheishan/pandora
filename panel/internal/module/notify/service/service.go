// Package service 是 notify 模块的业务层：投递记录、SMTP 与 Telegram 发送、通知模板与后台邮件设置（P2），
// 站内信、通知去重、通知偏好、公告、Telegram 绑定与设置、到期与流量扫描（P6）。
//
// 「决定要发」和「实际送出」分两步，中间隔着投递表：SendToAddress 在调用方事务里写一行 queued，
// 提交后入队 notify:dispatch；Dispatch 认领到期的行逐封发送，失败按退避重试。业务操作不因 SMTP
// 连不上而回滚，投递的重试与终态是独立的生命周期。
//
// 文件划分：registry.go 内置模板；render.go 模板校验与渲染；smtp.go 发信；mailer.go 写投递与入队；
// dispatch.go 认领与发送；templates.go 后台模板管理；mailsettings.go 后台邮件设置与测试发信。
// P6：p6.go 对外类型与跨模块端口；notice.go 给用户写一条通知（去重、站内信、邮件、Telegram）；
// inbox.go 收件箱与偏好；scan.go 到期与流量扫描；announce*.go 公告；telegram*.go Telegram；
// dispatch_telegram.go Telegram 派发。
package service

import (
	"context"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/module/notify/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// TaskDispatch 是投递任务类型：业务提交后入队（TaskID 按秒分桶去重），另有每 30 秒的周期兜底。
const TaskDispatch = "notify:dispatch"

// 派发与保留期参数。
const (
	dispatchBatch      = 20               // 每批认领条数
	dispatchMaxBatches = 50               // 一次 Dispatch 最多批数
	dispatchLease      = 10 * time.Minute // 认领租约，必须长于任务超时（4 分钟）
	DispatchTimeout    = 4 * time.Minute  // notify:dispatch 的任务超时
	maxAttempts        = 5                // 每条投递最多尝试次数
	RetentionDays      = 90               // 投递记录保留期（产品定）
)

// Settings 是 notify 用到的 settings service 方法；*settingssvc.Service 实现它。
type Settings interface {
	Mail(ctx context.Context) (settingssvc.Mail, error)
	SetMail(ctx context.Context, v settingssvc.MailUpdate) error
	Registration(ctx context.Context) (settingssvc.Registration, error)
	SetRegistration(ctx context.Context, v settingssvc.Registration) error
	Site(ctx context.Context) (settingssvc.Site, error)
	SwitchEnabled(ctx context.Context, code string) (bool, error)
	Telegram(ctx context.Context) (settingssvc.Telegram, error)
	SetTelegram(ctx context.Context, v settingssvc.TelegramUpdate) error
}

var _ Settings = (*settingssvc.Service)(nil)

// Queue 是入队任务的接口；*asynq.Client 实现它。
type Queue interface {
	EnqueueContext(ctx context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// auditWriter 是 audit service 的写入面；*auditsvc.Service 实现它，测试用假实现。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// mailSender 发一封邮件；生产用 smtpSender，测试用假实现。
type mailSender interface {
	Send(ctx context.Context, cfg SMTPConfig, m Message) error
}

// store 是 service 对 repo 的全部要求；*repo.Repo 实现它。
type store interface {
	// ListTemplates 按 code、channel 升序返回全部模板。
	ListTemplates(ctx context.Context) ([]model.Template, error)
	// GetTemplate 读一个模板；不存在 ok 为 false。
	GetTemplate(ctx context.Context, code, channel string) (t model.Template, ok bool, err error)
	// LockTemplate 同 GetTemplate，并加 FOR UPDATE。
	LockTemplate(ctx context.Context, code, channel string) (t model.Template, ok bool, err error)
	// UpdateTemplateContent 改主题与正文、version 加 1，返回改后的行。
	UpdateTemplateContent(ctx context.Context, id uuid.UUID, subject, body string) (model.Template, error)

	// InsertDelivery 写一条投递（ID 由调用方生成）。
	InsertDelivery(ctx context.Context, d *model.Delivery) error
	// ClaimDeliveries 认领到期的 queued 行（指定渠道），把 next_retry_at 推到 now()+lease、attempts 加 1，按 created_at 升序返回。
	ClaimDeliveries(ctx context.Context, channel string, limit int, lease time.Duration) ([]model.Delivery, error)
	// FailExhausted 把尝试用尽、租约已过仍为 queued 的行置为 failed。
	FailExhausted(ctx context.Context, channel string) (int, error)
	// MarkSent 置为 sent：sent_at=now()、清空 payload_enc 与 error_message。行已不是 queued 时返回 false。
	MarkSent(ctx context.Context, id uuid.UUID) (bool, error)
	// MarkRetry 记一次失败：error_message、next_retry_at=now()+delay，仍为 queued。
	MarkRetry(ctx context.Context, id uuid.UUID, delay time.Duration, msg string) (bool, error)
	// MarkFinal 置为终态 failed / suppressed：清空 payload_enc、写 error_message。
	MarkFinal(ctx context.Context, id uuid.UUID, status, msg string) (bool, error)
	// SuppressQueuedTelegram 把某用户还在排队的 Telegram 投递置为 suppressed 并清空封存内容，返回行数。
	SuppressQueuedTelegram(ctx context.Context, userID uuid.UUID) (int, error)
	// PurgeDeliveries 删 created_at < before 的投递，每次至多 batch 行，返回删除行数。
	PurgeDeliveries(ctx context.Context, before time.Time, batch int) (int, error)

	p6Store
}

var _ store = (*repo.Repo)(nil)

// Service 是 notify 模块对外的唯一入口。
type Service struct {
	store    store
	tx       txRunner
	events   PluginEvents // 插件钩子出站事件，装配时注入（plugin_events.go）
	settings Settings
	audit    auditWriter
	queue    Queue
	sender   mailSender
	keys     *crypto.Keys
	sealer   *crypto.Sealer
	now      func() time.Time

	// P6：跨模块端口由装配注入（p6.go 的 Use* 方法）；tg 是 Telegram Bot API 客户端
	dir           Directory
	plans         Plans
	subs          Subscriptions
	orders        Orders
	tg            telegramAPI
	publicBaseURL string

	// rt 实时推送（realtime.go 的 UseRealtime）；nil 时不推
	rt *realtime.Publisher
}

// New 构造 notify service。queue 可以为 nil（不入队，只靠 notify:dispatch 的周期兜底）。
func New(r *repo.Repo, txm *tx.Manager, keys *crypto.Keys, settings Settings, audit *auditsvc.Service, queue Queue) (*Service, error) {
	return newService(r, txm, keys, settings, audit, queue, newSMTPSender())
}

func newService(st store, txr txRunner, keys *crypto.Keys, settings Settings, audit auditWriter, queue Queue, sender mailSender) (*Service, error) {
	sealer, err := keys.Sealer()
	if err != nil {
		return nil, err
	}
	return &Service{
		store:    st,
		tx:       txr,
		settings: settings,
		audit:    audit,
		queue:    queue,
		sender:   sender,
		keys:     keys,
		sealer:   sealer,
		now:      time.Now,
		tg:       newTelegramClient(telegramAPIBase),
	}, nil
}

// ---- 对外类型（handler 与其他模块使用） ----

// TemplateView 是后台看到的一个模板：表里的内容加上登记表里的说明与示例渲染。
type TemplateView struct {
	Code             string
	Channel          string
	Locale           string
	Category         string
	Status           string
	Version          int
	Subject          string
	Body             string
	AllowedVariables []string
	IsDefault        bool // 内容与默认内容相同
	HasDefault       bool // 有内置默认内容（「恢复默认」可用）
	Description      string
	UpdatedAt        time.Time
	PreviewSubject   string // 用示例值渲染的主题
	PreviewBody      string // 用示例值渲染的正文
}

// TemplateEdit 是保存或预览模板草稿的输入。
type TemplateEdit struct {
	Code    string
	Channel string
	Subject string
	Body    string
}

// DraftPreview 是草稿按示例值渲染的结果，以及白名单外的变量（升序、去重，没有时为空切片）。
type DraftPreview struct {
	Subject          string
	Body             string
	UnknownVariables []string
}

// TemplateTest 是「模板实发测试」的输入；Subject 或 Body 非 nil 时按草稿发送（缺的一项按空串校验）。
type TemplateTest struct {
	Code    string
	Channel string
	To      string
	Subject *string
	Body    *string
}

// MailSettings 是后台邮件页看到的设置；口令只给「是否已设置」。
type MailSettings struct {
	Host              string
	Port              int
	Encryption        string
	Username          string
	HasPassword       bool
	FromAddress       string
	FromName          string // 没单独设时为站点名
	EmailVerification bool
	RegistrationMode  string
}

// MailSettingsUpdate 是邮件页的保存输入。Password：nil 不改、指向空串清除；
// EmailVerification 与 RegistrationMode 都为 nil 时不动注册设置，只给一个时另一个取当前值。
type MailSettingsUpdate struct {
	Host              string
	Port              int
	Encryption        string
	Username          string
	Password          *string
	FromAddress       string
	FromName          string
	EmailVerification *bool
	RegistrationMode  *string
}
