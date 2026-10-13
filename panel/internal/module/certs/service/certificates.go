package service

import (
	"context"
	"errors"
	"slices"

	"github.com/aegispanel/aegis/internal/module/certs/model"
	"github.com/aegispanel/aegis/internal/module/certs/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// ListCertificates 列出全部证书与横幅计数。
func (s *Service) ListCertificates(ctx context.Context) (CertificateList, error) {
	rows, err := s.store.ListCertificates(ctx)
	if err != nil {
		return CertificateList{}, err
	}
	items := make([]Certificate, 0, len(rows))
	for _, r := range rows {
		c, err := s.certificateView(r)
		if err != nil {
			return CertificateList{}, err
		}
		items = append(items, c)
	}
	return CertificateList{Items: items, Summary: summarize(items)}, nil
}

// CertificateDetail 读一张证书的详情。
func (s *Service) CertificateDetail(ctx context.Context, id uuid.UUID) (CertificateDetail, error) {
	row, ok, err := s.store.GetCertificate(ctx, id)
	if err != nil {
		return CertificateDetail{}, err
	}
	if !ok {
		return CertificateDetail{}, notFound()
	}
	cert, err := s.certificateView(row)
	if err != nil {
		return CertificateDetail{}, err
	}
	vrows, err := s.store.ListVersions(ctx, id)
	if err != nil {
		return CertificateDetail{}, err
	}
	versions := make([]Version, 0, len(vrows))
	for _, r := range vrows {
		v, err := versionView(r)
		if err != nil {
			return CertificateDetail{}, err
		}
		versions = append(versions, v)
	}
	orows, err := s.store.ListOrders(ctx, id, 50)
	if err != nil {
		return CertificateDetail{}, err
	}
	orders := make([]Order, 0, len(orows))
	for _, r := range orows {
		orders = append(orders, orderView(r))
	}
	return CertificateDetail{Certificate: cert, Versions: versions, Orders: orders}, nil
}

// CreateCertificate 新建证书并排首次签发订单。
func (s *Service) CreateCertificate(ctx context.Context, in CertificateInput) (Certificate, error) {
	bad := map[string]string{}
	name, msg := validName(in.Name)
	if msg != "" {
		bad["name"] = msg
	}
	ids, msg := normalizeIdentifiers(in.Identifiers)
	if msg != "" {
		bad["identifiers"] = msg
	}
	keyType := in.KeyType
	if keyType == "" {
		keyType = KeyECDSAP256
	}
	if !validKeyType(keyType) {
		bad["key_type"] = "密钥类型只能是 ecdsa-p256 或 rsa-2048"
	}
	credID, err := uuid.Parse(in.DNSCredentialID)
	if err != nil {
		bad["dns_credential_id"] = "选一个 DNS 凭据"
	}
	var out Certificate
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if len(bad) == 0 {
			exists, err := s.store.CredentialExists(ctx, credID)
			if err != nil {
				return err
			}
			if !exists {
				bad["dns_credential_id"] = "选一个 DNS 凭据"
			}
		}
		if len(bad) > 0 {
			return errs.Invalid(bad)
		}
		id := uuid.Must(uuid.NewV7())
		if err := s.store.InsertCertificate(ctx, &model.Certificate{
			ID: id, Name: name, Identifiers: encodeIDs(ids), DNSCredentialID: credID,
			KeyType: keyType, Status: StatusPending, CreatedBy: actorID(ctx),
		}); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.Invalid(map[string]string{"name": "已有同名的证书"})
			}
			return err
		}
		if _, _, err := s.queueOrder(ctx, id, ReasonInitial); err != nil {
			return err
		}
		rid := id
		if err := s.writeAudit(ctx, "certificate.created", "certificate", &rid, "", map[string]any{
			"name": name, "identifiers": ids, "dns_credential_id": credID.String(), "key_type": keyType,
		}); err != nil {
			return err
		}
		row, ok, err := s.store.GetCertificate(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		out, err = s.certificateView(row)
		return err
	})
	return out, err
}

// UpdateCertificate 改证书规格；标识或密钥类型变了会排手动订单。
func (s *Service) UpdateCertificate(ctx context.Context, id uuid.UUID, p CertificatePatch) (Certificate, error) {
	var out Certificate
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		_, ok, err := s.store.LockCertificate(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		curRow, ok, err := s.store.GetCertificate(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		cur, err := s.certificateView(curRow)
		if err != nil {
			return err
		}
		bad := map[string]string{}
		name, ids, cred, key := cur.Name, cur.Identifiers, cur.DNSCredentialID, cur.KeyType
		if p.Name != nil {
			var msg string
			if name, msg = validName(*p.Name); msg != "" {
				bad["name"] = msg
			}
		}
		if p.Identifiers != nil {
			var msg string
			if ids, msg = normalizeIdentifiers(p.Identifiers); msg != "" {
				bad["identifiers"] = msg
			}
		}
		if p.DNSCredentialID != nil {
			parsed, err := uuid.Parse(*p.DNSCredentialID)
			if err != nil {
				bad["dns_credential_id"] = "选一个 DNS 凭据"
			} else {
				exists, err := s.store.CredentialExists(ctx, parsed)
				if err != nil {
					return err
				}
				if !exists {
					bad["dns_credential_id"] = "选一个 DNS 凭据"
				} else {
					cred = parsed
				}
			}
		}
		if p.KeyType != nil {
			if key = *p.KeyType; !validKeyType(key) {
				bad["key_type"] = "密钥类型只能是 ecdsa-p256 或 rsa-2048"
			}
		}
		if len(bad) > 0 {
			return errs.Invalid(bad)
		}
		reissue := !slices.Equal(ids, cur.Identifiers) || key != cur.KeyType
		credChanged := cred != cur.DNSCredentialID
		if err := s.store.UpdateCertificateSpec(ctx, id, repo.CertificateSpec{
			Name: name, Identifiers: encodeIDs(ids), DNSCredentialID: cred, KeyType: key,
			CredentialChanged: credChanged, Reissue: reissue,
		}); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.Invalid(map[string]string{"name": "已有同名的证书"})
			}
			return err
		}
		if reissue && (cur.Status == StatusActive || cur.Status == StatusPending) {
			if _, _, err := s.queueOrder(ctx, id, ReasonManual); err != nil {
				return err
			}
		}
		rid := id
		if err := s.writeAudit(ctx, "certificate.updated", "certificate", &rid, "", map[string]any{
			"before": map[string]any{"name": cur.Name, "identifiers": cur.Identifiers,
				"dns_credential_id": cur.DNSCredentialID.String(), "key_type": cur.KeyType},
			"after": map[string]any{"name": name, "identifiers": ids,
				"dns_credential_id": cred.String(), "key_type": key},
		}); err != nil {
			return err
		}
		row, ok, err := s.store.GetCertificate(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		out, err = s.certificateView(row)
		return err
	})
	return out, err
}

// DeleteCertificate 删除证书；签发进行中时不允许。
func (s *Service) DeleteCertificate(ctx context.Context, id uuid.UUID) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		_, ok, err := s.store.LockCertificate(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		curRow, ok, err := s.store.GetCertificate(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		cur, err := s.certificateView(curRow)
		if err != nil {
			return err
		}
		running, err := s.store.LockActiveOrders(ctx, id)
		if err != nil {
			return err
		}
		if running {
			return conflict("证书正在签发，等这次签发结束再删")
		}
		if err := s.store.DeleteCertificate(ctx, id); err != nil {
			return err
		}
		rid := id
		return s.writeAudit(ctx, "certificate.deleted", "certificate", &rid, "", map[string]any{
			"name": cur.Name, "identifiers": cur.Identifiers, "current_version": cur.CurrentVersion,
		})
	})
}

// RenewCertificate 立刻排一张手动签发订单。
func (s *Service) RenewCertificate(ctx context.Context, id uuid.UUID) (OrderBrief, error) {
	var out OrderBrief
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		locked, ok, err := s.store.LockCertificate(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		switch locked.Status {
		case StatusPaused:
			return conflict("证书已暂停自动签发，先点「恢复」")
		case StatusBlockedCredential:
			return conflict("证书的 DNS 凭据校验没通过，先去「DNS 凭据」修好并重新校验")
		}
		_, created, err := s.queueOrder(ctx, id, ReasonManual)
		if err != nil {
			return err
		}
		ob, ok, err := s.store.ActiveOrder(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Wrap(errs.Internal, errors.New("certs: 排单后没有进行中的订单"))
		}
		out = OrderBrief{ID: ob.ID, State: ob.State, Reason: ob.Reason, CreatedAt: ob.CreatedAt}
		if !created {
			return nil
		}
		rid := id
		return s.writeAudit(ctx, "certificate.renew_requested", "certificate", &rid, "", map[string]any{
			"order_id": out.ID.String(),
		})
	})
	return out, err
}

// SetCertificatePaused 暂停或恢复证书的自动签发。
func (s *Service) SetCertificatePaused(ctx context.Context, id uuid.UUID, paused bool) (Certificate, error) {
	var out Certificate
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		locked, ok, err := s.store.LockCertificate(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		status := locked.Status
		hasVersion := locked.CurrentVersionID != nil
		action := "certificate.paused"
		if paused {
			if status == StatusPaused {
				return conflict("证书已经是暂停状态")
			}
			if !CanTransition(status, StatusPaused) {
				return conflict("当前状态不能暂停")
			}
			reason := PausedManual
			ok, err := s.store.SetCertificateStatus(ctx, id, status, StatusPaused, &reason, false)
			if err != nil {
				return err
			}
			if !ok {
				return errs.New(errs.Conflict)
			}
			if err := s.store.CancelQueuedOrders(ctx, id, "paused"); err != nil {
				return err
			}
		} else {
			action = "certificate.resumed"
			if status != StatusPaused {
				return conflict("证书没有暂停")
			}
			target := StatusPending
			if hasVersion {
				target = StatusActive
			}
			if !CanTransition(status, target) {
				return conflict("当前状态不能恢复")
			}
			ok, err := s.store.SetCertificateStatus(ctx, id, status, target, nil, true)
			if err != nil {
				return err
			}
			if !ok {
				return errs.New(errs.Conflict)
			}
			if !hasVersion {
				if _, _, err := s.queueOrder(ctx, id, ReasonInitial); err != nil {
					return err
				}
			}
		}
		rid := id
		if err := s.writeAudit(ctx, action, "certificate", &rid, "", map[string]any{
			"before": map[string]any{"status": status},
		}); err != nil {
			return err
		}
		row, ok, err := s.store.GetCertificate(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		out, err = s.certificateView(row)
		return err
	})
	return out, err
}
