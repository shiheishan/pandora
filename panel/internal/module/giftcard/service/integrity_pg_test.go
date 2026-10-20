package service

import (
	"slices"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// 巡检「礼品卡兑换对不上」：正常兑换不算；码标了 used 却没有兑换流水、兑换人与码上的 used_by 不同，各算一条。
// 计数是全库的（库里可能有别的包提交的行），只比前后差，样例按本例的码 id 找。
func TestCheckRedemptionsPG(t *testing.T) {
	e, ctx := newEnv(t)
	check := func() integrity.Result {
		t.Helper()
		r, err := e.svc.CheckRedemptions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	user, other := e.mkUser(t, ctx), e.mkUser(t, ctx)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("3.00")}})
	codes := e.mkCodes(t, ctx, tpl.ID, 2, nil)
	redeemed, bare := e.codeID(t, ctx, codes[0]), e.codeID(t, ctx, codes[1])

	before := check()
	if _, err := e.redeem(ctx, user, codes[0], nil); err != nil {
		t.Fatalf("兑换: %v", err)
	}
	if r := check(); r.Count != before.Count || slices.Contains(r.Sample, redeemed.String()) {
		t.Fatalf("正常兑换不该算违例：%d → %d，样例 %v", before.Count, r.Count, r.Sample)
	}

	// 码标了 used、没有兑换流水（兑换写了一半）
	e.mustExec(t, ctx, `UPDATE gift_card_codes SET status = 'used', used_by = ?, used_at = now() WHERE id = ?`, user, bare)
	r := check()
	if r.Count-before.Count != 1 || !slices.Contains(r.Sample, bare.String()) {
		t.Fatalf("没有兑换流水的 used 码：%d → %d，样例 %v", before.Count, r.Count, r.Sample)
	}

	// 兑换流水的兑换人与码上的 used_by 不同
	e.mustExec(t, ctx, `UPDATE gift_card_codes SET used_by = ? WHERE id = ?`, other, redeemed)
	r = check()
	if r.Count-before.Count != 2 || !slices.Contains(r.Sample, redeemed.String()) {
		t.Fatalf("兑换人对不上：%d → %d，样例 %v", before.Count, r.Count, r.Sample)
	}
}
