// [INPUT]: 依赖 codes.go 的 Stats / PreviewCode、redeem.go 的 MyRedemptions，依赖 batches_pg18_test.go 的 openGiftcardPG18 夹具
// [OUTPUT]: 对外提供 TestGiftCardReadsPG18（run-pg18-gates.sh 的 giftcard 域）
// [POS]: giftcard 读模型扩展的 PG18 集成门禁：已发行面额只算通用卡、门户预览不带运营数据并补套餐名与周期、兑换记录的卡码提示
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package giftcard

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGiftCardReadsPG18(t *testing.T) {
	ctx, admin, app := openGiftcardPG18(t)

	const (
		tenantID  = "74100000-0000-7000-8000-000000000001"
		userID    = "74100000-0000-7000-8000-000000000011"
		productID = "74100000-0000-7000-8000-000000000021"
		planID    = "74100000-0000-7000-8000-000000000022"
		priceID   = "74100000-0000-7000-8000-000000000023"
		generalT  = "74100000-0000-7000-8000-000000000031"
		planT     = "74100000-0000-7000-8000-000000000032"
		usedCode  = "74100000-0000-7000-8000-000000000041"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	// 目录与兑换流水只是读路径的素材：replica 模式跳过外键与状态守卫，CHECK 照常生效。
	must(`SET session_replication_role = replica`)
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'giftcard-reads-pg18', 'Giftcard Reads PG18', 'CNY')`, tenantID)
	must(`INSERT INTO users (id, tenant_id, email, display_name, status)
		VALUES ($1, $2, 'giftcard-reads@example.test', 'Reader', 'active')`, userID, tenantID)
	must(`INSERT INTO products (id, tenant_id, code, name, status) VALUES ($1, $2, 'gc-pro', '专业版', 'active')`, productID, tenantID)
	must(`INSERT INTO plans (id, tenant_id, product_id, code, name, visibility, status)
		VALUES ($1, $2, $3, 'gc-pro', '专业版', 'public', 'active')`, planID, tenantID, productID)
	must(`INSERT INTO prices (id, tenant_id, product_id, currency, unit_amount, billing_interval, interval_count, status)
		VALUES ($1, $2, $3, 'CNY', 1500, 'month', 3, 'active')`, priceID, tenantID, productID)
	must(`INSERT INTO gift_card_templates (id, tenant_id, name, type, rewards) VALUES
		($1, $3, '五元卡', 'general', '{"balance":500}'::jsonb),
		($2, $3, '专业版季卡', 'plan', jsonb_build_object('plan_id', $4::text, 'price_id', $5::text))`,
		generalT, planT, tenantID, planID, priceID)
	must(`INSERT INTO gift_card_codes (id, tenant_id, template_id, code, status) VALUES
		($1, $2, $3, 'GCREADSUSED0001', 'used')`, usedCode, tenantID, generalT)
	must(`INSERT INTO gift_card_codes (tenant_id, template_id, code, status) VALUES
		($1, $2, 'GCREADSOPEN0002', 'unused'),
		($1, $2, 'GCREADSDEAD0003', 'disabled'),
		($1, $3, 'GCREADSPLAN0004', 'unused')`, tenantID, generalT, planT)
	must(`INSERT INTO gift_card_redemptions (tenant_id, code_id, template_id, user_id, granted)
		VALUES ($1, $2, $3, $4, '{"balance":500}'::jsonb)`, tenantID, usedCode, generalT, userID)
	must(`SET session_replication_role = origin`)

	svc := New(app, nil, nil)

	// 已发行面额：三张通用卡（不论已用、停用）× 500；套餐卡不算面额。
	st, err := svc.Stats(ctx, tenantID)
	if err != nil || st.BalanceIssued != 1500 || st.BalanceOut != 500 || st.CodesTotal != 4 {
		t.Fatalf("stats=%+v err=%v, want balance_issued=1500 balance_out=500 codes_total=4", st, err)
	}
	t.Log("marker=giftcard_reads_balance_issued_ok")

	// 门户预览：套餐卡补套餐名与周期，不带发行量与兑换量。
	card, err := svc.PreviewCode(ctx, tenantID, "gcreadsplan0004")
	if err != nil {
		t.Fatalf("preview plan card: %v", err)
	}
	if card.PlanName != "专业版" || card.Interval != "month" || card.IntervalCount != 3 {
		t.Fatalf("plan card preview=%+v, want 专业版 month×3", card)
	}
	raw, _ := json.Marshal(card)
	if strings.Contains(string(raw), "code_total") || strings.Contains(string(raw), "code_used") {
		t.Fatalf("portal preview leaks operating counts: %s", raw)
	}
	general, err := svc.PreviewCode(ctx, tenantID, "GCREADSOPEN0002")
	if err != nil || general.PlanName != "" || general.Rewards.Balance != 500 {
		t.Fatalf("general card preview=%+v err=%v", general, err)
	}
	t.Log("marker=giftcard_reads_portal_preview_ok")

	// 兑换记录：卡码只给前 12 位提示。
	mine, err := svc.MyRedemptions(ctx, tenantID, userID)
	if err != nil || len(mine) != 1 || mine[0].CodeHint != "GCREADSUSED0…" {
		t.Fatalf("my redemptions=%+v err=%v, want one with code hint GCREADSUSED0…", mine, err)
	}
	t.Log("marker=giftcard_reads_code_hint_ok")
}
