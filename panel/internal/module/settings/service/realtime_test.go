package service

import (
	"context"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/settings/model"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// 切换开关后只推管理端一条 switches.changed，载荷是开关代码与新状态。
func TestSetSwitchPublishesToAdmin(t *testing.T) {
	st := &fakeStore{rows: map[string]model.FeatureSwitch{
		"billing.checkout": {Code: "billing.checkout", Enabled: true},
	}}
	s := newService(st, fakeTx{}, nil, nil, &auditLog{})
	pub, spy := newRTSpy(t, realtime.ChannelAdmin(), realtime.ChannelPublic())
	s.UseRealtime(pub)

	if err := s.SetSwitch(context.Background(), "billing.checkout", false, "维护"); err != nil {
		t.Fatal(err)
	}
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicSwitches, map[string]any{
		"code":    "billing.checkout",
		"enabled": false,
	})
	spy.expectNoneOn(t, realtime.ChannelPublic(), 200*time.Millisecond)
}

// 被规则拒绝的切换不推。
func TestSetSwitchRejectedDoesNotPublish(t *testing.T) {
	st := &fakeStore{rows: map[string]model.FeatureSwitch{
		"billing.checkout": {Code: "billing.checkout", Enabled: true},
	}}
	s := newService(st, fakeTx{}, nil, nil, &auditLog{})
	pub, spy := newRTSpy(t, realtime.ChannelAdmin(), realtime.ChannelPublic())
	s.UseRealtime(pub)

	if err := s.SetSwitch(context.Background(), "billing.checkout", false, "  "); err == nil {
		t.Fatal("不填原因的关闭不该成功")
	}
	spy.expectNone(t, 200*time.Millisecond)
}
