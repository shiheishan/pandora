package service

import (
	"context"
	"encoding/csv"
	"io"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/audit/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// Filter 是后台审计筛选；From/To 是半开区间 [From, To)，只有导出用。
type Filter struct {
	ActionPrefix, ActorKind, Outcome, Query string
	From, To                                *time.Time
}

// Page 是分页参数。
type Page struct {
	Limit, Offset int
}

// Row 是后台看到的一行。
type Row struct {
	ID            uuid.UUID
	OccurredAt    time.Time
	ActorKind     string
	ActorEmail    *string
	Action        string
	ResourceType  *string
	ResourceID    *uuid.UUID
	APIDomain     *string
	Outcome       string
	Reason        *string
	ResourceLabel *string
	AuthContext   *string
	SourceIP      *string
}

// ExportMax 是单次 CSV 导出行数上限。
const ExportMax = 50000

var csvHeader = []string{
	"occurred_at", "actor_kind", "actor_email", "action", "resource_type", "resource_id",
	"resource_label", "api_domain", "outcome", "reason", "source_ip", "auth_context",
}

// List 分页查询审计列表。
func (s *Service) List(ctx context.Context, f Filter, p Page) ([]Row, int64, error) {
	limit := p.Limit
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset := p.Offset
	if offset < 0 {
		offset = 0
	}

	q, err := s.query(ctx, f)
	if err != nil {
		return nil, 0, err
	}

	total, _, err := s.repo.Count(ctx, q)
	if err != nil {
		return nil, 0, err
	}

	recs, err := s.repo.List(ctx, q, limit, offset)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.enrich(ctx, recs)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ExportCSV 先计数（超过 ExportMax 回 422），再记 audit.export，然后写 CSV。
func (s *Service) ExportCSV(ctx context.Context, f Filter, open func() io.Writer) error {
	q, err := s.query(ctx, f)
	if err != nil {
		return err
	}

	n, asOf, err := s.repo.Count(ctx, q)
	if err != nil {
		return err
	}
	if n > ExportMax {
		return errs.Newf(errs.ValidationFailed, "超过 50000 行，请缩小时间范围")
	}

	exportData := map[string]any{
		"action":       strings.TrimSpace(f.ActionPrefix),
		"actor_kind":   strings.TrimSpace(f.ActorKind),
		"outcome":      strings.TrimSpace(f.Outcome),
		"q":            strings.TrimSpace(f.Query),
		"from":         formatDatePtr(f.From),
		"to_exclusive": formatDatePtr(f.To),
		"rows":         n,
	}
	if err := s.Write(ctx, Event{Action: "audit.export", Data: exportData}); err != nil {
		return err
	}

	q.AsOf = &asOf
	w := open()
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return err
	}

	var cursor *repo.Cursor
	for {
		recs, err := s.repo.ListAfter(ctx, q, cursor, 1000)
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			break
		}
		rows, err := s.enrich(ctx, recs)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := cw.Write(rowToCSV(row)); err != nil {
				return err
			}
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			return err
		}
		if len(recs) < 1000 {
			break
		}
		last := recs[len(recs)-1]
		cursor = &repo.Cursor{OccurredAt: last.OccurredAt, ID: last.ID}
	}
	return nil
}

// Purge 调用库函数批量删除过期审计。
func (s *Service) Purge(ctx context.Context, before time.Time, batch int) (int, error) {
	return s.repo.Purge(ctx, before, batch)
}

func (s *Service) query(ctx context.Context, f Filter) (repo.Query, error) {
	q := repo.Query{
		ActionPrefix: strings.TrimSpace(f.ActionPrefix),
		ActorKind:    strings.TrimSpace(f.ActorKind),
		Outcome:      strings.TrimSpace(f.Outcome),
		From:         f.From,
		To:           f.To,
	}
	text := strings.TrimSpace(f.Query)
	if text != "" {
		q.Text = text
		if id, err := uuid.Parse(text); err == nil {
			q.TextResourceID = &id
		}
		if s.actors != nil {
			ids, err := s.actors.IDsByEmail(ctx, text, 1000)
			if err != nil {
				return repo.Query{}, err
			}
			q.TextUserIDs = ids
		}
	}
	return q, nil
}

func (s *Service) enrich(ctx context.Context, recs []repo.Record) ([]Row, error) {
	actorSet := map[uuid.UUID]struct{}{}
	for _, r := range recs {
		if r.ActorID != nil {
			actorSet[*r.ActorID] = struct{}{}
		}
	}
	actorIDs := make([]uuid.UUID, 0, len(actorSet))
	for id := range actorSet {
		actorIDs = append(actorIDs, id)
	}

	emails := map[uuid.UUID]string{}
	if s.actors != nil && len(actorIDs) > 0 {
		m, err := s.actors.Emails(ctx, actorIDs)
		if err != nil {
			return nil, err
		}
		emails = m
	}

	byType := map[string][]uuid.UUID{}
	for _, r := range recs {
		if r.ResourceType == nil || r.ResourceID == nil {
			continue
		}
		if s.labels[*r.ResourceType] == nil {
			continue
		}
		byType[*r.ResourceType] = appendUniqueUUID(byType[*r.ResourceType], *r.ResourceID)
	}

	labelsByType := map[string]map[uuid.UUID]string{}
	for rt, ids := range byType {
		l := s.labels[rt]
		if l == nil {
			continue
		}
		m, err := l(ctx, ids)
		if err != nil {
			return nil, err
		}
		labelsByType[rt] = m
	}

	rows := make([]Row, len(recs))
	for i, r := range recs {
		row := Row{
			ID:           r.ID,
			OccurredAt:   r.OccurredAt,
			ActorKind:    r.ActorKind,
			Action:       r.Action,
			ResourceType: r.ResourceType,
			ResourceID:   r.ResourceID,
			APIDomain:    r.ActorAudience,
			Outcome:      r.Outcome,
			Reason:       r.Reason,
			AuthContext:  r.AuthContext,
		}
		if r.ActorID != nil {
			if em, ok := emails[*r.ActorID]; ok && em != "" {
				row.ActorEmail = &em
			}
		}
		if r.ResourceType != nil && r.ResourceID != nil {
			if m, ok := labelsByType[*r.ResourceType]; ok {
				if lbl, ok := m[*r.ResourceID]; ok && lbl != "" {
					row.ResourceLabel = &lbl
				}
			}
		}
		if len(r.IPEnc) > 0 {
			plain, err := s.sealer.Open(r.IPEnc, ipAAD)
			if err == nil {
				ip := string(plain)
				row.SourceIP = &ip
			}
		}
		rows[i] = row
	}
	return rows, nil
}

func rowToCSV(row Row) []string {
	return []string{
		row.OccurredAt.UTC().Format(time.RFC3339),
		row.ActorKind,
		csvSafe(ptrStr(row.ActorEmail)),
		row.Action,
		ptrStr(row.ResourceType),
		uuidStr(row.ResourceID),
		csvSafe(ptrStr(row.ResourceLabel)),
		ptrStr(row.APIDomain),
		row.Outcome,
		csvSafe(ptrStr(row.Reason)),
		ptrStr(row.SourceIP),
		ptrStr(row.AuthContext),
	}
}

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func uuidStr(p *uuid.UUID) string {
	if p == nil {
		return ""
	}
	return p.String()
}

func formatDatePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format("2006-01-02")
}

func appendUniqueUUID(slice []uuid.UUID, id uuid.UUID) []uuid.UUID {
	for _, x := range slice {
		if x == id {
			return slice
		}
	}
	return append(slice, id)
}

// csvSafe 防止表格软件把单元格当公式。
func csvSafe(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	default:
		return s
	}
}
