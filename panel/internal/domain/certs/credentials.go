package certs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// DNSCredential 是凭据的读形状：只有末四位提示，没有任何密文字段。
type DNSCredential struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Provider     string     `json:"provider"`
	Zone         string     `json:"zone"`
	SecretHint   string     `json:"secret_hint"`
	VerifyStatus string     `json:"verify_status"`
	VerifiedAt   *time.Time `json:"verified_at"`
	VerifyError  *string    `json:"verify_error"`
	VisibleZones *int       `json:"visible_zones"`
	// CertificateCount 是引用它的证书数；大于 0 时不能删
	CertificateCount int       `json:"certificate_count"`
	RowVersion       int64     `json:"row_version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// DNSCredentialInput 是新建凭据；Secret 的字段按提供方（providerFields）。
type DNSCredentialInput struct {
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	Zone     string            `json:"zone"`
	Secret   map[string]string `json:"secret"`
}

// DNSCredentialPatch 是修改凭据：缺席的字段（含 Secret 里缺席的键）保留原值；提供方不能改。
type DNSCredentialPatch struct {
	Name   *string           `json:"name"`
	Zone   *string           `json:"zone"`
	Secret map[string]string `json:"secret"`
}

// VerifyResult 是一次校验的结论，连同校验后的凭据一起回。
type VerifyResult struct {
	Credential DNSCredential `json:"credential"`
	OK         bool          `json:"ok"`
	// Message 是失败原因（成功时为空）
	Message  string   `json:"message"`
	Warnings []string `json:"warnings"`
	// Resumed 是因为这次校验通过而恢复自动签发的证书数
	Resumed int `json:"resumed"`
}

func credentialAAD(tenantID, id string) []byte {
	return []byte("dns_credential:" + tenantID + ":" + id)
}

const credentialColumns = `c.id::text, c.name, c.provider, c.zone, c.secret_hint, c.verify_status, c.verified_at,
	c.verify_error, c.visible_zones, c.row_version, c.created_at, c.updated_at,
	(SELECT count(*) FROM certificates x WHERE x.tenant_id = c.tenant_id AND x.dns_credential_id = c.id)::int`

func scanCredential(row pgx.Row, c *DNSCredential) error {
	return row.Scan(&c.ID, &c.Name, &c.Provider, &c.Zone, &c.SecretHint, &c.VerifyStatus, &c.VerifiedAt,
		&c.VerifyError, &c.VisibleZones, &c.RowVersion, &c.CreatedAt, &c.UpdatedAt, &c.CertificateCount)
}

// ListDNSCredentials 列出全部凭据（数量很少，不分页）。
func (s *Service) ListDNSCredentials(ctx context.Context, tenantID string) ([]DNSCredential, error) {
	out := []DNSCredential{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+credentialColumns+` FROM dns_credentials c
			 WHERE c.tenant_id = $1 ORDER BY lower(c.name), c.id`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c DNSCredential
			if err := scanCredential(rows, &c); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func getCredential(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (DNSCredential, error) {
	var c DNSCredential
	q := `SELECT ` + credentialColumns + ` FROM dns_credentials c WHERE c.tenant_id = $1 AND c.id = $2::uuid`
	if lock {
		q += ` FOR UPDATE OF c`
	}
	err := scanCredential(tx.QueryRow(ctx, q, tenantID, id), &c)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, httpx.NotFoundOrForbidden()
	}
	return c, err
}

func validName(raw string) (string, string) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", "填写名称"
	}
	if len([]rune(name)) > 64 {
		return "", "名称最多 64 个字"
	}
	return name, ""
}

func validID(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return httpx.NotFoundOrForbidden()
	}
	return nil
}

func (s *Service) sealSecret(tenantID, id string, secret map[string]string) ([]byte, error) {
	raw, err := json.Marshal(secret)
	if err != nil {
		return nil, err
	}
	return s.sealer.Seal(raw, credentialAAD(tenantID, id))
}

func (s *Service) openSecret(tenantID, id string, sealed []byte) (map[string]string, error) {
	raw, err := s.sealer.Open(sealed, credentialAAD(tenantID, id))
	if err != nil {
		return nil, err
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func credentialNameTaken(err error) error {
	if db.IsUniqueViolation(err) && db.ConstraintName(err) == "dns_credentials_name_key" {
		return httpx.Invalid(map[string]string{"name": "已有同名的 DNS 凭据"})
	}
	return err
}

// CreateDNSCredential 新建凭据并立刻校验一次（读 + 写 TXT）。校验失败不回滚新建：凭据留着，
// 界面显示失败原因，改好后再校验。
func (s *Service) CreateDNSCredential(ctx context.Context, tenantID string, actor Actor, in DNSCredentialInput) (VerifyResult, error) {
	bad := map[string]string{}
	name, msg := validName(in.Name)
	if msg != "" {
		bad["name"] = msg
	}
	zone, msg := normalizeZone(in.Zone)
	if msg != "" {
		bad["zone"] = msg
	}
	secret, secretBad := mergeSecret(in.Provider, nil, in.Secret)
	for k, v := range secretBad {
		bad[k] = v
	}
	if len(bad) > 0 {
		return VerifyResult{}, httpx.Invalid(bad)
	}
	id := uuid.Must(uuid.NewV7()).String()
	sealed, err := s.sealSecret(tenantID, id, secret)
	if err != nil {
		return VerifyResult{}, err
	}
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor.ID}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO dns_credentials (id, tenant_id, name, provider, zone, secret_sealed, secret_hint)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)`,
			id, tenantID, name, in.Provider, zone, sealed, secretHint(in.Provider, secret)); err != nil {
			return credentialNameTaken(err)
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: actor.kind(), ActorID: actor.auditID(), Action: "dns_credential.created",
			ResourceType: "dns_credential", ResourceID: &id, APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"name": name, "provider": in.Provider, "zone": zone},
		})
	})
	if err != nil {
		return VerifyResult{}, err
	}
	return s.VerifyDNSCredential(ctx, tenantID, actor, id)
}

// UpdateDNSCredential 改名、改 zone、换凭据字段。凭据或 zone 变了就回到「未校验」并立刻重新校验；
// 只改名不校验。
func (s *Service) UpdateDNSCredential(ctx context.Context, tenantID string, actor Actor, id string, in DNSCredentialPatch) (VerifyResult, error) {
	if err := validID(id); err != nil {
		return VerifyResult{}, err
	}
	var out DNSCredential
	reverify := false
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor.ID}, func(tx pgx.Tx) error {
		cur, err := getCredential(ctx, tx, tenantID, id, true)
		if err != nil {
			return err
		}
		bad := map[string]string{}
		name, zone := cur.Name, cur.Zone
		if in.Name != nil {
			var msg string
			if name, msg = validName(*in.Name); msg != "" {
				bad["name"] = msg
			}
		}
		if in.Zone != nil {
			var msg string
			if zone, msg = normalizeZone(*in.Zone); msg != "" {
				bad["zone"] = msg
			}
		}
		var sealed []byte
		hint := cur.SecretHint
		if len(in.Secret) > 0 {
			var oldSealed []byte
			if err := tx.QueryRow(ctx, `SELECT secret_sealed FROM dns_credentials WHERE tenant_id = $1 AND id = $2::uuid`,
				tenantID, id).Scan(&oldSealed); err != nil {
				return err
			}
			old, err := s.openSecret(tenantID, id, oldSealed)
			if err != nil {
				return fmt.Errorf("open dns credential: %w", err)
			}
			secret, secretBad := mergeSecret(cur.Provider, old, in.Secret)
			for k, v := range secretBad {
				bad[k] = v
			}
			if len(secretBad) == 0 {
				if sealed, err = s.sealSecret(tenantID, id, secret); err != nil {
					return err
				}
				hint = secretHint(cur.Provider, secret)
			}
		}
		if len(bad) > 0 {
			return httpx.Invalid(bad)
		}
		reverify = sealed != nil || zone != cur.Zone
		if _, err := tx.Exec(ctx, `
			UPDATE dns_credentials
			   SET name = $3, zone = $4, secret_sealed = coalesce($5, secret_sealed), secret_hint = $6,
			       verify_status = CASE WHEN $7 THEN 'unverified' ELSE verify_status END,
			       verified_at = CASE WHEN $7 THEN NULL ELSE verified_at END,
			       verify_error = CASE WHEN $7 THEN NULL ELSE verify_error END,
			       visible_zones = CASE WHEN $7 THEN NULL ELSE visible_zones END,
			       row_version = row_version + 1
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, id, name, zone, sealed, hint, reverify); err != nil {
			return credentialNameTaken(err)
		}
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: actor.kind(), ActorID: actor.auditID(), Action: "dns_credential.updated",
			ResourceType: "dns_credential", ResourceID: &id, APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"name": cur.Name, "zone": cur.Zone},
			AfterDigest:  map[string]any{"name": name, "zone": zone, "secret_changed": sealed != nil},
		}); err != nil {
			return err
		}
		out, err = getCredential(ctx, tx, tenantID, id, false)
		return err
	})
	if err != nil {
		return VerifyResult{}, err
	}
	if reverify {
		return s.VerifyDNSCredential(ctx, tenantID, actor, id)
	}
	return VerifyResult{Credential: out, OK: out.VerifyStatus == "ok", Warnings: []string{}}, nil
}

// DeleteDNSCredential 删除凭据；还有证书引用它时回 409 并列出证书名。
func (s *Service) DeleteDNSCredential(ctx context.Context, tenantID string, actor Actor, id string) error {
	if err := validID(id); err != nil {
		return err
	}
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor.ID}, func(tx pgx.Tx) error {
		cur, err := getCredential(ctx, tx, tenantID, id, true)
		if err != nil {
			return err
		}
		var names []string
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(array_agg(name ORDER BY lower(name)), '{}') FROM (
			  SELECT name FROM certificates WHERE tenant_id = $1 AND dns_credential_id = $2::uuid
			   ORDER BY lower(name) LIMIT 5) t`, tenantID, id).Scan(&names); err != nil {
			return err
		}
		if len(names) > 0 {
			return httpx.New(httpx.CodeConflict, "还有证书在用这个凭据（"+strings.Join(names, "、")+"），先改用别的凭据或删掉这些证书")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM dns_credentials WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: actor.kind(), ActorID: actor.auditID(), Action: "dns_credential.deleted",
			ResourceType: "dns_credential", ResourceID: &id, APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"name": cur.Name, "provider": cur.Provider, "zone": cur.Zone},
		})
	})
}

// loadCredentialSecret 读凭据与解开的字段（worker 与校验用）。
func (s *Service) loadCredentialSecret(ctx context.Context, tx pgx.Tx, tenantID, id string) (DNSCredential, map[string]string, error) {
	c, err := getCredential(ctx, tx, tenantID, id, false)
	if err != nil {
		return c, nil, err
	}
	var sealed []byte
	if err := tx.QueryRow(ctx, `SELECT secret_sealed FROM dns_credentials WHERE tenant_id = $1 AND id = $2::uuid`,
		tenantID, id).Scan(&sealed); err != nil {
		return c, nil, err
	}
	secret, err := s.openSecret(tenantID, id, sealed)
	if err != nil {
		return c, nil, fmt.Errorf("open dns credential: %w", err)
	}
	return c, secret, nil
}

// VerifyDNSCredential 调提供方 API 校验凭据：列 zone（鉴权 + 看得到 zone + 数 zone），再建删一条
// _pandora-check TXT（证明能写）。提供方明确拒绝时凭据标 error，用它的证书停在 blocked_credential；
// 通过时把被它卡住的证书恢复成自动签发。网络错误只回结论、不改状态。
func (s *Service) VerifyDNSCredential(ctx context.Context, tenantID string, actor Actor, id string) (VerifyResult, error) {
	if err := validID(id); err != nil {
		return VerifyResult{}, err
	}
	var cred DNSCredential
	var secret map[string]string
	if err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var err error
		cred, secret, err = s.loadCredentialSecret(ctx, tx, tenantID, id)
		return err
	}); err != nil {
		return VerifyResult{}, err
	}
	provider, err := s.newDNSProvider(cred.Provider, secret)
	if err != nil {
		return VerifyResult{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	res, checkErr := provider.check(cctx, cred.Zone, true)
	cancel()

	out := VerifyResult{OK: checkErr == nil, Warnings: res.Warnings}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	if res.VisibleZones > 1 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("这个凭据能看到 %d 个域名，不是最小权限：建议只授权 %s", res.VisibleZones, cred.Zone))
	}
	var credErr *credentialError
	rejected := errors.As(checkErr, &credErr)
	switch {
	case checkErr == nil:
	case rejected:
		out.Message = credErr.msg
	default:
		out.Message = "暂时连不上 DNS 提供方，稍后再校验：" + providerErrorText(checkErr)
	}
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor.ID}, func(tx pgx.Tx) error {
		if checkErr == nil || rejected {
			status, verr := "ok", (*string)(nil)
			var zones *int
			if rejected {
				status = "error"
				m := truncate(credErr.msg, 500)
				verr = &m
			} else {
				zones = &res.VisibleZones
			}
			// 校验期间凭据被改过（row_version 变了）就不写这次的结论，免得旧凭据的结果盖住新凭据
			tag, err := tx.Exec(ctx, `
				UPDATE dns_credentials
				   SET verify_status = $4, verified_at = CASE WHEN $4 = 'ok' THEN now() ELSE verified_at END,
				       verify_error = $5, visible_zones = coalesce($6, visible_zones)
				 WHERE tenant_id = $1 AND id = $2::uuid AND row_version = $3`,
				tenantID, id, cred.RowVersion, status, verr, zones)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				if out.Resumed, err = setCredentialBlocked(ctx, tx, tenantID, id, rejected); err != nil {
					return err
				}
			}
		}
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: actor.kind(), ActorID: actor.auditID(), Action: "dns_credential.verified",
			ResourceType: "dns_credential", ResourceID: &id, APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			Outcome:     map[bool]string{true: "success", false: "failure"}[checkErr == nil],
			AfterDigest: map[string]any{"ok": checkErr == nil, "rejected": rejected, "visible_zones": res.VisibleZones, "resumed": out.Resumed},
		}); err != nil {
			return err
		}
		out.Credential, err = getCredential(ctx, tx, tenantID, id, false)
		return err
	})
	return out, err
}

// setCredentialBlocked 按凭据的校验结论切换引用它的证书：blocked=true 时把自动签发中的证书停在
// blocked_credential；false 时把被它卡住的证书恢复（没签出过的回 pending，签出过的回 active），
// 并清掉退避，下一轮 worker 就排队。返回恢复的证书数。
func setCredentialBlocked(ctx context.Context, tx pgx.Tx, tenantID, credentialID string, blocked bool) (int, error) {
	if blocked {
		_, err := tx.Exec(ctx, `
			UPDATE certificates SET status = 'blocked_credential', row_version = row_version + 1
			 WHERE tenant_id = $1 AND dns_credential_id = $2::uuid AND status IN ('pending', 'active')`,
			tenantID, credentialID)
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE certificates
		   SET status = CASE WHEN current_version_id IS NULL THEN 'pending' ELSE 'active' END,
		       next_attempt_at = NULL, row_version = row_version + 1
		 WHERE tenant_id = $1 AND dns_credential_id = $2::uuid AND status = 'blocked_credential'`,
		tenantID, credentialID)
	return int(tag.RowsAffected()), err
}
