package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/certs/service"
)

// --- 请求 DTO ---

type createCertificateRequest struct {
	Name            string   `json:"name"`
	Identifiers     []string `json:"identifiers"`
	DNSCredentialID string   `json:"dns_credential_id"`
	KeyType         string   `json:"key_type"`
}

func (r createCertificateRequest) input() service.CertificateInput {
	ids := r.Identifiers
	if ids == nil {
		ids = []string{}
	}
	return service.CertificateInput{
		Name:            r.Name,
		Identifiers:     ids,
		DNSCredentialID: r.DNSCredentialID,
		KeyType:         r.KeyType,
	}
}

type patchCertificateRequest struct {
	Name            *string  `json:"name"`
	Identifiers     []string `json:"identifiers"`
	DNSCredentialID *string  `json:"dns_credential_id"`
	KeyType         *string  `json:"key_type"`
}

func (r patchCertificateRequest) patch() service.CertificatePatch {
	var ids []string
	if r.Identifiers != nil {
		ids = r.Identifiers
	}
	return service.CertificatePatch{
		Name:            r.Name,
		Identifiers:     ids,
		DNSCredentialID: r.DNSCredentialID,
		KeyType:         r.KeyType,
	}
}

type createDNSCredentialRequest struct {
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	Zone     string            `json:"zone"`
	Secret   map[string]string `json:"secret"`
}

func (r createDNSCredentialRequest) input() service.DNSCredentialInput {
	sec := r.Secret
	if sec == nil {
		sec = map[string]string{}
	}
	return service.DNSCredentialInput{
		Name:     r.Name,
		Provider: r.Provider,
		Zone:     r.Zone,
		Secret:   sec,
	}
}

type patchDNSCredentialRequest struct {
	Name   *string           `json:"name"`
	Zone   *string           `json:"zone"`
	Secret map[string]string `json:"secret"`
}

func (r patchDNSCredentialRequest) patch() service.DNSCredentialPatch {
	return service.DNSCredentialPatch{
		Name:   r.Name,
		Zone:   r.Zone,
		Secret: r.Secret,
	}
}

type acmeSettingsRequest struct {
	ContactEmail   string  `json:"contact_email"`
	UseStaging     bool    `json:"use_staging"`
	ZeroSSLEnabled bool    `json:"zerossl_enabled"`
	ZeroSSLEABKID  string  `json:"zerossl_eab_kid"`
	ZeroSSLEABHMAC *string `json:"zerossl_eab_hmac"`
}

func (r acmeSettingsRequest) input() service.ACMESettingsInput {
	return service.ACMESettingsInput{
		ContactEmail:   r.ContactEmail,
		UseStaging:     r.UseStaging,
		ZeroSSLEnabled: r.ZeroSSLEnabled,
		ZeroSSLEABKID:  r.ZeroSSLEABKID,
		ZeroSSLEABHMAC: r.ZeroSSLEABHMAC,
	}
}

// --- 响应 DTO ---

type orderBriefResponse struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

func orderBriefFrom(o service.OrderBrief) orderBriefResponse {
	return orderBriefResponse{
		ID:        o.ID.String(),
		State:     o.State,
		Reason:    o.Reason,
		CreatedAt: o.CreatedAt,
	}
}

type certificateResponse struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	Identifiers         []string            `json:"identifiers"`
	Wildcard            bool                `json:"wildcard"`
	Challenge           string              `json:"challenge"`
	DNSCredentialID     string              `json:"dns_credential_id"`
	DNSCredentialName   string              `json:"dns_credential_name"`
	DNSProvider         string              `json:"dns_provider"`
	KeyType             string              `json:"key_type"`
	Status              string              `json:"status"`
	PausedReason        *string             `json:"paused_reason"`
	CurrentVersion      *int                `json:"current_version"`
	CurrentCA           *string             `json:"current_ca"`
	NotBefore           *time.Time          `json:"not_before"`
	NotAfter            *time.Time          `json:"not_after"`
	RenewAfter          *time.Time          `json:"renew_after"`
	ARIWindowStart      *time.Time          `json:"ari_window_start"`
	ARIWindowEnd        *time.Time          `json:"ari_window_end"`
	NextAttemptAt       *time.Time          `json:"next_attempt_at"`
	ConsecutiveFailures int                 `json:"consecutive_failures"`
	LastErrorCode       *string             `json:"last_error_code"`
	LastError           *string             `json:"last_error"`
	ExpiryLevel         string              `json:"expiry_level"`
	ActiveOrder         *orderBriefResponse `json:"active_order"`
	RowVersion          int64               `json:"row_version"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

func certificateFrom(c service.Certificate) certificateResponse {
	ids := c.Identifiers
	if ids == nil {
		ids = []string{}
	}
	var active *orderBriefResponse
	if c.ActiveOrder != nil {
		ob := orderBriefFrom(*c.ActiveOrder)
		active = &ob
	}
	return certificateResponse{
		ID:                  c.ID.String(),
		Name:                c.Name,
		Identifiers:         ids,
		Wildcard:            c.Wildcard,
		Challenge:           c.Challenge,
		DNSCredentialID:     c.DNSCredentialID.String(),
		DNSCredentialName:   c.DNSCredentialName,
		DNSProvider:         c.DNSProvider,
		KeyType:             c.KeyType,
		Status:              c.Status,
		PausedReason:        c.PausedReason,
		CurrentVersion:      c.CurrentVersion,
		CurrentCA:           c.CurrentCA,
		NotBefore:           c.NotBefore,
		NotAfter:            c.NotAfter,
		RenewAfter:          c.RenewAfter,
		ARIWindowStart:      c.ARIWindowStart,
		ARIWindowEnd:        c.ARIWindowEnd,
		NextAttemptAt:       c.NextAttemptAt,
		ConsecutiveFailures: c.ConsecutiveFailures,
		LastErrorCode:       c.LastErrorCode,
		LastError:           c.LastError,
		ExpiryLevel:         string(c.ExpiryLevel),
		ActiveOrder:         active,
		RowVersion:          c.RowVersion,
		CreatedAt:           c.CreatedAt,
		UpdatedAt:           c.UpdatedAt,
	}
}

type certificateSummaryResponse struct {
	Total    int `json:"total"`
	Warning  int `json:"warning"`
	Critical int `json:"critical"`
	Expired  int `json:"expired"`
	Failing  int `json:"failing"`
	Paused   int `json:"paused"`
	Blocked  int `json:"blocked"`
}

func summaryFrom(s service.CertificateSummary) certificateSummaryResponse {
	return certificateSummaryResponse{
		Total:    s.Total,
		Warning:  s.Warning,
		Critical: s.Critical,
		Expired:  s.Expired,
		Failing:  s.Failing,
		Paused:   s.Paused,
		Blocked:  s.Blocked,
	}
}

type certificateListResponse struct {
	Items   []certificateResponse      `json:"items"`
	Summary certificateSummaryResponse `json:"summary"`
}

func listFrom(l service.CertificateList) certificateListResponse {
	items := make([]certificateResponse, len(l.Items))
	for i, c := range l.Items {
		items[i] = certificateFrom(c)
	}
	return certificateListResponse{Items: items, Summary: summaryFrom(l.Summary)}
}

type versionResponse struct {
	ID              string    `json:"id"`
	Version         int       `json:"version"`
	CA              string    `json:"ca"`
	Identifiers     []string  `json:"identifiers"`
	IsRenewal       bool      `json:"is_renewal"`
	Serial          string    `json:"serial"`
	NotBefore       time.Time `json:"not_before"`
	NotAfter        time.Time `json:"not_after"`
	ChainSHA256     string    `json:"chain_sha256"`
	PublicKeySHA256 string    `json:"public_key_sha256"`
	Current         bool      `json:"current"`
	CreatedAt       time.Time `json:"created_at"`
}

func versionFrom(v service.Version) versionResponse {
	ids := v.Identifiers
	if ids == nil {
		ids = []string{}
	}
	return versionResponse{
		ID:              v.ID.String(),
		Version:         v.Version,
		CA:              v.CA,
		Identifiers:     ids,
		IsRenewal:       v.IsRenewal,
		Serial:          v.Serial,
		NotBefore:       v.NotBefore,
		NotAfter:        v.NotAfter,
		ChainSHA256:     v.ChainSHA256,
		PublicKeySHA256: v.PublicKeySHA256,
		Current:         v.Current,
		CreatedAt:       v.CreatedAt,
	}
}

type orderResponse struct {
	ID          string     `json:"id"`
	Reason      string     `json:"reason"`
	State       string     `json:"state"`
	CA          *string    `json:"ca"`
	Replaces    bool       `json:"replaces"`
	Attempt     int        `json:"attempt"`
	ErrorCode   *string    `json:"error_code"`
	ErrorDetail *string    `json:"error_detail"`
	Version     *int       `json:"version"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

func orderFrom(o service.Order) orderResponse {
	return orderResponse{
		ID:          o.ID.String(),
		Reason:      o.Reason,
		State:       o.State,
		CA:          o.CA,
		Replaces:    o.Replaces,
		Attempt:     o.Attempt,
		ErrorCode:   o.ErrorCode,
		ErrorDetail: o.ErrorDetail,
		Version:     o.Version,
		CreatedAt:   o.CreatedAt,
		StartedAt:   o.StartedAt,
		FinishedAt:  o.FinishedAt,
	}
}

type certificateDetailResponse struct {
	Certificate certificateResponse `json:"certificate"`
	Versions    []versionResponse   `json:"versions"`
	Orders      []orderResponse     `json:"orders"`
}

func detailFrom(d service.CertificateDetail) certificateDetailResponse {
	vers := make([]versionResponse, len(d.Versions))
	for i, v := range d.Versions {
		vers[i] = versionFrom(v)
	}
	ords := make([]orderResponse, len(d.Orders))
	for i, o := range d.Orders {
		ords[i] = orderFrom(o)
	}
	return certificateDetailResponse{
		Certificate: certificateFrom(d.Certificate),
		Versions:    vers,
		Orders:      ords,
	}
}

type dnsCredentialResponse struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Provider         string     `json:"provider"`
	Zone             string     `json:"zone"`
	SecretHint       string     `json:"secret_hint"`
	VerifyStatus     string     `json:"verify_status"`
	VerifiedAt       *time.Time `json:"verified_at"`
	VerifyError      *string    `json:"verify_error"`
	VisibleZones     *int       `json:"visible_zones"`
	CertificateCount int        `json:"certificate_count"`
	RowVersion       int64      `json:"row_version"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func credentialFrom(c service.DNSCredential) dnsCredentialResponse {
	return dnsCredentialResponse{
		ID:               c.ID.String(),
		Name:             c.Name,
		Provider:         c.Provider,
		Zone:             c.Zone,
		SecretHint:       c.SecretHint,
		VerifyStatus:     c.VerifyStatus,
		VerifiedAt:       c.VerifiedAt,
		VerifyError:      c.VerifyError,
		VisibleZones:     c.VisibleZones,
		CertificateCount: c.CertificateCount,
		RowVersion:       c.RowVersion,
		CreatedAt:        c.CreatedAt,
		UpdatedAt:        c.UpdatedAt,
	}
}

type verifyResultResponse struct {
	Credential dnsCredentialResponse `json:"credential"`
	OK         bool                  `json:"ok"`
	Message    string                `json:"message"`
	Warnings   []string              `json:"warnings"`
	Resumed    int                   `json:"resumed"`
}

func verifyResultFrom(v service.VerifyResult) verifyResultResponse {
	warns := v.Warnings
	if warns == nil {
		warns = []string{}
	}
	return verifyResultResponse{
		Credential: credentialFrom(v.Credential),
		OK:         v.OK,
		Message:    v.Message,
		Warnings:   warns,
		Resumed:    v.Resumed,
	}
}

type dnsCredentialListResponse struct {
	Items []dnsCredentialResponse `json:"items"`
}

type acmeSettingsResponse struct {
	ContactEmail      string `json:"contact_email"`
	UseStaging        bool   `json:"use_staging"`
	ZeroSSLEnabled    bool   `json:"zerossl_enabled"`
	ZeroSSLEABKID     string `json:"zerossl_eab_kid"`
	ZeroSSLEABHMACSet bool   `json:"zerossl_eab_hmac_set"`
}

func acmeFrom(s service.ACMESettings) acmeSettingsResponse {
	return acmeSettingsResponse{
		ContactEmail:      s.ContactEmail,
		UseStaging:        s.UseStaging,
		ZeroSSLEnabled:    s.ZeroSSLEnabled,
		ZeroSSLEABKID:     s.ZeroSSLEABKID,
		ZeroSSLEABHMACSet: s.ZeroSSLEABHMACSet,
	}
}
