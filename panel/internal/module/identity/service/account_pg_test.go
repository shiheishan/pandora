package service

import (
	"testing"

	"github.com/google/uuid"
)

// TestPGGetCarriesUserGroup：Get、GetMany 带回用户组，默认组（没有分组）是 nil。
// 订阅拉取按 Get 的结果过滤线路，不再另读一次用户组。
func TestPGGetCarriesUserGroup(t *testing.T) {
	e, ctx := newEnv(t)

	var groupID uuid.UUID
	if err := e.row(ctx, `INSERT INTO user_groups (code, name) VALUES (?, ?) RETURNING id`,
		randGroupCode(t), "测试组").Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	grouped := e.mkUser(t, ctx, uniqEmail(t, "grp"), "Passw0rdLong1")
	if err := e.exec(ctx, `UPDATE users SET user_group_id = ? WHERE id = ?`, groupID, grouped.ID); err != nil {
		t.Fatal(err)
	}
	plain := e.mkUser(t, ctx, uniqEmail(t, "nogrp"), "Passw0rdLong1")

	u, err := e.svc.Get(ctx, grouped.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.UserGroupID == nil || *u.UserGroupID != groupID {
		t.Fatalf("有组用户的 UserGroupID 是 %v，期望 %s", u.UserGroupID, groupID)
	}
	u, err = e.svc.Get(ctx, plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.UserGroupID != nil {
		t.Fatalf("默认组用户的 UserGroupID 应为 nil，得 %s", *u.UserGroupID)
	}

	many, err := e.svc.GetMany(ctx, []uuid.UUID{grouped.ID, plain.ID})
	if err != nil {
		t.Fatal(err)
	}
	if g := many[grouped.ID].UserGroupID; g == nil || *g != groupID {
		t.Fatalf("GetMany 有组用户的 UserGroupID 是 %v，期望 %s", g, groupID)
	}
	if g := many[plain.ID].UserGroupID; g != nil {
		t.Fatalf("GetMany 默认组用户的 UserGroupID 应为 nil，得 %s", *g)
	}
}
