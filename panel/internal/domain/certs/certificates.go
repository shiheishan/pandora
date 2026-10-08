package certs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 证书状态（certificates.status）。
const (
	StatusPending           = "pending"
	StatusActive            = "active"
	StatusPaused            = "paused"
	StatusBlockedCredential = "blocked_credential"
)

const (
	KeyECDSAP256 = "ecdsa-p256"
	KeyRSA2048   = "rsa-2048"
)

// pauseAfterFailures 是连续失败几次后停止自动重试（远离 Let's Encrypt 每小时 5 次验证失败的上限）。
const pauseAfterFailures = 3

// Certificate 是证书的读形状（列表与详情共用）。没有任何密文字段。
type Certificate struct {
	ID                  string      `json:"id"`
	Name                string      `json:"name"`
	Identifiers         []string    `json:"identifiers"`
	Wildcard            bool        `json:"wildcard"`
	Challenge           string      `json:"challenge"`
	DNSCredentialID     string      `json:"dns_credential_id"`
	DNSCredentialName   string      `json:"dns_credential_name"`
	DNSProvider         string      `json:"dns_provider"`
	KeyType             string      `json:"key_type"`
	Status              string      `json:"status"`
	PausedReason        *string     `json:"paused_reason"`
	CurrentVersion      *int        `json:"current_version"`
	CurrentCA           *string     `json:"current_ca"`
	NotBefore           *time.Time  `json:"not_before"`
	NotAfter            *time.Time  `json:"not_after"`
	RenewAfter          *time.Time  `json:"renew_after"`
	ARIWindowStart      *time.Time  `json:"ari_window_start"`
	ARIWindowEnd        *time.Time  `json:"ari_window_end"`
	NextAttemptAt       *time.Time  `json:"next_attempt_at"`
	ConsecutiveFailures int         `json:"consecutive_failures"`
	LastErrorCode       *string     `json:"last_error_code"`
	LastError           *string     `json:"last_error"`
	ExpiryLevel         ExpiryLevel `json:"expiry_level"`
	// ActiveOrder 是进行中（排队或签发中）的订单；没有为 null
	ActiveOrder *OrderBrief `json:"active_order"`
	RowVersion  int64       `json:"row_version"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// OrderBrief 是列表上的进行中订单。
type OrderBrief struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

// CertificateSummary 是后台横幅的计数（10-07 定：到期与失败只在后台显示）。
type CertificateSummary struct {
	Total    int `json:"total"`
	Warning  int `json:"warning"`
	Critical int `json:"critical"`
	Expired  int `json:"expired"`
	// Failing 是最近一次签发失败、还没成功过的证书数（含暂停与凭据失效）
	Failing int `json:"failing"`
	Paused  int `json:"paused"`
	Blocked int `json:"blocked"`
}

// CertificateList 是列表接口的回包。
type CertificateList struct {
	Items   []Certificate      `json:"items"`
	Summary CertificateSummary `json:"summary"`
}

// CertificateInput 是新建证书。
type CertificateInput struct {
	Name            string   `json:"name"`
	Identifiers     []string `json:"identifiers"`
	DNSCredentialID string   `json:"dns_credential_id"`
	KeyType         string   `json:"key_type"`
}

// CertificatePatch 是修改证书：缺席的字段不改。改了标识就排一张新订单（下次签发用新标识）。
type CertificatePatch struct {
	Name            *string  `json:"name"`
	Identifiers     []string `json:"identifiers"`
	DNSCredentialID *string  `json:"dns_credential_id"`
	KeyType         *string  `json:"key_type"`
}

const certificateColumns = `c.id::text, c.name, c.identifiers, c.challenge, c.dns_credential_id::text, d.name, d.provider,
	c.key_type, c.status, c.paused_reason, v.version, v.ca, c.not_before, c.not_after, c.renew_after,
	c.ari_window_start, c.ari_window_end, c.next_attempt_at, c.consecutive_failures, c.last_error_code, c.last_error,
	o.id::text, o.state, o.reason, o.created_at, c.row_version, c.created_at, c.updated_at`

const certificateFrom = ` FROM certificates c
	JOIN dns_credentials d ON d.tenant_id = c.tenant_id AND d.id = c.dns_credential_id
	LEFT JOIN certificate_versions v ON v.tenant_id = c.tenant_id AND v.id = c.current_version_id
	LEFT JOIN certificate_orders o ON o.tenant_id = c.tenant_id AND o.certificate_id = c.id
	                              AND o.state IN ('queued', 'running')`

func (s *Service) scanCertificate(row pgx.Row, c *Certificate) error {
	var oID, oState, oReason *string
	var oAt *time.Time
	if err := row.Scan(&c.ID, &c.Name, &c.Identifiers, &c.Challenge, &c.DNSCredentialID, &c.DNSCredentialName,
		&c.DNSProvider, &c.KeyType, &c.Status, &c.PausedReason, &c.CurrentVersion, &c.CurrentCA, &c.NotBefore,
		&c.NotAfter, &c.RenewAfter, &c.ARIWindowStart, &c.ARIWindowEnd, &c.NextAttemptAt, &c.ConsecutiveFailures,
		&c.LastErrorCode, &c.LastError, &oID, &oState, &oReason, &oAt, &c.RowVersion, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return err
	}
	if oID != nil {
		c.ActiveOrder = &OrderBrief{ID: *oID, State: *oState, Reason: *oReason, CreatedAt: *oAt}
	}
	c.Wildcard = hasWildcard(c.Identifiers)
	c.ExpiryLevel = expiryLevel(c.NotBefore, c.NotAfter, s.opts.Now())
	return nil
}

// ListCertificates 列出全部证书与横幅计数。
func (s *Service) ListCertificates(ctx context.Context, tenantID string) (CertificateList, error) {
	out := CertificateList{Items: []Certificate{}}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+certificateColumns+certificateFrom+`
			 WHERE c.tenant_id = $1 ORDER BY lower(c.name), c.id`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Certificate
			if err := s.scanCertificate(rows, &c); err != nil {
				return err
			}
			out.Items = append(out.Items, c)
		}
		return rows.Err()
	})
	for _, c := range out.Items {
		out.Summary.Total++
		switch c.ExpiryLevel {
		case ExpiryWarning:
			out.Summary.Warning++
		case ExpiryCritical:
			out.Summary.Critical++
		case ExpiryExpired:
			out.Summary.Expired++
		}
		if c.ConsecutiveFailures > 0 || c.Status == StatusBlockedCredential {
			out.Summary.Failing++
		}
		switch c.Status {
		case StatusPaused:
			out.Summary.Paused++
		case StatusBlockedCredential:
			out.Summary.Blocked++
		}
	}
	return out, err
}

func (s *Service) getCertificate(ctx context.Context, tx pgx.Tx, tenantID, id string) (Certificate, error) {
	var c Certificate
	err := s.scanCertificate(tx.QueryRow(ctx, `SELECT `+certificateColumns+certificateFrom+`
		 WHERE c.tenant_id = $1 AND c.id = $2::uuid`, tenantID, id), &c)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, httpx.NotFoundOrForbidden()
	}
	return c, err
}

// lockCertificate 锁住证书行并返回状态与当前版本，给写路径做判断。
func lockCertificate(ctx context.Context, tx pgx.Tx, tenantID, id string) (status string, hasVersion bool, err error) {
	err = tx.QueryRow(ctx, `SELECT status, current_version_id IS NOT NULL FROM certificates
		 WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`, tenantID, id).Scan(&status, &hasVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, httpx.NotFoundOrForbidden()
	}
	return status, hasVersion, err
}

func certificateNameTaken(err error) error {
	if db.IsUniqueViolation(err) && db.ConstraintName(err) == "certificates_name_key" {
		return httpx.Invalid(map[string]string{"name": "已有同名的证书"})
	}
	return err
}

func validKeyType(k string) bool { return k == KeyECDSAP256 || k == KeyRSA2048 }

// credentialForCertificate 确认凭据存在（同租户），用于新建与换凭据。
func credentialForCertificate(ctx context.Context, tx pgx.Tx, tenantID, id string) (bool, error) {
	if _, err := uuid.Parse(id); err != nil {
		return false, nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM dns_credentials WHERE tenant_id = $1 AND id = $2::uuid)`,
		tenantID, id).Scan(&ok)
	return ok, err
}

// CreateCertificate 新建证书并排一张首次签发订单（worker 下一轮就签）。
func (s *Service) CreateCertificate(ctx context.Context, tenantID string, actor Actor, in CertificateInput) (Certificate, error) {
	bad := map[string]string{}
	name, msg := validName(in.Name)
	if msg != "" {
		bad["name"] = msg
	}
	ids, msg := normalizeIdentifiers(in.Identifiers)
	if msg != "" {
		bad["identifiers"] = msg
	}
	if in.KeyType == "" {
		in.KeyType = KeyECDSAP256
	}
	if !validKeyType(in.KeyType) {
		bad["key_type"] = "密钥类型只能是 ecdsa-p256 或 rsa-2048"
	}
	var out Certificate
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor.ID}, func(tx pgx.Tx) error {
		ok, err := credentialForCertificate(ctx, tx, tenantID, in.DNSCredentialID)
		if err != nil {
			return err
		}
		if !ok {
			bad["dns_credential_id"] = "选一个 DNS 凭据"
		}
		if len(bad) > 0 {
			return httpx.Invalid(bad)
		}
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO certificates (tenant_id, name, identifiers, dns_credential_id, key_type, created_by)
			VALUES ($1, $2, $3, $4::uuid, $5, nullif($6, '')::uuid) RETURNING id::text`,
			tenantID, name, ids, in.DNSCredentialID, in.KeyType, actor.ID).Scan(&id); err != nil {
			return certificateNameTaken(err)
		}
		if _, err := queueOrder(ctx, tx, tenantID, id, "initial", actor.ID); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: actor.kind(), ActorID: actor.auditID(), Action: "certificate.created",
			ResourceType: "certificate", ResourceID: &id, APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"name": name, "identifiers": ids, "dns_credential_id": in.DNSCredentialID,
				"key_type": in.KeyType},
		}); err != nil {
			return err
		}
		out, err = s.getCertificate(ctx, tx, tenantID, id)
		return err
	})
	return out, err
}

// UpdateCertificate 改名、改标识、换凭据或密钥类型。标识或密钥类型变了就排一张手动订单。
func (s *Service) UpdateCertificate(ctx context.Context, tenantID string, actor Actor, id string, in CertificatePatch) (Certificate, error) {
	if err := validID(id); err != nil {
		return Certificate{}, err
	}
	var out Certificate
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor.ID}, func(tx pgx.Tx) error {
		if _, _, err := lockCertificate(ctx, tx, tenantID, id); err != nil {
			return err
		}
		cur, err := s.getCertificate(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		bad := map[string]string{}
		name, ids, cred, key := cur.Name, cur.Identifiers, cur.DNSCredentialID, cur.KeyType
		if in.Name != nil {
			var msg string
			if name, msg = validName(*in.Name); msg != "" {
				bad["name"] = msg
			}
		}
		if in.Identifiers != nil {
			var msg string
			if ids, msg = normalizeIdentifiers(in.Identifiers); msg != "" {
				bad["identifiers"] = msg
			}
		}
		if in.DNSCredentialID != nil {
			cred = *in.DNSCredentialID
			ok, err := credentialForCertificate(ctx, tx, tenantID, cred)
			if err != nil {
				return err
			}
			if !ok {
				bad["dns_credential_id"] = "选一个 DNS 凭据"
			}
		}
		if in.KeyType != nil {
			if key = *in.KeyType; !validKeyType(key) {
				bad["key_type"] = "密钥类型只能是 ecdsa-p256 或 rsa-2048"
			}
		}
		if len(bad) > 0 {
			return httpx.Invalid(bad)
		}
		reissue := !slices.Equal(ids, cur.Identifiers) || key != cur.KeyType
		credChanged := cred != cur.DNSCredentialID
		if _, err := tx.Exec(ctx, `
			UPDATE certificates
			   SET name = $3, identifiers = $4, dns_credential_id = $5::uuid, key_type = $6,
			       status = CASE WHEN $7 AND status = 'blocked_credential'
			                     THEN CASE WHEN current_version_id IS NULL THEN 'pending' ELSE 'active' END
			                     ELSE status END,
			       next_attempt_at = CASE WHEN $8 OR $7 THEN NULL ELSE next_attempt_at END,
			       row_version = row_version + 1
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, id, name, ids, cred, key, credChanged, reissue); err != nil {
			return certificateNameTaken(err)
		}
		if reissue && (cur.Status == StatusActive || cur.Status == StatusPending) {
			if _, err := queueOrder(ctx, tx, tenantID, id, "manual", actor.ID); err != nil {
				return err
			}
		}
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: actor.kind(), ActorID: actor.auditID(), Action: "certificate.updated",
			ResourceType: "certificate", ResourceID: &id, APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"name": cur.Name, "identifiers": cur.Identifiers,
				"dns_credential_id": cur.DNSCredentialID, "key_type": cur.KeyType},
			AfterDigest: map[string]any{"name": name, "identifiers": ids, "dns_credential_id": cred, "key_type": key},
		}); err != nil {
			return err
		}
		out, err = s.getCertificate(ctx, tx, tenantID, id)
		return err
	})
	return out, err
}

// DeleteCertificate 删除证书与它的全部版本（含私钥密文）和订单。签发中的不让删（worker 正拿着租约）。
func (s *Service) DeleteCertificate(ctx context.Context, tenantID string, actor Actor, id string) error {
	if err := validID(id); err != nil {
		return err
	}
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor.ID}, func(tx pgx.Tx) error {
		if _, _, err := lockCertificate(ctx, tx, tenantID, id); err != nil {
			return err
		}
		cur, err := s.getCertificate(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if cur.ActiveOrder != nil && cur.ActiveOrder.State == "running" {
			return httpx.New(httpx.CodeConflict, "证书正在签发，等这次签发结束再删")
		}
		for _, q := range []string{
			`UPDATE certificates SET current_version_id = NULL, not_before = NULL, not_after = NULL,
			        status = CASE WHEN status = 'active' THEN 'pending' ELSE status END
			  WHERE tenant_id = $1 AND id = $2::uuid`,
			`DELETE FROM certificate_orders WHERE tenant_id = $1 AND certificate_id = $2::uuid`,
			`DELETE FROM certificate_versions WHERE tenant_id = $1 AND certificate_id = $2::uuid`,
			`DELETE FROM certificates WHERE tenant_id = $1 AND id = $2::uuid`,
		} {
			if _, err := tx.Exec(ctx, q, tenantID, id); err != nil {
				if db.IsForeignKeyViolation(err) {
					return httpx.New(httpx.CodeConflict, "还有节点在用这张证书，先给节点换证书再删")
				}
				return err
			}
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: actor.kind(), ActorID: actor.auditID(), Action: "certificate.deleted",
			ResourceType: "certificate", ResourceID: &id, APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"name": cur.Name, "identifiers": cur.Identifiers, "current_version": cur.CurrentVersion},
		})
	})
}

// RenewCertificate 立刻排一张手动订单（每次签发都换新私钥，所以「重新签发」也是它）。已有进行中的
// 订单时直接回那一张；暂停或凭据失效的证书要先恢复。
func (s *Service) RenewCertificate(ctx context.Context, tenantID string, actor Actor, id string) (OrderBrief, error) {
	if err := validID(id); err != nil {
		return OrderBrief{}, err
	}
	var out OrderBrief
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor.ID}, func(tx pgx.Tx) error {
		status, _, err := lockCertificate(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		switch status {
		case StatusPaused:
			return httpx.New(httpx.CodeConflict, "证书已暂停自动签发，先点「恢复」")
		case StatusBlockedCredential:
			return httpx.New(httpx.CodeConflict, "证书的 DNS 凭据校验没通过，先去「DNS 凭据」修好并重新校验")
		}
		created, err := queueOrder(ctx, tx, tenantID, id, "manual", actor.ID)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::text, state, reason, created_at FROM certificate_orders
			 WHERE tenant_id = $1 AND certificate_id = $2::uuid AND state IN ('queued', 'running')`,
			tenantID, id).Scan(&out.ID, &out.State, &out.Reason, &out.CreatedAt); err != nil {
			return err
		}
		if !created {
			return nil
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: actor.kind(), ActorID: actor.auditID(), Action: "certificate.renew_requested",
			ResourceType: "certificate", ResourceID: &id, APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"order_id": out.ID},
		})
	})
	return out, err
}

// SetCertificatePaused 暂停或恢复自动签发。暂停会取消排队中的订单（签发中的跑完为止）；
// 恢复清掉失败计数与退避，并立刻排一张订单。
func (s *Service) SetCertificatePaused(ctx context.Context, tenantID string, actor Actor, id string, paused bool) (Certificate, error) {
	if err := validID(id); err != nil {
		return Certificate{}, err
	}
	var out Certificate
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor.ID}, func(tx pgx.Tx) error {
		status, hasVersion, err := lockCertificate(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		action := "certificate.paused"
		if paused {
			if status == StatusPaused {
				return httpx.New(httpx.CodeConflict, "证书已经是暂停状态")
			}
			if _, err := tx.Exec(ctx, `UPDATE certificates SET status = 'paused', paused_reason = 'manual',
				row_version = row_version + 1 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE certificate_orders SET state = 'cancelled', finished_at = now(),
				error_code = 'paused' WHERE tenant_id = $1 AND certificate_id = $2::uuid AND state = 'queued'`,
				tenantID, id); err != nil {
				return err
			}
		} else {
			action = "certificate.resumed"
			if status != StatusPaused {
				return httpx.New(httpx.CodeConflict, "证书没有暂停")
			}
			next := StatusPending
			if hasVersion {
				next = StatusActive
			}
			if _, err := tx.Exec(ctx, `UPDATE certificates SET status = $3, paused_reason = NULL,
				consecutive_failures = 0, next_attempt_at = NULL, row_version = row_version + 1
				WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id, next); err != nil {
				return err
			}
			reason := "manual"
			if !hasVersion {
				reason = "initial"
			}
			if _, err := queueOrder(ctx, tx, tenantID, id, reason, actor.ID); err != nil {
				return err
			}
		}
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: actor.kind(), ActorID: actor.auditID(), Action: action,
			ResourceType: "certificate", ResourceID: &id, APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"status": status},
		}); err != nil {
			return err
		}
		out, err = s.getCertificate(ctx, tx, tenantID, id)
		return err
	})
	return out, err
}

// queueOrder 给证书排一张订单；已有进行中的订单时什么也不做（部分唯一索引兜底），返回是否新建。
// 续期订单带上当前版本的 ARI certID（replaces）。
func queueOrder(ctx context.Context, tx pgx.Tx, tenantID, certID, reason, actorID string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO certificate_orders (tenant_id, certificate_id, reason, replaces_ari_id, created_by)
		SELECT c.tenant_id, c.id, $3, v.ari_cert_id, nullif($4, '')::uuid
		  FROM certificates c
		  LEFT JOIN certificate_versions v ON v.tenant_id = c.tenant_id AND v.id = c.current_version_id
		 WHERE c.tenant_id = $1 AND c.id = $2::uuid
		ON CONFLICT (tenant_id, certificate_id) WHERE state IN ('queued', 'running') DO NOTHING`,
		tenantID, certID, reason, actorID)
	return tag.RowsAffected() == 1, err
}

// identifiersKey 把排好序的标识拼成比较用的键。
func identifiersKey(ids []string) string { return strings.Join(ids, ",") }
