// [INPUT]: 依赖 pgx 的业务事务读取生效主题，依赖 platform/httpx 的校验错误
// [OUTPUT]: 对外提供 DesignTokenKeys、TokenGroups、DefaultSiteName、SiteNameTx；包内提供 normalizeTokens、filterTokens
// [POS]: domain/appearance 的主题令牌规则：后端这一侧的 token 白名单（照抄前端 design-tokens.ts，测试守一致）与 light/dark 分组，service.go 的保存与门户读取都经过这里
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package appearance

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// DesignTokenKeys 是主题能覆盖的全部 CSS 变量，键即变量名（带 --）。
//
// 唯一来源是前端 panel/frontend/src/styles/design-tokens.ts 的 COLOR_TOKENS：
// 模块文件 html:root 的 33 个 + 设计规范页补充的 10 个（危险按钮字色、Toast
// 圆点、分段控件阴影、后台侧栏配色）。theme_seed_test.go 逐条对照那个文件，
// 两边改一边测试就会红。主题只管颜色：字号、间距、圆角是设计规范的骨架，
// 换主题不该让排版走样，所以不在白名单里。门户照这张表 setProperty(键, 值)。
var DesignTokenKeys = []string{
	"--bg", "--surface", "--surface-2", "--surface-3",
	"--border", "--border-strong", "--border-hover",
	"--text", "--text-2", "--text-3",
	"--brand", "--brand-hover", "--brand-ink", "--brand-soft", "--brand-tint", "--on-brand",
	"--ok", "--ok-soft", "--ok-line",
	"--warn", "--warn-soft", "--warn-line",
	"--danger", "--danger-soft", "--danger-line", "--danger-tint",
	"--info", "--info-soft", "--info-line",
	"--on-ink", "--hdr", "--scrim", "--shadow",
	"--on-danger", "--toast-ok", "--toast-danger", "--shadow-segment",
	"--sidebar", "--sidebar-active", "--sidebar-text", "--sidebar-text-2", "--sidebar-text-3", "--sidebar-brand",
}

// TokenGroups 是 tokens 的两个分组。主题值按深浅两套分开存：旧主题是一组
// 扁平值，用户切到暗色时照样被浅色值覆盖，这正是要消灭的问题。
var TokenGroups = []string{"light", "dark"}

// DefaultSiteName 是取不到生效主题站点名时的兜底。
const DefaultSiteName = "Pandora"

var (
	designTokenSet = func() map[string]bool {
		m := make(map[string]bool, len(DesignTokenKeys))
		for _, k := range DesignTokenKeys {
			m[k] = true
		}
		return m
	}()
	// 取值里出现这些字符就可能跳出 CSS 声明（;）、打开规则块（{}）或拼出标签（<>）
	tokenValueBad = regexp.MustCompile(`[;{}<>\\]`)
)

const maxTokenValueLen = 200

// normalizeTokens 校验管理员提交的 tokens，返回规范化后的 JSON。
//
// 形状只能是 {"light":{键:值}, "dark":{键:值}}，两组都可省略；键必须在
// DesignTokenKeys 里，值必须是不含危险字符的短字符串。不认识的一律拒绝而
// 不是静默丢掉：管理员以为生效了、页面上却没有，是最难查的那种问题。
func normalizeTokens(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var groups map[string]json.RawMessage
	if err := json.Unmarshal(raw, &groups); err != nil || groups == nil {
		return nil, httpx.Invalid(map[string]string{
			"tokens": `必须是 {"light":{…},"dark":{…}} 形状的对象`})
	}
	out := map[string]map[string]string{}
	for name, body := range groups {
		if name != "light" && name != "dark" {
			return nil, httpx.Invalid(map[string]string{
				"tokens": "只允许 light 与 dark 两组，不认识：" + name})
		}
		var vals map[string]any
		if err := json.Unmarshal(body, &vals); err != nil || vals == nil {
			return nil, httpx.Invalid(map[string]string{"tokens": name + " 必须是对象"})
		}
		group := map[string]string{}
		for k, v := range vals {
			if !designTokenSet[k] {
				return nil, httpx.Invalid(map[string]string{
					"tokens": name + "." + k + " 不是设计稿的变量名"})
			}
			s, ok := v.(string)
			s = strings.TrimSpace(s)
			if !ok || s == "" || len(s) > maxTokenValueLen || tokenValueBad.MatchString(s) {
				return nil, httpx.Invalid(map[string]string{
					"tokens": name + "." + k + " 的取值不合法（须为非空字符串，不含 ; { } < > \\）"})
			}
			group[k] = s
		}
		out[name] = group
	}
	b, err := json.Marshal(out)
	return b, err
}

// filterTokens 是门户读取时的防线：不论库里存了什么（旧主题的扁平键、
// 迁移前留下的自定义主题），只放行 light/dark 两组里白名单内的字符串值。
func filterTokens(raw json.RawMessage) json.RawMessage {
	var groups map[string]map[string]any
	_ = json.Unmarshal(raw, &groups)
	out := map[string]map[string]string{}
	for _, name := range TokenGroups {
		group := map[string]string{}
		for k, v := range groups[name] {
			if s, ok := v.(string); ok && designTokenSet[k] && !tokenValueBad.MatchString(s) {
				group[k] = s
			}
		}
		if len(group) > 0 {
			out[name] = group
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// SiteNameTx 返回生效主题 branding.site_name，取不到时是 DefaultSiteName。
//
// 站点名只有这一处来源：门户标题、邮件里的 {{site}}、发件人名都从这里取，
// 管理员在主题里改一次就处处生效。
func SiteNameTx(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	var name string
	err := tx.QueryRow(ctx, `
		SELECT coalesce(btrim(branding->>'site_name'), '')
		  FROM site_themes WHERE tenant_id = $1 AND is_active`, tenantID).Scan(&name)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if name == "" {
		return DefaultSiteName, nil
	}
	return name, nil
}
