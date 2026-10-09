package certs

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// 订单 = 任务 + 分布式锁（设计稿 §2.2）。部分唯一索引保证同一张证书同时最多一张 queued / running
// 订单；worker 用 FOR UPDATE SKIP LOCKED 认领并写租约，签发在事务外跑、定时续租；进程挂了租约一过
// 别的 worker 接手（attempt + 1）。落库前锁住订单行、核对 (lease_owner, attempt) 还是自己，
// 不是就丢掉结果：同一张订单绝不会写出两个版本。
const (
	orderLease       = 5 * time.Minute
	leaseRenewEvery  = 30 * time.Second
	maxOrderAttempts = 5
	dueBatch         = 50
)

var errLeaseLost = errors.New("certificate order lease lost")

// claimed 是认领到的订单。
type claimed struct {
	id, certID, reason string
	replaces           *string
	attempt            int
	owner              string
}

// queueDue 给到期的证书排订单：没签出过的（pending）、到了续期时间的（active 且 renew_after 已过），
// 都要过了退避时间、且没有进行中的订单。返回新排的张数。
func (s *Service) queueDue(ctx context.Context, tenantID string) (int, error) {
	var n int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO certificate_orders (tenant_id, certificate_id, reason, replaces_ari_id)
			SELECT c.tenant_id, c.id,
			       CASE WHEN c.current_version_id IS NULL THEN 'initial'
			            WHEN c.ari_window_start IS NOT NULL THEN 'ari' ELSE 'renewal' END,
			       v.ari_cert_id
			  FROM certificates c
			  LEFT JOIN certificate_versions v ON v.tenant_id = c.tenant_id AND v.id = c.current_version_id
			 WHERE c.tenant_id = $1 AND c.status IN ('pending', 'active')
			   AND (c.current_version_id IS NULL OR c.renew_after <= now())
			   AND (c.next_attempt_at IS NULL OR c.next_attempt_at <= now())
			   AND NOT EXISTS (SELECT 1 FROM certificate_orders o
			                    WHERE o.tenant_id = c.tenant_id AND o.certificate_id = c.id
			                      AND o.state IN ('queued', 'running'))
			 ORDER BY c.renew_after NULLS FIRST, c.id
			 LIMIT $2
			ON CONFLICT (tenant_id, certificate_id) WHERE state IN ('queued', 'running') DO NOTHING`,
			tenantID, dueBatch)
		n = tag.RowsAffected()
		return err
	})
	return int(n), err
}

// claimOrder 认领一张排队中、或租约已过期的订单。
func (s *Service) claimOrder(ctx context.Context, tenantID, owner string) (*claimed, error) {
	var c *claimed
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var o claimed
		err := tx.QueryRow(ctx, `
			UPDATE certificate_orders o
			   SET state = 'running', attempt = o.attempt + 1, lease_owner = $2,
			       lease_until = now() + make_interval(secs => $3), started_at = coalesce(o.started_at, now())
			 WHERE o.id = (
			       SELECT id FROM certificate_orders
			        WHERE tenant_id = $1 AND state IN ('queued', 'running')
			          AND (state = 'queued' OR lease_until < now())
			        ORDER BY created_at, id
			        LIMIT 1
			        FOR UPDATE SKIP LOCKED)
			RETURNING o.id::text, o.certificate_id::text, o.reason, o.replaces_ari_id, o.attempt`,
			tenantID, owner, orderLease.Seconds()).Scan(&o.id, &o.certID, &o.reason, &o.replaces, &o.attempt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		o.owner = owner
		c = &o
		return nil
	})
	return c, err
}

// extendLease 续租；返回 false 表示租约已经不是自己的（过期后被别人接手、或订单被取消）。
func (s *Service) extendLease(ctx context.Context, tenantID string, c *claimed) (bool, error) {
	var n int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE certificate_orders SET lease_until = now() + make_interval(secs => $5)
			 WHERE tenant_id = $1 AND id = $2::uuid AND state = 'running' AND lease_owner = $3 AND attempt = $4`,
			tenantID, c.id, c.owner, c.attempt, orderLease.Seconds())
		n = tag.RowsAffected()
		return err
	})
	return n == 1, err
}

// lockOwnedOrder 在落库事务里锁住订单行并确认租约还是自己的。
func lockOwnedOrder(ctx context.Context, tx pgx.Tx, tenantID string, c *claimed) error {
	var mine bool
	err := tx.QueryRow(ctx, `
		SELECT state = 'running' AND lease_owner = $3 AND attempt = $4
		  FROM certificate_orders WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`,
		tenantID, c.id, c.owner, c.attempt).Scan(&mine)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !mine {
		return errLeaseLost
	}
	return err
}

// orderWork 是处理一张订单要读的全部东西（一个只读事务里取齐）。
type orderWork struct {
	certName           string
	identifiers        []string
	keyType            string
	status             string
	cred               DNSCredential
	secret             map[string]string
	currentIdentifiers []string
	currentCA          *string
	notAfter           *time.Time
	cfg                acmeConfig
	recent             []recentIssuance
	now                time.Time
}

func (s *Service) loadOrderWork(ctx context.Context, tenantID string, c *claimed) (*orderWork, error) {
	w := &orderWork{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var credID string
		if err := tx.QueryRow(ctx, `
			SELECT c.name, c.identifiers, c.key_type, c.status, c.dns_credential_id::text, v.identifiers, v.ca, c.not_after
			  FROM certificates c
			  LEFT JOIN certificate_versions v ON v.tenant_id = c.tenant_id AND v.id = c.current_version_id
			 WHERE c.tenant_id = $1 AND c.id = $2::uuid`, tenantID, c.certID).Scan(
			&w.certName, &w.identifiers, &w.keyType, &w.status, &credID, &w.currentIdentifiers, &w.currentCA,
			&w.notAfter); err != nil {
			return err
		}
		var err error
		if w.cred, w.secret, err = s.loadCredentialSecret(ctx, tx, tenantID, credID); err != nil {
			return err
		}
		if w.cfg, err = s.loadACMEConfig(ctx, tx, tenantID, true); err != nil {
			return err
		}
		w.recent, w.now, err = loadRecentIssuances(ctx, tx, tenantID)
		return err
	})
	return w, err
}

// finishSuccess 落库一张签出的证书：新版本（私钥密文）、证书的当前版本与续期时间、订单完成、审计，
// 同一个事务。签发期间证书的标识或密钥类型被改过，就接着再排一张订单。
func (s *Service) finishSuccess(ctx context.Context, tenantID string, c *claimed, w *orderWork, target caTarget,
	accountID, replaces string, iss *issued) (int, error) {
	var version int
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := lockOwnedOrder(ctx, tx, tenantID, c); err != nil {
			return err
		}
		var curIDs []string
		var curKey string
		if err := tx.QueryRow(ctx, `SELECT identifiers, key_type FROM certificates
			 WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`, tenantID, c.certID).Scan(&curIDs, &curKey); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(version), 0) + 1 FROM certificate_versions
			 WHERE tenant_id = $1 AND certificate_id = $2::uuid`, tenantID, c.certID).Scan(&version); err != nil {
			return err
		}
		sealed, err := s.sealer.Seal(iss.keyDER, versionKeyAAD(tenantID, c.certID, version))
		if err != nil {
			return err
		}
		renewal := w.currentIdentifiers != nil && slices.Equal(w.currentIdentifiers, w.identifiers)
		var versionID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO certificate_versions (tenant_id, certificate_id, version, acme_account_id, ca, identifiers,
			  is_renewal, serial, chain_pem, private_key_sealed, public_key_sha256, chain_sha256, not_before, not_after,
			  ari_cert_id, cert_url, order_id)
			VALUES ($1, $2::uuid, $3, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, nullif($15, ''),
			        nullif($16, ''), $17::uuid)
			RETURNING id::text`,
			tenantID, c.certID, version, accountID, target.CA, w.identifiers, renewal, serialHex(iss.leaf),
			string(iss.chainPEM), sealed, iss.pubSHA256, iss.chainSHA256, iss.leaf.NotBefore, iss.leaf.NotAfter,
			iss.ariCertID, truncate(iss.certURL, 500), c.id).Scan(&versionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE certificates
			   SET current_version_id = $3::uuid, not_before = $4, not_after = $5, renew_after = $6,
			       ari_window_start = NULL, ari_window_end = NULL, ari_checked_at = NULL,
			       status = CASE WHEN status = 'pending' THEN 'active' ELSE status END,
			       consecutive_failures = 0, next_attempt_at = NULL, last_error_code = NULL, last_error = NULL,
			       alerted_level = 0, row_version = row_version + 1
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, c.certID, versionID, iss.leaf.NotBefore, iss.leaf.NotAfter,
			defaultRenewAfter(iss.leaf.NotBefore, iss.leaf.NotAfter)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE certificate_orders
			   SET state = 'succeeded', finished_at = now(), version_id = $3::uuid, ca = $4,
			       replaces_ari_id = nullif($5, '')
			 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, c.id, versionID, target.CA, replaces); err != nil {
			return err
		}
		if !slices.Equal(curIDs, w.identifiers) || curKey != w.keyType {
			if _, err := queueOrder(ctx, tx, tenantID, c.certID, "manual", ""); err != nil {
				return err
			}
		}
		id := c.certID
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "system", Action: "certificate.issued", ResourceType: "certificate", ResourceID: &id,
			APIDomain: "admin", AfterDigest: map[string]any{"version": version, "ca": target.CA,
				"serial": serialHex(iss.leaf), "identifiers": w.identifiers, "not_after": iss.leaf.NotAfter,
				"order_id": c.id, "reason": c.reason, "replaces": replaces != ""},
		})
	})
	return version, err
}

func versionKeyAAD(tenantID, certID string, version int) []byte {
	return []byte("certificate:" + tenantID + ":" + certID + ":" + strconv.Itoa(version) + ":private_key")
}

// failureOutcome 是一次没签出来的结论。
type failureOutcome struct {
	code   string
	detail string
	ca     string
	// countsAsFailure：CA 那边真失败了（验证失败、订单出错），计入连续失败、按 1h→24h 退避，3 次暂停
	countsAsFailure bool
	// retryIn：不计失败时的退避（CA 限流读 Retry-After、本地限额等到最早那张满 7 天、提供方不可达 15 分钟）
	retryIn time.Duration
	// blockCredential：提供方明确拒绝了凭据，凭据标 error，用它的证书都停在 blocked_credential
	blockCredential bool
	// cancel：订单作废（证书已暂停或凭据失效），不算失败
	cancel bool
	// credRowVersion 是预检时读到的凭据版本：只在凭据没被改过时才把它标 error、拦下证书，
	// 免得管理员刚换的新令牌被旧令牌的结论打回去
	credRowVersion int64
	// work 是这一单用的标识与密钥类型；签发期间被改过就补排一张（与成功路径一致）
	work *orderWork
}

// finishFailure 记一次失败：订单结束、证书的退避与失败计数、需要时暂停或拦下凭据，写审计。
func (s *Service) finishFailure(ctx context.Context, tenantID string, c *claimed, f failureOutcome) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := lockOwnedOrder(ctx, tx, tenantID, c); err != nil {
			return err
		}
		state := "failed"
		if f.cancel {
			state = "cancelled"
		}
		detail := truncate(f.detail, 2000)
		if _, err := tx.Exec(ctx, `
			UPDATE certificate_orders SET state = $3, finished_at = now(), error_code = $4, error_detail = $5,
			       ca = nullif($6, '')
			 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, c.id, state, f.code, detail, f.ca); err != nil {
			return err
		}
		if f.cancel {
			return nil
		}
		var failures int
		var status, credID, curKey string
		var curIDs []string
		if err := tx.QueryRow(ctx, `SELECT consecutive_failures, status, dns_credential_id::text, identifiers, key_type
			 FROM certificates WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`, tenantID, c.certID).Scan(
			&failures, &status, &credID, &curIDs, &curKey); err != nil {
			return err
		}
		retry := f.retryIn
		paused := false
		if f.countsAsFailure {
			failures++
			retry = failureBackoff(failures)
			paused = failures >= pauseAfterFailures && (status == StatusPending || status == StatusActive)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE certificates
			   SET consecutive_failures = $3, next_attempt_at = now() + make_interval(secs => $4),
			       last_error_code = $5, last_error = $6,
			       status = CASE WHEN $7 THEN 'paused' ELSE status END,
			       paused_reason = CASE WHEN $7 THEN 'failures' ELSE paused_reason END,
			       row_version = row_version + 1
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, c.certID, failures, retry.Seconds(), f.code, detail, paused); err != nil {
			return err
		}
		if f.blockCredential {
			// 只在凭据还是预检时那一版才下结论：管理员在这期间换了令牌，新令牌由它自己的校验定
			tag, err := tx.Exec(ctx, `
				UPDATE dns_credentials SET verify_status = 'error', verify_error = $3
				 WHERE tenant_id = $1 AND id = $2::uuid AND row_version = $4`,
				tenantID, credID, truncate(f.detail, 500), f.credRowVersion)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				if _, err := setCredentialBlocked(ctx, tx, tenantID, credID, true); err != nil {
					return err
				}
			}
		}
		// 签发期间域名或密钥类型被改过：这次失败的是旧的，按新的立刻再排一张（与 finishSuccess 一致）
		if f.work != nil && !paused && (status == StatusPending || status == StatusActive) &&
			(!slices.Equal(curIDs, f.work.identifiers) || curKey != f.work.keyType) {
			if _, err := queueOrder(ctx, tx, tenantID, c.certID, "manual", ""); err != nil {
				return err
			}
		}
		id := c.certID
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "system", Action: "certificate.issue_failed", ResourceType: "certificate", ResourceID: &id,
			APIDomain: "admin", Outcome: "failure", ErrorCode: f.code,
			AfterDigest: map[string]any{"order_id": c.id, "code": f.code, "consecutive_failures": failures,
				"retry_in_seconds": int64(retry.Seconds()), "credential_blocked": f.blockCredential},
		}); err != nil {
			return err
		}
		if !paused {
			return nil
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "system", Action: "certificate.paused", ResourceType: "certificate", ResourceID: &id,
			APIDomain: "admin", AfterDigest: map[string]any{"reason": "failures", "consecutive_failures": failures},
		})
	})
}
