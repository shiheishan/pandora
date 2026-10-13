package task

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/certs/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

type fakeSvc struct {
	mu        sync.Mutex
	patrols   int
	patrolErr error
	issued    []uuid.UUID
	issueErr  error
}

func (f *fakeSvc) Patrol(context.Context) (service.PatrolResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patrols++
	return service.PatrolResult{Queued: 1}, f.patrolErr
}

func (f *fakeSvc) IssueOrder(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issued = append(f.issued, id)
	return f.issueErr
}

func newModule(f *fakeSvc, vars ...string) *Module {
	m := New(f)
	m.envVars = func() []string { return vars }
	return m
}

func TestPeriodicPatrolOptions(t *testing.T) {
	ps := newModule(&fakeSvc{}).Periodic()
	if len(ps) != 1 || ps[0].Type != TypePatrol || ps[0].Cronspec != "@every 1m" {
		t.Fatalf("periodic: %+v", ps)
	}
	var queue string
	retry := -1
	var unique, timeout time.Duration
	for _, o := range ps[0].Opts {
		switch o.Type() {
		case asynq.QueueOpt:
			queue = o.Value().(string)
		case asynq.MaxRetryOpt:
			retry = o.Value().(int)
		case asynq.UniqueOpt:
			unique = o.Value().(time.Duration)
		case asynq.TimeoutOpt:
			timeout = o.Value().(time.Duration)
		}
	}
	if queue != "default" || retry != 0 || timeout != 30*time.Second {
		t.Fatalf("opts: queue=%s retry=%d timeout=%s", queue, retry, timeout)
	}
	// Unique 必须短于周期，否则下一分钟的巡检会被当成重复丢掉
	if unique <= 0 || unique >= time.Minute {
		t.Fatalf("unique 应短于 1 分钟：%s", unique)
	}
}

func TestPatrolReturnsError(t *testing.T) {
	f := &fakeSvc{patrolErr: errors.New("boom")}
	if err := newModule(f).patrol(context.Background(), asynq.NewTask(TypePatrol, nil)); err == nil {
		t.Fatal("巡检出错应回错误")
	}
	f.patrolErr = nil
	if err := newModule(f).patrol(context.Background(), asynq.NewTask(TypePatrol, nil)); err != nil {
		t.Fatalf("patrol: %v", err)
	}
	if f.patrols != 2 {
		t.Fatalf("patrols = %d", f.patrols)
	}
}

func TestIssueDecodesPayloadAndSwallowsErrors(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	task, err := jobs.NewTask(context.Background(), service.TaskIssue, service.IssuePayload{OrderID: id})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSvc{issueErr: errors.New("db down")}
	// 签发出错也回 nil：失败的任务会被归档并占住 TaskID，同一张订单就再也入不了队
	if err := newModule(f).issue(context.Background(), task); err != nil {
		t.Fatalf("issue 应回 nil：%v", err)
	}
	if len(f.issued) != 1 || f.issued[0] != id {
		t.Fatalf("issued: %v", f.issued)
	}
}

func TestIssueRejectsBadPayload(t *testing.T) {
	f := &fakeSvc{}
	if err := newModule(f).issue(context.Background(), asynq.NewTask(service.TaskIssue, []byte("{"))); err == nil {
		t.Fatal("载荷解不开应回错误")
	}
	if len(f.issued) != 0 {
		t.Fatal("载荷解不开不该调 service")
	}
}

func TestLibraryEnvWarnedOnce(t *testing.T) {
	calls := 0
	m := New(&fakeSvc{})
	m.envVars = func() []string { calls++; return []string{"LEGO_DEBUG_CLIENT_VERBOSE_ERROR"} }
	for range 3 {
		_ = m.patrol(context.Background(), asynq.NewTask(TypePatrol, nil))
	}
	if calls != 1 {
		t.Fatalf("环境变量只该查一次：%d", calls)
	}
}
