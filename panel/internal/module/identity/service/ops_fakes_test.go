package service

import (
	"context"
	"testing"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/shopspring/decimal"
)

// fakeOpsSubs 是 OpsSubscriptions 的测试替身。
type fakeOpsSubs struct {
	ids         []uuid.UUID
	exclude     bool
	lastFilter  SubFilter
	filterCalls int
	current     map[uuid.UUID]CurrentSub
	fetches     []FetchRecord
	lastFetch   FetchQuery
	fetchCalls  int
	userFetches []FetchRecord
	sources     int
	admin       map[uuid.UUID]AdminUserSubs
	adminCalls  int
	adminIDs    [][]uuid.UUID
}

var _ OpsSubscriptions = (*fakeOpsSubs)(nil)

func (f *fakeOpsSubs) UserIDsBySubFilter(_ context.Context, sf SubFilter) ([]uuid.UUID, bool, error) {
	f.filterCalls++
	f.lastFilter = sf
	return f.ids, f.exclude, nil
}

func (f *fakeOpsSubs) CurrentSubs(_ context.Context, userIDs []uuid.UUID) (map[uuid.UUID]CurrentSub, error) {
	out := make(map[uuid.UUID]CurrentSub)
	for _, id := range userIDs {
		if cs, ok := f.current[id]; ok {
			out[id] = cs
		}
	}
	return out, nil
}

func (f *fakeOpsSubs) Fetches(_ context.Context, q FetchQuery) ([]FetchRecord, error) {
	f.fetchCalls++
	f.lastFetch = q
	return f.fetches, nil
}

func (f *fakeOpsSubs) UserFetches(_ context.Context, _ uuid.UUID) ([]FetchRecord, int, error) {
	return f.userFetches, f.sources, nil
}

func (f *fakeOpsSubs) AdminSubs(_ context.Context, userIDs []uuid.UUID) (map[uuid.UUID]AdminUserSubs, error) {
	f.adminCalls++
	f.adminIDs = append(f.adminIDs, userIDs)
	out := make(map[uuid.UUID]AdminUserSubs, len(userIDs))
	for _, id := range userIDs {
		if us, ok := f.admin[id]; ok {
			out[id] = us
		} else {
			out[id] = AdminUserSubs{Subscriptions: []AdminSub{}}
		}
	}
	return out, nil
}

// fakeWallets 是 OpsWallets 的测试替身。
type fakeWallets struct {
	currency string
	bal      map[uuid.UUID]decimal.Decimal
	calls    int
}

var _ OpsWallets = (*fakeWallets)(nil)

func (f *fakeWallets) UserBalances(_ context.Context, userIDs []uuid.UUID) (string, map[uuid.UUID]decimal.Decimal, error) {
	f.calls++
	out := make(map[uuid.UUID]decimal.Decimal, len(userIDs))
	for _, id := range userIDs {
		if v, ok := f.bal[id]; ok {
			out[id] = v
		} else {
			out[id] = decimal.Zero
		}
	}
	return f.currency, out, nil
}

// fakeTelegram 是 OpsTelegram 的测试替身。
type fakeTelegram struct {
	refs  map[uuid.UUID]*TelegramRef
	calls int
}

var _ OpsTelegram = (*fakeTelegram)(nil)

func (f *fakeTelegram) AdminTelegramBinding(_ context.Context, userID uuid.UUID) (*TelegramRef, error) {
	f.calls++
	return f.refs[userID], nil
}

// fakeGroupUsage 是 OpsGroupUsage 的测试替身。
type fakeGroupUsage struct {
	usage map[uuid.UUID]GroupUsage
	calls int
}

var _ OpsGroupUsage = (*fakeGroupUsage)(nil)

func (f *fakeGroupUsage) GroupUsage(_ context.Context) (map[uuid.UUID]GroupUsage, error) {
	f.calls++
	return f.usage, nil
}

// opsFakes 汇总后台列表/详情用到的假端口。
type opsFakes struct {
	subs     *fakeOpsSubs
	orders   *fakeExportOpsOrders
	wallets  *fakeWallets
	telegram *fakeTelegram
	groups   *fakeGroupUsage
}

// fakeJobQueue 记录入队调用，不真实入队。
type fakeJobQueue struct {
	types []string
	opts  []int
}

func (q *fakeJobQueue) EnqueueContext(_ context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	q.types = append(q.types, task.Type())
	q.opts = append(q.opts, len(opts))
	return &asynq.TaskInfo{}, nil
}

// writeEvent 以门户用户身份写一条带来源 IP 的审计。
func writeEvent(t *testing.T, e *env, ctx context.Context, user uuid.UUID, ip, action string) {
	t.Helper()
	op := e.svc.ops.(*auditsvc.Service)
	uctx := asUser(reqmeta.With(ctx, reqmeta.Meta{IP: ip, UserAgent: "ua-test/1"}), user)
	if err := op.Write(uctx, auditsvc.Event{Action: action}); err != nil {
		t.Fatal(err)
	}
}

// adminEnv 在 newEnv 上挂齐五个假端口（wallets 币种 CNY）。
func adminEnv(t *testing.T) (*env, context.Context, *opsFakes) {
	e, ctx := newEnv(t)
	f := &opsFakes{
		subs:     &fakeOpsSubs{current: map[uuid.UUID]CurrentSub{}, admin: map[uuid.UUID]AdminUserSubs{}},
		orders:   &fakeExportOpsOrders{},
		wallets:  &fakeWallets{currency: "CNY", bal: map[uuid.UUID]decimal.Decimal{}},
		telegram: &fakeTelegram{refs: map[uuid.UUID]*TelegramRef{}},
		groups:   &fakeGroupUsage{usage: map[uuid.UUID]GroupUsage{}},
	}
	e.svc.UseOpsSubscriptions(f.subs)
	e.svc.UseOpsOrders(f.orders)
	e.svc.UseOpsWallets(f.wallets)
	e.svc.UseOpsTelegram(f.telegram)
	e.svc.UseOpsGroupUsage(f.groups)
	return e, ctx, f
}

// opsEnv 在 newEnv 上挂假订阅端口与假订单统计端口（无订单），并装上 wallets、telegram、groups 以免 ListUsers 缺端口报错。
func opsEnv(t *testing.T) (*env, context.Context, *fakeOpsSubs) {
	e, ctx := newEnv(t)
	fs := &fakeOpsSubs{current: map[uuid.UUID]CurrentSub{}, admin: map[uuid.UUID]AdminUserSubs{}}
	e.svc.UseOpsSubscriptions(fs)
	e.svc.UseOpsOrders(&fakeExportOpsOrders{})
	e.svc.UseOpsWallets(&fakeWallets{currency: "CNY", bal: map[uuid.UUID]decimal.Decimal{}})
	e.svc.UseOpsTelegram(&fakeTelegram{refs: map[uuid.UUID]*TelegramRef{}})
	e.svc.UseOpsGroupUsage(&fakeGroupUsage{usage: map[uuid.UUID]GroupUsage{}})
	return e, ctx, fs
}

func fieldsOf(err error) map[string]string {
	if err == nil {
		return nil
	}
	return errs.As(err).Fields
}
