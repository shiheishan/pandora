package certs

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"slices"
	"time"

	"github.com/go-acme/lego/v4/acme/api"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// Worker 是 aegis-admin 的「证书巡检」循环：每轮排到期的订单、（隔几分钟）刷新 ARI 窗口与到期等级，
// 再认领至多一张订单签掉。多实例时靠订单的部分唯一索引与 SKIP LOCKED 分活。
type Worker struct {
	s     *Service
	owner string
	log   *slog.Logger
	// lastSweep 是上次刷新 ARI 与到期等级的时间（这两件事不用每轮都做）
	lastSweep time.Time
}

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

// NewWorker 装配 worker；租约主人是「主机名:进程号:随机数」，重启后是新主人。
func (s *Service) NewWorker(log *slog.Logger) *Worker {
	if log == nil {
		log = slog.Default()
	}
	if len(s.opts.LibraryEnv) > 0 {
		// lego 会自己读这些变量：调试开关可能把 DNS 服务商 API 的请求（含令牌）打进日志，
		// CNAME / TCP 开关会改变签发行为。面板不用它们，提醒运维删掉
		log.Warn("进程环境里设了 lego 自己会读的变量，可能泄露 DNS 凭据或改变签发行为，请从环境文件里删掉",
			"vars", s.opts.LibraryEnv)
	}
	host, _ := os.Hostname()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return &Worker{s: s, log: log, owner: truncate(fmt.Sprintf("%s:%d:%s", host, os.Getpid(), hex.EncodeToString(b)), 120)}
}

// RunOnce 跑一轮；返回是否处理了一张订单。
func (w *Worker) RunOnce(ctx context.Context, tenantID string) (bool, error) {
	if n, err := w.s.queueDue(ctx, tenantID); err != nil {
		return false, fmt.Errorf("排证书订单: %w", err)
	} else if n > 0 {
		w.log.Info("证书订单已排队", "count", n)
	}
	if time.Since(w.lastSweep) >= sweepEvery {
		w.lastSweep = time.Now()
		if err := w.s.refreshARI(ctx, tenantID, ariBatch, w.log); err != nil && ctx.Err() == nil {
			w.log.Warn("证书 ARI 窗口刷新失败", "error", err.Error())
		}
		if n, err := w.s.raiseExpiryAlerts(ctx, tenantID); err != nil {
			w.log.Warn("证书到期等级刷新失败", "error", err.Error())
		} else if n > 0 {
			w.log.Warn("证书到期等级上升", "count", n)
		}
	}
	c, err := w.s.claimOrder(ctx, tenantID, w.owner)
	if err != nil || c == nil {
		return false, err
	}
	err = w.s.runOrder(ctx, tenantID, c, w.log)
	if errors.Is(err, errLeaseLost) {
		w.log.Warn("证书订单的租约已被接手，丢弃这次结果", "order", c.id)
		return true, nil
	}
	return true, err
}

// runOrder 处理一张认领到的订单：凭据预检 → 本地限额对账（必要时切备用 CA）→ ACME 账号 → lego 签发 → 落库。
func (s *Service) runOrder(ctx context.Context, tenantID string, c *claimed, log *slog.Logger) error {
	if c.attempt > maxOrderAttempts {
		return s.finishFailure(ctx, tenantID, c, failureOutcome{code: "interrupted", countsAsFailure: true,
			detail: "签发多次中断（进程退出或超时），已放弃这一单"})
	}
	w, err := s.loadOrderWork(ctx, tenantID, c)
	if err != nil {
		return err
	}
	if w.status != StatusPending && w.status != StatusActive {
		return s.finishFailure(ctx, tenantID, c, failureOutcome{code: "not_active", cancel: true,
			detail: "证书已暂停或凭据失效，这一单作废"})
	}
	provider, err := s.newDNSProvider(w.cred.Provider, w.secret)
	if err != nil {
		return err
	}

	// 1. 凭据预检：在碰 CA 之前确认凭据还能用，失败不消耗 CA 的验证失败次数
	pctx, cancel := context.WithTimeout(ctx, time.Minute)
	_, checkErr := provider.check(pctx, w.cred.Zone, false)
	cancel()
	var credErr *credentialError
	switch {
	case errors.As(checkErr, &credErr):
		return s.finishFailure(ctx, tenantID, c, failureOutcome{code: "credential_rejected", detail: credErr.msg,
			blockCredential: true, credRowVersion: w.cred.RowVersion, work: w})
	case checkErr != nil:
		if ctx.Err() != nil {
			return nil
		}
		return s.finishFailure(ctx, tenantID, c, failureOutcome{code: "dns_api_unavailable", retryIn: dnsAPIBackoff,
			detail: "DNS 提供方 API 暂时不可用：" + providerErrorText(checkErr), work: w})
	}

	// 2. 本地限额对账；超限且证书快到期（或从没签出过）时切到备用 CA
	target := s.primaryTarget(w.cfg)
	renewal := w.currentIdentifiers != nil && slices.Equal(w.currentIdentifiers, w.identifiers)
	replaces := ""
	if c.replaces != nil && renewal && w.currentCA != nil && *w.currentCA == target.CA {
		replaces = *c.replaces
	}
	if d := checkLocalLimits(w.recent, w.identifiers, target.CA, renewal, replaces != "", w.now); d.blocked {
		fb := s.fallbackTarget(w.cfg)
		if fb == nil || (w.notAfter != nil && w.notAfter.Sub(w.now) >= fallbackBelow) {
			return s.finishFailure(ctx, tenantID, c, failureOutcome{code: "rate_limited_local", detail: d.reason,
				retryIn: max(d.retryAt.Sub(w.now), time.Minute), work: w})
		}
		log.Warn("首选 CA 预计超限，改用备用 CA", "certificate", c.certID, "reason", d.reason)
		target, replaces = *fb, ""
	}

	// 3. ACME 账号（第一次用这个 CA 时注册）
	acct, err := s.ensureAccount(ctx, tenantID, target, w.cfg.ContactEmail)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		f := classifyFailure(err, &retryRecorder{})
		return s.finishFailure(ctx, tenantID, c, failureOutcome{code: "acme_account_error", ca: target.CA,
			retryIn: defaultCA429, detail: "注册 ACME 账号失败：" + f.detail, work: w})
	}

	// ARI replaces 只能由签出旧证书的那个账号发（账号失效重注册后换了新账号就不带）
	if replaces != "" && (w.currentAccountID == nil || *w.currentAccountID != acct.id) {
		replaces = ""
	}

	// 4. 签发：lego 没有 context，放在另一个协程里跑；期间续租，停机或租约丢了就不等它。
	//    CA 一签出就写签发流水（限额对账按它数），不管之后落库成不成功
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
			if lerr := s.recordIssuance(tenantID, c.certID, target, w.identifiers, renewal, replaces != "", iss); lerr != nil {
				log.Error("证书签发流水写入失败（本地限额对账会少数这一张）", "certificate", c.certID, "error", lerr.Error())
			}
		}
		done <- result{iss, err}
	}()
	tick := time.NewTicker(leaseRenewEvery)
	defer tick.Stop()
	var res result
wait:
	for {
		select {
		case res = <-done:
			break wait
		case <-ctx.Done():
			return nil // 停机：租约一过别的 worker（或重启后的自己）接着做
		case <-tick.C:
			ok, err := s.extendLease(ctx, tenantID, c)
			if err != nil && ctx.Err() != nil {
				return nil
			}
			if err == nil && !ok {
				return errLeaseLost
			}
		}
	}
	if res.err != nil {
		if accountGone(res.err) {
			// CA 说账号不存在或已失效：标失效，下一单重新注册（账号唯一键只管 valid 的行，见 00152）
			if derr := s.deactivateAccount(ctx, tenantID, acct.id); derr != nil {
				log.Error("ACME 账号标失效失败", "account", acct.id, "error", derr.Error())
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
		return s.finishFailure(ctx, tenantID, c, out)
	}
	version, err := s.finishSuccess(ctx, tenantID, c, w, target, acct.id, replaces, res.iss)
	if err == nil {
		log.Info("证书已签发", "certificate", c.certID, "version", version, "ca", target.CA,
			"not_after", res.iss.leaf.NotAfter)
	}
	return err
}

// refreshARI 问 CA 最多 limit 张在用证书的 ARI 续期窗口，窗口内取随机点作为续期时间（RFC 9773）。
// CA 不支持 ARI 时只记检查时间，续期时间保持「剩余 1/3」。
func (s *Service) refreshARI(ctx context.Context, tenantID string, limit int, log *slog.Logger) error {
	type due struct {
		certID, versionID, ca, chain string
	}
	var list []due
	var cfg acmeConfig
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var err error
		if cfg, err = s.loadACMEConfig(ctx, tx, tenantID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT c.id::text, v.id::text, v.ca, v.chain_pem
			  FROM certificates c
			  JOIN certificate_versions v ON v.tenant_id = c.tenant_id AND v.id = c.current_version_id
			 WHERE c.tenant_id = $1 AND c.status = 'active'
			   AND (c.ari_checked_at IS NULL OR c.ari_checked_at < now() - make_interval(secs => $2))
			 ORDER BY c.ari_checked_at NULLS FIRST, c.id
			 LIMIT $3`, tenantID, ariRecheck.Seconds(), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.certID, &d.versionID, &d.ca, &d.chain); err != nil {
				return err
			}
			list = append(list, d)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, d := range list {
		if ctx.Err() != nil {
			return nil
		}
		var start, end, renewAt *time.Time
		if target, ok := s.targetForCA(d.ca, cfg); ok {
			if leaf := parseLeaf(d.chain); leaf != nil {
				info, err := s.renewalInfoCtx(ctx, target, leaf)
				switch {
				case err == nil:
					a, b := info.SuggestedWindow.Start, info.SuggestedWindow.End
					r := pickInWindow(a, b, time.Now())
					start, end, renewAt = &a, &b, &r
				case errors.Is(err, api.ErrNoARI):
				default:
					log.Warn("查询 ARI 窗口失败", "certificate", d.certID, "error", providerErrorText(err))
				}
			}
		}
		if err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				UPDATE certificates
				   SET ari_checked_at = now(),
				       ari_window_start = coalesce($4, ari_window_start), ari_window_end = coalesce($5, ari_window_end),
				       renew_after = coalesce($6, renew_after)
				 WHERE tenant_id = $1 AND id = $2::uuid AND current_version_id = $3::uuid`,
				tenantID, d.certID, d.versionID, start, end, renewAt)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// renewalInfoCtx 给没有 context 的 lego 调用加上停机即返回。
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

// raiseExpiryAlerts 把到期等级上升的证书记下来（certificates.alerted_level）并写一条审计；后台横幅
// 直接看列表接口的 summary，不靠这里。签出新版本时等级清零。
func (s *Service) raiseExpiryAlerts(ctx context.Context, tenantID string) (int, error) {
	raised := 0
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		type row struct {
			id, name     string
			nb, na       time.Time
			alerted      int
			level        ExpiryLevel
			targetRanked int
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id::text, name, not_before, not_after, alerted_level FROM certificates
			 WHERE tenant_id = $1 AND current_version_id IS NOT NULL AND alerted_level < 2
			 ORDER BY not_after LIMIT $2`, tenantID, alertBatch)
		if err != nil {
			return err
		}
		var up []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.name, &r.nb, &r.na, &r.alerted); err != nil {
				rows.Close()
				return err
			}
			r.level = expiryLevel(&r.nb, &r.na, now)
			if r.targetRanked = alertRank(r.level); r.targetRanked > r.alerted {
				up = append(up, r)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, r := range up {
			tag, err := tx.Exec(ctx, `UPDATE certificates SET alerted_level = $3
				 WHERE tenant_id = $1 AND id = $2::uuid AND alerted_level < $3`, tenantID, r.id, r.targetRanked)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				continue
			}
			raised++
			id := r.id
			if err := audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "system", Action: "certificate.expiry_warning", ResourceType: "certificate", ResourceID: &id,
				APIDomain: "admin", AfterDigest: map[string]any{"name": r.name, "level": string(r.level), "not_after": r.na},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return raised, err
}
