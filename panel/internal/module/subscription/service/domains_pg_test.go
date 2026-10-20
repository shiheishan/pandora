package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

func seedUserGroup(t *testing.T, e *env, ctx context.Context, code string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO user_groups (code, name) VALUES (?, ?) RETURNING id`, code, "组 "+code).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAdminDomainConfigEmptyPG(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx(e.txCtx(), uuid.New())
	view, err := e.svc.AdminDomainConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Groups == nil || len(view.Groups) != 0 {
		t.Fatalf("Groups 应为空切片 %+v", view.Groups)
	}
	if view.ScoresAvailable {
		t.Fatal("未注入 Redis 时 ScoresAvailable 应为假")
	}
}

func TestAdminSaveDomainConfigPG(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx(e.txCtx(), uuid.New())
	G1 := seedUserGroup(t, e, ctx, "g1")
	G2 := seedUserGroup(t, e, ctx, "g2")

	in := []DomainGroupInput{
		{
			Name: "缺省", IsDefault: true,
			Domains: []Domain{
				{BaseURL: "https://SUB-A.example.test/", Weight: 0},
				{BaseURL: "https://b.example.test", Weight: 2},
			},
		},
		{
			Name: "专线", IsDefault: false,
			Domains:      []Domain{{BaseURL: "https://vip.example.test", Weight: 1}},
			UserGroupIDs: []uuid.UUID{G1, G2},
		},
	}
	view, err := e.svc.AdminSaveDomainConfig(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Groups) != 2 || !view.Groups[0].IsDefault || view.Groups[0].Domains[0].BaseURL != "https://sub-a.example.test" {
		t.Fatalf("保存结果 %+v", view.Groups)
	}
	if view.Groups[0].Domains[0].Weight != 1 || view.Groups[0].Domains[0].Score != 1 || view.Groups[0].Domains[0].EffectiveWeight != 1 {
		t.Fatalf("权重 0→1 与分数 %+v", view.Groups[0].Domains[0])
	}
	if len(view.Groups[1].UserGroupIDs) != 2 || view.Groups[1].UserGroupIDs[0] != G1 || view.Groups[1].UserGroupIDs[1] != G2 {
		t.Fatalf("UserGroupIDs 升序 %v", view.Groups[1].UserGroupIDs)
	}

	view2, err := e.svc.AdminDomainConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(view2.Groups) != 2 || view2.Groups[0].Name != view.Groups[0].Name {
		t.Fatalf("再读 %+v", view2)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ?`, ActionDomainsChanged); n != 1 {
		t.Fatalf("审计 +1 实际 %d", n)
	}

	_, err = e.svc.AdminSaveDomainConfig(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ?`, ActionDomainsChanged); n != 1 {
		t.Fatalf("原样再存审计不变 %d", n)
	}

	in[0].Domains[1].Weight = 3
	_, err = e.svc.AdminSaveDomainConfig(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ?`, ActionDomainsChanged); n != 2 {
		t.Fatalf("改权重再存 +1 实际 %d", n)
	}
}

func TestAdminSaveDomainConfigErrorsPG(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx(e.txCtx(), uuid.New())
	G1 := seedUserGroup(t, e, ctx, "ok")
	base := []DomainGroupInput{{
		Name: "缺省", IsDefault: true,
		Domains:      []Domain{{BaseURL: "https://a.example.test", Weight: 1}},
		UserGroupIDs: []uuid.UUID{G1},
	}}
	_, err := e.svc.AdminSaveDomainConfig(ctx, base)
	if err != nil {
		t.Fatal(err)
	}

	var ve *errs.Error
	user := newUser(t, e, ctx)
	_, err = e.svc.AdminSaveDomainConfig(userCtx(ctx, user), base)
	if !errors.As(err, &ve) || ve.Code != errs.Forbidden {
		t.Fatalf("非管理员: %v", err)
	}

	// 外键冲突放最后：PG 报错后测试事务已中止（tx.Do 加入外层事务、不开保存点），之后不能再查库；
	// 生产上整个保存事务回滚，原配置不变
	badUG := uuid.New()
	bad := []DomainGroupInput{{
		Name: "缺省", IsDefault: true,
		Domains:      []Domain{{BaseURL: "https://a.example.test", Weight: 1}},
		UserGroupIDs: []uuid.UUID{badUG},
	}}
	_, err = e.svc.AdminSaveDomainConfig(ctx, bad)
	if !errors.As(err, &ve) || ve.Code != errs.ValidationFailed || ve.Fields["groups"] != "有用户组不存在（可能刚被删除），刷新后重试" {
		t.Fatalf("外键: %v", err)
	}
}

func TestDomainAssignmentCascadePG(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx(e.txCtx(), uuid.New())
	G1 := seedUserGroup(t, e, ctx, "cascade")
	_, err := e.svc.AdminSaveDomainConfig(ctx, []DomainGroupInput{
		{Name: "缺省", IsDefault: true, Domains: []Domain{{BaseURL: "https://a.example.test", Weight: 1}}},
		{Name: "专线", Domains: []Domain{{BaseURL: "https://b.example.test", Weight: 1}}, UserGroupIDs: []uuid.UUID{G1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.exec(t, ctx, `DELETE FROM user_groups WHERE id = ?`, G1)
	view, err := e.svc.AdminDomainConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Groups) != 2 {
		t.Fatalf("组应仍在 %+v", view.Groups)
	}
	for _, g := range view.Groups {
		if g.Name == "专线" && len(g.UserGroupIDs) != 0 {
			t.Fatalf("专线组 UserGroupIDs 应为空 %+v", g)
		}
	}
}

func TestAdminDomainConfigScoresPG(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx(e.txCtx(), uuid.New())
	_, rdb := testkit.Redis(t)
	e.svc.UseDomainScores(rdb)
	_, err := e.svc.AdminSaveDomainConfig(ctx, []DomainGroupInput{{
		Name: "缺省", IsDefault: true,
		Domains: []Domain{{BaseURL: "https://a.example.test", Weight: 2}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := e.svc.scores.Apply(ctx, map[string]map[string]float64{"https://a.example.test": {domainScoreFieldAll: 0.5}}, now); err != nil {
		t.Fatal(err)
	}
	view, err := e.svc.AdminDomainConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !view.ScoresAvailable {
		t.Fatal("ScoresAvailable 应为真")
	}
	d := view.Groups[0].Domains[0]
	if math.Abs(d.Score-1.5) > 0.01 || math.Abs(d.EffectiveWeight-3) > 0.01 {
		t.Fatalf("score/weight %+v", d)
	}
}
