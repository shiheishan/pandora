package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func ptrBool(b bool) *bool { return &b }

func TestAdmTelegramExtrasGET(t *testing.T) {
	chatID := int64(-100123456789)
	fake := &fakeNotify{p6Fake: p6Fake{
		tgExtras: settingssvc.TelegramExtras{
			SubCommand:        true,
			StartMessage:      "hello start",
			AdminPaidNotify:   true,
			AdminTicketNotify: false,
			JoinReview:        true,
			UserGroupChatID:   &chatID,
			DiscussLink:       "https://t.me/discuss",
		},
	}}
	e, _ := p6Engine(fake, admActor)
	rec := admDo(e, http.MethodGet, "/v1/settings/telegram/extras", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var res telegramExtrasResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.SubCommand || res.StartMessage != "hello start" || !res.AdminPaidNotify || res.AdminTicketNotify || !res.JoinReview {
		t.Fatalf("unexpected fields: %+v", res)
	}
	if res.UserGroupChatID == nil || *res.UserGroupChatID != chatID {
		t.Fatalf("user_group_chat_id want %d, got %v", chatID, res.UserGroupChatID)
	}
	if res.DiscussLink != "https://t.me/discuss" {
		t.Fatalf("discuss_link want https://t.me/discuss, got %q", res.DiscussLink)
	}
}

func TestAdmTelegramExtrasPOST(t *testing.T) {
	// 1. 只传 {"sub_command_enabled": true}
	{
		fake := &fakeNotify{p6Fake: p6Fake{
			tgExtras: settingssvc.TelegramExtras{
				SubCommand: true,
			},
		}}
		e, k := p6Engine(fake, admActor)
		rec := admDo(e, http.MethodPost, "/v1/settings/telegram/extras", `{"sub_command_enabled": true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d %s", rec.Code, rec.Body.String())
		}
		if k.reauthCalls != 1 {
			t.Fatalf("expected 1 reauth call, got %d", k.reauthCalls)
		}
		if fake.tgExtrasSave == nil {
			t.Fatal("tgExtrasSave is nil")
		}
		if fake.tgExtrasSave.SubCommand == nil || !*fake.tgExtrasSave.SubCommand {
			t.Fatalf("sub_command_enabled want true, got %v", fake.tgExtrasSave.SubCommand)
		}
		if fake.tgExtrasSave.StartMessage != nil || fake.tgExtrasSave.AdminPaidNotify != nil ||
			fake.tgExtrasSave.AdminTicketNotify != nil || fake.tgExtrasSave.JoinReview != nil ||
			fake.tgExtrasSave.DiscussLink != nil {
			t.Fatalf("other pointers should be nil: %+v", fake.tgExtrasSave)
		}
		if fake.tgExtrasSave.UserGroupChatSet {
			t.Fatal("UserGroupChatSet should be false")
		}
	}

	// 2. "user_group_chat_id": null -> Set 为 true、ID 为 nil
	{
		fake := &fakeNotify{}
		e, _ := p6Engine(fake, admActor)
		rec := admDo(e, http.MethodPost, "/v1/settings/telegram/extras", `{"user_group_chat_id": null}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d %s", rec.Code, rec.Body.String())
		}
		if fake.tgExtrasSave == nil {
			t.Fatal("tgExtrasSave is nil")
		}
		if !fake.tgExtrasSave.UserGroupChatSet {
			t.Fatal("UserGroupChatSet want true")
		}
		if fake.tgExtrasSave.UserGroupChatID != nil {
			t.Fatalf("UserGroupChatID want nil, got %v", fake.tgExtrasSave.UserGroupChatID)
		}
	}

	// 3. 0 -> 422
	{
		fake := &fakeNotify{}
		e, _ := p6Engine(fake, admActor)
		rec := admDo(e, http.MethodPost, "/v1/settings/telegram/extras", `{"user_group_chat_id": 0}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status %d want 422 %s", rec.Code, rec.Body.String())
		}
		fields := admErrFields(t, rec)
		if fields["user_group_chat_id"] == "" {
			t.Fatalf("missing user_group_chat_id error field: %v", fields)
		}
	}
}

func TestAdminMeTelegramRoutesAndReauth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeNotify{p6Fake: p6Fake{
		tgBinding: service.TelegramBindingInfo{
			Bound:       true,
			Username:    "admin_tg",
			BotUsername: "admin_bot",
			Enabled:     true,
			DiscussLink: "https://t.me/admin_discuss",
		},
		bindCode: service.BindCode{
			Code:        "BIND1234",
			BotUsername: "admin_bot",
		},
	}}

	// 建立一个 RequireAuth / RecentReauth 支持检查请求头模拟重认证失败的 engine
	kit := route.Kit{
		RequireAuth: func(c *gin.Context) {
			if c.GetHeader("X-No-Auth") == "1" {
				httpx.Fail(c, errs.New(errs.Unauthorized))
				return
			}
			authn.Set(c, authn.Principal{UserID: admActor, Audience: authn.AudienceAdmin})
			c.Next()
		},
		RecentReauth: func(c *gin.Context) {
			if c.GetHeader("X-No-Reauth") == "1" {
				httpx.Fail(c, errs.New(errs.ReauthRequired))
				return
			}
			c.Next()
		},
	}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterAdmin(e.Group("/v1"), kit)

	// GET /v1/me/telegram
	rec := admDoRaw(e, http.MethodGet, "/v1/me/telegram", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/me/telegram status %d %s", rec.Code, rec.Body.String())
	}
	var bRes telegramBindingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &bRes); err != nil {
		t.Fatal(err)
	}
	if !bRes.Bound || bRes.Username != "admin_tg" || bRes.DiscussLink == nil || *bRes.DiscussLink != "https://t.me/admin_discuss" {
		t.Fatalf("GET /v1/me/telegram response: %+v", bRes)
	}

	// POST /v1/me/telegram/bind-code 没有重认证 -> 403 reauth_required
	rec = admDoRaw(e, http.MethodPost, "/v1/me/telegram/bind-code", []byte(`{}`), map[string]string{"X-No-Reauth": "1"})
	if rec.Code != http.StatusForbidden || errCode(t, rec) != "reauth_required" {
		t.Fatalf("bind-code without reauth status %d code %s", rec.Code, errCode(t, rec))
	}

	// POST /v1/me/telegram/bind-code 带重认证 -> 200
	rec = admDoRaw(e, http.MethodPost, "/v1/me/telegram/bind-code", []byte(`{}`), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/me/telegram/bind-code status %d %s", rec.Code, rec.Body.String())
	}
	var cRes bindCodeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &cRes); err != nil || cRes.Code != "BIND1234" {
		t.Fatalf("bind-code response: %+v", cRes)
	}

	// DELETE /v1/me/telegram -> 200
	rec = admDoRaw(e, http.MethodDelete, "/v1/me/telegram", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /v1/me/telegram status %d %s", rec.Code, rec.Body.String())
	}
	if !fake.unbound {
		t.Fatal("expected unbound to be true")
	}
}

func TestPortalMeTelegramDiscussLink(t *testing.T) {
	uid := ptlUID()

	// 1. DiscussLink 为空 -> 响应里 discuss_link 为 null
	{
		fake := &fakeNotify{p6Fake: p6Fake{
			tgBinding: service.TelegramBindingInfo{
				Bound:       false,
				Enabled:     true,
				DiscussLink: "",
			},
		}}
		e, _ := p6Engine(fake, uid)
		rec := ptlDo(e, http.MethodGet, "/v1/me/telegram", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d %s", rec.Code, rec.Body.String())
		}
		m := ptlJSONMap(t, rec)
		if m["discuss_link"] == nil || string(m["discuss_link"]) != "null" {
			t.Fatalf("discuss_link want null, got %s", m["discuss_link"])
		}
	}

	// 2. DiscussLink 非空 -> 响应里 discuss_link 为字符串
	{
		fake := &fakeNotify{p6Fake: p6Fake{
			tgBinding: service.TelegramBindingInfo{
				Bound:       true,
				Username:    "ptl_user",
				Enabled:     true,
				DiscussLink: "https://t.me/portal_group",
			},
		}}
		e, _ := p6Engine(fake, uid)
		rec := ptlDo(e, http.MethodGet, "/v1/me/telegram", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d %s", rec.Code, rec.Body.String())
		}
		var res telegramBindingResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if res.DiscussLink == nil || *res.DiscussLink != "https://t.me/portal_group" {
			t.Fatalf("discuss_link want https://t.me/portal_group, got %v", res.DiscussLink)
		}
	}
}
