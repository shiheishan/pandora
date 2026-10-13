package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/commission/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func (f *fakeSvc) AdminReviewEntries(_ context.Context, cursor string, limit int) (service.ReviewEntryPage, error) {
	f.gotCursor, f.gotLimit = cursor, limit
	return f.reviewPage, nil
}

func (f *fakeSvc) ReviewEntry(_ context.Context, _ uuid.UUID, approve bool, reason string) (string, error) {
	f.gotApprove, f.gotReason = approve, reason
	if approve {
		return service.EntryPending, nil
	}
	return service.EntryVoided, nil
}

// 待复核列表的 JSON 形状：金额是分、邮箱不打码、next_cursor 原样透传；没有条目时 entries 是 []。
func TestListReviewEntriesShape(t *testing.T) {
	at := time.Date(2026, 10, 13, 2, 0, 0, 0, time.UTC)
	id := uuid.New()
	f := &fakeSvc{reviewPage: service.ReviewEntryPage{
		Entries: []service.ReviewEntryRow{{
			ID: id, OrderNo: "N100", ReferrerEmail: "ref@x.test", RefereeEmail: "fr@x.test",
			Amount: decimal.RequireFromString("12.34"), Currency: "CNY", ReviewReason: "同源嫌疑", ReviewSource: service.ReviewSourceSameSource,
			CreatedAt: at, FrozenUntil: at.Add(72 * time.Hour),
		}},
		NextCursor: "abc",
	}}
	e := newEngine(f, true, nil)
	w := do(e, http.MethodGet, "/v1/commission/entries?review=required&cursor=xyz&limit=20", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if f.gotCursor != "xyz" || f.gotLimit != 20 {
		t.Fatalf("参数没透传：cursor=%q limit=%d", f.gotCursor, f.gotLimit)
	}
	var resp struct {
		Entries    []map[string]any `json:"entries"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.NextCursor != "abc" || len(resp.Entries) != 1 {
		t.Fatalf("列表 %s", w.Body)
	}
	row := resp.Entries[0]
	want := map[string]any{
		"id": id.String(), "order_no": "N100", "referrer_email": "ref@x.test", "referee_email": "fr@x.test",
		"amount": float64(1234), "currency": "CNY", "review_reason": "同源嫌疑", "review_source": "same_source",
		"created_at": "2026-10-13T02:00:00Z", "frozen_until": "2026-10-16T02:00:00Z",
	}
	if len(row) != len(want) {
		t.Fatalf("字段集合不对：%v", row)
	}
	for k, v := range want {
		if row[k] != v {
			t.Errorf("%s = %v，期望 %v", k, row[k], v)
		}
	}

	f.reviewPage = service.ReviewEntryPage{}
	w = do(e, http.MethodGet, "/v1/commission/entries?review=required", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"entries":[]`) || !strings.Contains(w.Body.String(), `"next_cursor":""`) {
		t.Fatalf("空列表：%d %s", w.Code, w.Body)
	}
}

// 只支持 review=required（否则 422）；limit 不是数字是请求格式错误（400，与 httpx.BindQuery 的统一做法一致）。
func TestListReviewEntriesQuery(t *testing.T) {
	e := newEngine(&fakeSvc{}, true, nil)
	for _, q := range []string{"", "?review=all"} {
		if w := do(e, http.MethodGet, "/v1/commission/entries"+q, ""); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%q 应回 422，得到 %d %s", q, w.Code, w.Body)
		}
	}
	if w := do(e, http.MethodGet, "/v1/commission/entries?review=required&limit=abc", ""); w.Code != http.StatusBadRequest {
		t.Errorf("limit=abc 应回 400，得到 %d %s", w.Code, w.Body)
	}
}

func TestReviewEntry(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, true, nil)
	id := uuid.New().String()
	if w := do(e, http.MethodPost, "/v1/commission/entries/"+id+"/review", `{"action":"reject","reason":"xxxxx"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法 action：%d", w.Code)
	}
	if w := do(e, http.MethodPost, "/v1/commission/entries/not-a-uuid/review", `{"action":"approve"}`); w.Code != http.StatusNotFound {
		t.Fatalf("非 UUID：%d", w.Code)
	}
	w := do(e, http.MethodPost, "/v1/commission/entries/"+id+"/review", `{"action":"void","reason":"同源刷单确认"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"voided"`) || f.gotApprove || f.gotReason != "同源刷单确认" {
		t.Fatalf("作废：%d %s", w.Code, w.Body)
	}
	w = do(e, http.MethodPost, "/v1/commission/entries/"+id+"/review", `{"action":"approve"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"pending"`) || !f.gotApprove {
		t.Fatalf("通过：%d %s", w.Code, w.Body)
	}
}
