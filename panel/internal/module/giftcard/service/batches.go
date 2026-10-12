package service

// 批次是「一次生成的一批码」。卡密是等价现金，完整明文只出站两次：生码响应的样例，和一次性导出的那份文件。
// 码表只存哈希与掩码；整批明文封存在批次行上，导出时在同一事务里清空，导出文件丢了就是丢了（设计明确接受的运营风险）。

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/giftcard/model"
	"github.com/aegispanel/aegis/internal/module/giftcard/repo"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// maxCollisionRounds 是生码时撞哈希后重抽的最多轮数；码空间 31^12，正常一轮就够。
const maxCollisionRounds = 5

// 批次列表的分页口径（与旧接口相同）。
const (
	BatchListDefault = 50
	BatchListMax     = 200
)

// GenerateCodes 给一个模板批量生码：先插码（只存哈希与掩码，撞了哈希就重抽），再建批次（整批明文封存在批次上；
// 码到批次的外键延迟到提交时检查）。返回前 4 张明文作样例与批次视图。已归档的模板不能再生码。
// ctx 里有幂等认领时，在同一事务里用 in.Reply 的响应完成认领并绑定批次（Reply 负责去掉明文样例，重放拿不到明文）。
func (s *Service) GenerateCodes(ctx context.Context, in GenerateInput) (Generated, error) {
	p, err := adminPrincipal(ctx)
	if err != nil {
		return Generated{}, err
	}
	fields := map[string]string{}
	if in.Count < 1 || in.Count > maxGenerateCount {
		fields["count"] = "一次生成 1 到 5000 个。要更多就分批 —— 单次几万个会把事务拖很久"
	}
	prefix, ok := normalizePrefix(in.Prefix)
	if !ok {
		fields["prefix"] = "前缀只能用大写字母和数字，最多 8 位"
	}
	now := s.clock()
	var expires *time.Time
	if in.ExpiresAt != nil {
		e := in.ExpiresAt.UTC().Truncate(time.Microsecond)
		if !e.After(now) {
			fields["expires_at"] = "有效期不能设在过去"
		}
		expires = &e
	}
	if len(fields) > 0 {
		return Generated{}, errs.Invalid(fields)
	}

	var out Generated
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		t, found, err := s.repo.TemplateByID(ctx, in.TemplateID, false)
		if err != nil {
			return gcErr(err)
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		if t.Status == TemplateArchived {
			return errs.Newf(errs.Conflict, "已归档的礼品卡不能再生成新码")
		}
		batchID := uuid.Must(uuid.NewV7())
		plain, err := s.insertCodes(ctx, t.ID, batchID, prefix, in.Count, expires)
		if err != nil {
			return err
		}
		sealed, err := s.sealer.Seal(encodeCodes(plain), batchAAD(batchID))
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		admin := p.UserID
		if err := s.repo.CreateBatch(ctx, &model.Batch{
			ID:          batchID,
			TemplateID:  t.ID,
			Prefix:      prefix,
			Count:       in.Count,
			ExpiresAt:   expires,
			CodesSealed: sealed,
			CreatedBy:   &admin,
		}); err != nil {
			return gcErr(err)
		}
		b, err := s.batchView(ctx, batchID)
		if err != nil {
			return err
		}
		n := generateSampleSize
		if len(plain) < n {
			n = len(plain)
		}
		out = Generated{BatchID: batchID, Count: in.Count, Sample: append([]string(nil), plain[:n]...), Batch: b}
		tid := t.ID
		if err := s.writeAudit(ctx, ActionCodesGenerated, ResourceTemplate, &tid, map[string]any{
			"batch_id": batchID.String(), "count": in.Count, "prefix": prefix,
		}); err != nil {
			return err
		}
		if in.Reply == nil {
			return nil
		}
		return s.completeIdem(ctx, func() (idemsvc.Response, error) { return in.Reply(out) },
			&idemsvc.Resource{Type: ResourceBatch, ID: batchID})
	})
	if err != nil {
		return Generated{}, err
	}
	return out, nil
}

// insertCodes 生成 count 张不撞的码并插入（只存哈希与掩码），返回按生成顺序的明文。
// 撞了已有哈希的（ON CONFLICT DO NOTHING 跳过）重抽，最多 maxCollisionRounds 轮。
func (s *Service) insertCodes(ctx context.Context, templateID, batchID uuid.UUID, prefix string, count int, expires *time.Time) ([]string, error) {
	plain := make([]string, 0, count)
	seen := make(map[string]bool, count)
	for round := 0; len(plain) < count; round++ {
		if round >= maxCollisionRounds {
			return nil, errs.Newf(errs.Conflict, "生成卡密时连续撞码，请重试")
		}
		need := count - len(plain)
		rows := make([]model.Code, 0, need)
		codes := make([]string, 0, need)
		for len(codes) < need {
			c, err := newCode(prefix)
			if err != nil {
				return nil, errs.Wrap(errs.Internal, err)
			}
			if seen[c] {
				continue
			}
			seen[c] = true
			codes = append(codes, c)
			rows = append(rows, model.Code{
				ID:         uuid.Must(uuid.NewV7()),
				TemplateID: templateID,
				BatchID:    batchID,
				CodeHash:   s.hashCode(c),
				CodeMask:   MaskCode(c),
				Status:     CodeUnused,
				ExpiresAt:  expires,
			})
		}
		inserted, err := s.repo.InsertCodes(ctx, rows)
		if err != nil {
			return nil, gcErr(err)
		}
		for i, r := range rows {
			if inserted[r.ID] {
				plain = append(plain, codes[i])
			}
		}
	}
	return plain, nil
}

// ListBatches 后台批次列表：按生成时间倒序。
func (s *Service) ListBatches(ctx context.Context, f BatchFilter) ([]Batch, int64, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return nil, 0, err
	}
	rows, total, err := s.repo.ListBatches(ctx, f.TemplateID, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, gcErr(err)
	}
	out, err := s.batchViews(ctx, rows)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ExportBatch 一次性取出整批明文卡密，并在同一事务里把批次标记为已导出、清空密文、写审计。
// 标记与读取同生共死：任何一步失败，明文都不会离开事务。第二次回 409。
// ctx 里有幂等认领时在同一事务里把它记成「已完成但不可重放」：导出的 CSV 不进幂等表（那是明文的第二个落点），
// 同一个键的重试回 409，不会再执行。
func (s *Service) ExportBatch(ctx context.Context, batchID uuid.UUID) (BatchExport, error) {
	p, err := adminPrincipal(ctx)
	if err != nil {
		return BatchExport{}, err
	}
	var out BatchExport
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		b, found, err := s.repo.BatchByID(ctx, batchID, true)
		if err != nil {
			return gcErr(err)
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		if b.ExportedAt != nil || len(b.CodesSealed) == 0 {
			return ErrBatchAlreadyExported
		}
		raw, err := s.sealer.Open(b.CodesSealed, batchAAD(b.ID))
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		plain := decodeCodes(raw)
		states, err := s.repo.CodeStatesByBatch(ctx, b.ID)
		if err != nil {
			return gcErr(err)
		}
		byHash := make(map[string]repo.CodeState, len(states))
		for _, st := range states {
			byHash[string(st.CodeHash)] = st
		}
		ok, err := s.repo.MarkBatchExported(ctx, b.ID, p.UserID, s.clock())
		if err != nil {
			return gcErr(err)
		}
		if !ok {
			return ErrBatchAlreadyExported
		}
		view, err := s.batchView(ctx, b.ID)
		if err != nil {
			return err
		}
		rows := make([]ExportRow, 0, len(plain))
		for _, c := range plain {
			st, ok := byHash[string(s.hashCode(c))]
			if !ok {
				// 密文里的码在码表里找不到：封存与码表对不上，宁可整批失败也不导出一份对不上的文件
				return errs.Wrap(errs.Internal, errSealedMismatch)
			}
			rows = append(rows, ExportRow{Code: c, Status: st.Status, ExpiresAt: st.ExpiresAt, TemplateName: view.TemplateName})
		}
		out = BatchExport{Batch: view, Rows: rows}
		id := b.ID
		// 审计只记数量，不记任何码：审计表不能成为明文的第二个出口
		if err := s.writeAudit(ctx, ActionBatchExported, ResourceBatch, &id, map[string]any{
			"template_id": b.TemplateID.String(), "codes": len(rows),
		}); err != nil {
			return err
		}
		if claim, ok := idemsvc.ClaimFrom(ctx); ok {
			return s.idem.CompleteUnreplayable(ctx, claim, http.StatusOK)
		}
		return nil
	})
	if err != nil {
		return BatchExport{}, err
	}
	return out, nil
}

// batchView 读一个批次视图（含模板名、已用与已停用数、生成人与导出人邮箱）。
func (s *Service) batchView(ctx context.Context, id uuid.UUID) (Batch, error) {
	row, found, err := s.repo.BatchView(ctx, id)
	if err != nil {
		return Batch{}, gcErr(err)
	}
	if !found {
		return Batch{}, errs.New(errs.NotFound)
	}
	views, err := s.batchViews(ctx, []repo.BatchRow{row})
	if err != nil {
		return Batch{}, err
	}
	return views[0], nil
}

// batchViews 给一组批次补上生成人与导出人的邮箱（一次批量取）。
func (s *Service) batchViews(ctx context.Context, rows []repo.BatchRow) ([]Batch, error) {
	ids := make([]uuid.UUID, 0, len(rows)*2)
	for _, r := range rows {
		if r.CreatedBy != nil {
			ids = append(ids, *r.CreatedBy)
		}
		if r.ExportedBy != nil {
			ids = append(ids, *r.ExportedBy)
		}
	}
	emails, err := s.emails(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Batch, 0, len(rows))
	for _, r := range rows {
		out = append(out, Batch{
			ID:              r.ID,
			TemplateID:      r.TemplateID,
			TemplateName:    r.TemplateName,
			Prefix:          r.Prefix,
			Count:           r.Count,
			Used:            r.Used,
			Disabled:        r.Disabled,
			ExpiresAt:       r.ExpiresAt,
			CreatedByEmail:  emailPtr(emails, r.CreatedBy),
			CreatedAt:       r.CreatedAt,
			ExportedAt:      r.ExportedAt,
			ExportedByEmail: emailPtr(emails, r.ExportedBy),
		})
	}
	return out, nil
}

// emails 批量取邮箱；ids 为空不查。
func (s *Service) emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	ids = dedupeIDs(ids)
	if len(ids) == 0 {
		return map[uuid.UUID]string{}, nil
	}
	return s.users.Emails(ctx, ids)
}

// emailPtr 取 id 对应的邮箱；id 为 nil 或账号已不在时为 nil。
func emailPtr(emails map[uuid.UUID]string, id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	e, ok := emails[*id]
	if !ok {
		return nil
	}
	return &e
}

// completeIdem 在当前事务里完成幂等认领（ctx 里没有认领时什么都不做）；res 非 nil 时绑定到业务资源。
func (s *Service) completeIdem(ctx context.Context, build func() (idemsvc.Response, error), res *idemsvc.Resource) error {
	claim, ok := idemsvc.ClaimFrom(ctx)
	if !ok {
		return nil
	}
	resp, err := build()
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	if res != nil {
		return s.idem.CompleteBound(ctx, claim, resp, *res)
	}
	return s.idem.Complete(ctx, claim, resp)
}
