// Package service 是 certs 模块的业务层：DNS 凭据、ACME 设置与账号、证书资源，以及签发（lego DNS-01）。
//
// 范围（用户 10-07 / 10-08 定）：
//   - DNS-01 只做 Cloudflare、阿里云 DNS、腾讯云 DNSPod 三家；不做 IP 证书、HTTP-01、TLS-ALPN-01；
//   - 备用 CA（ZeroSSL，要 EAB）可选、默认关；
//   - 到期与失败只在后台显示（列表的 summary 给横幅用），不发 Telegram、不发邮件。
//
// 签发流程：certs:patrol 每分钟给到期的证书排订单，把待处理的订单以 TaskID cert:<订单> 入队 certs:issue；
// certs:issue 先在库里认领订单（attempt 是栅栏令牌）→ 凭据预检 → 本地限额对账（必要时切备用 CA）→ ACME 账号 →
// lego 签发 → 落库前核对 attempt 没变。asynq 的 TaskID 只防同一张订单重复入队；lego 不认 context，任务超时或
// asynq 租约过期后签发协程可能还在跑，所以「同一张订单不会写出两个版本」仍由库里的认领与栅栏保证。
//
// 秘密（DNS 凭据、ACME 账号私钥、证书私钥、ZeroSSL EAB HMAC）只以封存密文落库，AAD 绑定行；
// 读形状里没有任何密文字段。证书私钥是 PKCS#8 DER。
package service

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/certs/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// TaskIssue 是签发一张订单的任务类型；TaskID 为 cert:<订单 id>，同一张订单同时只有一个任务。
const TaskIssue = "certs:issue"

// IssueTimeout 是 certs:issue 的 asynq 超时（第 12 节）。
const IssueTimeout = 15 * time.Minute

// IssuePayload 是 certs:issue 的载荷。
type IssuePayload struct {
	OrderID uuid.UUID `json:"order_id"`
}

// IssueTaskID 是订单对应的 asynq TaskID。
func IssueTaskID(orderID uuid.UUID) string { return "cert:" + orderID.String() }

// ACMEStore 是 ACME 设置的读写；*settingssvc.Service 实现它。
type ACMEStore interface {
	ACME(ctx context.Context) (settingssvc.ACME, error)
	ACMEEABHMAC(ctx context.Context) (string, error)
	SetACME(ctx context.Context, u settingssvc.ACMEUpdate) (settingssvc.ACME, error)
}

var _ ACMEStore = (*settingssvc.Service)(nil)

// Queue 是入队任务的接口；*asynq.Client 实现它。
type Queue interface {
	EnqueueContext(ctx context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// sealer 加解密落库的秘密；*crypto.Sealer 实现它。
type sealer interface {
	Seal(plain, aad []byte) ([]byte, error)
	Open(sealed, aad []byte) ([]byte, error)
}

var _ sealer = (*crypto.Sealer)(nil)

// auditWriter 是 audit service 的写入面；*auditsvc.Service 实现它。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// Options 是装配项。生产只用前两项（非生产环境的目录覆盖，由配置给）；其余只给测试注入
// （模拟的 DNS 提供方 API、进程内 DNS 服务器、更短的超时）。
type Options struct {
	// DirectoryOverride 非空时所有签发都打到这个 ACME 目录（ca=custom）
	DirectoryOverride string
	// TrustedRoots 是 ACME 目录 HTTPS 证书的信任根；nil 用系统根
	TrustedRoots *x509.CertPool

	// ZeroSSLDirectory 覆盖备用 CA 的目录地址（测试指向第二个 pebble）
	ZeroSSLDirectory string
	// CloudflareBaseURL 覆盖 Cloudflare API 地址（测试指向模拟）
	CloudflareBaseURL string
	// ProviderHTTPClient 是调 Cloudflare API 的 HTTP 客户端；nil 用缺省
	ProviderHTTPClient *http.Client
	// RecursiveNameservers 非空时 lego 查 SOA 与传播检查都问这几台（测试的进程内 DNS），不再问权威服务器
	RecursiveNameservers []string
	// PropagationTimeout / PollingInterval 覆盖 DNS 传播检查的时限与间隔
	PropagationTimeout time.Duration
	PollingInterval    time.Duration
	// Now 覆盖时钟（只影响到期等级等纯计算；排程一律用数据库的 now()）
	Now func() time.Time
	// Log 是 lego 与签发过程的日志去处；nil 用 zap.NewNop
	Log *zap.Logger
}

// Service 是 certs 模块对外的唯一入口：后台读写与签发任务共用。
type Service struct {
	store  store
	tx     txRunner
	audit  auditWriter
	acme   ACMEStore
	sealer sealer
	queue  Queue
	opts   Options
	// lastSweep 是上次刷新 ARI 窗口与到期等级的时刻（UnixNano）；这两件事每 5 分钟做一次，不用每轮巡检都做
	lastSweep atomic.Int64
}

// New 构造 certs service。queue 用于建单后立刻入队签发任务（nil 时只靠每分钟的巡检）。
func New(r *repo.Repo, txm *tx.Manager, audit *auditsvc.Service, acme ACMEStore, sealer *crypto.Sealer,
	queue Queue, opts Options) *Service {
	return newService(r, txm, audit, acme, sealer, queue, opts)
}

func newService(st store, txr txRunner, audit auditWriter, acme ACMEStore, sl sealer, queue Queue, opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = zap.NewNop()
	}
	if opts.ZeroSSLDirectory == "" {
		opts.ZeroSSLDirectory = zeroSSLDirectory
	}
	if opts.PropagationTimeout <= 0 {
		opts.PropagationTimeout = 4 * time.Minute
	}
	if opts.PollingInterval <= 0 {
		opts.PollingInterval = 10 * time.Second
	}
	configureLegoLog(opts.Log)
	return &Service{store: st, tx: txr, audit: audit, acme: acme, sealer: sl, queue: queue, opts: opts}
}

// legoEnvExact 是 lego 在面板用到的包里自己读的、不带 LEGO_DEBUG_ 前缀的开关。
var legoEnvExact = []string{"LEGO_DISABLE_CNAME_SUPPORT", "LEGO_EXPERIMENTAL_DNS_TCP_ONLY"}

// LibraryEnvWarnings 列出进程环境里设了的 lego 自己会读的变量名（只回名字）：调试开关可能把 DNS 服务商 API 的
// 请求（含令牌）打进日志，CNAME / TCP 开关会改变签发行为。worker 启动签发任务时告警一次。
func LibraryEnvWarnings() []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "LEGO_DEBUG_") || slices.Contains(legoEnvExact, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// acmeConfig 是签发用的完整 ACME 设置（含解封的 EAB HMAC）。
type acmeConfig struct {
	ContactEmail   string
	UseStaging     bool
	ZeroSSLEnabled bool
	ZeroSSLEABKID  string
	eabHMAC        string
}

// loadACMEConfig 读签发用的 ACME 设置；withSecret 为真时解封 EAB HMAC。
func (s *Service) loadACMEConfig(ctx context.Context, withSecret bool) (acmeConfig, error) {
	a, err := s.acme.ACME(ctx)
	if err != nil {
		return acmeConfig{}, err
	}
	cfg := acmeConfig{ContactEmail: a.ContactEmail, UseStaging: a.UseStaging, ZeroSSLEnabled: a.ZeroSSLEnabled,
		ZeroSSLEABKID: a.ZeroSSLEABKID}
	if withSecret && a.ZeroSSLEABHMACSet {
		if cfg.eabHMAC, err = s.acme.ACMEEABHMAC(ctx); err != nil {
			return acmeConfig{}, err
		}
	}
	return cfg, nil
}

// ACMESettings 读后台的 ACME 设置。
func (s *Service) ACMESettings(ctx context.Context) (ACMESettings, error) {
	a, err := s.acme.ACME(ctx)
	if err != nil {
		return ACMESettings{}, err
	}
	return s.acmeView(a), nil
}

// SaveACMESettings 整体保存 ACME 设置（校验与审计在 settings 模块）。
func (s *Service) SaveACMESettings(ctx context.Context, in ACMESettingsInput) (ACMESettings, error) {
	a, err := s.acme.SetACME(ctx, settingssvc.ACMEUpdate{
		ContactEmail: in.ContactEmail, UseStaging: in.UseStaging, ZeroSSLEnabled: in.ZeroSSLEnabled,
		ZeroSSLEABKID: in.ZeroSSLEABKID, ZeroSSLEABHMAC: in.ZeroSSLEABHMAC,
	})
	if err != nil {
		return ACMESettings{}, err
	}
	return s.acmeView(a), nil
}

func (s *Service) acmeView(a settingssvc.ACME) ACMESettings {
	return ACMESettings{ContactEmail: a.ContactEmail, UseStaging: a.UseStaging, ZeroSSLEnabled: a.ZeroSSLEnabled,
		ZeroSSLEABKID: a.ZeroSSLEABKID, ZeroSSLEABHMACSet: a.ZeroSSLEABHMACSet,
		DirectoryOverride: s.opts.DirectoryOverride != ""}
}

// --- 共用的小工具 ---

// actorID 取 ctx 里登录主体的用户 ID；没有登录主体（签发任务）时为 nil。
func actorID(ctx context.Context) *uuid.UUID {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return nil
	}
	id := p.UserID
	return &id
}

// writeAudit 在 ctx 的事务里写一条审计；没有登录主体时 audit 记为 system。
func (s *Service) writeAudit(ctx context.Context, action, resourceType string, id *uuid.UUID, outcome string, data map[string]any) error {
	return s.audit.Write(ctx, auditsvc.Event{Action: action, ResourceType: resourceType, ResourceID: id,
		Outcome: outcome, Data: data})
}

func notFound() error { return errs.New(errs.NotFound) }

func conflict(msg string) error { return errs.Newf(errs.Conflict, msg) }

// encodeIDs 把标识编码成 jsonb 数组文本。
func encodeIDs(ids []string) string {
	if ids == nil {
		ids = []string{}
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

// decodeIDs 解 jsonb 数组文本；库里由 CHECK 保证是数组，解不开说明被手工改坏，当内部错误。
func decodeIDs(raw string) ([]string, error) {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, errs.Wrap(errs.Internal, err)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// credentialAAD、accountAAD、versionKeyAAD 是各类密文的 AAD：密文挪到别的行上解不开。
func credentialAAD(id uuid.UUID) []byte { return []byte("dns_credential:" + id.String()) }

func accountAAD(id uuid.UUID) []byte { return []byte("acme_account:" + id.String()) }

func versionKeyAAD(certID uuid.UUID, version int) []byte {
	return []byte("certificate:" + certID.String() + ":" + strconv.Itoa(version) + ":private_key")
}

// validName 校验名称（凭据与证书共用）：去空白后 1–64 字、不含 NUL。
func validName(raw string) (string, string) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", "填写名称"
	}
	if strings.ContainsRune(name, 0) {
		return "", "名称不能包含空字符"
	}
	if len([]rune(name)) > 64 {
		return "", "名称最多 64 个字"
	}
	return name, ""
}

func validKeyType(k string) bool { return k == KeyECDSAP256 || k == KeyRSA2048 }

// --- 签发任务入队 ---

// queueOrder 给证书排一张订单（已有进行中的订单时什么也不做），新建时登记提交后入队签发任务。
// 返回订单 id 与是否新建。
func (s *Service) queueOrder(ctx context.Context, certID uuid.UUID, reason string) (uuid.UUID, bool, error) {
	id, created, err := s.store.QueueOrder(ctx, certID, reason, actorID(ctx))
	if err != nil || !created {
		return id, created, err
	}
	s.kickIssue(ctx, id)
	return id, true, nil
}

// kickIssue 在事务提交后把订单入队 certs:issue；失败只记日志，每分钟的巡检会补上。
func (s *Service) kickIssue(ctx context.Context, orderID uuid.UUID) {
	if s.queue == nil {
		return
	}
	tx.AfterCommit(ctx, "certs:issue:"+orderID.String(), func(ctx context.Context) {
		if _, err := s.enqueueIssue(ctx, orderID); err != nil {
			plog.From(ctx).Warn("证书签发任务入队失败，等巡检补上", zap.String("order", orderID.String()), zap.Error(err))
		}
	})
}

// enqueueIssue 以 TaskID cert:<订单> 入队 certs:issue；同一张订单已有任务（排队、执行中）时不重复入队，
// 回 false。
func (s *Service) enqueueIssue(ctx context.Context, orderID uuid.UUID) (bool, error) {
	if s.queue == nil {
		return false, nil
	}
	task, err := jobs.NewTask(ctx, TaskIssue, IssuePayload{OrderID: orderID})
	if err != nil {
		return false, err
	}
	_, err = s.queue.EnqueueContext(ctx, task,
		asynq.TaskID(IssueTaskID(orderID)),
		asynq.Queue("default"),
		asynq.MaxRetry(0),
		asynq.Timeout(IssueTimeout),
	)
	if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	return err == nil, err
}
