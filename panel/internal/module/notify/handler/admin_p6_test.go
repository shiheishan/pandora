package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

var admActor = uuid.MustParse("018f0000-0000-7000-8000-000000000001")

func admDo(e http.Handler, method, path, body string) *httptest.ResponseRecorder {
	return doNotify(e, method, path, body)
}

func admDoRaw(e http.Handler, method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		if len(body) > 0 {
			r.Header.Set("Content-Type", "application/json")
		}
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	return rec
}

func admErrFields(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body struct {
		Error struct {
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Error.Fields
}

func TestAdmListAnnouncementsJSONShape(t *testing.T) {
	annID := uuid.MustParse("018f0000-0000-7000-8000-000000000010")
	fake := &fakeNotify{p6Fake: p6Fake{
		adminAnns: service.AdminAnnouncements{
			Announcements: []service.AnnouncementView{{
				ID: annID, Title: "t", Body: "b", Severity: "info", Status: "draft", Version: 1,
				PlanIDs: nil, PlanTargets: nil, GroupIDs: nil, GroupTargets: nil,
				PublishAt: nil, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			}},
			Plans:      nil,
			UserGroups: nil,
		},
	}}
	e, _ := p6Engine(fake, admActor)
	rec := admDo(e, http.MethodGet, "/v1/announcements", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"announcements", "plans", "user_groups"} {
		if root[k] == nil {
			t.Fatalf("missing %q", k)
		}
	}
	var anns []map[string]json.RawMessage
	if err := json.Unmarshal(root["announcements"], &anns); err != nil || len(anns) != 1 {
		t.Fatalf("announcements %v", anns)
	}
	row := anns[0]
	wantKeys := []string{
		"id", "title", "body", "severity", "pinned", "status", "version",
		"target_plan_ids", "plan_targets", "target_user_group_ids", "user_group_targets",
		"publish_at", "expires_at", "created_at",
	}
	for _, k := range wantKeys {
		if row[k] == nil {
			t.Fatalf("ann missing %q", k)
		}
	}
	if row["publish_at"] == nil || string(row["publish_at"]) != "null" {
		t.Fatalf("publish_at want null, got %s", row["publish_at"])
	}
	if !strings.Contains(rec.Body.String(), `"target_plan_ids":[]`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestAdmSaveAnnouncementCreateAndEdit(t *testing.T) {
	p1 := uuid.MustParse("018f0000-0000-7000-8000-000000000020")
	p2 := uuid.MustParse("018f0000-0000-7000-8000-000000000021")
	g1 := uuid.MustParse("018f0000-0000-7000-8000-000000000030")
	pub := "2026-06-01T10:00:00+08:00"
	wantPub := time.Date(2026, 6, 1, 2, 0, 0, 0, time.UTC)

	fake := &fakeNotify{p6Fake: p6Fake{
		saved: service.AnnouncementSaved{
			ID: uuid.MustParse("018f0000-0000-7000-8000-000000000099"), Status: "draft", Version: 1,
		},
	}}
	e, k := p6Engine(fake, admActor)

	createBody := `{"title":"  hi  ","body":"  b  ","severity":"notice","pinned":true,` +
		`"target_plan_ids":["` + p2.String() + `","` + p1.String() + `","` + p2.String() + `"],` +
		`"target_user_group_ids":["` + g1.String() + `"],` +
		`"publish_at":"` + pub + `","expires_at":"","publish":false,"expected_version":0}`
	rec := admDo(e, http.MethodPost, "/v1/announcements", createBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status %d %s", rec.Code, rec.Body.String())
	}
	if fake.savedIn == nil {
		t.Fatal("savedIn nil")
	}
	if fake.savedIn.ID != nil {
		t.Fatalf("create id %v", fake.savedIn.ID)
	}
	if fake.savedIn.Title != "hi" || fake.savedIn.Body != "b" {
		t.Fatalf("trim %+v", fake.savedIn)
	}
	if len(fake.savedIn.PlanIDs) != 2 || fake.savedIn.PlanIDs[0] != p1 || fake.savedIn.PlanIDs[1] != p2 {
		t.Fatalf("plan ids %v", fake.savedIn.PlanIDs)
	}
	if fake.savedIn.PublishAt == nil || !fake.savedIn.PublishAt.Equal(wantPub) {
		t.Fatalf("publish_at %v", fake.savedIn.PublishAt)
	}
	if k.reauthCalls != 1 || len(k.idemScopes) != 1 || k.idemScopes[0] != "announcement_save" {
		t.Fatalf("reauth %d idem %v", k.reauthCalls, k.idemScopes)
	}

	editID := uuid.MustParse("018f0000-0000-7000-8000-000000000040")
	fake.saved = service.AnnouncementSaved{ID: editID, Status: "published", Version: 2}
	rec = admDo(e, http.MethodPost, "/v1/announcements/"+editID.String(),
		`{"title":"t","body":"b","severity":"info","pinned":false,"target_plan_ids":[],`+
			`"target_user_group_ids":[],"publish_at":"","expires_at":"","publish":true,"expected_version":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit status %d %s", rec.Code, rec.Body.String())
	}
	if fake.savedIn.ID == nil || *fake.savedIn.ID != editID {
		t.Fatalf("edit id %v", fake.savedIn.ID)
	}
	if k.reauthCalls != 2 || len(k.idemScopes) != 2 {
		t.Fatalf("reauth %d idem %v", k.reauthCalls, k.idemScopes)
	}
}

func TestAdmSaveAnnouncementValidation(t *testing.T) {
	e, _ := p6Engine(&fakeNotify{}, admActor)
	base := `{"title":"t","body":"b","severity":"info","pinned":false,"target_plan_ids":[],` +
		`"target_user_group_ids":[],"publish_at":"","expires_at":"","publish":false,"expected_version":0}`

	rec := admDo(e, http.MethodPost, "/v1/announcements/not-a-uuid", base)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bad path id status %d", rec.Code)
	}

	rec = admDo(e, http.MethodPost, "/v1/announcements", strings.Replace(base,
		`"target_plan_ids":[]`, `"target_plan_ids":["x"]`, 1))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad plan status %d", rec.Code)
	}
	if admErrFields(t, rec)["target_plan_ids"] == "" {
		t.Fatal("missing target_plan_ids field")
	}

	rec = admDo(e, http.MethodPost, "/v1/announcements", strings.Replace(base,
		`"target_user_group_ids":[]`, `"target_user_group_ids":["y"]`, 1))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad group status %d", rec.Code)
	}
	if admErrFields(t, rec)["target_user_group_ids"] == "" {
		t.Fatal("missing target_user_group_ids field")
	}

	rec = admDo(e, http.MethodPost, "/v1/announcements", strings.Replace(base,
		`"publish_at":""`, `"publish_at":"not-rfc3339"`, 1))
	if rec.Code != http.StatusUnprocessableEntity || admErrFields(t, rec)["publish_at"] == "" {
		t.Fatalf("publish_at field %v", admErrFields(t, rec))
	}
}

func TestAdmWithdrawAnnouncement(t *testing.T) {
	id := uuid.MustParse("018f0000-0000-7000-8000-000000000050")
	fake := &fakeNotify{p6Fake: p6Fake{withdrawnVer: 7}}
	e, k := p6Engine(fake, admActor)
	rec := admDo(e, http.MethodPost, "/v1/announcements/"+id.String()+"/withdraw",
		`{"expected_version":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK      bool `json:"ok"`
		Version int  `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !out.OK || out.Version != 7 {
		t.Fatalf("body %+v err %v", out, err)
	}
	if fake.withdrawID != id || fake.withdrawVer != 3 {
		t.Fatalf("withdraw %v %d", fake.withdrawID, fake.withdrawVer)
	}
	if len(k.idemScopes) != 1 || k.idemScopes[0] != "announcement_withdraw" {
		t.Fatalf("idem %v", k.idemScopes)
	}
}

func TestAdmTelegramSettingsGET(t *testing.T) {
	chat := int64(-100999)
	for _, tc := range []struct {
		name     string
		view     service.TelegramSettingsView
		wantNull bool
		wantNum  *int64
	}{
		{"null", service.TelegramSettingsView{Enabled: true, BotUsername: "bot", HasToken: true, AdminChatID: nil}, true, nil},
		{"number", service.TelegramSettingsView{Enabled: false, HasToken: false, AdminChatID: &chat}, false, &chat},
	} {
		fake := &fakeNotify{p6Fake: p6Fake{tgSettings: tc.view}}
		e, _ := p6Engine(fake, admActor)
		rec := admDo(e, http.MethodGet, "/v1/settings/telegram", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status %d", tc.name, rec.Code)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"enabled", "bot_username", "has_token", "admin_chat_id"} {
			if m[k] == nil {
				t.Fatalf("%s missing %q", tc.name, k)
			}
		}
		if tc.wantNull && string(m["admin_chat_id"]) != "null" {
			t.Fatalf("%s admin_chat_id %s", tc.name, m["admin_chat_id"])
		}
		if tc.wantNum != nil {
			var n int64
			if err := json.Unmarshal(m["admin_chat_id"], &n); err != nil || n != *tc.wantNum {
				t.Fatalf("%s chat %v err %v", tc.name, n, err)
			}
		}
	}
}

func TestAdmSetTelegramSettingsAdminChatID(t *testing.T) {
	base := `{"enabled":true,"bot_username":"u","bot_token":""`
	cases := []struct {
		name    string
		suffix  string
		wantSet bool
		wantID  *int64
		status  int
	}{
		{"missing", `}`, false, nil, http.StatusOK},
		{"null", `,"admin_chat_id":null}`, true, nil, http.StatusOK},
		{"number", `,"admin_chat_id":-42}`, true, admPtrI64(-42), http.StatusOK},
		{"zero", `,"admin_chat_id":0}`, false, nil, http.StatusUnprocessableEntity},
		{"positive", `,"admin_chat_id":42}`, false, nil, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		fake := &fakeNotify{}
		e, _ := p6Engine(fake, admActor)
		rec := admDo(e, http.MethodPost, "/v1/settings/telegram", base+tc.suffix)
		if rec.Code != tc.status {
			t.Fatalf("%s status %d %s", tc.name, rec.Code, rec.Body.String())
		}
		if tc.status != http.StatusOK {
			if admErrFields(t, rec)["admin_chat_id"] == "" {
				t.Fatalf("%s missing field", tc.name)
			}
			continue
		}
		if fake.tgSaved == nil {
			t.Fatalf("%s tgSaved nil", tc.name)
		}
		if fake.tgSaved.AdminChatSet != tc.wantSet {
			t.Fatalf("%s set %v want %v", tc.name, fake.tgSaved.AdminChatSet, tc.wantSet)
		}
		if tc.wantID == nil {
			if fake.tgSaved.AdminChatID != nil {
				t.Fatalf("%s id %v", tc.name, fake.tgSaved.AdminChatID)
			}
		} else if fake.tgSaved.AdminChatID == nil || *fake.tgSaved.AdminChatID != *tc.wantID {
			t.Fatalf("%s id %v", tc.name, fake.tgSaved.AdminChatID)
		}
	}
}

func admPtrI64(v int64) *int64 { return &v }

func TestAdmTestTelegramBody(t *testing.T) {
	fake := &fakeNotify{}
	e, _ := p6Engine(fake, admActor)

	rec := admDoRaw(e, http.MethodPost, "/v1/settings/telegram/test", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty status %d %s", rec.Code, rec.Body.String())
	}
	if fake.tgTestChat == nil || *fake.tgTestChat != 0 {
		t.Fatalf("empty chat %v", fake.tgTestChat)
	}

	rec = admDo(e, http.MethodPost, "/v1/settings/telegram/test", `{"chat_id":123}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat status %d", rec.Code)
	}
	if fake.tgTestChat == nil || *fake.tgTestChat != 123 {
		t.Fatalf("chat %v", fake.tgTestChat)
	}
	if !strings.Contains(rec.Body.String(), `"sent":true`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestAdmTelegramWebhook(t *testing.T) {
	payload := []byte(`{"update_id":1}`)
	secret := "sec-token"

	fake := &fakeNotify{}
	e, k := p6Engine(fake, admActor)
	beforeAuth := k.authCalls
	rec := admDoRaw(e, http.MethodPost, "/v1/webhooks/telegram", payload, map[string]string{
		"X-Telegram-Bot-Api-Secret-Token": secret,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("ok status %d %s", rec.Code, rec.Body.String())
	}
	if fake.updSecret != secret || !bytes.Equal(fake.updBody, payload) {
		t.Fatalf("secret/body %q %q", fake.updSecret, fake.updBody)
	}
	if k.authCalls != beforeAuth {
		t.Fatalf("auth calls %d want %d", k.authCalls, beforeAuth)
	}

	fake.err = errs.New(errs.Unauthorized)
	rec = admDoRaw(e, http.MethodPost, "/v1/webhooks/telegram", payload, map[string]string{
		"X-Telegram-Bot-Api-Secret-Token": "bad",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status %d", rec.Code)
	}

	fake.err = nil
	big := make([]byte, telegramWebhookMaxBody+1)
	rec = admDoRaw(e, http.MethodPost, "/v1/webhooks/telegram", big, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("big body status %d", rec.Code)
	}
}
