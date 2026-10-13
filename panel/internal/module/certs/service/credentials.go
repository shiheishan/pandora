package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/certs/model"
	"github.com/aegispanel/aegis/internal/module/certs/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// ListDNSCredentials 列出全部 DNS 凭据。
func (s *Service) ListDNSCredentials(ctx context.Context) ([]DNSCredential, error) {
	rows, err := s.store.ListCredentials(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DNSCredential, 0, len(rows))
	for _, r := range rows {
		out = append(out, credentialView(r))
	}
	return out, nil
}

// CreateDNSCredential 新建凭据并立刻校验一次。
func (s *Service) CreateDNSCredential(ctx context.Context, in DNSCredentialInput) (VerifyResult, error) {
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
	for k, v := range secret {
		if strings.ContainsRune(v, 0) {
			bad["secret."+k] = "不能包含空字符"
		}
	}
	if len(bad) > 0 {
		return VerifyResult{}, errs.Invalid(bad)
	}
	id := uuid.Must(uuid.NewV7())
	sealed, err := s.sealSecret(id, secret)
	if err != nil {
		return VerifyResult{}, err
	}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.store.InsertCredential(ctx, &model.DNSCredential{
			ID: id, Name: name, Provider: in.Provider, Zone: zone,
			SecretSealed: sealed, SecretHint: secretHint(in.Provider, secret),
		}); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.Invalid(map[string]string{"name": "已有同名的 DNS 凭据"})
			}
			return err
		}
		rid := id
		return s.writeAudit(ctx, "dns_credential.created", "dns_credential", &rid, "", map[string]any{
			"name": name, "provider": in.Provider, "zone": zone,
		})
	})
	if err != nil {
		return VerifyResult{}, err
	}
	return s.VerifyDNSCredential(ctx, id)
}

// UpdateDNSCredential 改名、改 zone、换凭据字段；凭据或 zone 变了会重新校验。
func (s *Service) UpdateDNSCredential(ctx context.Context, id uuid.UUID, p DNSCredentialPatch) (VerifyResult, error) {
	var out DNSCredential
	reverify := false
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		cur, ok, err := s.store.GetCredential(ctx, id, true)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		bad := map[string]string{}
		name, zone := cur.Name, cur.Zone
		if p.Name != nil {
			var msg string
			if name, msg = validName(*p.Name); msg != "" {
				bad["name"] = msg
			}
		}
		if p.Zone != nil {
			var msg string
			if zone, msg = normalizeZone(*p.Zone); msg != "" {
				bad["zone"] = msg
			}
		}
		var sealed []byte
		hint := cur.SecretHint
		if len(p.Secret) > 0 {
			var old map[string]string
			if !secretComplete(cur.Provider, p.Secret) {
				oldSealed, ok, err := s.store.CredentialSecret(ctx, id)
				if err != nil {
					return err
				}
				if !ok {
					return notFound()
				}
				old, err = s.openSecret(id, oldSealed)
				if err != nil {
					return err
				}
			}
			secret, secretBad := mergeSecret(cur.Provider, old, p.Secret)
			for k, v := range secretBad {
				bad[k] = v
			}
			for k, v := range secret {
				if strings.ContainsRune(v, 0) {
					bad["secret."+k] = "不能包含空字符"
				}
			}
			if len(bad) == 0 {
				sealed, err = s.sealSecret(id, secret)
				if err != nil {
					return err
				}
				hint = secretHint(cur.Provider, secret)
			}
		}
		if len(bad) > 0 {
			return errs.Invalid(bad)
		}
		reverify = sealed != nil || zone != cur.Zone
		if err := s.store.UpdateCredential(ctx, id, repo.CredentialUpdate{
			Name: name, Zone: zone, SecretSealed: sealed, SecretHint: hint, Reverify: reverify,
		}); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.Invalid(map[string]string{"name": "已有同名的 DNS 凭据"})
			}
			return err
		}
		rid := id
		if err := s.writeAudit(ctx, "dns_credential.updated", "dns_credential", &rid, "", map[string]any{
			"before": map[string]any{"name": cur.Name, "zone": cur.Zone},
			"after":  map[string]any{"name": name, "zone": zone, "secret_changed": sealed != nil},
		}); err != nil {
			return err
		}
		cur, ok, err = s.store.GetCredential(ctx, id, false)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		out = credentialView(cur)
		return nil
	})
	if err != nil {
		return VerifyResult{}, err
	}
	if reverify {
		return s.VerifyDNSCredential(ctx, id)
	}
	return VerifyResult{Credential: out, OK: out.VerifyStatus == VerifyOK, Warnings: []string{}}, nil
}

// DeleteDNSCredential 删除凭据；仍有证书引用时拒绝。
func (s *Service) DeleteDNSCredential(ctx context.Context, id uuid.UUID) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		cur, ok, err := s.store.GetCredential(ctx, id, true)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		names, err := s.store.CertificateNamesByCredential(ctx, id, 5)
		if err != nil {
			return err
		}
		if len(names) > 0 {
			return conflict("还有证书在用这个凭据（" + strings.Join(names, "、") + "），先改用别的凭据或删掉这些证书")
		}
		if err := s.store.DeleteCredential(ctx, id); err != nil {
			if errors.Is(err, repo.ErrForeignKey) {
				if len(names) == 0 {
					names, err = s.store.CertificateNamesByCredential(ctx, id, 5)
					if err != nil {
						return err
					}
				}
				return conflict("还有证书在用这个凭据（" + strings.Join(names, "、") + "），先改用别的凭据或删掉这些证书")
			}
			return err
		}
		rid := id
		return s.writeAudit(ctx, "dns_credential.deleted", "dns_credential", &rid, "", map[string]any{
			"name": cur.Name, "provider": cur.Provider, "zone": cur.Zone,
		})
	})
}

// VerifyDNSCredential 调提供方 API 校验凭据并写结论。
func (s *Service) VerifyDNSCredential(ctx context.Context, id uuid.UUID) (VerifyResult, error) {
	cred, secret, err := s.loadCredentialSecret(ctx, id)
	if err != nil {
		return VerifyResult{}, err
	}
	provider, err := s.newDNSProvider(cred.Provider, secret)
	if err != nil {
		return VerifyResult{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
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
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if checkErr == nil || rejected {
			verdict := repo.CredentialVerdict{Status: VerifyOK}
			if rejected {
				verdict.Status = VerifyError
				m := truncate(credErr.msg, 500)
				verdict.VerifyError = &m
			} else {
				verdict.VisibleZones = &res.VisibleZones
			}
			wrote, err := s.store.RecordCredentialVerdict(ctx, id, cred.RowVersion, verdict)
			if err != nil {
				return err
			}
			if wrote {
				if rejected {
					if err := s.store.BlockCertificatesByCredential(ctx, id); err != nil {
						return err
					}
				} else {
					out.Resumed, err = s.store.ResumeCertificatesByCredential(ctx, id)
					if err != nil {
						return err
					}
				}
			}
		}
		outcome := ""
		if checkErr != nil {
			outcome = auditsvc.OutcomeFailure
		}
		rid := id
		if err := s.writeAudit(ctx, "dns_credential.verified", "dns_credential", &rid, outcome, map[string]any{
			"ok": checkErr == nil, "rejected": rejected, "visible_zones": res.VisibleZones, "resumed": out.Resumed,
		}); err != nil {
			return err
		}
		row, ok, err := s.store.GetCredential(ctx, id, false)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		out.Credential = credentialView(row)
		return nil
	})
	return out, err
}

func (s *Service) sealSecret(id uuid.UUID, secret map[string]string) ([]byte, error) {
	raw, err := json.Marshal(secret)
	if err != nil {
		return nil, err
	}
	return s.sealer.Seal(raw, credentialAAD(id))
}

func (s *Service) openSecret(id uuid.UUID, sealed []byte) (map[string]string, error) {
	raw, err := s.sealer.Open(sealed, credentialAAD(id))
	if err != nil {
		return nil, fmt.Errorf("open dns credential: %w", err)
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) loadCredentialSecret(ctx context.Context, id uuid.UUID) (repo.CredentialRow, map[string]string, error) {
	row, ok, err := s.store.GetCredential(ctx, id, false)
	if err != nil {
		return repo.CredentialRow{}, nil, err
	}
	if !ok {
		return repo.CredentialRow{}, nil, notFound()
	}
	sealed, ok, err := s.store.CredentialSecret(ctx, id)
	if err != nil {
		return repo.CredentialRow{}, nil, err
	}
	if !ok {
		return repo.CredentialRow{}, nil, notFound()
	}
	secret, err := s.openSecret(id, sealed)
	if err != nil {
		return repo.CredentialRow{}, nil, err
	}
	return row, secret, nil
}
