package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/appearance/repo"
	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	settingsvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
)

type apFakeSite struct {
	name     string
	keywords string
	allow    bool
	url      string
}

func (f apFakeSite) Site(context.Context) (settingsvc.Site, error) {
	return settingsvc.Site{
		Name: f.name, Timezone: "UTC",
		SEOKeywords: f.keywords, AllowIndexing: f.allow,
	}, nil
}

func (f apFakeSite) SiteURL(context.Context) string { return f.url }

type apEnv struct {
	row   func(ctx context.Context, q string, args ...any) *sql.Row
	txCtx func() context.Context
	svc   *Service
}

func newApEnv(t *testing.T) *apEnv {
	return newApEnvWithSite(t, nil)
}

func newApEnvWithSite(t *testing.T, site SiteSettings) *apEnv {
	t.Helper()
	d := testkit.DB(t)
	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x01
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	txm := tx.NewManager(d)
	svc := New(repo.New(d), txm, auditSvc, site)
	return &apEnv{
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		txCtx: func() context.Context { return testkit.TxCtx(t, d) },
		svc:   svc,
	}
}

func apAdminCtx(ctx context.Context, adminID uuid.UUID) context.Context {
	ctx = authn.With(ctx, authn.Principal{
		UserID:    adminID,
		SessionID: "s-admin",
		Audience:  authn.AudienceAdmin,
	})
	return reqid.With(ctx, reqid.New())
}

func apNewUser(t *testing.T, e *apEnv, ctx context.Context) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	email := "appearance-" + strings.ReplaceAll(uuid.New().String(), "-", "") + "@example.test"
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func apErrCode(err error) errs.Code {
	if err == nil {
		return ""
	}
	return errs.As(err).Code
}

func TestAppearanceSeedAndPublic(t *testing.T) {
	e := newApEnv(t)
	ctx := e.txCtx()

	themes, err := e.svc.ListThemes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var paper *Theme
	for i := range themes {
		if themes[i].Code == "paper" {
			paper = &themes[i]
			break
		}
	}
	if paper == nil || !paper.IsBuiltin || !paper.IsActive {
		t.Fatalf("paper: %+v", paper)
	}

	pub, err := e.svc.Public(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Theme == nil || pub.Theme.Code != "paper" {
		t.Fatalf("public theme: %+v", pub.Theme)
	}
	var tokens map[string]map[string]string
	if err := json.Unmarshal(pub.Theme.Tokens, &tokens); err != nil {
		t.Fatal(err)
	}
	if tokens["light"] == nil || tokens["dark"] == nil {
		t.Fatalf("tokens groups: %v", tokens)
	}
	var branding map[string]string
	if err := json.Unmarshal(pub.Theme.Branding, &branding); err != nil {
		t.Fatal(err)
	}
	if _, ok := branding["site_name"]; ok {
		t.Fatalf("site_name 不应在 branding 中: %v", branding)
	}
	if pub.SiteName != "Pandora" {
		t.Fatalf("pub.SiteName=%q, want Pandora", pub.SiteName)
	}
}

func TestAppearanceThemeSaveAndConflict(t *testing.T) {
	e := newApEnv(t)
	ctx := e.txCtx()
	actx := apAdminCtx(ctx, apNewUser(t, e, ctx))

	// 输入本身合法，被拒只因为 paper 是内置主题
	err := e.svc.SaveTheme(actx, SaveThemeInput{Code: "paper", Name: "改", Create: false,
		Branding: json.RawMessage(`{}`)})
	if ae := errs.As(err); ae.Code != errs.ValidationFailed || len(ae.Fields) != 0 {
		t.Fatalf("改内置: %v", err)
	}

	err = e.svc.SaveTheme(actx, SaveThemeInput{
		Code: "my-theme", Name: "我的", Create: true,
		Branding: json.RawMessage(`{"tagline":"一路畅通"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	err = e.svc.SaveTheme(actx, SaveThemeInput{
		Code: "my-theme", Name: "别的", Create: true,
		Branding: json.RawMessage(`{"tagline":"一路畅通"}`),
	})
	if apErrCode(err) != errs.Conflict || errs.As(err).Fields["code"] == "" {
		t.Fatalf("重复 Create: %v", err)
	}

	err = e.svc.SaveTheme(actx, SaveThemeInput{
		Code: "my-theme", Name: "覆盖名", Create: false,
		Branding: json.RawMessage(`{"tagline":"一路畅通"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	themes, err := e.svc.ListThemes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Code == "my-theme" && th.Name != "覆盖名" {
			t.Fatalf("name=%q", th.Name)
		}
	}
}

func TestAppearanceActivateTheme(t *testing.T) {
	e := newApEnv(t)
	ctx := e.txCtx()
	actx := apAdminCtx(ctx, apNewUser(t, e, ctx))

	err := e.svc.SaveTheme(actx, SaveThemeInput{
		Code: "my-theme", Name: "我的", Create: true,
		Branding: json.RawMessage(`{"tagline":"一路畅通"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ActivateTheme(actx, "my-theme"); err != nil {
		t.Fatal(err)
	}
	pub, err := e.svc.Public(ctx)
	if err != nil || pub.Theme == nil || pub.Theme.Code != "my-theme" {
		t.Fatalf("public after activate: %+v err=%v", pub.Theme, err)
	}
	var activeN int64
	if err := e.row(ctx, `SELECT count(*) FROM site_themes WHERE is_active`).Scan(&activeN); err != nil {
		t.Fatal(err)
	}
	if activeN != 1 {
		t.Fatalf("active count=%d", activeN)
	}

	if err := e.svc.ActivateTheme(actx, "no-such-theme"); apErrCode(err) != errs.NotFound {
		t.Fatalf("missing theme: %v", err)
	}
	pub2, err := e.svc.Public(ctx)
	if err != nil || pub2.Theme == nil || pub2.Theme.Code != "my-theme" {
		t.Fatalf("theme unchanged: %+v", pub2.Theme)
	}
}

func TestAppearanceDeleteTheme(t *testing.T) {
	e := newApEnv(t)
	ctx := e.txCtx()
	actx := apAdminCtx(ctx, apNewUser(t, e, ctx))

	err := e.svc.SaveTheme(actx, SaveThemeInput{
		Code: "my-theme", Name: "我的", Create: true,
		Branding: json.RawMessage(`{"tagline":"一路畅通"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ActivateTheme(actx, "my-theme"); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.DeleteTheme(actx, "paper"); apErrCode(err) != errs.ValidationFailed {
		t.Fatalf("删内置: %v", err)
	}
	if err := e.svc.DeleteTheme(actx, "my-theme"); apErrCode(err) != errs.ValidationFailed {
		t.Fatalf("删生效: %v", err)
	}

	if err := e.svc.ActivateTheme(actx, "paper"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteTheme(actx, "my-theme"); err != nil {
		t.Fatal(err)
	}
	themes, err := e.svc.ListThemes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Code == "my-theme" {
			t.Fatal("my-theme 仍在列表")
		}
	}
}

func TestAppearanceSaveSlotAndPublic(t *testing.T) {
	e := newApEnv(t)
	ctx := e.txCtx()
	actx := apAdminCtx(ctx, apNewUser(t, e, ctx))

	raw := `<p onclick="x()">hi</p><script>alert(1)</script>`
	dropped, err := e.svc.SaveSlot(actx, SaveSlotInput{
		Key: "portal.plans.notice", Content: raw, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) == 0 {
		t.Fatal("期望 dropped 非空")
	}
	var stored string
	if err := e.row(ctx, `SELECT content FROM site_slots WHERE slot_key = ?`, "portal.plans.notice").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "script") || strings.Contains(stored, "onclick") {
		t.Fatalf("未净化: %q", stored)
	}

	slots, err := e.svc.ListSlots(ctx)
	if err != nil || len(slots) != 5 {
		t.Fatalf("ListSlots: len=%d (want 5) err=%v", len(slots), err)
	}
	var notice *Slot
	for i := range slots {
		if slots[i].Key == "portal.plans.notice" {
			notice = &slots[i]
			break
		}
	}
	if notice == nil || !notice.Enabled || notice.Content != stored || notice.UpdatedAt == "" {
		t.Fatalf("notice slot: %+v", notice)
	}
	if slots[0].UpdatedAt != "" {
		t.Fatalf("未保存的插槽应有空 UpdatedAt: %+v", slots[0])
	}

	pub, err := e.svc.Public(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Slots["portal.plans.notice"] != stored {
		t.Fatalf("public notice: %q", pub.Slots["portal.plans.notice"])
	}
	for k, v := range pub.Slots {
		if v == "" {
			t.Fatalf("public 含空内容 key=%s", k)
		}
	}

	_, err = e.svc.SaveSlot(actx, SaveSlotInput{
		Key: "portal.plans.notice", Content: stored, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	pub2, err := e.svc.Public(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pub2.Slots["portal.plans.notice"]; ok {
		t.Fatal("禁用后不应出现在 Public.Slots")
	}
}

func TestAppearancePageMetaReadsSiteSettings(t *testing.T) {
	e := newApEnvWithSite(t, apFakeSite{
		name: "夜海加速", keywords: "加速", allow: true, url: "https://panel.example.test/",
	})
	ctx := e.txCtx()
	actx := apAdminCtx(ctx, apNewUser(t, e, ctx))

	meta := e.svc.PageMeta(ctx)
	if meta.SiteName != "夜海加速" || meta.Tagline != "" {
		t.Fatalf("内置主题初始 PageMeta 期望 夜海加速//空，得到 %+v", meta)
	}
	if meta.SEOKeywords != "加速" || !meta.AllowIndexing || meta.SiteURL != "https://panel.example.test/" {
		t.Fatalf("SEO 与 SiteURL 期望 keywords=加速 allow=true url=https://panel.example.test/，得到 %+v", meta)
	}

	err := e.svc.SaveTheme(actx, SaveThemeInput{
		Code: "fast-ocean", Name: "夜海", Create: true,
		Branding: json.RawMessage(`{"tagline":"一路畅通"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ActivateTheme(actx, "fast-ocean"); err != nil {
		t.Fatal(err)
	}

	// TxCtx 整例回滚，提交后钩子（AfterCommit 里的快照失效）不执行：这里手动失效，模拟提交后的效果
	e.svc.invalidatePageMeta()
	metaAfter := e.svc.PageMeta(ctx)
	if metaAfter.Tagline != "一路畅通" {
		t.Fatalf("激活后 PageMeta 期望 一路畅通，得到 %+v", metaAfter)
	}

	pub, err := e.svc.Public(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pub.SiteName != "夜海加速" {
		t.Fatalf("pub.SiteName=%q, want 夜海加速", pub.SiteName)
	}
}

func TestAppearancePublicResanitizesStoredSlot(t *testing.T) {
	e := newApEnv(t)
	ctx := e.txCtx()
	actx := apAdminCtx(ctx, apNewUser(t, e, ctx))

	_, err := e.svc.SaveSlot(actx, SaveSlotInput{
		Key: "portal.plans.notice", Content: `<p>选购页说明</p>`, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	var k string
	if err := e.row(ctx, `UPDATE site_slots SET content = ? WHERE slot_key = ? RETURNING slot_key`,
		`<p onclick="x()">改过</p><img src="https://cdn.example.com/a.png">`, "portal.plans.notice").Scan(&k); err != nil {
		t.Fatal(err)
	}

	pub, err := e.svc.Public(ctx)
	if err != nil {
		t.Fatal(err)
	}
	notice := pub.Slots["portal.plans.notice"]
	if !strings.Contains(notice, "改过") {
		t.Fatalf("public notice: %q", notice)
	}
	if strings.Contains(notice, "onclick") {
		t.Fatalf("不应含 onclick: %q", notice)
	}
	if strings.Contains(notice, "cdn.example.com") {
		t.Fatalf("img 外链应被去掉: %q", notice)
	}

	if err := e.row(ctx, `UPDATE site_slots SET content = ? WHERE slot_key = ? RETURNING slot_key`,
		`<script>alert(1)</script>`, "portal.plans.notice").Scan(&k); err != nil {
		t.Fatal(err)
	}

	// TxCtx 整例回滚，SaveSlot 的提交后钩子不执行；这里靠原文比较让备忘重算，正好也验证兜底。
	pub2, err := e.svc.Public(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pub2.Slots["portal.plans.notice"]; ok {
		t.Fatalf("纯 script 再净化后不应下发: %v", pub2.Slots)
	}
}

func TestAppearanceRejectsLogoPG(t *testing.T) {
	e := newApEnv(t)
	ctx := e.txCtx()
	actx := apAdminCtx(ctx, apNewUser(t, e, ctx))

	err := e.svc.SaveTheme(actx, SaveThemeInput{
		Code: "theme-with-logo", Name: "带Logo", Create: true,
		Branding: json.RawMessage(`{"tagline":"x","logo":"data:image/png;base64,AAAA"}`),
	})
	if err == nil {
		t.Fatal("期望 SaveTheme 拒绝 logo，但未报错")
	}
	ae := errs.As(err)
	if ae.Code != errs.ValidationFailed {
		t.Fatalf("期望 422 validation_failed, 得到 %v", ae.Code)
	}
	if ae.Fields["branding"] == "" {
		t.Fatalf("期望 fields.branding 非空，得到 fields: %v", ae.Fields)
	}

	var count int64
	if err := e.row(ctx, `SELECT count(*) FROM site_themes WHERE code = ?`, "theme-with-logo").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("库里不应存在该主题，count=%d", count)
	}
}
