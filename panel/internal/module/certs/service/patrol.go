package service

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"time"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/go-acme/lego/v4/acme/api"
	"github.com/go-acme/lego/v4/certificate"
	"go.uber.org/zap"
)

const (
	sweepEvery    = 5 * time.Minute
	ariBatch      = 5
	ariRecheck    = 6 * time.Hour
	alertBatch    = 200
	dnsAPIBackoff = 15 * time.Minute
	defaultCA429  = time.Hour
	maxRetryAfter = 7 * 24 * time.Hour
	fallbackBelow = 7 * 24 * time.Hour
)

// Patrol 是 certs:patrol 的一轮：排到期订单、收中断单、入队签发，并周期性刷新 ARI 与到期等级。
func (s *Service) Patrol(ctx context.Context) (PatrolResult, error) {
	var out PatrolResult
	ids, err := s.store.QueueDue(ctx, dueBatch)
	if err != nil {
		return out, err
	}
	out.Queued = len(ids)

	expired, err := s.store.ExpiredOrders(ctx, dueBatch)
	if err != nil {
		return out, err
	}
	for _, c := range expired {
		if err := s.finishFailure(ctx, c, failureOutcome{code: "interrupted", countsAsFailure: true,
			detail: "签发任务中断（进程退出或任务丢失），这一单作废"}); err != nil {
			if errors.Is(err, errLeaseLost) {
				continue
			}
			return out, err
		}
		out.Expired++
	}

	open, err := s.store.OpenOrders(ctx, dueBatch)
	if err != nil {
		return out, err
	}
	for _, id := range open {
		ok, err := s.enqueueIssue(ctx, id)
		if err != nil {
			return out, err
		}
		if ok {
			out.Enqueued++
		}
	}

	now := s.opts.Now()
	last := time.Unix(0, s.lastSweep.Load())
	if now.Sub(last) >= sweepEvery {
		s.lastSweep.Store(now.UnixNano())
		if _, err := s.refreshARI(ctx); err != nil && ctx.Err() == nil {
			plog.From(ctx).Warn("证书 ARI 窗口刷新失败", zap.Error(err))
		}
		if n, err := s.raiseExpiryAlerts(ctx); err != nil && ctx.Err() == nil {
			plog.From(ctx).Warn("证书到期等级刷新失败", zap.Error(err))
		} else if err == nil {
			out.Raised = n
		}
	}
	return out, nil
}

// refreshARI 问 CA 最多 ariBatch 张在用证书的 ARI 续期窗口，窗口内取随机点作为续期时间（RFC 9773）；
// CA 不支持 ARI 时只记检查时间，续期时间保持「剩余 1/3」。返回写了检查时间的张数。
func (s *Service) refreshARI(ctx context.Context) (int, error) {
	list, err := s.store.ARIDue(ctx, ariRecheck, ariBatch)
	if err != nil {
		return 0, err
	}
	cfg, err := s.loadACMEConfig(ctx, false)
	if err != nil {
		return 0, err
	}
	log := plog.From(ctx)
	updated := 0
	for _, d := range list {
		if ctx.Err() != nil {
			return updated, nil
		}
		var start, end, renewAt *time.Time
		if target, ok := s.targetForCA(d.CA, cfg); ok {
			if leaf := parseLeaf(d.ChainPEM); leaf != nil {
				info, err := s.renewalInfoCtx(ctx, target, leaf)
				switch {
				case err == nil:
					a, b := info.SuggestedWindow.Start, info.SuggestedWindow.End
					r := pickInWindow(a, b, time.Now())
					start, end, renewAt = &a, &b, &r
				case errors.Is(err, api.ErrNoARI):
				default:
					log.Warn("查询 ARI 窗口失败", zap.String("certificate", d.CertificateID.String()),
						zap.String("error", providerErrorText(err)))
				}
			}
		}
		if err := s.store.RecordARI(ctx, d.CertificateID, d.VersionID, start, end, renewAt); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// renewalInfoCtx 给没有 context 的 lego 调用加上取消即返回。
func (s *Service) renewalInfoCtx(ctx context.Context, target caTarget, leaf *x509.Certificate) (*certificate.RenewalInfoResponse, error) {
	type result struct {
		info *certificate.RenewalInfoResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		info, err := s.renewalInfo(target, leaf)
		done <- result{info, err}
	}()
	select {
	case r := <-done:
		return r.info, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// pickInWindow 在 ARI 窗口里均匀取一个时间点；已经过了就取 now。
func pickInWindow(start, end, now time.Time) time.Time {
	t := start
	if w := end.Sub(start); w > 0 {
		if n, err := rand.Int(rand.Reader, big.NewInt(int64(w))); err == nil {
			t = start.Add(time.Duration(n.Int64()))
		}
	}
	if t.Before(now) {
		return now
	}
	return t
}

// parseLeaf 取证书链里的第一张（叶子）证书。
func parseLeaf(chain string) *x509.Certificate {
	block, _ := pem.Decode([]byte(chain))
	if block == nil {
		return nil
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	return leaf
}

// raiseExpiryAlerts 把到期等级上升的证书记下来（alerted_level）并写一条审计；后台横幅看列表的 summary，
// 不靠这里。签出新版本时等级清零。
func (s *Service) raiseExpiryAlerts(ctx context.Context) (int, error) {
	raised := 0
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		now, err := s.store.DBNow(ctx)
		if err != nil {
			return err
		}
		rows, err := s.store.AlertCandidates(ctx, alertBatch)
		if err != nil {
			return err
		}
		for _, r := range rows {
			level := expiryLevel(&r.NotBefore, &r.NotAfter, now)
			targetRanked := alertRank(level)
			if targetRanked <= r.AlertedLevel {
				continue
			}
			changed, err := s.store.RaiseAlert(ctx, r.ID, targetRanked)
			if err != nil {
				return err
			}
			if !changed {
				continue
			}
			raised++
			rid := r.ID
			if err := s.writeAudit(ctx, "certificate.expiry_warning", "certificate", &rid, "", map[string]any{
				"name": r.Name, "level": string(level), "not_after": r.NotAfter,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return raised, err
}
