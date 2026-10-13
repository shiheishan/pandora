package handler

import (
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

func ptlUID() uuid.UUID {
	return uuid.MustParse("018f0000-0000-7000-8000-000000000001")
}

func ptlDo(e http.Handler, method, path, body string) *httptest.ResponseRecorder {
	return doNotify(e, method, path, body)
}

func ptlJSONMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v body %s", err, rec.Body.String())
	}
	return m
}

func TestPortalListNotificationsLimitAndUnread(t *testing.T) {
	uid := ptlUID()
	fake := &fakeNotify{}
	e, _ := p6Engine(fake, uid)

	for _, tc := range []struct {
		path       string
		wantLimit  int
		wantUnread bool
	}{
		{"/v1/me/notifications", 30, false},
		{"/v1/me/notifications?limit=100", 100, false},
		{"/v1/me/notifications?limit=101", 30, false},
		{"/v1/me/notifications?unread=1", 30, true},
	} {
		fake.inboxLimit = 0
		fake.inboxUnread = false
		rec := ptlDo(e, http.MethodGet, tc.path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status %d", tc.path, rec.Code)
		}
		if fake.inboxUser != uid {
			t.Fatalf("%s user %v want %v", tc.path, fake.inboxUser, uid)
		}
		if fake.inboxLimit != tc.wantLimit {
			t.Fatalf("%s limit %d want %d", tc.path, fake.inboxLimit, tc.wantLimit)
		}
		if fake.inboxUnread != tc.wantUnread {
			t.Fatalf("%s unread %v want %v", tc.path, fake.inboxUnread, tc.wantUnread)
		}
	}
}

func TestPortalListNotificationsJSONShape(t *testing.T) {
	uid := ptlUID()
	id := uuid.MustParse("018f0000-0000-7000-8000-000000000099")
	sent := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fake := &fakeNotify{p6Fake: p6Fake{
		inbox: []service.InboxEntry{{
			ID: id, Code: "c", Subject: "s", Body: "b", SentAt: sent, ReadAt: nil,
		}},
		unread: 3,
	}}
	e, _ := p6Engine(fake, uid)
	rec := ptlDo(e, http.MethodGet, "/v1/me/notifications", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, k := range []string{"notifications", "unread"} {
		if !strings.Contains(body, "\""+k+"\"") {
			t.Fatalf("missing key %q: %s", k, body)
		}
	}
	var out listNotificationsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Unread != 3 || len(out.Notifications) != 1 {
		t.Fatalf("out %+v", out)
	}
	if out.Notifications[0].ReadAt != nil {
		t.Fatalf("read_at want nil")
	}
	if out.Notifications[0].SentAt == nil || !out.Notifications[0].SentAt.Equal(sent) {
		t.Fatalf("sent_at %+v", out.Notifications[0].SentAt)
	}

	fake.inbox = nil
	fake.unread = 0
	rec = ptlDo(e, http.MethodGet, "/v1/me/notifications", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"notifications":[]`) {
		t.Fatalf("empty list body %s", rec.Body.String())
	}
}

func TestPortalMarkNotificationRead(t *testing.T) {
	uid := ptlUID()
	fake := &fakeNotify{}
	e, _ := p6Engine(fake, uid)

	rec := ptlDo(e, http.MethodPost, "/v1/me/notifications/not-a-uuid/read", "")
	if rec.Code != http.StatusNotFound || errCode(t, rec) != "not_found" {
		t.Fatalf("bad id status %d code %s", rec.Code, errCode(t, rec))
	}

	id := uuid.MustParse("018f0000-0000-7000-8000-0000000000aa")
	rec = ptlDo(e, http.MethodPost, "/v1/me/notifications/"+id.String()+"/read", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if fake.readID != id {
		t.Fatalf("readID %v want %v", fake.readID, id)
	}
	var ok okResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil || !ok.OK {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestPortalPreferences(t *testing.T) {
	uid := ptlUID()
	fake := &fakeNotify{p6Fake: p6Fake{
		prefs: []service.Preference{{
			Category: "service", Channel: "email", Enabled: true, Locked: false,
		}},
	}}
	e, _ := p6Engine(fake, uid)

	rec := ptlDo(e, http.MethodGet, "/v1/me/notification-preferences", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status %d", rec.Code)
	}
	var prefs notificationPreferencesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &prefs); err != nil {
		t.Fatal(err)
	}
	if len(prefs.Preferences) != 1 || prefs.Preferences[0].Category != "service" {
		t.Fatalf("prefs %+v", prefs)
	}

	rec = ptlDo(e, http.MethodPut, "/v1/me/notification-preferences",
		`{"category":"marketing","channel":"telegram","enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status %d %s", rec.Code, rec.Body.String())
	}
	if fake.setPref == nil || fake.setPref.UserID != uid || fake.setPref.Category != "marketing" ||
		fake.setPref.Channel != "telegram" || fake.setPref.Enabled {
		t.Fatalf("setPref %+v", fake.setPref)
	}

	fake.err = errs.Invalid(map[string]string{"category": "x"})
	rec = ptlDo(e, http.MethodPut, "/v1/me/notification-preferences",
		`{"category":"marketing","channel":"email","enabled":true}`)
	if rec.Code != http.StatusUnprocessableEntity || errCode(t, rec) != "validation_failed" {
		t.Fatalf("invalid status %d code %s", rec.Code, errCode(t, rec))
	}
}

func TestPortalAnnouncementsShape(t *testing.T) {
	uid := ptlUID()
	pub := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	fake := &fakeNotify{p6Fake: p6Fake{
		portalAnns: []service.PortalAnnouncement{{
			ID:    uuid.MustParse("018f0000-0000-7000-8000-000000000010"),
			Title: "t", Body: "b", Severity: "info", Pinned: true, PublishedAt: pub,
		}},
	}}
	e, _ := p6Engine(fake, uid)
	rec := ptlDo(e, http.MethodGet, "/v1/me/announcements", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out portalAnnouncementsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Announcements) != 1 || !out.Announcements[0].PublishedAt.Equal(pub) {
		t.Fatalf("out %+v", out)
	}
	if !strings.Contains(rec.Body.String(), `"announcements":[`) {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestPortalTelegramAndBindCode(t *testing.T) {
	uid := ptlUID()
	exp := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	fake := &fakeNotify{p6Fake: p6Fake{
		tgBinding: service.TelegramBindingInfo{Bound: false, Enabled: true},
		bindCode:  service.BindCode{Code: "ABCD", BotUsername: "mybot", ExpiresAt: exp},
	}}
	e, _ := p6Engine(fake, uid)

	rec := ptlDo(e, http.MethodGet, "/v1/me/telegram", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("telegram status %d", rec.Code)
	}
	m := ptlJSONMap(t, rec)
	if _, ok := m["username"]; ok {
		t.Fatalf("unbound must omit username: %s", rec.Body.String())
	}
	if _, ok := m["bot_username"]; ok {
		t.Fatalf("empty bot_username must omit key: %s", rec.Body.String())
	}

	rec = ptlDo(e, http.MethodPost, "/v1/me/telegram/bind-code", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("bind-code status %d", rec.Code)
	}
	var bc bindCodeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &bc); err != nil {
		t.Fatal(err)
	}
	if bc.Code != "ABCD" || bc.BotUsername != "mybot" || !bc.ExpiresAt.Equal(exp) {
		t.Fatalf("bc %+v", bc)
	}

	rec = ptlDo(e, http.MethodDelete, "/v1/me/telegram", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unbind status %d", rec.Code)
	}
	if !fake.unbound {
		t.Fatal("unbound not called")
	}
	var ub telegramUnbindResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &ub); err != nil || !ub.Unbound {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestPortalUnauthorizedWithoutPrincipal(t *testing.T) {
	fake := &fakeNotify{}
	e, _ := p6Engine(fake, uuid.Nil)

	paths := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/me/announcements", ""},
		{http.MethodGet, "/v1/me/notifications", ""},
		{http.MethodPost, "/v1/me/notifications/read-all", ""},
		{http.MethodPost, "/v1/me/notifications/" + uuid.New().String() + "/read", ""},
		{http.MethodGet, "/v1/me/notification-preferences", ""},
		{http.MethodPut, "/v1/me/notification-preferences", `{"category":"service","channel":"email","enabled":true}`},
		{http.MethodGet, "/v1/me/telegram", ""},
		{http.MethodPost, "/v1/me/telegram/bind-code", ""},
		{http.MethodDelete, "/v1/me/telegram", ""},
	}
	for _, c := range paths {
		rec := ptlDo(e, c.method, c.path, c.body)
		if rec.Code != http.StatusUnauthorized || errCode(t, rec) != "unauthorized" {
			t.Fatalf("%s %s status %d code %s", c.method, c.path, rec.Code, errCode(t, rec))
		}
	}
}
