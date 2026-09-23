package appearance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 主题与插槽的读写。
//
// 用户端每次打开页面都要读一次生效主题，所以读路径要便宜：
// 一次查询把主题和所有启用的插槽一起取回来，不做 N+1。

type Service struct {
	pool *db.Pool
}

func New(pool *db.Pool) *Service { return &Service{pool: pool} }

//------------------------------------------------------------------------------
// 类型
//------------------------------------------------------------------------------

type Theme struct {
	ID        string          `json:"id"`
	Code      string          `json:"code"`
	Name      string          `json:"name"`
	IsBuiltin bool            `json:"is_builtin"`
	IsActive  bool            `json:"is_active"`
	Tokens    json.RawMessage `json:"tokens"`
	Branding  json.RawMessage `json:"branding"`
	CustomCSS string          `json:"custom_css"`
}

type Slot struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Where     string `json:"where"`
	Content   string `json:"content"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// SlotCatalog 是插槽位的说明。
//
// 放在代码里而不是库里：这些位置对应前端模板里的挂载点，两者必须同步改。
// 存进库只会造成「后台列出一个前端根本不渲染的插槽」这种查半天的问题。
var SlotCatalog = []struct{ Key, Label, Where string }{
	{"portal.login.notice", "登录页提示", "登录框上方，未登录访客可见"},
	{"portal.home.banner", "概览页横幅", "概览页最顶部，通栏"},
	{"portal.home.aside", "概览页附加卡片", "概览页内容下方"},
	{"portal.sidebar.extra", "侧栏附加内容", "侧栏导航下方、账号信息上方"},
	{"portal.subscribe.notice", "订阅页说明", "「我的订阅」页顶部"},
	{"portal.plans.notice", "选购页说明", "「选购套餐」页顶部，适合放退换与发票说明"},
	{"portal.footer", "页脚", "所有页面底部"},
}

//------------------------------------------------------------------------------
// 用户端：一次取齐
//------------------------------------------------------------------------------

// PublicAppearance 是用户端渲染需要的全部外观数据。
type PublicAppearance struct {
	Theme *Theme            `json:"theme"`
	Slots map[string]string `json:"slots"`
}

func (s *Service) Public(ctx context.Context, tenantID string) (*PublicAppearance, error) {
	out := &PublicAppearance{Slots: map[string]string{}}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var t Theme
		err := tx.QueryRow(ctx, `
			SELECT id, code, name, is_builtin, is_active, tokens, branding, custom_css
			  FROM site_themes WHERE tenant_id = $1 AND is_active`, tenantID).
			Scan(&t.ID, &t.Code, &t.Name, &t.IsBuiltin, &t.IsActive,
				&t.Tokens, &t.Branding, &t.CustomCSS)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// 没有生效主题不是错误：站点就用前端内置的默认样式。
			// 这里返回 nil 而不是造一个空主题，前端凭 null 能分清
			// 「没配主题」和「配了一套空的」。
		case err != nil:
			return err
		default:
			out.Theme = &t
		}

		rows, err := tx.Query(ctx, `
			SELECT slot_key, content FROM site_slots
			 WHERE tenant_id = $1 AND enabled AND content <> ''`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k, c string
			if err := rows.Scan(&k, &c); err != nil {
				return err
			}
			out.Slots[k] = c
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

//------------------------------------------------------------------------------
// 管理端：主题
//------------------------------------------------------------------------------

func (s *Service) ListThemes(ctx context.Context, tenantID string) ([]Theme, error) {
	var out []Theme
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, code, name, is_builtin, is_active, tokens, branding, custom_css
			  FROM site_themes WHERE tenant_id = $1
			 ORDER BY is_builtin DESC, code`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Theme
			if err := rows.Scan(&t.ID, &t.Code, &t.Name, &t.IsBuiltin, &t.IsActive,
				&t.Tokens, &t.Branding, &t.CustomCSS); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

type SaveThemeInput struct {
	Code      string
	Name      string
	Tokens    json.RawMessage
	Branding  json.RawMessage
	CustomCSS string
	ActorID   string
}

// SaveTheme 新建或改一套主题，返回净化 CSS 时丢掉的东西。
//
// 内置主题不允许原地改：它们是「回到已知可用状态」的退路。
// 想基于内置改就复制一份 —— 复制在管理端是一次带新 code 的保存。
func (s *Service) SaveTheme(ctx context.Context, tenantID string, in SaveThemeInput) ([]string, error) {
	in.Code = strings.ToLower(strings.TrimSpace(in.Code))
	in.Name = strings.TrimSpace(in.Name)
	if in.Code == "" || in.Name == "" {
		return nil, httpx.Invalid(map[string]string{
			"code": "主题标识必填", "name": "主题名称必填"})
	}
	if len(in.Tokens) == 0 {
		in.Tokens = json.RawMessage(`{}`)
	}
	if len(in.Branding) == 0 {
		in.Branding = json.RawMessage(`{}`)
	}
	if !json.Valid(in.Tokens) || !json.Valid(in.Branding) {
		return nil, httpx.Invalid(map[string]string{"tokens": "不是合法的 JSON"})
	}

	css, notes := SanitizeCSS(in.CustomCSS)
	if len(css) > 65536 {
		return nil, httpx.Invalid(map[string]string{
			"custom_css": "自定义 CSS 超过 64KB，请精简"})
	}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID},
		func(tx pgx.Tx) error {
			var builtin bool
			err := tx.QueryRow(ctx,
				`SELECT is_builtin FROM site_themes WHERE tenant_id=$1 AND code=$2`,
				tenantID, in.Code).Scan(&builtin)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil && builtin {
				return httpx.New(httpx.CodeValidationFailed,
					"内置主题不能直接改，请用另一个标识另存为自定义主题")
			}

			if _, err := tx.Exec(ctx, `
				INSERT INTO site_themes (tenant_id, code, name, tokens, branding, custom_css)
				VALUES ($1,$2,$3,$4,$5,$6)
				ON CONFLICT (tenant_id, code) DO UPDATE
				   SET name=EXCLUDED.name, tokens=EXCLUDED.tokens,
				       branding=EXCLUDED.branding, custom_css=EXCLUDED.custom_css,
				       updated_at=now()`,
				tenantID, in.Code, in.Name, in.Tokens, in.Branding, css); err != nil {
				return err
			}
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &in.ActorID, Action: "appearance.theme.save",
				ResourceType: "site_theme", APIDomain: "admin",
				RequestID:   httpx.RequestIDFrom(ctx),
				AfterDigest: map[string]any{"code": in.Code, "css_bytes": len(css)},
			})
		})
	return notes, err
}

func (s *Service) ActivateTheme(ctx context.Context, tenantID, code, actorID string) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID},
		func(tx pgx.Tx) error {
			// 先全关再开一个。库上有部分唯一索引兜底，
			// 所以哪怕这两步之间出错，也不会出现两套同时生效。
			if _, err := tx.Exec(ctx,
				`UPDATE site_themes SET is_active=false, updated_at=now()
				  WHERE tenant_id=$1 AND is_active`, tenantID); err != nil {
				return err
			}
			ct, err := tx.Exec(ctx,
				`UPDATE site_themes SET is_active=true, updated_at=now()
				  WHERE tenant_id=$1 AND code=$2`, tenantID, code)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				return httpx.New(httpx.CodeNotFound, "主题不存在")
			}
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actorID, Action: "appearance.theme.activate",
				ResourceType: "site_theme", APIDomain: "admin",
				RequestID:   httpx.RequestIDFrom(ctx),
				AfterDigest: map[string]any{"code": code},
			})
		})
}

func (s *Service) DeleteTheme(ctx context.Context, tenantID, code, actorID string) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID},
		func(tx pgx.Tx) error {
			ct, err := tx.Exec(ctx,
				`DELETE FROM site_themes
				  WHERE tenant_id=$1 AND code=$2 AND NOT is_builtin AND NOT is_active`,
				tenantID, code)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				// 三种情况合成一句：不存在、是内置、正在生效。
				// 分开报没有价值 —— 管理员看得到列表，知道自己点的是哪个。
				return httpx.New(httpx.CodeValidationFailed,
					"删不掉：内置主题和正在生效的主题都不能删")
			}
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actorID, Action: "appearance.theme.delete",
				ResourceType: "site_theme", APIDomain: "admin",
				RequestID:    httpx.RequestIDFrom(ctx),
				BeforeDigest: map[string]any{"code": code},
			})
		})
}

//------------------------------------------------------------------------------
// 管理端：插槽
//------------------------------------------------------------------------------

func (s *Service) ListSlots(ctx context.Context, tenantID string) ([]Slot, error) {
	saved := map[string]Slot{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT slot_key, content, enabled, to_char(updated_at,'YYYY-MM-DD HH24:MI')
			  FROM site_slots WHERE tenant_id = $1`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x Slot
			if err := rows.Scan(&x.Key, &x.Content, &x.Enabled, &x.UpdatedAt); err != nil {
				return err
			}
			saved[x.Key] = x
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}

	// 以代码里的目录为准来拼列表：库里少了某一行（比如新加的插槽还没
	// 建过），后台照样列得出来，管理员填了就自动落库。
	out := make([]Slot, 0, len(SlotCatalog))
	for _, c := range SlotCatalog {
		x := saved[c.Key]
		x.Key, x.Label, x.Where = c.Key, c.Label, c.Where
		out = append(out, x)
	}
	return out, nil
}

type SaveSlotInput struct {
	Key     string
	Content string
	Enabled bool
	ActorID string
}

// SaveSlot 写入一个插槽，返回净化时丢掉的东西。
func (s *Service) SaveSlot(ctx context.Context, tenantID string, in SaveSlotInput) ([]string, error) {
	known := false
	for _, c := range SlotCatalog {
		if c.Key == in.Key {
			known = true
			break
		}
	}
	if !known {
		return nil, httpx.Invalid(map[string]string{"key": "未知的插槽位"})
	}

	clean, notes := SanitizeHTML(in.Content)
	if len(clean) > 32768 {
		return nil, httpx.Invalid(map[string]string{"content": "内容超过 32KB，请精简"})
	}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID},
		func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `
				INSERT INTO site_slots (tenant_id, slot_key, content, enabled, updated_by)
				VALUES ($1,$2,$3,$4,nullif($5,'')::uuid)
				ON CONFLICT (tenant_id, slot_key) DO UPDATE
				   SET content=EXCLUDED.content, enabled=EXCLUDED.enabled,
				       updated_by=EXCLUDED.updated_by, updated_at=now()`,
				tenantID, in.Key, clean, in.Enabled, in.ActorID); err != nil {
				return err
			}
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &in.ActorID, Action: "appearance.slot.save",
				ResourceType: "site_slot", APIDomain: "admin",
				RequestID: httpx.RequestIDFrom(ctx),
				AfterDigest: map[string]any{
					"key": in.Key, "enabled": in.Enabled,
					"bytes": len(clean), "dropped": len(notes)},
			})
		})
	return notes, err
}
