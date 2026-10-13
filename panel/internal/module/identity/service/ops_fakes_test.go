package service

import (
	"context"
	"testing"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
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

// opsEnv 在 newEnv 上挂假订阅端口。
func opsEnv(t *testing.T) (*env, context.Context, *fakeOpsSubs) {
	e, ctx := newEnv(t)
	fs := &fakeOpsSubs{current: map[uuid.UUID]CurrentSub{}}
	e.svc.UseOpsSubscriptions(fs)
	return e, ctx, fs
}

func fieldsOf(err error) map[string]string {
	if err == nil {
		return nil
	}
	return errs.As(err).Fields
}
