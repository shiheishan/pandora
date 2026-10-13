package service

import (
	"context"
	"testing"
	"time"

	"github.com/hibiken/asynq"
)

func TestKickUsesFixedTaskID(t *testing.T) {
	clock := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	q := &fakeQueue{}
	s := &Service{
		queue: q,
		now:   func() time.Time { return clock },
	}

	ctx := context.Background()
	s.kick(ctx)

	clock = clock.Add(5 * time.Second)
	s.kick(ctx)

	if len(q.opts) != 2 {
		t.Fatalf("期望入队 2 次，实际 %d 次", len(q.opts))
	}

	extractTaskID := func(opts []asynq.Option) string {
		for _, o := range opts {
			if o.Type() == asynq.TaskIDOpt {
				return o.Value().(string)
			}
		}
		return ""
	}

	for i, opts := range q.opts {
		id := extractTaskID(opts)
		if id != kickTaskID {
			t.Errorf("第 %d 次入队 TaskID = %q，期望 %q", i+1, id, kickTaskID)
		}
	}
}
