package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// p6Fake 是 fakeNotify 里站内信、偏好、公告、Telegram 部分的假实现：返回预设值、记下收到的参数；err 非 nil 时所有方法都返回它。
type p6Fake struct {
	err error

	// 公告
	adminAnns    service.AdminAnnouncements
	savedIn      *service.AnnouncementInput
	saved        service.AnnouncementSaved
	withdrawID   uuid.UUID
	withdrawVer  int
	withdrawnVer int
	portalAnns   []service.PortalAnnouncement

	// 站内信与偏好
	inbox       []service.InboxEntry
	unread      int64
	inboxUser   uuid.UUID
	inboxUnread bool
	inboxLimit  int
	readID      uuid.UUID
	readAll     bool
	prefs       []service.Preference
	setPref     *p6PrefCall

	// Telegram
	tgSettings   service.TelegramSettingsView
	tgSaved      *service.TelegramSettingsInput
	tgExtras     settingssvc.TelegramExtras
	tgExtrasSave *service.TelegramExtrasInput
	tgTestChat   *int64
	tgBinding    service.TelegramBindingInfo
	bindCode     service.BindCode
	unbound      bool
	updSecret    string
	updBody      []byte
	updateCalls  int
}

// p6PrefCall 是一次 SetPreference 的参数。
type p6PrefCall struct {
	UserID   uuid.UUID
	Category string
	Channel  string
	Enabled  bool
}

func (f *p6Fake) ListAnnouncements(context.Context) (service.AdminAnnouncements, error) {
	return f.adminAnns, f.err
}

func (f *p6Fake) SaveAnnouncement(_ context.Context, in service.AnnouncementInput) (service.AnnouncementSaved, error) {
	f.savedIn = &in
	return f.saved, f.err
}

func (f *p6Fake) WithdrawAnnouncement(_ context.Context, id uuid.UUID, expectedVersion int) (int, error) {
	f.withdrawID, f.withdrawVer = id, expectedVersion
	return f.withdrawnVer, f.err
}

func (f *p6Fake) MyAnnouncements(context.Context, uuid.UUID) ([]service.PortalAnnouncement, error) {
	return f.portalAnns, f.err
}

func (f *p6Fake) Inbox(_ context.Context, userID uuid.UUID, onlyUnread bool, limit int) ([]service.InboxEntry, int64, error) {
	f.inboxUser, f.inboxUnread, f.inboxLimit = userID, onlyUnread, limit
	return f.inbox, f.unread, f.err
}

func (f *p6Fake) MarkInboxRead(_ context.Context, _, id uuid.UUID) error {
	f.readID = id
	return f.err
}

func (f *p6Fake) MarkAllInboxRead(context.Context, uuid.UUID) error {
	f.readAll = true
	return f.err
}

func (f *p6Fake) Preferences(context.Context, uuid.UUID) ([]service.Preference, error) {
	return f.prefs, f.err
}

func (f *p6Fake) SetPreference(_ context.Context, userID uuid.UUID, category, channel string, enabled bool) error {
	f.setPref = &p6PrefCall{UserID: userID, Category: category, Channel: channel, Enabled: enabled}
	return f.err
}

func (f *p6Fake) TelegramSettings(context.Context) (service.TelegramSettingsView, error) {
	return f.tgSettings, f.err
}

func (f *p6Fake) SaveTelegramSettings(_ context.Context, in service.TelegramSettingsInput) error {
	f.tgSaved = &in
	return f.err
}

func (f *p6Fake) TestTelegram(_ context.Context, chatID int64) error {
	f.tgTestChat = &chatID
	return f.err
}

func (f *p6Fake) TelegramExtras(context.Context) (settingssvc.TelegramExtras, error) {
	return f.tgExtras, f.err
}

func (f *p6Fake) SaveTelegramExtras(_ context.Context, in service.TelegramExtrasInput) error {
	f.tgExtrasSave = &in
	return f.err
}

func (f *p6Fake) TelegramBinding(context.Context, uuid.UUID) (service.TelegramBindingInfo, error) {
	return f.tgBinding, f.err
}

func (f *p6Fake) IssueBindCode(context.Context, uuid.UUID) (service.BindCode, error) {
	return f.bindCode, f.err
}

func (f *p6Fake) UnbindTelegram(context.Context, uuid.UUID) error {
	f.unbound = true
	return f.err
}

func (f *p6Fake) HandleTelegramUpdate(_ context.Context, secret string, body []byte) error {
	f.updSecret, f.updBody = secret, body
	f.updateCalls++
	return f.err
}

// p6Engine 搭一个同时注册门户与后台路由的引擎；门户 RequireAuth 把 uid 作为登录主体放进请求（uid 为 uuid.Nil 时不放）。
func p6Engine(fake Notify, uid uuid.UUID) (*gin.Engine, *notifyTestKit) {
	gin.SetMode(gin.TestMode)
	k := &notifyTestKit{}
	kit := k.kit()
	auth := kit.RequireAuth
	kit.RequireAuth = func(c *gin.Context) {
		if uid != uuid.Nil {
			authn.Set(c, authn.Principal{UserID: uid, Audience: authn.AudiencePublic})
		}
		auth(c)
	}
	h := New(fake)
	e := gin.New()
	e.Use(httpx.Errors())
	h.RegisterPublic(e.Group("/v1"), kit)
	h.RegisterAdmin(e.Group("/v1"), kit)
	return e, k
}
