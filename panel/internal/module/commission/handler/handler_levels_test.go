package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/commission/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestMyCommissionLevelsShape(t *testing.T) {
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	f := &fakeSvc{my: service.MyCommission{
		Summary: service.Summary{
			Currency: "CNY", MinWithdraw: decimal.RequireFromString("100"), Scope: "every_order", RatePercent: 10,
			DirectRateBP: 500, WithdrawEnabled: false, WithdrawMethods: nil, SiteCurrency: "CNY",
		},
		Entries: []service.EntryView{{
			Amount: decimal.RequireFromString("1"), Base: decimal.RequireFromString("10"), RateBP: 1000,
			Currency: "CNY", Status: "pending", FrozenUntil: now, CreatedAt: now, OrderNo: "N1", From: "u@example.test",
			Level: 2, SharePercent: 30,
		}},
		Withdrawals: []service.WithdrawalView{{
			ID: uuid.New(), Amount: decimal.RequireFromString("10"), Currency: "CNY", Status: "requested",
			Method: "usdt_trc20", RequestedAt: now,
		}},
		Transfers: []service.TransferView{},
	}}
	w := do(newEngine(f, false, nil), http.MethodGet, "/v1/me/commission", "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态 %d %s", w.Code, w.Body)
	}
	var resp struct {
		Summary     map[string]any   `json:"summary"`
		Entries     []map[string]any `json:"entries"`
		Withdrawals []map[string]any `json:"withdrawals"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Summary["direct_rate_bp"] != float64(500) {
		t.Fatalf("direct_rate_bp %v", resp.Summary["direct_rate_bp"])
	}
	if resp.Summary["site_currency"] != "CNY" {
		t.Fatalf("site_currency %v", resp.Summary["site_currency"])
	}
	if resp.Summary["withdraw_enabled"] != false {
		t.Fatalf("withdraw_enabled %v", resp.Summary["withdraw_enabled"])
	}
	wm, _ := json.Marshal(resp.Summary["withdraw_methods"])
	if string(wm) != "[]" {
		t.Fatalf("withdraw_methods 应为 []，得到 %s", wm)
	}
	e := resp.Entries[0]
	if e["level"] != float64(2) || e["share_percent"] != float64(30) || e["rate_bp"] != float64(1000) || e["rate_percent"] != float64(10) {
		t.Fatalf("entry %v", e)
	}
	if resp.Withdrawals[0]["method"] != "usdt_trc20" {
		t.Fatalf("withdrawal method %v", resp.Withdrawals[0]["method"])
	}
}

func TestOverviewLevelsShape(t *testing.T) {
	f := &fakeSvc{overview: service.Overview{
		Currency: "CNY", MinWithdraw: decimal.RequireFromString("100"),
		DistributionEnabled: true, DistributionLevels: [3]int{50, 30, 20},
		WithdrawEnabled: true, WithdrawMethods: []string{"alipay", "usdt_trc20"},
	}}
	w := do(newEngine(f, true, nil), http.MethodGet, "/v1/commission/overview", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["distribution_levels"]) != "[50,30,20]" {
		t.Fatalf("distribution_levels %s", raw["distribution_levels"])
	}
	if string(raw["withdraw_methods"]) != `["alipay","usdt_trc20"]` {
		t.Fatalf("withdraw_methods %s", raw["withdraw_methods"])
	}
}

func TestListWithdrawalsUSDTShape(t *testing.T) {
	d := decimal.RequireFromString("88.5")
	f := &fakeSvc{withdrawals: []service.AdminWithdrawal{
		{
			ID: uuid.New(), UserID: uuid.New(), Email: "a@example.test",
			Amount: decimal.RequireFromString("10"), Currency: "CNY", Status: "paid",
			Method: "usdt_erc20", PayoutReference: "0x" + strings.Repeat("ab", 32),
			PaidUSDTAmount: &d, RequestedAt: time.Now().UTC(),
		},
		{
			ID: uuid.New(), UserID: uuid.New(), Email: "b@example.test",
			Amount: decimal.RequireFromString("20"), Currency: "CNY", Status: "paid",
			Method: "alipay", PayoutDetail: "alipay", RequestedAt: time.Now().UTC(),
			PaidUSDTAmount: nil,
		},
	}}
	w := do(newEngine(f, true, nil), http.MethodGet, "/v1/withdrawals", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var resp struct {
		Withdrawals []map[string]any `json:"withdrawals"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	row0 := resp.Withdrawals[0]
	if row0["paid_usdt_amount"] != "88.5" || row0["method"] != "usdt_erc20" {
		t.Fatalf("row0 %v", row0)
	}
	if row0["payout_reference"] != "0x"+strings.Repeat("ab", 32) {
		t.Fatalf("payout_reference %v", row0["payout_reference"])
	}
	row1 := resp.Withdrawals[1]
	if _, ok := row1["paid_usdt_amount"]; !ok {
		t.Fatal("第二行应有 paid_usdt_amount 键")
	}
	if row1["paid_usdt_amount"] != nil {
		t.Fatalf("第二行 paid_usdt_amount 应为 nil，得到 %v", row1["paid_usdt_amount"])
	}
}

func TestMarkPaidUSDTAmount(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, true, nil)
	path := "/v1/withdrawals/" + uuid.New().String() + "/paid"

	w := do(e, http.MethodPost, path, `{"payout_reference":"TX-1","paid_usdt_amount":"abc"}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"paid_usdt_amount"`) {
		t.Fatalf("非法 paid_usdt_amount：%d %s", w.Code, w.Body)
	}

	w = do(e, http.MethodPost, path, `{"payout_reference":"TX-1","paid_usdt_amount":"  "}`)
	if w.Code != http.StatusOK || f.gotPaidUSDT != nil {
		t.Fatalf("空白应视为未填：%d gotPaidUSDT=%v", w.Code, f.gotPaidUSDT)
	}

	w = do(e, http.MethodPost, path, `{"payout_reference":"TX-2","paid_usdt_amount":"100.5"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("合法数量：%d %s", w.Code, w.Body)
	}
	if f.gotRef != "TX-2" || f.gotPaidUSDT == nil || !f.gotPaidUSDT.Equal(decimal.RequireFromString("100.5")) {
		t.Fatalf("收到 ref=%q paid=%v", f.gotRef, f.gotPaidUSDT)
	}

	f2 := &fakeSvc{}
	e2 := newEngine(f2, true, nil)
	w = do(e2, http.MethodPost, path, `{"payout_reference":"TX-1"}`)
	if w.Code != http.StatusOK || f2.gotPaidUSDT != nil {
		t.Fatalf("无 paid_usdt_amount 字段：%d got=%v", w.Code, f2.gotPaidUSDT)
	}

	w = do(e, http.MethodPost, path, `{"payout_reference":"TX-1","extra":1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未知字段应 400，得到 %d %s", w.Code, w.Body)
	}
}

func TestSetConfigLevelsFields(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, true, nil)
	body := `{"distribution_enabled":true,"distribution_l1":50,"distribution_l2":30,"distribution_l3":20,"withdraw_enabled":false,"withdraw_methods":["alipay","usdt_trc20"]}`
	w := do(e, http.MethodPost, "/v1/commission/config", body)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	u := f.gotConfig
	if u.DistributionEnabled == nil || !*u.DistributionEnabled {
		t.Fatalf("DistributionEnabled %+v", u.DistributionEnabled)
	}
	if u.DistributionL1 == nil || *u.DistributionL1 != 50 || u.DistributionL2 == nil || *u.DistributionL2 != 30 || u.DistributionL3 == nil || *u.DistributionL3 != 20 {
		t.Fatalf("distribution_l* %+v", u)
	}
	if u.WithdrawEnabled == nil || *u.WithdrawEnabled {
		t.Fatalf("WithdrawEnabled %+v", u.WithdrawEnabled)
	}
	if u.WithdrawMethods == nil || len(*u.WithdrawMethods) != 2 || (*u.WithdrawMethods)[0] != "alipay" || (*u.WithdrawMethods)[1] != "usdt_trc20" {
		t.Fatalf("WithdrawMethods %+v", u.WithdrawMethods)
	}
	if u.RatePercent != nil || u.FreezeDays != nil || u.MinWithdraw != nil || u.Scope != nil {
		t.Fatalf("旧字段应为 nil %+v", u)
	}

	w = do(e, http.MethodPost, "/v1/commission/config", `{"withdraw_enabled":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	u2 := f.gotConfig
	if u2.WithdrawEnabled == nil || !*u2.WithdrawEnabled {
		t.Fatalf("WithdrawEnabled %+v", u2.WithdrawEnabled)
	}
	if u2.DistributionEnabled != nil || u2.DistributionL1 != nil || u2.DistributionL2 != nil || u2.DistributionL3 != nil || u2.WithdrawMethods != nil {
		t.Fatalf("第二次只应改 withdraw_enabled %+v", u2)
	}
}

func TestOverridesEndpoints(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, true, nil)

	w := do(e, http.MethodGet, "/v1/commission/overrides", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"overrides":[]`) {
		t.Fatalf("空列表：%d %s", w.Code, w.Body)
	}

	uid := uuid.New()
	rate := 15
	f.overrides = []service.UserOverride{{
		UserID: uid, Email: "a@example.test", RatePercent: &rate, Scope: nil, UpdatedAt: time.Now().UTC(),
	}}
	w = do(e, http.MethodGet, "/v1/commission/overrides", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var listResp struct {
		Overrides []map[string]any `json:"overrides"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	row := listResp.Overrides[0]
	if row["rate_percent"] != float64(15) {
		t.Fatalf("rate_percent %v", row["rate_percent"])
	}
	if _, ok := row["scope"]; !ok || row["scope"] != nil {
		t.Fatalf("scope 应为 null 键存在 %v", row["scope"])
	}
	if row["user_id"] != uid.String() {
		t.Fatalf("user_id %v", row["user_id"])
	}

	w = do(e, http.MethodPost, "/v1/commission/overrides", `{"email":"b@example.test","scope":"first_order"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("POST override：%d %s", w.Code, w.Body)
	}
	if f.gotOverride.Email != "b@example.test" || f.gotOverride.RatePercent != nil {
		t.Fatalf("gotOverride %+v", f.gotOverride)
	}
	if f.gotOverride.Scope == nil || *f.gotOverride.Scope != "first_order" {
		t.Fatalf("scope %+v", f.gotOverride.Scope)
	}
	if !strings.Contains(w.Body.String(), `"email":"b@example.test"`) {
		t.Fatalf("响应 %s", w.Body)
	}

	w = do(e, http.MethodPost, "/v1/commission/overrides", `{"email":"b@example.test","rate_percent":null,"scope":"first_order","x":1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未知字段应 400，得到 %d", w.Code)
	}

	w = do(e, http.MethodDelete, "/v1/commission/overrides/not-a-uuid", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("非 UUID：%d", w.Code)
	}

	delID := uuid.New()
	w = do(e, http.MethodDelete, "/v1/commission/overrides/"+delID.String(), "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("DELETE：%d %s", w.Code, w.Body)
	}
	if f.gotDeleted != delID {
		t.Fatalf("gotDeleted %v", f.gotDeleted)
	}
}
