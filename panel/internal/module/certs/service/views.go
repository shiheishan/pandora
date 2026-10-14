package service

import (
	"encoding/hex"

	"github.com/aegispanel/aegis/internal/module/certs/repo"
)

// credentialView 把凭据行转成对外读形状。
func credentialView(r repo.CredentialRow) DNSCredential {
	return DNSCredential{
		ID:               r.ID,
		Name:             r.Name,
		Provider:         r.Provider,
		Zone:             r.Zone,
		SecretHint:       r.SecretHint,
		VerifyStatus:     r.VerifyStatus,
		VerifiedAt:       r.VerifiedAt,
		VerifyError:      r.VerifyError,
		VisibleZones:     r.VisibleZones,
		CertificateCount: r.CertificateCount,
		RowVersion:       r.RowVersion,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
}

// certificateView 把证书行转成对外读形状。
func (s *Service) certificateView(r repo.CertificateRow) (Certificate, error) {
	ids, err := decodeIDs(r.Identifiers)
	if err != nil {
		return Certificate{}, err
	}
	c := Certificate{
		ID:                  r.ID,
		Name:                r.Name,
		Identifiers:         ids,
		Wildcard:            hasWildcard(ids),
		Challenge:           r.Challenge,
		DNSCredentialID:     r.DNSCredentialID,
		DNSCredentialName:   r.DNSCredentialName,
		DNSProvider:         r.DNSProvider,
		KeyType:             r.KeyType,
		Status:              r.Status,
		PausedReason:        r.PausedReason,
		CurrentVersion:      r.CurrentVersion,
		CurrentCA:           r.CurrentCA,
		NotBefore:           r.NotBefore,
		NotAfter:            r.NotAfter,
		RenewAfter:          r.RenewAfter,
		ARIWindowStart:      r.ARIWindowStart,
		ARIWindowEnd:        r.ARIWindowEnd,
		NextAttemptAt:       r.NextAttemptAt,
		ConsecutiveFailures: r.ConsecutiveFailures,
		LastErrorCode:       r.LastErrorCode,
		LastError:           r.LastError,
		ExpiryLevel:         expiryLevel(r.NotBefore, r.NotAfter, s.opts.Now()),
		RowVersion:          r.RowVersion,
		CreatedAt:           r.CreatedAt,
		UpdatedAt:           r.UpdatedAt,
	}
	if r.OrderID != nil {
		c.ActiveOrder = &OrderBrief{
			ID:        *r.OrderID,
			State:     *r.OrderState,
			Reason:    *r.OrderReason,
			CreatedAt: *r.OrderCreatedAt,
		}
	}
	return c, nil
}

// versionView 把版本行转成对外读形状。
func versionView(r repo.VersionRow) (Version, error) {
	ids, err := decodeIDs(r.Identifiers)
	if err != nil {
		return Version{}, err
	}
	return Version{
		ID:              r.ID,
		Version:         r.Version,
		CA:              r.CA,
		Identifiers:     ids,
		IsRenewal:       r.IsRenewal,
		Serial:          r.Serial,
		NotBefore:       r.NotBefore,
		NotAfter:        r.NotAfter,
		ChainSHA256:     hex.EncodeToString(r.ChainSHA256),
		PublicKeySHA256: hex.EncodeToString(r.PublicKeySHA256),
		Current:         r.Current,
		CreatedAt:       r.CreatedAt,
	}, nil
}

// orderView 把订单行转成对外读形状。
func orderView(r repo.OrderRow) Order {
	return Order{
		ID:          r.ID,
		Reason:      r.Reason,
		State:       r.State,
		CA:          r.CA,
		Replaces:    r.Replaces,
		Attempt:     r.Attempt,
		ErrorCode:   r.ErrorCode,
		ErrorDetail: r.ErrorDetail,
		Version:     r.Version,
		CreatedAt:   r.CreatedAt,
		StartedAt:   r.StartedAt,
		FinishedAt:  r.FinishedAt,
	}
}

// summarize 汇总列表上的横幅计数。
func summarize(items []Certificate) CertificateSummary {
	var sum CertificateSummary
	for _, c := range items {
		sum.Total++
		switch c.ExpiryLevel {
		case ExpiryWarning:
			sum.Warning++
		case ExpiryCritical:
			sum.Critical++
		case ExpiryExpired:
			sum.Expired++
		}
		if c.ConsecutiveFailures > 0 || c.Status == StatusBlockedCredential {
			sum.Failing++
		}
		switch c.Status {
		case StatusPaused:
			sum.Paused++
		case StatusBlockedCredential:
			sum.Blocked++
		}
	}
	return sum
}
