package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/google/uuid"
)

// SendBulkMail 记下输入，回固定结果。
func (f *fakeNotify) SendBulkMail(_ context.Context, in service.BulkMailInput) (service.BulkMailResult, error) {
	f.bulkMailIn = append(f.bulkMailIn, in)
	return service.BulkMailResult{Queued: 5, Skipped: 1}, nil
}

func TestSendBulkMail(t *testing.T) {
	fake := &fakeNotify{}
	e, k := setupEngine(fake)
	group := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	body := `{"status":"active","group_id":"` + group.String() + `","has_active_sub":true,` +
		`"expires_within_days":7,"sub_state":"active","subject":"续费提醒","body":"$email 你好"}`

	w := doNotify(e, http.MethodPost, "/v1/users/bulk/mail", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(got))
	for key := range got {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "queued,skipped" || got["queued"] != float64(5) || got["skipped"] != float64(1) {
		t.Fatalf("响应 %v", got)
	}
	if k.authCalls != 1 || k.reauthCalls != 1 || len(k.idemScopes) != 1 || k.idemScopes[0] != "user_bulk_mail" {
		t.Fatalf("中间件 auth=%d reauth=%d idem=%v", k.authCalls, k.reauthCalls, k.idemScopes)
	}
	if len(fake.bulkMailIn) != 1 {
		t.Fatalf("调用 %d 次", len(fake.bulkMailIn))
	}
	in := fake.bulkMailIn[0]
	f := in.Filter
	if f.Status != "active" || f.GroupID == nil || *f.GroupID != group || f.HasActiveSub == nil || !*f.HasActiveSub ||
		f.ExpiresWithinDays != 7 || f.SubState != "active" || f.PlanID != nil || in.Subject != "续费提醒" || in.Body != "$email 你好" {
		t.Fatalf("输入 %+v", in)
	}

	w = doNotify(e, http.MethodPost, "/v1/users/bulk/mail", `{"group_id":"x","plan_id":"y","subject":"s","body":"b"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("坏 uuid status %d", w.Code)
	}
	if s := w.Body.String(); !strings.Contains(s, `"group_id"`) || !strings.Contains(s, `"plan_id"`) {
		t.Fatalf("fields %s", s)
	}
	w = doNotify(e, http.MethodPost, "/v1/users/bulk/mail", `{"subject":"s","body":"b","extra":1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未知字段 status %d", w.Code)
	}
	w = doNotify(e, http.MethodPost, "/v1/users/bulk/mail", `{"status":"pending","subject":"s","body":"b"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("坏 status status %d", w.Code)
	}
	if len(fake.bulkMailIn) != 1 {
		t.Fatalf("校验失败不应调到 service，调用 %d 次", len(fake.bulkMailIn))
	}
}
