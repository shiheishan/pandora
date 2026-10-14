package pluginevents

import (
	"context"
	"errors"
	"testing"
)

type recorder struct {
	events []string
	keys   []string
	err    error
}

func (r *recorder) Emit(_ context.Context, event, dedupeKey string, _ any) error {
	r.events = append(r.events, event)
	r.keys = append(r.keys, dedupeKey)
	return r.err
}

func TestEmitNilEmitterIsNoop(t *testing.T) {
	if err := Emit(context.Background(), nil, "order.paid", "k", map[string]any{"a": 1}); err != nil {
		t.Fatalf("nil 事件口应返回 nil，得到 %v", err)
	}
}

func TestEmitForwardsAndReturnsError(t *testing.T) {
	r := &recorder{}
	if err := Emit(context.Background(), r, "order.paid", "order.paid:1", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if len(r.events) != 1 || r.events[0] != "order.paid" || r.keys[0] != "order.paid:1" {
		t.Fatalf("转发不对：%v %v", r.events, r.keys)
	}
	r.err = errors.New("写待投递失败")
	if err := Emit(context.Background(), r, "order.paid", "order.paid:2", nil); !errors.Is(err, r.err) {
		t.Fatalf("应原样返回事件口的错误，得到 %v", err)
	}
}
