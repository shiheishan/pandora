package service

import (
	"context"
	"testing"
	"time"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// annEnv 是公告 PG 用例的环境：固定时钟、管理员主体、三个假端口。
type annEnv struct {
	*pgEnv
	dir   *fakeDir
	plans *fakePlans
	subs  *fakeSubs
	admin context.Context
	now   time.Time
}

func annSetup(t *testing.T) *annEnv {
	t.Helper()
	e := newPG(t)
	dir, plans, subs := e.usePorts()
	adminID := e.insertTestUser(t, dir)
	a := &annEnv{
		pgEnv: e, dir: dir, plans: plans, subs: subs,
		admin: authn.With(e.ctx, authn.Principal{UserID: adminID, Audience: authn.AudienceAdmin}),
		now:   time.Now().UTC().Truncate(time.Microsecond),
	}
	e.svc.now = func() time.Time { return a.now }
	return a
}

func (a *annEnv) annSave(t *testing.T, in AnnouncementInput) AnnouncementSaved {
	t.Helper()
	saved, err := a.svc.SaveAnnouncement(a.admin, in)
	if err != nil {
		t.Fatalf("save %+v: %v", in, err)
	}
	return saved
}

func annInput(title string) AnnouncementInput {
	return AnnouncementInput{Title: title, Body: "正文内容", Severity: "info"}
}

func annAuditCount(e *pgEnv, action string) int {
	n := 0
	for _, ev := range e.audit.events {
		if ev.Action == action {
			n++
		}
	}
	return n
}

func TestAnnouncementLifecyclePG(t *testing.T) {
	a := annSetup(t)
	planID := uuid.New()
	a.plans.plans = []catalogsvc.Plan{{ID: planID, Name: "旗舰", Status: catalogsvc.PlanActive}}

	in := annInput("维护通知")
	in.PlanIDs = []uuid.UUID{planID}
	draft := a.annSave(t, in)
	if draft.Status != model.AnnouncementDraft || draft.Version != 1 {
		t.Fatalf("draft %+v", draft)
	}

	id := draft.ID
	edit := annInput("维护通知（更新）")
	// 保存是整份替换（与前端表单一致），定向套餐要随表单一起带上
	edit.ID, edit.ExpectedVersion, edit.Publish, edit.PlanIDs = &id, 1, true, []uuid.UUID{planID}
	pub := a.annSave(t, edit)
	if pub.Status != model.AnnouncementPublished || pub.Version != 2 {
		t.Fatalf("publish %+v", pub)
	}

	// 旧版本再保存：版本冲突
	if _, err := a.svc.SaveAnnouncement(a.admin, edit); !isCode(err, errs.Conflict) {
		t.Fatalf("stale version: %v", err)
	}
	// 已发布改回草稿：不允许
	back := annInput("维护通知")
	back.ID, back.ExpectedVersion = &id, 2
	if _, err := a.svc.SaveAnnouncement(a.admin, back); !isCode(err, errs.Conflict) {
		t.Fatalf("published -> draft: %v", err)
	}

	ver, err := a.svc.WithdrawAnnouncement(a.admin, id, 2)
	if err != nil || ver != 3 {
		t.Fatalf("withdraw %d %v", ver, err)
	}
	if _, err := a.svc.WithdrawAnnouncement(a.admin, id, 3); !isCode(err, errs.Conflict) {
		t.Fatalf("withdraw twice: %v", err)
	}
	again := annInput("撤回后再改")
	again.ID, again.ExpectedVersion, again.Publish = &id, 3, true
	if _, err := a.svc.SaveAnnouncement(a.admin, again); !isCode(err, errs.Conflict) {
		t.Fatalf("edit withdrawn: %v", err)
	}
	if _, err := a.svc.WithdrawAnnouncement(a.admin, uuid.New(), 1); !isCode(err, errs.NotFound) {
		t.Fatalf("withdraw missing: %v", err)
	}

	list, err := a.svc.ListAnnouncements(a.admin)
	if err != nil || len(list.Announcements) != 1 {
		t.Fatalf("list %+v %v", list, err)
	}
	got := list.Announcements[0]
	if got.Status != model.AnnouncementWithdrawn || got.Version != 3 || got.Title != "维护通知（更新）" {
		t.Fatalf("row %+v", got)
	}
	if len(got.PlanTargets) != 1 || got.PlanTargets[0].Name != "旗舰" || got.GroupTargets == nil {
		t.Fatalf("targets %+v", got)
	}
	if annAuditCount(a.pgEnv, "announcement.saved") != 2 || annAuditCount(a.pgEnv, "announcement.withdrawn") != 1 {
		t.Fatalf("audit %+v", a.audit.events)
	}
}

func TestAnnouncementTargetsMustExistPG(t *testing.T) {
	a := annSetup(t)
	in := annInput("定向公告")
	in.PlanIDs = []uuid.UUID{uuid.New()}
	_, err := a.svc.SaveAnnouncement(a.admin, in)
	if f := fieldsOf(t, err); f["target_plan_ids"] == "" {
		t.Fatalf("missing plan: %v", err)
	}
	in = annInput("定向公告")
	in.GroupIDs = []uuid.UUID{uuid.New()}
	_, err = a.svc.SaveAnnouncement(a.admin, in)
	if f := fieldsOf(t, err); f["target_user_group_ids"] == "" {
		t.Fatalf("missing group: %v", err)
	}
	// 没有登录主体不能新建
	if _, err := a.svc.SaveAnnouncement(a.ctx, annInput("匿名")); !isCode(err, errs.Unauthorized) {
		t.Fatalf("anonymous: %v", err)
	}
}

func TestAnnouncementScheduledPublishPG(t *testing.T) {
	a := annSetup(t)
	user := a.insertTestUser(t, a.dir)

	in := annInput("定时公告")
	at := a.now.Add(time.Hour)
	in.Publish, in.PublishAt = true, &at
	saved := a.annSave(t, in)
	if saved.Status != model.AnnouncementScheduled {
		t.Fatalf("scheduled %+v", saved)
	}
	mine, err := a.svc.MyAnnouncements(a.ctx, user)
	if err != nil || len(mine) != 0 {
		t.Fatalf("before publish %+v %v", mine, err)
	}

	a.now = a.now.Add(2 * time.Hour)
	n, err := a.svc.PublishDueAnnouncements(a.ctx)
	if err != nil || n < 1 {
		t.Fatalf("publish due %d %v", n, err)
	}
	if annAuditCount(a.pgEnv, "announcement.published") < 1 {
		t.Fatalf("audit %+v", a.audit.events)
	}
	mine, err = a.svc.MyAnnouncements(a.ctx, user)
	if err != nil || len(mine) != 1 || mine[0].ID != saved.ID {
		t.Fatalf("after publish %+v %v", mine, err)
	}
}

func TestAnnouncementPortalVisibilityPG(t *testing.T) {
	a := annSetup(t)
	livePlan, otherPlan := uuid.New(), uuid.New()
	myGroup, otherGroup := uuid.New(), uuid.New()
	a.plans.plans = []catalogsvc.Plan{
		{ID: livePlan, Name: "在用", Status: catalogsvc.PlanActive},
		{ID: otherPlan, Name: "别的", Status: catalogsvc.PlanActive},
	}
	a.dir.groups = []identitysvc.Group{{ID: myGroup, Name: "甲组"}, {ID: otherGroup, Name: "乙组"}}
	user := a.insertTestUser(t, a.dir)
	a.dir.userGroup[user] = &myGroup
	a.subs.live[user] = []uuid.UUID{livePlan}

	publish := func(title string, pinned bool, plans, groups []uuid.UUID) uuid.UUID {
		in := annInput(title)
		in.Publish, in.Pinned, in.PlanIDs, in.GroupIDs = true, pinned, plans, groups
		return a.annSave(t, in).ID
	}

	// 已过期的：在两小时前发布、一小时前下线
	base := a.now
	a.now = base.Add(-2 * time.Hour)
	expiresAt := base.Add(-time.Hour)
	expired := annInput("已过期")
	expired.Publish, expired.ExpiresAt = true, &expiresAt
	expiredID := a.annSave(t, expired).ID
	a.now = base

	global := publish("全员公告", false, nil, nil)
	pinned := publish("置顶公告", true, nil, nil)
	hit := publish("命中组与套餐", false, []uuid.UUID{livePlan}, []uuid.UUID{myGroup})
	missPlan := publish("别的套餐", false, []uuid.UUID{otherPlan}, nil)
	missGroup := publish("别的组", false, nil, []uuid.UUID{otherGroup})
	draft := a.annSave(t, annInput("草稿")).ID

	mine, err := a.svc.MyAnnouncements(a.ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uuid.UUID]bool{}
	for _, m := range mine {
		seen[m.ID] = true
	}
	for _, id := range []uuid.UUID{global, pinned, hit} {
		if !seen[id] {
			t.Fatalf("应可见 %s，得 %+v", id, mine)
		}
	}
	for _, id := range []uuid.UUID{expiredID, missPlan, missGroup, draft} {
		if seen[id] {
			t.Fatalf("不应可见 %s，得 %+v", id, mine)
		}
	}
	if len(mine) != 3 || mine[0].ID != pinned {
		t.Fatalf("置顶应排第一：%+v", mine)
	}
}
