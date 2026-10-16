package service

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 生码：库里只有 HMAC 与掩码，批次密文里也看不出明文；一次性导出拿回整批明文、清空密文，第二次回 409。
func TestGenerateAndExportBatchOnce(t *testing.T) {
	e, ctx := newEnv(t)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("3")}})
	g, err := e.svc.GenerateCodes(e.asAdmin(ctx), GenerateInput{TemplateID: tpl.ID, Count: 6, Prefix: "vip"})
	if err != nil {
		t.Fatalf("生码: %v", err)
	}
	if g.Count != 6 || len(g.Sample) != generateSampleSize || g.Batch.Count != 6 || g.Batch.Prefix != "VIP" {
		t.Fatalf("生码结果 %+v", g)
	}
	for _, c := range g.Sample {
		if !strings.HasPrefix(c, "VIP") || len(c) != 3+codeRandomLen {
			t.Fatalf("卡密 %q 格式不对", c)
		}
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM gift_card_codes WHERE batch_id = ?`, g.BatchID); n != 6 {
		t.Fatalf("码 %d 张，期望 6", n)
	}

	// 明文不落库：码表里任何一列都不等于、也不包含明文
	for _, c := range g.Sample {
		if n := e.count(t, ctx, `SELECT count(*) FROM gift_card_codes
			WHERE code_mask = ? OR position(convert_to(?, 'UTF8') in code_hash) > 0 OR code_mask LIKE ?`,
			c, c, "%"+c[len(c)-maskedTail:]+"%"); n != 0 {
			t.Fatalf("码表里出现了明文 %s", MaskCode(c))
		}
		if st := e.codeStatus(t, ctx, c); st != CodeUnused {
			t.Fatalf("新码状态 %s", st)
		}
	}
	var sealed []byte
	if err := e.row(ctx, `SELECT codes_sealed FROM gift_card_batches WHERE id = ?`, g.BatchID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	for _, c := range g.Sample {
		if bytes.Contains(sealed, []byte(c)) {
			t.Fatal("批次密文里能直接看到明文")
		}
	}
	// 审计里也不能有明文
	for _, c := range g.Sample {
		if n := e.count(t, ctx, `SELECT count(*) FROM audit_events WHERE data::text LIKE ?`, "%"+c+"%"); n != 0 {
			t.Fatal("审计里出现了明文卡密")
		}
	}

	exp, err := e.svc.ExportBatch(e.asAdmin(ctx), g.BatchID)
	if err != nil {
		t.Fatalf("导出: %v", err)
	}
	if len(exp.Rows) != 6 || exp.Batch.ExportedAt == nil || exp.Batch.ExportedByEmail == nil {
		t.Fatalf("导出结果 rows=%d batch=%+v", len(exp.Rows), exp.Batch)
	}
	got := map[string]bool{}
	for _, r := range exp.Rows {
		got[r.Code] = true
		if r.Status != CodeUnused || r.TemplateName != tpl.Name {
			t.Fatalf("导出行 %+v", r)
		}
	}
	for _, c := range g.Sample {
		if !got[c] {
			t.Fatalf("导出里缺样例码 %s", MaskCode(c))
		}
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM gift_card_batches WHERE id = ? AND codes_sealed IS NULL AND exported_at IS NOT NULL`, g.BatchID); n != 1 {
		t.Fatal("导出后批次应清空密文并记导出时间")
	}

	_, err = e.svc.ExportBatch(e.asAdmin(ctx), g.BatchID)
	if !errors.Is(err, ErrBatchAlreadyExported) {
		t.Fatalf("第二次导出应回 ErrBatchAlreadyExported，得到 %v", err)
	}

	// 导出过的码照样能兑
	user := e.mkUser(t, ctx)
	if _, err := e.redeem(ctx, user, g.Sample[0], nil); err != nil {
		t.Fatalf("兑换导出过的码: %v", err)
	}
	if exp2, _, err := e.svc.ListBatches(e.asAdmin(ctx), BatchFilter{TemplateID: &tpl.ID, Limit: 10}); err != nil || len(exp2) != 1 || exp2[0].Used != 1 {
		t.Fatalf("批次列表 %+v (%v)", exp2, err)
	}
}

// 生码的输入校验：数量、前缀、过去的有效期；已归档模板不能再生码。
func TestGenerateValidation(t *testing.T) {
	e, ctx := newEnv(t)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("1")}})
	admin := e.asAdmin(ctx)
	for _, in := range []GenerateInput{
		{TemplateID: tpl.ID, Count: 0},
		{TemplateID: tpl.ID, Count: maxGenerateCount + 1},
		{TemplateID: tpl.ID, Count: 1, Prefix: "TOO-LONG-PREFIX"},
	} {
		_, err := e.svc.GenerateCodes(admin, in)
		mustCode(t, err, errs.ValidationFailed)
	}
	_, err := e.svc.GenerateCodes(admin, GenerateInput{TemplateID: uuid.Must(uuid.NewV7()), Count: 1})
	mustCode(t, err, errs.NotFound)

	id := tpl.ID
	if _, err := e.svc.SaveTemplate(admin, TemplateInput{ID: &id, Name: tpl.Name, Type: TypeGeneral, Status: TemplateArchived,
		Rewards: Rewards{Balance: dec("1")}}); err != nil {
		t.Fatalf("归档: %v", err)
	}
	_, err = e.svc.GenerateCodes(admin, GenerateInput{TemplateID: tpl.ID, Count: 1})
	mustCode(t, err, errs.Conflict)

	// 门户主体不能生码
	user := e.mkUser(t, ctx)
	_, err = e.svc.GenerateCodes(asUser(ctx, user), GenerateInput{TemplateID: tpl.ID, Count: 1})
	mustCode(t, err, errs.Forbidden)
}

// 模板保存的校验：卡型不能改、价格档必须属于套餐、互斥条件、重名。
func TestSaveTemplateValidation(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.asAdmin(ctx)
	planA, _, _ := e.mkPlan(t, ctx, 3000)
	_, _, priceB := e.mkPlan(t, ctx, 5000)

	_, err := e.svc.SaveTemplate(admin, TemplateInput{Name: "错价", Type: TypePlan, Rewards: Rewards{PlanID: &planA, PriceID: &priceB}})
	mustCode(t, err, errs.ValidationFailed)

	_, err = e.svc.SaveTemplate(admin, TemplateInput{Name: "无价", Type: TypePlan, Rewards: Rewards{PlanID: &planA}})
	mustCode(t, err, errs.ValidationFailed)

	planArch, _, priceArch := e.mkPlan(t, ctx, 2000)
	e.mustExec(t, ctx, `UPDATE prices SET status = 'archived', row_version = row_version + 1 WHERE id = ?`, priceArch)
	_, err = e.svc.SaveTemplate(admin, TemplateInput{Name: "已归档价", Type: TypePlan, Rewards: Rewards{PlanID: &planArch, PriceID: &priceArch}})
	mustCode(t, err, errs.ValidationFailed)

	_, err = e.svc.SaveTemplate(admin, TemplateInput{Name: "互斥", Type: TypeGeneral, Rewards: Rewards{Balance: dec("1")},
		Conditions: Conditions{NewUserOnly: true, PaidUserOnly: true}})
	mustCode(t, err, errs.ValidationFailed)

	_, err = e.svc.SaveTemplate(admin, TemplateInput{Name: "过长", Type: TypeGeneral, Rewards: Rewards{ExpireDays: maxExpireDays + 1}})
	mustCode(t, err, errs.ValidationFailed)

	tpl := e.mkTemplate(t, ctx, TemplateInput{Name: "唯一名", Type: TypeGeneral, Rewards: Rewards{Balance: dec("1")}})
	// 重名靠库里的唯一约束挡：失败的语句会中止测试事务，包在保存点里
	e.mustExec(t, ctx, `SAVEPOINT sp_dup_name`)
	_, err = e.svc.SaveTemplate(admin, TemplateInput{Name: "唯一名", Type: TypeGeneral, Rewards: Rewards{Balance: dec("2")}})
	mustCode(t, err, errs.ValidationFailed)
	e.mustExec(t, ctx, `ROLLBACK TO SAVEPOINT sp_dup_name`)

	id := tpl.ID
	_, err = e.svc.SaveTemplate(admin, TemplateInput{ID: &id, Name: "唯一名", Type: TypeMystery, Rewards: Rewards{Pool: []Prize{
		{Label: "a", Weight: 1, Balance: dec("1")}, {Label: "b", Weight: 1, Balance: dec("2")},
	}}})
	if err == nil {
		t.Fatal("改卡型应被拒")
	}

	list, err := e.svc.ListTemplates(admin)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, x := range list {
		if x.ID == tpl.ID {
			seen = true
			mustEq(t, "模板余额", x.Rewards.Balance, dec("1"))
		}
	}
	if !seen {
		t.Fatal("模板列表里没有刚建的模板")
	}
}

// 建套餐卡后归档它的价格，ListTemplates 里这张模板 PriceArchived == true，别的模板为 false。
func TestListTemplatesMarksArchivedPrice(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.asAdmin(ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 3000)
	planTpl := e.mkTemplate(t, ctx, TemplateInput{Name: "套餐卡模板", Type: TypePlan, Rewards: Rewards{PlanID: &planID, PriceID: &priceID}})
	genTpl := e.mkTemplate(t, ctx, TemplateInput{Name: "通用卡模板", Type: TypeGeneral, Rewards: Rewards{Balance: dec("5")}})

	// 归档价格
	e.mustExec(t, ctx, `UPDATE prices SET status = 'archived', row_version = row_version + 1 WHERE id = ?`, priceID)

	list, err := e.svc.ListTemplates(admin)
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	foundPlan, foundGen := false, false
	for _, x := range list {
		if x.ID == planTpl.ID {
			foundPlan = true
			if !x.PriceArchived {
				t.Errorf("套餐卡模板 %s 的 PriceArchived 应为 true", x.ID)
			}
		}
		if x.ID == genTpl.ID {
			foundGen = true
			if x.PriceArchived {
				t.Errorf("通用卡模板 %s 的 PriceArchived 应为 false", x.ID)
			}
		}
	}
	if !foundPlan || !foundGen {
		t.Fatalf("列表里未找全模板: plan=%v gen=%v", foundPlan, foundGen)
	}
}

// 同一张码并发兑换：恰好一个成功，其余都回 ErrCodeUnusable，余额只到一次账。
func TestConcurrentRedeemSameCode(t *testing.T) {
	e, ctx := newCommitEnv(t)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("8")}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	const n = 12
	users := make([]uuid.UUID, n)
	for i := range users {
		users[i] = e.mkUser(t, ctx)
	}
	errsOut := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range users {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errsOut[i] = e.redeem(ctx, users[i], code, nil)
		}(i)
	}
	close(start)
	wg.Wait()

	ok := 0
	for i, err := range errsOut {
		switch {
		case err == nil:
			ok++
			mustEq(t, "赢家余额", e.balanceOf(t, ctx, users[i]), dec("8"))
		case errors.Is(err, ErrCodeUnusable):
		default:
			t.Fatalf("并发兑换意外错误: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("成功 %d 次，期望恰好 1 次", ok)
	}
	if c := e.count(t, ctx, `SELECT count(*) FROM gift_card_redemptions`); c != 1 {
		t.Fatalf("兑换流水 %d 条，期望 1", c)
	}
	if c := e.count(t, ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'gift_card_balance'`); c != 1 {
		t.Fatalf("赠送交易 %d 笔，期望 1", c)
	}
}

// 统计与使用明细：明细只有掩码。
func TestStatsAndUsages(t *testing.T) {
	e, ctx := newEnv(t)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("4")}})
	codes := e.mkCodes(t, ctx, tpl.ID, 3, nil)
	user := e.mkUser(t, ctx)
	if _, err := e.redeem(ctx, user, codes[0], nil); err != nil {
		t.Fatal(err)
	}
	admin := e.asAdmin(ctx)
	us, err := e.svc.ListUsages(admin, &tpl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 1 || us[0].CodeMasked != MaskCode(codes[0]) || us[0].UserEmail == "" {
		t.Fatalf("使用明细 %+v", us)
	}
	mustEq(t, "明细余额", us[0].Granted.Balance, dec("4"))

	st, err := e.svc.Stats(admin)
	if err != nil {
		t.Fatal(err)
	}
	if st.CodesUsed < 1 || st.CodesTotal < 3 {
		t.Fatalf("统计 %+v", st)
	}

	rows, err := e.svc.ExportCodes(admin, CodeFilter{TemplateID: &tpl.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("掩码报表 %d 行，期望 3", len(rows))
	}
	for _, r := range rows {
		for _, c := range codes {
			if r.CodeMasked == c {
				t.Fatal("掩码报表里出现了明文")
			}
		}
	}
	_, err = e.svc.ExportCodes(admin, CodeFilter{Status: "bogus"})
	mustCode(t, err, errs.BadRequest)
}
