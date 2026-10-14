package service

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/reqid"
)

// spyRoster 记录 UsersChanged 收到的用户 id（真提交钩子用）。
type spyRoster struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (s *spyRoster) UsersChanged(_ context.Context, userIDs []uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, userIDs...)
}

func (s *spyRoster) count(userID uuid.UUID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, id := range s.ids {
		if id == userID {
			n++
		}
	}
	return n
}

func (s *spyRoster) contains(userID uuid.UUID) bool {
	return s.count(userID) > 0
}

func TestPGRosterNotify(t *testing.T) {
	spy := &spyRoster{}
	e, _ := newEnv(t)
	ctx := reqid.With(context.Background(), reqid.New())
	e.svc.UseRosterNotifier(spy)

	var groupID uuid.UUID
	if err := e.row(ctx, `INSERT INTO user_groups (code, name) VALUES (?, ?) RETURNING id`,
		randGroupCode(t), "名单通知组").Scan(&groupID); err != nil {
		t.Fatal(err)
	}

	var target uuid.UUID
	email := uniqEmail(t, "roster")
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&target); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = e.exec(ctx, `DELETE FROM users WHERE id = ?`, target)
		_ = e.exec(ctx, `DELETE FROM user_groups WHERE id = ?`, groupID)
	})

	actx := asAdmin(ctx, uuid.New())

	if err := e.svc.SetStatus(actx, target, StatusSuspended, "ops"); err != nil {
		t.Fatal(err)
	}
	if spy.count(target) != 1 {
		t.Fatalf("停用后应通知 1 次，得 %d", spy.count(target))
	}

	if err := e.svc.SetStatus(actx, target, StatusSuspended, "ops"); err != nil {
		t.Fatal(err)
	}
	if spy.count(target) != 1 {
		t.Fatalf("同状态再设不应再通知，得 %d", spy.count(target))
	}

	if err := e.svc.SetStatus(actx, target, StatusActive, ""); err != nil {
		t.Fatal(err)
	}
	if spy.count(target) != 2 {
		t.Fatalf("恢复后应为 2 次，得 %d", spy.count(target))
	}

	if err := e.svc.AssignGroup(actx, target, &groupID); err != nil {
		t.Fatal(err)
	}
	if spy.count(target) != 3 {
		t.Fatalf("入组后应为 3 次，得 %d", spy.count(target))
	}

	if err := e.svc.AssignGroup(actx, target, nil); err != nil {
		t.Fatal(err)
	}
	if spy.count(target) != 4 {
		t.Fatalf("移出组后应为 4 次，得 %d", spy.count(target))
	}

	// 回滚事务里提交不会跑 AfterCommit
	e2, txctx := newEnv(t)
	e2.svc.UseRosterNotifier(spy)
	before := len(spy.ids)
	other := e2.mkUser(t, txctx, uniqEmail(t, "tx"), "Passw0rdLong1")
	if err := e2.svc.SetStatus(asAdmin(txctx, uuid.New()), other.ID, StatusBanned, "x"); err != nil {
		t.Fatal(err)
	}
	if spy.contains(other.ID) {
		t.Fatal("未提交事务不应触发名单通知")
	}
	if len(spy.ids) != before {
		t.Fatalf("spy 总长不应变: before=%d after=%d", before, len(spy.ids))
	}
}
