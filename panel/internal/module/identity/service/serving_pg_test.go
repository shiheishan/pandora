package service

import (
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"
)

func randGroupCode(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "g" + hex.EncodeToString(b)
}

func TestPGActiveUserGroups(t *testing.T) {
	e, ctx := newEnv(t)

	var groupID uuid.UUID
	if err := e.row(ctx, `INSERT INTO user_groups (code, name) VALUES (?, ?) RETURNING id`,
		randGroupCode(t), "测试组").Scan(&groupID); err != nil {
		t.Fatal(err)
	}

	activeGrouped := e.mkUser(t, ctx, uniqEmail(t, "ag"), "Passw0rdLong1")
	if err := e.exec(ctx, `UPDATE users SET user_group_id = ? WHERE id = ?`, groupID, activeGrouped.ID); err != nil {
		t.Fatal(err)
	}

	activeNoGroup := e.mkUser(t, ctx, uniqEmail(t, "ang"), "Passw0rdLong1")

	suspended := e.mkUser(t, ctx, uniqEmail(t, "sus"), "Passw0rdLong1")
	if err := e.exec(ctx, `UPDATE users SET status = ? WHERE id = ?`, StatusSuspended, suspended.ID); err != nil {
		t.Fatal(err)
	}

	banned := e.mkUser(t, ctx, uniqEmail(t, "ban"), "Passw0rdLong1")
	if err := e.exec(ctx, `UPDATE users SET status = ? WHERE id = ?`, StatusBanned, banned.ID); err != nil {
		t.Fatal(err)
	}

	missing := uuid.New()
	ids := []uuid.UUID{activeGrouped.ID, activeNoGroup.ID, suspended.ID, banned.ID, missing}

	got, err := e.svc.ActiveUserGroups(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("active 结果应为 2 条，得 %d: %v", len(got), got)
	}
	if got[activeGrouped.ID] == nil || *got[activeGrouped.ID] != groupID {
		t.Fatalf("有组用户: %v", got[activeGrouped.ID])
	}
	if _, ok := got[activeNoGroup.ID]; !ok || got[activeNoGroup.ID] != nil {
		t.Fatalf("无组 active 应为 nil 指针值: %v", got[activeNoGroup.ID])
	}
	if _, ok := got[suspended.ID]; ok {
		t.Fatal("suspended 不应在结果里")
	}
	if _, ok := got[banned.ID]; ok {
		t.Fatal("banned 不应在结果里")
	}
	if _, ok := got[missing]; ok {
		t.Fatal("不存在的 id 不应在结果里")
	}

	empty, err := e.svc.ActiveUserGroups(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("nil 输入应得空 map，得 %d", len(empty))
	}
	empty2, err := e.svc.ActiveUserGroups(ctx, []uuid.UUID{})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty2) != 0 {
		t.Fatalf("空切片应得空 map，得 %d", len(empty2))
	}
}
