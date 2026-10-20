// Package service 是 settings 模块的业务层：站点设置的类型化读写与降级开关。
//
// 设置按组读写（Site、Registration、Mail……），每组一对 X(ctx) / SetX(ctx, v)，Set 在事务里写行并写审计。
// 缺行一律取 Go 里定义的缺省值（keys.go），迁移不种设置行。以后各模块迁移时在本包加自己那一组。
package service

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/settings/model"
	"github.com/aegispanel/aegis/internal/module/settings/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// auditWriter 是 audit service 的写入面；*auditsvc.Service 实现它，单测用假实现。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// store 是 service 对 repo 的全部要求；*repo.Repo 实现它，单测用假实现。
type store interface {
	// Values 读给定键；缺行的键不在结果里。
	Values(ctx context.Context, keys []string) (map[string]json.RawMessage, error)
	// LockValues 同 Values，并对已存在的行按 key 顺序加 FOR UPDATE。
	LockValues(ctx context.Context, keys []string) (map[string]json.RawMessage, error)
	// Upsert 写一个键（插入或覆盖值）。
	Upsert(ctx context.Context, key string, value json.RawMessage, by *uuid.UUID) error
	// InsertIfAbsent 只在键不存在时写入，返回是否写入。
	InsertIfAbsent(ctx context.Context, key string, value json.RawMessage) (bool, error)
	// Delete 删除给定键；不存在不算错。
	Delete(ctx context.Context, keys ...string) error

	// ListSwitches 按 essential 降序、code 升序返回全部开关。
	ListSwitches(ctx context.Context) ([]model.FeatureSwitch, error)
	// GetSwitch 读一个开关；缺行 ok 为 false。
	GetSwitch(ctx context.Context, code string) (sw model.FeatureSwitch, ok bool, err error)
	// LockSwitch 同 GetSwitch，并加 FOR UPDATE。
	LockSwitch(ctx context.Context, code string) (sw model.FeatureSwitch, ok bool, err error)
	// UpdateSwitch 改 enabled、reason、changed_by。
	UpdateSwitch(ctx context.Context, code string, enabled bool, reason *string, by *uuid.UUID) error
}

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// Service 是 settings 模块对外的唯一入口。
type Service struct {
	store  store
	tx     txRunner
	sealer *crypto.Sealer
	rdb    *redis.Client
	audit  auditWriter
	now    func() time.Time

	// 降级开关的进程内缓存，见 switches.go。
	swMu    sync.Mutex
	swCache map[string]switchEntry
	swGen   uint64
	swGroup singleflight.Group

	// rt 实时推送（realtime.go 的 UseRealtime）；nil 时不推
	rt *realtime.Publisher

	// siteListener 站点设置提交后清 appearance 的 PageMeta 快照；extrasListener 订阅附加设置提交后清 subscription 拉取快照；
	// homeListener 首页设置提交后清 app 层门户首页快照。
	siteListener   SiteChangeListener
	extrasListener SubscriptionExtrasListener
	homeListener   HomeChangeListener

	// signupGen 是本进程保存注册设置的次数（提交后加一），门户入口页 CSP 的缓存按它失效，见 signup_provider.go。
	signupGen atomic.Uint64

	// publicBaseURL 由 UsePublicBaseURL 注入的配置值，只经 SiteURL 读。
	publicBaseURL string
}

// New 构造 settings service。sealer 用于封存 SMTP 口令；rdb 用于开关缓存失效的 pub/sub；
// audit 写审计（在调用方事务里）。
func New(r *repo.Repo, txm *tx.Manager, sealer *crypto.Sealer, rdb *redis.Client, audit *auditsvc.Service) *Service {
	return newService(r, txm, sealer, rdb, audit)
}

func newService(st store, txr txRunner, sealer *crypto.Sealer, rdb *redis.Client, audit auditWriter) *Service {
	return &Service{
		store:   st,
		tx:      txr,
		sealer:  sealer,
		rdb:     rdb,
		audit:   audit,
		now:     time.Now,
		swCache: map[string]switchEntry{},
	}
}

// actorID 取 ctx 里登录主体的用户 ID；没有登录主体（命令行、任务）时为 nil。
func actorID(ctx context.Context) *uuid.UUID {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return nil
	}
	id := p.UserID
	return &id
}

// decodeOr 把 raw 解码成 T；缺行（raw 为空）返回 def。解码失败说明库里的值被手工改坏，按内部错误处理。
func decodeOr[T any](vals map[string]json.RawMessage, key string, def T) (T, error) {
	raw, ok := vals[key]
	if !ok || len(raw) == 0 {
		return def, nil
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return def, errs.Wrap(errs.Internal, err)
	}
	return v, nil
}

// change 是一组设置里要写的一个键。
type change struct {
	key   string
	value any
}

// writeChanges 在 ctx 的事务里按 key 顺序 upsert 各键（与读侧加锁顺序一致，避免死锁）。
func (s *Service) writeChanges(ctx context.Context, changes []change) error {
	sort.Slice(changes, func(i, j int) bool { return changes[i].key < changes[j].key })
	by := actorID(ctx)
	for _, c := range changes {
		raw, err := json.Marshal(c.value)
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		if err := s.store.Upsert(ctx, c.key, raw, by); err != nil {
			return err
		}
	}
	return nil
}

// writeAudit 在 ctx 的事务里写一条审计；设置键、开关码这类非 UUID 标识放在 data 里，ResourceID 留空。
func (s *Service) writeAudit(ctx context.Context, action, resourceType string, data map[string]any) error {
	return s.audit.Write(ctx, auditsvc.Event{Action: action, ResourceType: resourceType, Data: data})
}

// MissingPorts 返回还没注入的必需端口名（装配完成后由 internal/app 统一检查）。
// publicBaseURL 是配置值，不算端口。
func (s *Service) MissingPorts() []string {
	var out []string
	if s.rt == nil {
		out = append(out, "Realtime")
	}
	if s.siteListener == nil {
		out = append(out, "SiteChangeListener")
	}
	if s.extrasListener == nil {
		out = append(out, "SubscriptionExtrasListener")
	}
	if s.homeListener == nil {
		out = append(out, "HomeChangeListener")
	}
	return out
}
