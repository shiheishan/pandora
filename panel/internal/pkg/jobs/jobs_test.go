package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

type payload struct {
	UserID int64  `json:"user_id"`
	Note   string `json:"note"`
}

func payloadReqID(t *testing.T, task *asynq.Task) string {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(task.Payload(), &env); err != nil {
		t.Fatal(err)
	}
	return env.ReqID
}

func TestNewTaskDecodeRoundTrip(t *testing.T) {
	task, err := NewTask(context.Background(), "demo:run", payload{UserID: 7, Note: "x"})
	if err != nil {
		t.Fatal(err)
	}
	var got payload
	if err := Decode(task, &got); err != nil {
		t.Fatal(err)
	}
	if got.UserID != 7 || got.Note != "x" {
		t.Fatalf("got %+v", got)
	}
	if !reqid.Valid(payloadReqID(t, task)) {
		t.Fatal("missing generated req_id")
	}
}

func TestNewTaskNilData(t *testing.T) {
	task, err := NewTask(context.Background(), "demo:tick", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got payload
	if err := Decode(task, &got); err != nil {
		t.Fatal(err)
	}
}

func TestNewTaskCarriesCtxReqID(t *testing.T) {
	id := reqid.New()
	task, err := NewTask(reqid.With(context.Background(), id), "demo:run", payload{})
	if err != nil {
		t.Fatal(err)
	}
	if payloadReqID(t, task) != id {
		t.Fatal("req_id not propagated")
	}
}

func run(t *testing.T, task *asynq.Task, h asynq.HandlerFunc) error {
	t.Helper()
	return Middleware(zap.NewNop())(h).ProcessTask(context.Background(), task)
}

func TestMiddlewarePutsReqIDInCtx(t *testing.T) {
	id := reqid.New()
	task, _ := NewTask(reqid.With(context.Background(), id), "demo:run", payload{})
	var seen string
	if err := run(t, task, func(ctx context.Context, _ *asynq.Task) error {
		seen = reqid.From(ctx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != id {
		t.Fatalf("seen %q want %q", seen, id)
	}
}

func TestMiddlewareBadPayloadStillRuns(t *testing.T) {
	var seen string
	if err := run(t, asynq.NewTask("demo:raw", []byte("not json")), func(ctx context.Context, _ *asynq.Task) error {
		seen = reqid.From(ctx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reqid.Valid(seen) {
		t.Fatalf("want generated req_id, got %q", seen)
	}
}

func TestMiddlewareReturnsHandlerError(t *testing.T) {
	want := errors.New("boom")
	task, _ := NewTask(context.Background(), "demo:run", nil)
	if err := run(t, task, func(context.Context, *asynq.Task) error { return want }); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}

func TestZapLoggerIsAsynqLogger(t *testing.T) {
	var _ asynq.Logger = ZapLogger{zap.NewNop().Sugar()}
}

func TestPeriodicTaskPayloadStable(t *testing.T) {
	t1 := newPeriodicTask("demo:tick")
	t2 := newPeriodicTask("demo:tick")
	if string(t1.Payload()) != string(t2.Payload()) {
		t.Fatalf("payloads not equal: %q vs %q", t1.Payload(), t2.Payload())
	}
	if reqID := payloadReqID(t, t1); reqID != "" {
		t.Fatalf("expected empty req_id in payload, got %q", reqID)
	}
	var got payload
	if err := Decode(t1, &got); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
}

func TestPeriodicTaskFreshReqIDPerRun(t *testing.T) {
	task := newPeriodicTask("demo:tick")
	var id1, id2 string
	if err := run(t, task, func(ctx context.Context, _ *asynq.Task) error {
		id1 = reqid.From(ctx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := run(t, task, func(ctx context.Context, _ *asynq.Task) error {
		id2 = reqid.From(ctx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reqid.Valid(id1) {
		t.Fatalf("run 1 got invalid req_id %q", id1)
	}
	if !reqid.Valid(id2) {
		t.Fatalf("run 2 got invalid req_id %q", id2)
	}
	if id1 == id2 {
		t.Fatalf("expected different req_ids, got %q == %q", id1, id2)
	}
}
