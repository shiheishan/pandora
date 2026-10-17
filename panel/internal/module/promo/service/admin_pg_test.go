package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/promo/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestCreateAndList(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)

	raw := " vip-x" + randHex(8)
	spec := fixedSpec("2.50")
	id, code, err := e.svc.CreateCoupon(actx, raw, spec)
	if err != nil {
		t.Fatal(err)
	}
	wantCode := strings.ToUpper(strings.TrimSpace(raw))
	if code != wantCode {
		t.Fatalf("返回码 %q，期望规范化 %q", code, wantCode)
	}

	_, _, err = e.svc.CreateCoupon(actx, code, fixedSpec("1.00"))
	if !errors.Is(err, ErrCodeExists) {
		t.Fatalf("重复码: %v", err)
	}

	badPlan := uuid.New()
	ps := fixedSpec("1.00")
	ps.PlanIDs = []uuid.UUID{badPlan}
	_, _, err = e.svc.CreateCoupon(actx, randCode("PLN"), ps)
	if err == nil || errs.As(err).Fields["applicable_plan_ids"] == "" {
		t.Fatalf("不存在套餐: %v", err)
	}

	badGrp := uuid.New()
	gs := fixedSpec("1.00")
	gs.UserGroupID = &badGrp
	execSQL(t, e, ctx, "SAVEPOINT sp")
	_, _, err = e.svc.CreateCoupon(actx, randCode("GRP"), gs)
	if err == nil || errs.As(err).Fields["user_group_id"] == "" {
		t.Fatalf("不存在用户组: %v", err)
	}
	execSQL(t, e, ctx, "ROLLBACK TO SAVEPOINT sp")

	q := strings.ToLower(code[1:5])
	rows, total, err := e.svc.ListCoupons(ctx, ListFilter{Query: q}, 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total < 1 {
		t.Fatalf("total=%d", total)
	}
	var found *Coupon
	for i := range rows {
		if rows[i].ID == id {
			found = &rows[i]
			break
		}
	}
	if found == nil {
		t.Fatal("列表未找到新建的券")
	}
	if found.Code != code || found.MaxPerUser != 1 {
		t.Fatalf("列表字段 code=%s maxPerUser=%d", found.Code, found.MaxPerUser)
	}
	if found.Face.Kind != KindFixed || !found.Face.AmountOff.Equal(decimal.RequireFromString("2.50")) {
		t.Fatalf("Face %v", found.Face)
	}

	paused, _, err := e.svc.ListCoupons(ctx, ListFilter{Query: q, Status: StatusPaused}, 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range paused {
		if c.ID == id {
			t.Fatal("paused 筛选不应命中 active 券")
		}
	}

	var createdBy *uuid.UUID
	if err := e.row(ctx, `SELECT created_by FROM coupons WHERE id = ?`, id).Scan(&createdBy); err != nil {
		t.Fatal(err)
	}
	if createdBy == nil || *createdBy != admin {
		t.Fatalf("created_by=%v admin=%v", createdBy, admin)
	}

	var auditN int64
	if err := e.row(ctx,
		`SELECT count(*) FROM audit_events WHERE action = ?`, ActionCreated,
	).Scan(&auditN); err != nil {
		t.Fatal(err)
	}
	if auditN < 1 {
		t.Fatalf("审计条数 %d", auditN)
	}

	P1, P2 := newPlan(t, e, ctx), newPlan(t, e, ctx)
	planSpec := fixedSpec("1.00")
	planSpec.PlanIDs = []uuid.UUID{P1, P2, P1}
	planCode := randCode("PL2")
	planID, _, err := e.svc.CreateCoupon(actx, planCode, planSpec)
	if err != nil {
		t.Fatal(err)
	}
	rows2, _, err := e.svc.ListCoupons(ctx, ListFilter{Query: planCode}, 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	var planCoupon *Coupon
	for i := range rows2 {
		if rows2[i].ID == planID {
			planCoupon = &rows2[i]
			break
		}
	}
	if planCoupon == nil {
		t.Fatal("列表未找到限定套餐券")
	}
	if len(planCoupon.PlanIDs) != 2 {
		t.Fatalf("PlanIDs 长度 %d，期望 2", len(planCoupon.PlanIDs))
	}
	got := map[uuid.UUID]bool{planCoupon.PlanIDs[0]: true, planCoupon.PlanIDs[1]: true}
	if !got[P1] || !got[P2] {
		t.Fatalf("PlanIDs=%v 期望含 %v %v", planCoupon.PlanIDs, P1, P2)
	}
	var planRows int64
	if err := e.row(ctx, `SELECT count(*) FROM coupon_plans WHERE coupon_id = ?`, planID).Scan(&planRows); err != nil {
		t.Fatal(err)
	}
	if planRows != 2 {
		t.Fatalf("coupon_plans 行数 %d，期望 2", planRows)
	}
}

func TestDiscountedTotalAndRedemptions(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)

	code := randCode("DSC")
	createCoupon(t, e, ctx, admin, code, fixedSpec("2.50"))

	u1 := newUser(t, e, ctx)
	u2 := newUser(t, e, ctx)
	o1 := newOrder(t, e, ctx, u1)
	o2 := newOrder(t, e, ctx, u2)

	if _, err := e.svc.Reserve(ctx, code, target(u1, "10.00"), o1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Reserve(ctx, code, target(u2, "10.00"), o2); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Release(ctx, o1.ID, ""); err != nil {
		t.Fatal(err)
	}

	rows, _, err := e.svc.ListCoupons(ctx, ListFilter{Query: code}, 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("未列出券")
	}
	wantTotal := decimal.RequireFromString("2.50")
	if !rows[0].DiscountedTotal.Equal(wantTotal) {
		t.Fatalf("DiscountedTotal=%s 期望 2.50", rows[0].DiscountedTotal)
	}

	reds, err := e.svc.Redemptions(ctx, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reds) != 2 {
		t.Fatalf("核销条数 %d", len(reds))
	}
	if reds[1].At.After(reds[0].At) {
		t.Fatal("核销未按时间倒序")
	}
	var releasedN, heldN int
	for _, r := range reds {
		if r.Released {
			releasedN++
		} else {
			heldN++
		}
		wantNo := o2.No
		if r.Released {
			wantNo = o1.No
		}
		if r.OrderNo != wantNo {
			t.Fatalf("OrderNo=%s 期望 %s released=%v", r.OrderNo, wantNo, r.Released)
		}
		em := e.users.emails[r.UserID]
		if r.Email != em {
			t.Fatalf("Email=%s 期望 %s", r.Email, em)
		}
	}
	if releasedN != 1 || heldN != 1 {
		t.Fatalf("Released=%d held=%d", releasedN, heldN)
	}
}

func TestGenerateCoupons(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)

	max := 1
	spec := fixedSpec("1.00")
	spec.MaxRedemptions = &max
	spec.Name = "批量活动"

	codes, err := e.svc.GenerateCoupons(actx, "t", 50, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 50 {
		t.Fatalf("生成数量 %d", len(codes))
	}
	seen := make(map[string]struct{}, 50)
	for _, c := range codes {
		if len(c) != 11 || !strings.HasPrefix(c, "T") {
			t.Fatalf("码 %q 长度或前缀不对", c)
		}
		if _, ok := seen[c]; ok {
			t.Fatalf("重复码 %s", c)
		}
		seen[c] = struct{}{}
	}

	var dbN int64
	if err := e.row(ctx,
		`SELECT count(*) FROM coupons WHERE code IN ? AND max_redemptions = 1`, codes,
	).Scan(&dbN); err != nil {
		t.Fatal(err)
	}
	if dbN != 50 {
		t.Fatalf("库里张数 %d", dbN)
	}

	_, err = e.svc.GenerateCoupons(actx, "t", 0, spec)
	if err == nil || errs.As(err).Fields["count"] == "" {
		t.Fatalf("count=0: %v", err)
	}
	_, err = e.svc.GenerateCoupons(actx, "t", 1001, spec)
	if err == nil || errs.As(err).Fields["count"] == "" {
		t.Fatalf("count=1001: %v", err)
	}

	badName := spec
	badName.Name = ""
	_, err = e.svc.GenerateCoupons(actx, "t", 1, badName)
	if err == nil || errs.As(err).Fields["name"] == "" {
		t.Fatalf("空名称: %v", err)
	}

	var dataJSON string
	if err := e.row(ctx,
		`SELECT data::text FROM audit_events WHERE action = ? ORDER BY occurred_at DESC LIMIT 1`, ActionBatchGenerated,
	).Scan(&dataJSON); err != nil {
		t.Fatal(err)
	}
	for _, c := range codes {
		if strings.Contains(dataJSON, c) {
			t.Fatalf("审计 data 含券码 %s", c)
		}
	}
}

// 批量生成 300 张带两个限定套餐的券：张数、去重与 coupon_plans 行数都对；已存在的券码由 ON CONFLICT 跳过。
func TestGenerateCouponsBatchWithPlansPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)

	p1 := newPlan(t, e, ctx)
	p2 := newPlan(t, e, ctx)
	spec := fixedSpec("1.00")
	spec.Name = "批量带套餐"
	spec.PlanIDs = []uuid.UUID{p1, p2}

	codes, err := e.svc.GenerateCoupons(actx, "BP", 300, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 300 {
		t.Fatalf("生成数量 %d，期望 300", len(codes))
	}
	seen := make(map[string]struct{}, 300)
	for _, c := range codes {
		if !strings.HasPrefix(c, "BP") {
			t.Fatalf("码 %q 前缀不对", c)
		}
		if _, ok := seen[c]; ok {
			t.Fatalf("重复码 %s", c)
		}
		seen[c] = struct{}{}
	}

	var dbN int64
	if err := e.row(ctx, `SELECT count(*) FROM coupons WHERE code IN ?`, codes).Scan(&dbN); err != nil {
		t.Fatal(err)
	}
	if dbN != 300 {
		t.Fatalf("库里张数 %d，期望 300", dbN)
	}

	var planRows int64
	if err := e.row(ctx, `
		SELECT count(*) FROM coupon_plans cp
		JOIN coupons c ON c.id = cp.coupon_id
		WHERE c.code IN ?`, codes).Scan(&planRows); err != nil {
		t.Fatal(err)
	}
	wantPlanRows := int64(300 * len(spec.PlanIDs))
	if planRows != wantPlanRows {
		t.Fatalf("coupon_plans 行数 %d，期望 %d", planRows, wantPlanRows)
	}

	// 撞码：已存在的码跳过，只插入新码
	dupSpec := fixedSpec("1.00")
	dupSpec.Name = "撞码测试"
	_, _, err = e.svc.CreateCoupon(actx, "DUPX0000000", dupSpec)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := couponFromSpec("", dupSpec)
	tmpl.CreatedBy = &admin
	inserted, err := e.svc.repo.InsertCouponsIfAbsent(ctx, &tmpl, []string{"DUPX0000000", "NEWX0000001"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inserted) != 1 || inserted[0] != "NEWX0000001" {
		t.Fatalf("撞码后插入 %v，期望仅 [NEWX0000001]", inserted)
	}
}

// 限定套餐在插入 coupon_plans 时不存在时，repo 应返回 ErrPlanGone（事务已作废，勿在同一 ctx 再查库）。
func TestInsertCouponsPlanGonePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()

	tmpl := couponFromSpec("", fixedSpec("1.00"))
	tmpl.Name = "套餐已删"
	_, err := e.svc.repo.InsertCouponsIfAbsent(ctx, &tmpl, []string{"PGONE000001"}, []uuid.UUID{uuid.New()})
	if !errors.Is(err, repo.ErrPlanGone) {
		t.Fatalf("期望 ErrPlanGone，得 %v", err)
	}
}

func TestSetStatus(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)

	code := randCode("STA")
	id, _, err := e.svc.CreateCoupon(actx, code, fixedSpec("1.00"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetStatus(actx, id, StatusPaused); err != nil {
		t.Fatal(err)
	}
	var st string
	if err := e.row(ctx, `SELECT status FROM coupons WHERE id = ?`, id).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != StatusPaused {
		t.Fatalf("status=%s", st)
	}

	if err := e.svc.SetStatus(actx, uuid.New(), StatusPaused); errCode(err) != errs.NotFound {
		t.Fatalf("不存在券: %v", err)
	}
	if err := e.svc.SetStatus(actx, id, "expired"); err == nil || errs.As(err).Fields["status"] == "" {
		t.Fatalf("非法 status: %v", err)
	}
}

func TestConstraints(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()

	assertSQLFails(t, e, ctx, `INSERT INTO coupons (code, name, amount_off, percent_off_bp)
		VALUES ('X1', 'n', 1, 1)`)
	assertSQLFails(t, e, ctx, `INSERT INTO coupons (code, name) VALUES ('X2', 'n')`)
	assertSQLFails(t, e, ctx, `INSERT INTO coupons (code, name, percent_off_bp)
		VALUES ('X3', 'n', 10001)`)
	assertSQLFails(t, e, ctx, `INSERT INTO coupons (code, name, amount_off)
		VALUES ('ÄB', 'n', 1)`)
	assertSQLFails(t, e, ctx, `INSERT INTO coupons (code, name, amount_off)
		VALUES ('A B', 'n', 1)`)
	assertSQLFails(t, e, ctx, `INSERT INTO coupons (code, name, amount_off, max_discount)
		VALUES ('X5', 'n', 1, 10)`)
	assertSQLFails(t, e, ctx, `INSERT INTO coupons (code, name, amount_off)
		VALUES ('low', 'n', 1)`)

	code := randCode("CAP")
	execSQL(t, e, ctx, `INSERT INTO coupons (code, name, amount_off, max_redemptions)
		VALUES (?, 'n', 1, 1)`, code)
	assertSQLFails(t, e, ctx, `UPDATE coupons SET reserved_count = 2 WHERE code = ?`, code)

	var cid uuid.UUID
	if err := e.row(ctx, `SELECT id FROM coupons WHERE code = ?`, code).Scan(&cid); err != nil {
		t.Fatal(err)
	}
	uid := newUser(t, e, ctx)
	ord1 := newOrder(t, e, ctx, uid)
	ord2 := newOrder(t, e, ctx, uid)
	execSQL(t, e, ctx, `INSERT INTO coupon_redemptions (coupon_id, user_id, order_id, order_no, discount_amount, status)
		VALUES (?, ?, ?, ?, 1, 'held')`, cid, uid, ord1.ID, ord1.No)
	execSQL(t, e, ctx, `INSERT INTO coupon_redemptions (coupon_id, user_id, order_id, order_no, discount_amount, status)
		VALUES (?, ?, ?, ?, 1, 'held')`, cid, uid, ord2.ID, ord2.No)
	assertSQLFails(t, e, ctx, `DELETE FROM coupon_redemptions WHERE coupon_id = ?`, cid)

	okCode := "A-B_" + strings.ToUpper(randHex(8))
	execSQL(t, e, ctx, `INSERT INTO coupons (code, name, amount_off) VALUES (?, 'n', 1)`, okCode)

	planID := newPlan(t, e, ctx)
	execSQL(t, e, ctx, `INSERT INTO coupon_plans (coupon_id, plan_id) VALUES (?, ?)`, cid, planID)
	assertSQLFails(t, e, ctx, `INSERT INTO coupon_plans (coupon_id, plan_id) VALUES (?, ?)`, cid, planID)
}

func TestCreateRejectsBadCode(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)

	for _, code := range []string{"新年", "AB\x00", "A.B"} {
		_, _, err := e.svc.CreateCoupon(actx, code, fixedSpec("1.00"))
		if err == nil || errs.As(err).Fields["code"] == "" {
			t.Fatalf("CreateCoupon %q: %v", code, err)
		}
	}
	_, err := e.svc.GenerateCoupons(actx, "新", 1, fixedSpec("1.00"))
	if err == nil || errs.As(err).Fields["prefix"] == "" {
		t.Fatalf("非法前缀: %v", err)
	}
	codes, err := e.svc.GenerateCoupons(actx, "a-", 1, fixedSpec("1.00"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(codes[0], "A-") {
		t.Fatalf("码 %q 应以 A- 开头", codes[0])
	}
}

func TestListRejectsBadQuery(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()

	_, _, err := e.svc.ListCoupons(ctx, ListFilter{Query: "a\x00b"}, 10, 0)
	if err == nil || errs.As(err).Fields["q"] == "" {
		t.Fatalf("NUL 查询: %v", err)
	}
	_, _, err = e.svc.ListCoupons(ctx, ListFilter{Query: "\xff"}, 10, 0)
	if err == nil || errs.As(err).Fields["q"] == "" {
		t.Fatalf("非法 UTF-8: %v", err)
	}
	_, _, err = e.svc.ListCoupons(ctx, ListFilter{Query: "正常"}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
}

func assertSQLFails(t *testing.T, e *env, ctx context.Context, sql string, args ...any) {
	t.Helper()
	execSQL(t, e, ctx, "SAVEPOINT sp")
	err := e.exec(ctx, sql, args...)
	if err == nil {
		t.Fatalf("期望失败: %s", sql)
	}
	execSQL(t, e, ctx, "ROLLBACK TO SAVEPOINT sp")
}
