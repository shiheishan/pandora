package service

// 实时推送的测试替身：起 miniredis 建真 Publisher，订阅频道收集发出的事件。
// 每个模块各放一份同样的文件（测试辅助不跨包共享）。

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// rtWait 是 expect 等一条推送的上限。
const rtWait = 2 * time.Second

// rtMsg 是收到的一条推送。
type rtMsg struct {
	Channel string
	Event   realtime.Event
}

// rtSpy 收集订阅频道上的推送；seen 是已收到、还没被 expect 认领的。
type rtSpy struct {
	ch   <-chan *redis.Message
	seen []rtMsg
}

// newRTSpy 起 miniredis，返回连上它的 Publisher 与订阅了 channels 的 spy。
func newRTSpy(t *testing.T, channels ...string) (*realtime.Publisher, *rtSpy) {
	t.Helper()
	mr, rdb := testkit.Redis(t)
	// miniredis 在 RESP3 下的订阅确认要等到读超时才到，订阅端用 RESP2
	subc := redis.NewClient(&redis.Options{Addr: mr.Addr(), Protocol: 2})
	t.Cleanup(func() { _ = subc.Close() })
	ctx := context.Background()
	sub := subc.Subscribe(ctx, channels...)
	t.Cleanup(func() { _ = sub.Close() })
	for range channels {
		if _, err := sub.Receive(ctx); err != nil {
			t.Fatalf("订阅实时频道: %v", err)
		}
	}
	return realtime.NewPublisher(rdb), &rtSpy{ch: sub.Channel()}
}

// read 在 d 内等下一条推送；超时回 false。
func (s *rtSpy) read(t *testing.T, d time.Duration) (rtMsg, bool) {
	t.Helper()
	select {
	case m, ok := <-s.ch:
		if !ok {
			return rtMsg{}, false
		}
		var ev realtime.Event
		if err := json.Unmarshal([]byte(m.Payload), &ev); err != nil {
			t.Fatalf("解码推送 %q: %v", m.Payload, err)
		}
		return rtMsg{Channel: m.Channel, Event: ev}, true
	case <-time.After(d):
		return rtMsg{}, false
	}
}

// matches 报告 m 是不是 channel 上 topic 的事件，且 payload 里 want 的每个键值都相等（按 fmt.Sprint 比较）。
func (m rtMsg) matches(channel, topic string, want map[string]any) bool {
	if m.Channel != channel || m.Event.Topic != topic {
		return false
	}
	for k, v := range want {
		if fmt.Sprint(m.Event.Payload[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

// expect 等到 channel 上出现 topic 的事件（payload 含 want 的键值），返回它；rtWait 内等不到就 Fatal。
func (s *rtSpy) expect(t *testing.T, channel, topic string, want map[string]any) realtime.Event {
	t.Helper()
	for i, m := range s.seen {
		if m.matches(channel, topic, want) {
			s.seen = append(s.seen[:i], s.seen[i+1:]...)
			return m.Event
		}
	}
	deadline := time.Now().Add(rtWait)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		m, ok := s.read(t, left)
		if !ok {
			break
		}
		if m.matches(channel, topic, want) {
			return m.Event
		}
		s.seen = append(s.seen, m)
	}
	t.Fatalf("没等到推送 channel=%s topic=%s want=%v；收到过 %+v", channel, topic, want, s.seen)
	return realtime.Event{}
}

// expectNone 断言 d 内（含之前没认领的）一条推送都没有。
func (s *rtSpy) expectNone(t *testing.T, d time.Duration) {
	t.Helper()
	if len(s.seen) > 0 {
		t.Fatalf("不该有推送，收到 %+v", s.seen)
	}
	if m, ok := s.read(t, d); ok {
		t.Fatalf("不该有推送，收到 %+v", m)
	}
}

// expectNoneOn 断言 d 内 channel 上没有推送（别的频道的推送收下不管）。
func (s *rtSpy) expectNoneOn(t *testing.T, channel string, d time.Duration) {
	t.Helper()
	for _, m := range s.seen {
		if m.Channel == channel {
			t.Fatalf("频道 %s 不该有推送，收到 %+v", channel, m)
		}
	}
	deadline := time.Now().Add(d)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return
		}
		m, ok := s.read(t, left)
		if !ok {
			return
		}
		if m.Channel == channel {
			t.Fatalf("频道 %s 不该有推送，收到 %+v", channel, m)
		}
		s.seen = append(s.seen, m)
	}
}
