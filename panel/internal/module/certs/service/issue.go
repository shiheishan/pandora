package service

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/aegispanel/aegis/internal/module/certs/repo"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// IssueOrder 认领并签发一张订单（certs:issue 任务的入口）。订单已被认领或已结束时什么也不做。
func (s *Service) IssueOrder(ctx context.Context, orderID uuid.UUID) error {
	c, ok, err := s.store.ClaimOrder(ctx, orderID, orderLease)
	if err != nil || !ok {
		return err
	}
	err = s.runOrder(ctx, c)
	if errors.Is(err, errLeaseLost) {
		plog.From(ctx).Warn("证书订单已不归这次任务，丢弃结果", zap.String("order", orderID.String()))
		return nil
	}
	return err
}

// runOrder 处理一张认领到的订单：凭据预检 → 本地限额对账（必要时切备用 CA）→ ACME 账号 → lego 签发 → 落库。
// lego 没有 context，在另一个协程里跑：停机时不等它（订单留在 running，租约一过巡检按中断收掉），
// 超过 issueBudget 按 timeout 失败收单；协程之后才回来的结果因为 attempt 栅栏落不了库。
func (s *Service) runOrder(ctx context.Context, c repo.ClaimedOrder) error {
	log := plog.From(ctx)
	w, err := s.loadOrderWork(ctx, c)
	if err != nil {
		return err
	}
	if w.status != StatusPending && w.status != StatusActive {
		return s.finishFailure(ctx, c, failureOutcome{code: "not_active", cancel: true,
			detail: "证书已暂停或凭据失效，这一单作废"})
	}
	provider, err := s.newDNSProvider(w.cred.Provider, w.secret)
	if err != nil {
		return err
	}

	pctx, cancel := context.WithTimeout(ctx, time.Minute)
	_, checkErr := provider.check(pctx, w.cred.Zone, false)
	cancel()
	var credErr *credentialError
	switch {
	case errors.As(checkErr, &credErr):
		return s.finishFailure(ctx, c, failureOutcome{code: "credential_rejected", detail: credErr.msg,
			blockCredential: true, credRowVersion: w.cred.RowVersion, work: w})
	case checkErr != nil:
		if ctx.Err() != nil {
			return nil
		}
		return s.finishFailure(ctx, c, failureOutcome{code: "dns_api_unavailable", retryIn: dnsAPIBackoff,
			detail: "DNS 提供方 API 暂时不可用：" + providerErrorText(checkErr), work: w})
	}

	target := s.primaryTarget(w.cfg)
	renewal := w.currentIdentifiers != nil && slices.Equal(w.currentIdentifiers, w.identifiers)
	replaces := ""
	if c.ReplacesARIID != nil && renewal && w.currentCA != nil && *w.currentCA == target.CA {
		replaces = *c.ReplacesARIID
	}
	if d := checkLocalLimits(w.recent, w.identifiers, target.CA, renewal, replaces != "", w.now); d.blocked {
		fb := s.fallbackTarget(w.cfg)
		if fb == nil || (w.notAfter != nil && w.notAfter.Sub(w.now) >= fallbackBelow) {
			return s.finishFailure(ctx, c, failureOutcome{code: "rate_limited_local", detail: d.reason,
				retryIn: max(d.retryAt.Sub(w.now), time.Minute), work: w})
		}
		log.Warn("首选 CA 预计超限，改用备用 CA", zap.String("certificate", c.CertificateID.String()), zap.String("reason", d.reason))
		target, replaces = *fb, ""
	}

	acct, err := s.ensureAccount(ctx, target, w.cfg.ContactEmail)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		f := classifyFailure(err, &retryRecorder{})
		return s.finishFailure(ctx, c, failureOutcome{code: "acme_account_error", ca: target.CA,
			retryIn: defaultCA429, detail: "注册 ACME 账号失败：" + f.detail, work: w})
	}

	if replaces != "" && (w.currentAccountID == nil || *w.currentAccountID != acct.id) {
		replaces = ""
	}

	rec := &retryRecorder{}
	req := issueRequest{target: target, accountKey: acct.key, accountURL: acct.url, email: w.cfg.ContactEmail,
		provider: provider, identifiers: w.identifiers, keyType: w.keyType, replaces: replaces}
	type result struct {
		iss *issued
		err error
	}
	done := make(chan result, 1)
	go func() {
		iss, err := s.obtain(req, rec)
		if err == nil {
			if lerr := s.recordIssuance(ctx, c.CertificateID, target, w.identifiers, renewal, replaces != "", iss); lerr != nil {
				log.Error("证书签发流水写入失败（本地限额对账会少数这一张）",
					zap.String("certificate", c.CertificateID.String()), zap.Error(lerr))
			}
		}
		done <- result{iss, err}
	}()

	budget := time.NewTimer(issueBudget)
	defer budget.Stop()
	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		return nil
	case <-budget.C:
		return s.finishFailure(ctx, c, failureOutcome{code: "timeout", countsAsFailure: true, ca: target.CA,
			detail: "签发超过 13 分钟没有结果，这一单作废", work: w})
	}

	if res.err != nil {
		if accountGone(res.err) {
			if derr := s.deactivateAccount(ctx, acct.id); derr != nil {
				log.Error("ACME 账号标失效失败", zap.String("account", acct.id.String()), zap.Error(derr))
			}
		}
		f := classifyFailure(res.err, rec)
		out := failureOutcome{code: f.code, detail: f.detail, ca: target.CA, countsAsFailure: !f.rateLimited, work: w}
		if f.rateLimited {
			out.retryIn = min(max(f.retryAfter, time.Minute), maxRetryAfter)
			if f.retryAfter <= 0 {
				out.retryIn = defaultCA429
			}
		}
		return s.finishFailure(ctx, c, out)
	}
	version, err := s.finishSuccess(ctx, c, w, target, acct.id, replaces, res.iss)
	if err == nil {
		log.Info("证书已签发", zap.String("certificate", c.CertificateID.String()), zap.Int("version", version),
			zap.String("ca", target.CA), zap.Time("not_after", res.iss.leaf.NotAfter))
	}
	return err
}
