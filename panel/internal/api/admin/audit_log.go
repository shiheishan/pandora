// [INPUT]: 依赖 domain/adminops 的 ListAudit / ExportAudit，依赖同包 profile.go 的 decryptIP 解来源 IP 密文，依赖 platform/httpx
// [OUTPUT]: 对外提供 listAudit、exportAudit 两个处理器与 auditExportRange 日期解析
// [POS]: api/admin 的审计日志处理器（后台-09「审计日志」表格与导出按钮）；导出挂 security.audit.read + ops.export + reauth，路由在 router.go 审计段
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func auditFilterFrom(q url.Values) adminops.AuditFilter {
	return adminops.AuditFilter{
		ActionPrefix: q.Get("action"),
		ActorKind:    q.Get("actor_kind"),
		Outcome:      q.Get("outcome"),
		Query:        q.Get("q"),
	}
}

// withSourceIPs 解出每行的来源 IP。与访问日志同权限、同做法：能看审计的人
// 本来就能在访问日志里看到明文 IP，这里不另设门槛。
func (h *handlers) withSourceIPs(rows []adminops.AuditRow) {
	for i := range rows {
		if ip := h.decryptIP(rows[i].SourceIPEnc); ip != "" {
			rows[i].SourceIP = &ip
		}
	}
}

func (h *handlers) listAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, total, err := h.d.Ops.ListAudit(r.Context(), httpx.TenantIDFrom(r.Context()),
		atoiDefault(q.Get("limit"), 50), atoiDefault(q.Get("offset"), 0), auditFilterFrom(q))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.withSourceIPs(rows)
	httpx.OK(w, map[string]any{"events": rows, "total": total})
}

// auditExportRange 解析导出的日期区间：YYYY-MM-DD，半开区间 [from, to+1 天)。
//
// 与订单列表同一种区间语义，但格式错了回 422 而不是当没填：订单列表错了
// 只是多看几行，导出错了会把全量记录带走。
func auditExportRange(q url.Values) (from, to *time.Time, err error) {
	parse := func(field string) (*time.Time, error) {
		raw := strings.TrimSpace(q.Get(field))
		if raw == "" {
			return nil, nil
		}
		t, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return nil, httpx.Invalid(map[string]string{field: "日期格式应为 YYYY-MM-DD"})
		}
		return &t, nil
	}
	if from, err = parse("from"); err != nil {
		return nil, nil, err
	}
	if to, err = parse("to"); err != nil {
		return nil, nil, err
	}
	if to != nil {
		next := to.AddDate(0, 0, 1)
		to = &next
	}
	if from != nil && to != nil && !from.Before(*to) {
		return nil, nil, httpx.Invalid(map[string]string{"to": "结束日期不能早于开始日期"})
	}
	return from, to, nil
}

// exportAudit 导出审计 CSV。整批读完、导出记录写进审计之后才开始输出：
// 查询失败时还能回一个正常的错误响应，而不是半截 CSV。
func (h *handlers) exportAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := auditFilterFrom(q)
	var err error
	if f.From, f.To, err = auditExportRange(q); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	rows, err := h.d.Ops.ExportAudit(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, f)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.withSourceIPs(rows)

	name := "audit-" + time.Now().UTC().Format("20060102-150405") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	// BOM：运营会用 Excel 打开，没有它中文是乱码（同用户导出）
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"occurred_at", "actor_kind", "actor_email", "action",
		"resource_type", "resource_id", "resource_label", "api_domain", "outcome",
		"reason", "source_ip", "auth_context"})
	for _, e := range rows {
		_ = cw.Write([]string{
			e.OccurredAt.UTC().Format(time.RFC3339), e.ActorKind, csvSafe(str(e.ActorEmail)),
			e.Action, str(e.ResourceType), str(e.ResourceID), csvSafe(str(e.ResourceLabel)),
			str(e.APIDomain), e.Outcome, csvSafe(str(e.Reason)), str(e.SourceIP),
			str(e.AuthContext),
		})
	}
}

// csvSafe 让自由文本在表格软件里不被当成公式：以 = + - @ 或控制字符开头的
// 单元格前加一个单引号。原因、对象名这些列都可能来自用户或管理员的输入，
// 导出文件会被直接双击打开。
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
