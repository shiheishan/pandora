package service

// 节点池 service 的 PostgreSQL 集成测试。

import (
	"strings"
	"testing"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func poolFind(t *testing.T, list []Pool, id uuid.UUID) Pool {
	t.Helper()
	for _, p := range list {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("ListPools 里没有池 %s", id)
	return Pool{}
}

func poolSliceLen(t *testing.T, label string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s 长度 %d，期望 %d", label, got, want)
	}
}

func TestCreatePoolDerivesCodeAndAudits(t *testing.T) {
	e, ctx := newPoolEnv(t)

	id, err := e.svc.CreatePool(ctx, PoolCreate{Name: "Hong Kong 01"})
	if err != nil {
		t.Fatal(err)
	}

	list, err := e.svc.ListPools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := poolFind(t, list, id)
	if p.Code != "hong-kong-01" {
		t.Fatalf("code 期望 hong-kong-01，得到 %q", p.Code)
	}
	if p.Status != PoolActive {
		t.Fatalf("status 期望 active，得到 %q", p.Status)
	}
	if p.Members == nil || p.PlanNames == nil || p.AllowedUserGroups == nil {
		t.Fatalf("Members/PlanNames/AllowedUserGroups 不应为 nil")
	}
	poolSliceLen(t, "Members", len(p.Members), 0)
	poolSliceLen(t, "PlanNames", len(p.PlanNames), 0)
	poolSliceLen(t, "AllowedUserGroups", len(p.AllowedUserGroups), 0)

	if poolAuditCount(t, ctx, e, ActionPoolCreated, id) != 1 {
		t.Fatalf("审计 node_pool.created 应为 1 条")
	}

	idHK, err := e.svc.CreatePool(ctx, PoolCreate{Name: "香港"})
	if err != nil {
		t.Fatal(err)
	}
	list2, err := e.svc.ListPools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	code := poolFind(t, list2, idHK).Code
	if !strings.HasPrefix(code, "pool-") {
		t.Fatalf("中文名派生的 code 应以 pool- 开头，得到 %q", code)
	}
}

func TestCreatePoolValidation(t *testing.T) {
	e, ctx := newPoolEnv(t)

	_, err := e.svc.CreatePool(ctx, PoolCreate{Name: ""})
	if poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("空名应为 ValidationFailed，得到 %v", err)
	}
	if poolErrFields(err)["name"] == "" {
		t.Fatalf("空名应带 fields.name")
	}

	_, err = e.svc.CreatePool(ctx, PoolCreate{Name: "ok", Code: "Bad Code"})
	if poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("非法 code 应为 ValidationFailed，得到 %v", err)
	}
	if poolErrFields(err)["code"] == "" {
		t.Fatalf("非法 code 应带 fields.code")
	}

	_, err = e.svc.CreatePool(ctx, PoolCreate{Name: "x\x00y"})
	if poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("含 NUL 的名字应为 ValidationFailed，得到 %v", err)
	}
	if poolErrFields(err)["name"] == "" {
		t.Fatalf("含 NUL 应带 fields.name")
	}

	_, err = e.svc.CreatePool(ctx, PoolCreate{Name: "first", Code: "dup1"})
	if err != nil {
		t.Fatal(err)
	}
	err = poolSavepoint(t, ctx, e, func() error {
		_, err := e.svc.CreatePool(ctx, PoolCreate{Name: "second", Code: "dup1"})
		return err
	})
	if poolErrCode(err) != errs.Conflict {
		t.Fatalf("重复 code 应为 Conflict，得到 %v", err)
	}
}

func TestPoolUserGroupsReplace(t *testing.T) {
	e, ctx := newPoolEnv(t)
	g1 := seedPoolGroup(t, ctx, e, "Alpha")
	g2 := seedPoolGroup(t, ctx, e, "Beta")

	g1only := []uuid.UUID{g1}
	id, err := e.svc.CreatePool(ctx, PoolCreate{Name: "grp pool", UserGroupIDs: &g1only})
	if err != nil {
		t.Fatal(err)
	}

	list, err := e.svc.ListPools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := poolFind(t, list, id)
	if len(p.AllowedUserGroups) != 1 || p.AllowedUserGroups[0].ID != g1 || p.AllowedUserGroups[0].Name != "Alpha" {
		t.Fatalf("CreatePool 名单应为 g1(Alpha)，得到 %v", p.AllowedUserGroups)
	}

	both := []uuid.UUID{g1, g2}
	if err := e.svc.UpdatePool(ctx, id, PoolUpdate{UserGroupIDs: &both}); err != nil {
		t.Fatal(err)
	}
	list, err = e.svc.ListPools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p = poolFind(t, list, id)
	if len(p.AllowedUserGroups) != 2 {
		t.Fatalf("期望 2 个组，得到 %d", len(p.AllowedUserGroups))
	}
	if p.AllowedUserGroups[0].Name != "Alpha" || p.AllowedUserGroups[1].Name != "Beta" {
		t.Fatalf("应按名字升序 Alpha,Beta，得到 %q,%q", p.AllowedUserGroups[0].Name, p.AllowedUserGroups[1].Name)
	}

	empty := []uuid.UUID{}
	if err := e.svc.UpdatePool(ctx, id, PoolUpdate{UserGroupIDs: &empty}); err != nil {
		t.Fatal(err)
	}
	if poolAuditCount(t, ctx, e, ActionPoolUserGroupsChanged, id) != 3 {
		t.Fatalf("user_groups_changed 审计应为 3 条")
	}

	beforeAudit := poolAuditCount(t, ctx, e, ActionPoolUserGroupsChanged, id)
	if err := e.svc.UpdatePool(ctx, id, PoolUpdate{UserGroupIDs: &empty}); err != nil {
		t.Fatal(err)
	}
	if poolAuditCount(t, ctx, e, ActionPoolUserGroupsChanged, id) != beforeAudit {
		t.Fatalf("相同名单再提交不应增加审计")
	}

	bad := []uuid.UUID{uuid.New()}
	if err := e.svc.UpdatePool(ctx, id, PoolUpdate{UserGroupIDs: &bad}); poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("不存在组 id 应为 ValidationFailed，得到 %v", err)
	}
	if poolErrFields(err)["allowed_user_group_ids"] == "" {
		t.Fatalf("应带 fields.allowed_user_group_ids")
	}
}

func TestUpdatePoolFields(t *testing.T) {
	e, ctx := newPoolEnv(t)
	id, err := e.svc.CreatePool(ctx, PoolCreate{Name: "原名", Region: "r0"})
	if err != nil {
		t.Fatal(err)
	}

	if err := e.svc.UpdatePool(ctx, id, PoolUpdate{Name: "新名", Region: "华东", Status: PoolDraining}); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.ListPools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := poolFind(t, list, id)
	if p.Name != "新名" || p.Region != "华东" || p.Status != PoolDraining {
		t.Fatalf("更新后字段不符: %+v", p)
	}

	if err := e.svc.UpdatePool(ctx, id, PoolUpdate{Name: "", Region: ""}); err != nil {
		t.Fatal(err)
	}
	list, err = e.svc.ListPools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p = poolFind(t, list, id)
	if p.Name != "新名" || p.Region != "华东" {
		t.Fatalf("空串更新应保持原值，得到 name=%q region=%q", p.Name, p.Region)
	}

	if err := e.svc.UpdatePool(ctx, id, PoolUpdate{Status: "bogus"}); poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("非法 status 应为 ValidationFailed，得到 %v", err)
	}

	if err := e.svc.UpdatePool(ctx, uuid.New(), PoolUpdate{Name: "x"}); poolErrCode(err) != errs.NotFound {
		t.Fatalf("不存在 id 应为 NotFound，得到 %v", err)
	}

	updatedBefore := poolAuditCount(t, ctx, e, ActionPoolUpdated, id)
	if err := e.svc.UpdatePool(ctx, id, PoolUpdate{}); err != nil {
		t.Fatal(err)
	}
	if poolAuditCount(t, ctx, e, ActionPoolUpdated, id) != updatedBefore {
		t.Fatalf("无字段变更不应写 node_pool.updated")
	}
}

func TestPoolChangesBumpUsersEpoch(t *testing.T) {
	e, ctx := newPoolEnvCommitted(t)

	id, err := e.svc.CreatePool(ctx, PoolCreate{Name: "epoch pool"})
	if err != nil {
		t.Fatal(err)
	}
	e.trackPool(id)

	e0 := poolEpoch(t, e)

	if err := e.svc.UpdatePool(ctx, id, PoolUpdate{Name: "epoch pool 2"}); err != nil {
		t.Fatal(err)
	}
	if poolEpoch(t, e) != e0 {
		t.Fatalf("只改名不应推进 users epoch")
	}

	if err := e.svc.UpdatePool(ctx, id, PoolUpdate{Status: PoolDraining}); err != nil {
		t.Fatal(err)
	}
	e1 := poolEpoch(t, e)
	if e1 == e0 {
		t.Fatalf("改 status 应推进 users epoch")
	}

	g1 := seedPoolGroup(t, ctx, e, "E1")
	ids := []uuid.UUID{g1}
	if err := e.svc.UpdatePool(ctx, id, PoolUpdate{UserGroupIDs: &ids}); err != nil {
		t.Fatal(err)
	}
	e2 := poolEpoch(t, e)
	if e2 == e1 {
		t.Fatalf("改用户组名单应推进 users epoch")
	}

	if err := e.svc.UpdatePool(ctx, id, PoolUpdate{UserGroupIDs: &ids}); err != nil {
		t.Fatal(err)
	}
	if poolEpoch(t, e) != e2 {
		t.Fatalf("相同名单再提交不应再推进 epoch")
	}
}

func TestDeletePool(t *testing.T) {
	e, ctx := newPoolEnv(t)

	emptyID := seedPoolRow(t, ctx, e, PoolActive)
	if err := e.svc.DeletePool(ctx, emptyID); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.ListPools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.ID == emptyID {
			t.Fatalf("空池删除后不应出现在 ListPools")
		}
	}
	if poolAuditCount(t, ctx, e, ActionPoolDeleted, emptyID) != 1 {
		t.Fatalf("删除空池应有 deleted 审计 1 条")
	}

	livePool := seedPoolRow(t, ctx, e, PoolActive)
	seedPoolNode(t, ctx, e, livePool, nil, "draft", 0)
	err = poolSavepoint(t, ctx, e, func() error {
		return e.svc.DeletePool(ctx, livePool)
	})
	if poolErrCode(err) != errs.Conflict {
		t.Fatalf("有未删节点应 Conflict，得到 %v", err)
	}

	retiredPool := seedPoolRow(t, ctx, e, PoolActive)
	nodeID := seedPoolNode(t, ctx, e, retiredPool, nil, "draft", 0)
	deletePoolNode(t, ctx, e, nodeID)
	if err := e.svc.DeletePool(ctx, retiredPool); err != nil {
		t.Fatal(err)
	}

	usagePool := seedPoolRow(t, ctx, e, PoolActive)
	e.cat.usage[usagePool] = catalogsvc.PoolUsage{Versions: 1}
	err = poolSavepoint(t, ctx, e, func() error {
		return e.svc.DeletePool(ctx, usagePool)
	})
	if poolErrCode(err) != errs.Conflict {
		t.Fatalf("catalog 报用量应 Conflict，得到 %v", err)
	}

	if err := e.svc.DeletePool(ctx, uuid.New()); poolErrCode(err) != errs.NotFound {
		t.Fatalf("不存在池应为 NotFound，得到 %v", err)
	}
}

func TestDeletePoolReferencedByVersionFK(t *testing.T) {
	e, ctx := newPoolEnv(t)
	poolID := seedPoolRow(t, ctx, e, PoolActive)

	var planID uuid.UUID
	code := poolRandName(t, "pl")
	if err := e.row(ctx, `INSERT INTO plans (code, name) VALUES (?, ?) RETURNING id`, code, "套餐").Scan(&planID); err != nil {
		t.Fatal(err)
	}
	var versionID uuid.UUID
	if err := e.row(ctx, `INSERT INTO plan_versions (plan_id, version) VALUES (?, 1) RETURNING id`, planID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `INSERT INTO plan_node_pools (plan_version_id, pool_id) VALUES (?, ?)`, versionID, poolID); err != nil {
		t.Fatal(err)
	}

	err := poolSavepoint(t, ctx, e, func() error {
		return e.svc.DeletePool(ctx, poolID)
	})
	if poolErrCode(err) != errs.Conflict {
		t.Fatalf("FK 绑定应 Conflict，得到 %v", err)
	}
}

func TestListPoolsCounts(t *testing.T) {
	e, ctx := newPoolEnv(t)
	poolID := seedPoolRow(t, ctx, e, PoolActive)

	srvHB := seedPoolServer(t, ctx, e, "ready", true)
	srvNoHB := seedPoolServer(t, ctx, e, "ready", false)
	seedPoolNode(t, ctx, e, poolID, &srvHB, "active", 443)
	seedPoolNode(t, ctx, e, poolID, &srvNoHB, "active", 444)
	seedPoolNode(t, ctx, e, poolID, nil, "draft", 0)
	delID := seedPoolNode(t, ctx, e, poolID, nil, "draft", 0)
	deletePoolNode(t, ctx, e, delID)

	e.cat.usage[poolID] = catalogsvc.PoolUsage{Versions: 2, PlanNames: []string{"A", "B"}}

	list, err := e.svc.ListPools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := poolFind(t, list, poolID)
	if p.Nodes != 3 {
		t.Fatalf("Nodes 期望 3，得到 %d", p.Nodes)
	}
	if p.ActiveNodes != 2 {
		t.Fatalf("ActiveNodes 期望 2，得到 %d", p.ActiveNodes)
	}
	if len(p.Members) != 3 {
		t.Fatalf("Members 期望 3（不含软删），得到 %d", len(p.Members))
	}
	if p.Plans != 2 {
		t.Fatalf("Plans 期望 2，得到 %d", p.Plans)
	}
	if len(p.PlanNames) != 2 || p.PlanNames[0] != "A" || p.PlanNames[1] != "B" {
		t.Fatalf("PlanNames 期望 [A,B]，得到 %v", p.PlanNames)
	}
}
