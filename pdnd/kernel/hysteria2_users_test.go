package kernel

import (
	"crypto/tls"
	"fmt"
	"testing"

	"github.com/aegispanel/nodeagent/core"
	hy2 "github.com/aegispanel/nodeagent/internal/nativewire/hysteria2"
)

func newHy2UsersAdapter(tb testing.TB, users int) *hysteria2Adapter {
	tb.Helper()
	value, err := newHysteria2Adapter(InboundSpec{})
	if err != nil {
		tb.Fatal(err)
	}
	adapter := value.(*hysteria2Adapter)
	service, err := hy2.NewService[int](hy2.ServiceOptions{TLSConfig: &hysteria2TLSConfig{std: &tls.Config{}}})
	if err != nil {
		tb.Fatal(err)
	}
	adapter.service = service
	batch := make([]core.User, users)
	for i := range batch {
		batch[i] = core.User{ID: int64(i + 1), UUID: fmt.Sprintf("hy2-user-%06d", i)}
	}
	if err := adapter.AddUsers(batch); err != nil {
		tb.Fatal(err)
	}
	return adapter
}

// BenchmarkHysteria2UserDelta：5000 用户时删 50、加回 50（面板增量同步的常见形状）。
func BenchmarkHysteria2UserDelta(b *testing.B) {
	adapter := newHy2UsersAdapter(b, 5000)
	changed := make([]core.User, 50)
	ids := make([]string, 50)
	for i := range changed {
		changed[i] = core.User{ID: int64(i + 1), UUID: fmt.Sprintf("hy2-user-%06d", i*97)}
		ids[i] = changed[i].UUID
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := adapter.DelUsers(ids); err != nil {
			b.Fatal(err)
		}
		if err := adapter.AddUsers(changed); err != nil {
			b.Fatal(err)
		}
	}
}

// hy2AssertServiceMatchesSlots 核对口令表与槽位表一致：活跃槽位都能按口令查到
// 自己的下标，停用槽位一个都查不到。
func hy2AssertServiceMatchesSlots(t *testing.T, adapter *hysteria2Adapter) {
	t.Helper()
	adapter.mu.RLock()
	defer adapter.mu.RUnlock()
	active := 0
	for index, slot := range adapter.slots {
		got, ok := adapter.service.LookupUser(slot.user.UUID)
		if slot.active {
			active++
			if !ok || got != index {
				t.Fatalf("活跃槽位 %d（%s）在口令表里是 %d/%v", index, slot.user.UUID, got, ok)
			}
		} else if ok && !adapter.slots[got].active {
			t.Fatalf("停用槽位 %d 的口令仍指向停用槽位 %d", index, got)
		}
	}
	if adapter.service.UserCount() != active || len(adapter.users) != active {
		t.Fatalf("口令表 %d 条、users %d 条、活跃槽位 %d 个", adapter.service.UserCount(), len(adapter.users), active)
	}
}

func TestHysteria2IncrementalUserUpdates(t *testing.T) {
	adapter := newHy2UsersAdapter(t, 20)
	hy2AssertServiceMatchesSlots(t, adapter)
	if err := adapter.DelUsers([]string{"hy2-user-000003", " hy2-user-000004 ", "missing"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := adapter.service.LookupUser("hy2-user-000003"); ok {
		t.Fatal("已删除用户仍能认证")
	}
	hy2AssertServiceMatchesSlots(t, adapter)
	// 加回：拿新槽位，旧槽位保持停用。
	if err := adapter.AddUsers([]core.User{{ID: 300, UUID: "hy2-user-000003"}}); err != nil {
		t.Fatal(err)
	}
	if index, ok := adapter.service.LookupUser("hy2-user-000003"); !ok || index != 20 {
		t.Fatalf("加回后口令指向 %d/%v，期望新槽位 20", index, ok)
	}
	// Upsert 已存在的用户：槽位不变，属性更新。
	if err := adapter.UpsertUsers([]core.User{{ID: 7, UUID: "hy2-user-000006", DeviceLimit: 3}, {ID: 400, UUID: "brand-new"}}); err != nil {
		t.Fatal(err)
	}
	if adapter.slots[6].user.DeviceLimit != 3 {
		t.Fatalf("Upsert 未更新属性：%+v", adapter.slots[6].user)
	}
	hy2AssertServiceMatchesSlots(t, adapter)
	// 整批里有空口令：整批拒绝，口令表不变。
	before := adapter.service.UserCount()
	if err := adapter.AddUsers([]core.User{{ID: 500, UUID: "ok-one"}, {ID: 501}}); err == nil || adapter.service.UserCount() != before {
		t.Fatalf("非法批次部分生效：err=%v count=%d→%d", err, before, adapter.service.UserCount())
	}
}

// 并发增删同一批口令：结束后口令表必须与槽位表完全一致（-race 下跑）。
func TestHysteria2ConcurrentUserUpdatesStayConsistent(t *testing.T) {
	adapter := newHy2UsersAdapter(t, 50)
	done := make(chan struct{})
	for worker := 0; worker < 4; worker++ {
		go func(worker int) {
			defer func() { done <- struct{}{} }()
			for round := 0; round < 200; round++ {
				password := fmt.Sprintf("hy2-user-%06d", (worker*13+round)%60)
				if round%2 == 0 {
					_ = adapter.DelUsers([]string{password})
				} else {
					_ = adapter.UpsertUsers([]core.User{{ID: int64(round), UUID: password}})
				}
			}
		}(worker)
	}
	for worker := 0; worker < 4; worker++ {
		<-done
	}
	hy2AssertServiceMatchesSlots(t, adapter)
}
