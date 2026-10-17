package service

// 覆盖 00050_user_group_status.sql 与随之而来的行为改动：
//   - DeleteGroup：专属价不再拦删组，没被买过的随组级联删除（审计记 prices_deleted），
//     被礼品卡等引用过的价格让级联撞 RESTRICT 外键，删组回 409；
//   - 价格冻结守卫 catalog_guard_price：组还在时直接 DELETE prices 照旧 23001，只有删组的级联放行；
//   - SetGroupStatus：停用 / 启用、幂等、审计 user_group.status_changed、不存在 404；
//   - AssignGroup：停用组不收新成员（已在组内再提交同组不拦）；
//   - SubmitGeneration / RunGenerationJob：组停用时入队 422、执行时任务失败。

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

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
