package service

// 覆盖 00050_user_group_status.sql 与随之而来的行为改动：
//   - DeleteGroup：专属价不再拦删组，没被买过的随组级联删除（审计记 prices_deleted），
//     被礼品卡等引用过的价格让级联撞 RESTRICT 外键，删组回 409；
//   - 价格冻结守卫 catalog_guard_price：组还在时直接 DELETE prices 照旧 23001，只有删组的级联放行；
//   - SetGroupStatus：停用 / 启用、幂等、审计 user_group.status_changed、不存在 404；
//   - AssignGroup：停用组不收新成员（已在组内再提交同组不拦）；
//   - SubmitGeneration / RunGenerationJob：组停用时入队 422、执行时任务失败。

import (
	"context"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
)

// inSavepoint 在 SAVEPOINT 里调 fn，fn 出错就回滚到保存点：用例整个跑在一个事务里（testkit.TxCtx），
// 撞库约束会让整个事务中止，之后还要读库的断言必须把会失败的调用包在这里。
func inSavepoint(t *testing.T, ctx context.Context, e *env, name string, fn func() error) error {
	t.Helper()
	if err := e.exec(ctx, `SAVEPOINT `+name); err != nil {
		t.Fatalf("SAVEPOINT %s: %v", name, err)
	}
	err := fn()
	if err != nil {
		if rbErr := e.exec(ctx, `ROLLBACK TO SAVEPOINT `+name); rbErr != nil {
			t.Fatalf("ROLLBACK TO SAVEPOINT %s: %v", name, rbErr)
		}
		return err
	}
	if err := e.exec(ctx, `RELEASE SAVEPOINT `+name); err != nil {
		t.Fatalf("RELEASE SAVEPOINT %s: %v", name, err)
	}
	return nil
}

// groupPrice 建一个套餐和挂在组上的一条专属价，返回价格 id。status 为 "active" 或 "archived"；amount 区分同组多条价格。
func groupPrice(t *testing.T, ctx context.Context, e *env, gid uuid.UUID, status string, amount int) uuid.UUID {
	t.Helper()
	var planID uuid.UUID
	if err := e.row(ctx, `INSERT INTO plans (code, name) VALUES (?, ?) RETURNING id`, "ug-"+uuid.NewString()[:8], "组价套餐").Scan(&planID); err != nil {
		t.Fatal(err)
	}
	var priceID uuid.UUID
	if err := e.row(ctx, `INSERT INTO prices (plan_id, unit_amount, user_group_id, status) VALUES (?, ?, ?, ?) RETURNING id`, planID, amount, gid, status).Scan(&priceID); err != nil {
		t.Fatal(err)
	}
	return priceID
}

// 删组连带删掉没被买过的专属价（在售、归档都删），审计记 prices_deleted。
func TestPGDeleteGroupDeletesUnsoldPrices(t *testing.T) {
	e, ctx, f := adminEnv(t)
	actx := adminCtx(t, e, ctx)
	gid, err := e.svc.CreateGroup(actx, "", "连价删组", "")
	if err != nil {
		t.Fatal(err)
	}
	p1 := groupPrice(t, ctx, e, gid, "active", 10)
	p2 := groupPrice(t, ctx, e, gid, "archived", 20)
	f.groups.usage[gid] = GroupUsage{Prices: 2}

	if err := e.svc.DeleteGroup(actx, gid); err != nil {
		t.Fatalf("没被买过时应能删组：%v", err)
	}
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM prices WHERE id IN (?, ?)`, p1, p2).Scan(&n); err != nil || n != 0 {
		t.Fatalf("专属价应随组删除 n=%d err=%v", n, err)
	}
	if err := e.row(ctx, `SELECT count(*) FROM user_groups WHERE id = ?`, gid).Scan(&n); err != nil || n != 0 {
		t.Fatalf("组应已删除 n=%d err=%v", n, err)
	}
	var deleted string
	if err := e.row(ctx, `SELECT data->>'prices_deleted' FROM audit_events WHERE action = 'user_group.deleted' AND resource_id = ?`, gid).
		Scan(&deleted); err != nil || deleted != "2" {
		t.Fatalf("prices_deleted=%q err=%v", deleted, err)
	}
}

// 专属价被礼品卡模板引用过（RESTRICT 外键）：级联删除失败，删组回 409，组与价格都还在。
func TestPGDeleteGroupRefusedWhenPriceSold(t *testing.T) {
	e, ctx, f := adminEnv(t)
	actx := adminCtx(t, e, ctx)
	gid, err := e.svc.CreateGroup(actx, "", "有买家的组", "")
	if err != nil {
		t.Fatal(err)
	}
	p := groupPrice(t, ctx, e, gid, "active", 10)
	var planID uuid.UUID
	if err := e.row(ctx, `SELECT plan_id FROM prices WHERE id = ?`, p).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `INSERT INTO gift_card_templates (name, type, plan_id, price_id) VALUES (?, 'plan', ?, ?)`, "ug-"+uuid.NewString()[:8], planID, p); err != nil {
		t.Fatal(err)
	}
	f.groups.usage[gid] = GroupUsage{Prices: 1}

	err = inSavepoint(t, ctx, e, "sp_sold", func() error { return e.svc.DeleteGroup(actx, gid) })
	if err == nil {
		t.Fatal("被买过的专属价应让删组失败")
	}
	if codeOf(err) != errs.Conflict {
		t.Fatalf("应回 409，得到 %v", err)
	}
	if !strings.Contains(errs.As(err).Message, "已经有人买过") {
		t.Fatalf("文案应含「已经有人买过」：%v", err)
	}
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM user_groups WHERE id = ?`, gid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("组应还在 n=%d err=%v", n, err)
	}
	if err := e.row(ctx, `SELECT count(*) FROM prices WHERE id = ?`, p).Scan(&n); err != nil || n != 1 {
		t.Fatalf("价格应还在 n=%d err=%v", n, err)
	}
}

// 价格冻结守卫：组还在时直接 DELETE prices 照旧被拒（23001），公开价（user_group_id 为空）同样不放行。
func TestPGDirectPriceDeleteStillGuarded(t *testing.T) {
	e, ctx, f := adminEnv(t)
	actx := adminCtx(t, e, ctx)
	gid, err := e.svc.CreateGroup(actx, "", "守卫仍在", "")
	if err != nil {
		t.Fatal(err)
	}
	p := groupPrice(t, ctx, e, gid, "active", 10)
	f.groups.usage[gid] = GroupUsage{Prices: 1}

	err = inSavepoint(t, ctx, e, "sp_del", func() error { return e.exec(ctx, `DELETE FROM prices WHERE id = ?`, p) })
	if testkit.SQLState(err) != "23001" {
		t.Fatalf("组还在时直接删专属价应 23001，得到 %v", err)
	}
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM prices WHERE id = ?`, p).Scan(&n); err != nil || n != 1 {
		t.Fatalf("价格应还在 n=%d err=%v", n, err)
	}

	// 公开价：user_group_id 为空，守卫永不放行
	var publicPlan uuid.UUID
	if err := e.row(ctx, `INSERT INTO plans (code, name) VALUES (?, ?) RETURNING id`, "ug-"+uuid.NewString()[:8], "公开套餐").Scan(&publicPlan); err != nil {
		t.Fatal(err)
	}
	var publicPrice uuid.UUID
	if err := e.row(ctx, `INSERT INTO prices (plan_id, unit_amount) VALUES (?, 5) RETURNING id`, publicPlan).Scan(&publicPrice); err != nil {
		t.Fatal(err)
	}
	err = inSavepoint(t, ctx, e, "sp_del_pub", func() error { return e.exec(ctx, `DELETE FROM prices WHERE id = ?`, publicPrice) })
	if testkit.SQLState(err) != "23001" {
		t.Fatalf("直接删公开价应 23001，得到 %v", err)
	}
}

// SetGroupStatus：停用、幂等（重复停用照常成功、各记一条审计）、启用回来、不存在回 404。
func TestPGGroupStatusToggle(t *testing.T) {
	e, ctx, _ := adminEnv(t)
	actx := adminCtx(t, e, ctx)
	gid, err := e.svc.CreateGroup(actx, "", "可停用的组", "")
	if err != nil {
		t.Fatal(err)
	}
	statusOf := func() string {
		groups, err := e.svc.AdminListGroups(actx)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range groups {
			if g.ID == gid {
				return g.Status
			}
		}
		t.Fatal("列表里找不到组")
		return ""
	}
	if got := statusOf(); got != GroupActive {
		t.Fatalf("新组 status=%s want active", got)
	}
	if err := e.svc.SetGroupStatus(actx, gid, true); err != nil {
		t.Fatalf("停用：%v", err)
	}
	if got := statusOf(); got != GroupDisabled {
		t.Fatalf("停用后 status=%s want disabled", got)
	}
	// 幂等：已是停用再停用一次照样成功，审计各记一条
	if err := e.svc.SetGroupStatus(actx, gid, true); err != nil {
		t.Fatalf("重复停用应幂等：%v", err)
	}
	if e.auditCount(ctx, ActionUserGroupStatusChanged, gid) != 2 {
		t.Fatal("审计应记两条 status_changed")
	}
	if err := e.svc.SetGroupStatus(actx, uuid.New(), true); codeOf(err) != errs.NotFound {
		t.Fatalf("组不存在应 404，得到 %v", err)
	}
	if err := e.svc.SetGroupStatus(actx, gid, false); err != nil {
		t.Fatalf("启用：%v", err)
	}
	if got := statusOf(); got != GroupActive {
		t.Fatalf("启用后 status=%s want active", got)
	}
}

// AssignGroup：停用的组不收新成员（409）；本来就在组里再提交同一个组不算新成员，不报错。
func TestPGAssignDisabledGroup(t *testing.T) {
	e, ctx, _ := adminEnv(t)
	actx := adminCtx(t, e, ctx)
	gid, err := e.svc.CreateGroup(actx, "", "停用收人", "")
	if err != nil {
		t.Fatal(err)
	}
	a := e.mkUser(t, ctx, uniqEmail(t, "ugA"), "Passw0rdLong1")
	b := e.mkUser(t, ctx, uniqEmail(t, "ugB"), "Passw0rdLong1")
	if err := e.svc.AssignGroup(actx, a.ID, &gid); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetGroupStatus(actx, gid, true); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AssignGroup(actx, b.ID, &gid); codeOf(err) != errs.Conflict {
		t.Fatalf("停用组收新成员应 409，得到 %v", err)
	}
	// a 已在组里，再提交同一个组是幂等操作，不拦
	if err := e.svc.AssignGroup(actx, a.ID, &gid); err != nil {
		t.Fatalf("组内成员再提交同组不应报错：%v", err)
	}
	if err := e.svc.AssignGroup(actx, a.ID, nil); err != nil {
		t.Fatalf("移出组：%v", err)
	}
	if err := e.svc.SetGroupStatus(actx, gid, false); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AssignGroup(actx, b.ID, &gid); err != nil {
		t.Fatalf("启用后应能加入：%v", err)
	}
}

// 批量生成：组已停用时提交任务回 422，fields.group_id = 「分组已停用」。
func TestPGGenerationRejectsDisabledGroup(t *testing.T) {
	e, ctx, _ := opsEnv(t)
	actx := asAdmin(ctx, uuid.New())
	gid, err := e.svc.CreateGroup(actx, "", "批量停用", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetGroupStatus(actx, gid, true); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.SubmitGeneration(actx, GenerateInput{
		Count: 2, EmailPrefix: "ugd", EmailDomain: "example.test", GroupID: &gid, Reason: "压测账号批量准备",
	})
	if codeOf(err) != errs.ValidationFailed {
		t.Fatalf("停用组提交批量生成应 422，得到 %v", err)
	}
	if fieldsOf(err)["group_id"] != "分组已停用" {
		t.Fatalf("fields.group_id=%q want 分组已停用（err=%v）", fieldsOf(err)["group_id"], err)
	}
}

// 批量生成执行时复查：提交后组被停用，任务失败、Error 含「已停用」、一个账号都没生成。
func TestPGGenerationStopsWhenGroupDisabled(t *testing.T) {
	e, ctx, _ := opsEnv(t)
	actx := asAdmin(ctx, uuid.New())
	gid, err := e.svc.CreateGroup(actx, "", "批量中断", "")
	if err != nil {
		t.Fatal(err)
	}
	job, err := e.svc.SubmitGeneration(actx, GenerateInput{
		Count: 2, EmailPrefix: "ugd", EmailDomain: "example.test", GroupID: &gid, Reason: "压测账号批量准备",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetGroupStatus(actx, gid, true); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RunGenerationJob(ctx, job.ID); err != nil {
		t.Fatalf("RunGenerationJob 返回 nil（失败记在任务上）：%v", err)
	}
	got, err := e.svc.GetGenerationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != JobFailed {
		t.Fatalf("status=%s want failed", got.Status)
	}
	if got.Error == nil || !strings.Contains(*got.Error, "已停用") {
		t.Fatalf("Error 应含「已停用」：%v", got.Error)
	}
	if got.Completed != 0 {
		t.Fatalf("completed=%d want 0", got.Completed)
	}
}
