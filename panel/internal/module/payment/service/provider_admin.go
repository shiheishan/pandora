package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/payment/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func providerAuditSnapshot(p providerRecord, extra map[string]any) map[string]any {
	d := map[string]any{
		"display_name":       p.DisplayName,
		"base_url":           p.BaseURL,
		"submit_path":        p.SubmitPath,
		"api_path":           p.APIPath,
		"methods":            p.Methods,
		"default_method":     p.DefaultMethod,
		"allow_private_host": p.AllowPrivateHost,
		"min_amount":         p.MinAmount.StringFixed(2),
		"fee_fixed":          p.FeeFixed.StringFixed(2),
		"fee_percent":        p.FeePercent.StringFixed(2),
		"enabled":            p.Enabled,
		"accepting_new":      p.AcceptingNew,
	}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

func recordFromNormalized(code, adapter string, norm normalized, creds credentials, enabled, acceptingNew bool, minAmt, feeFixed, feePercent decimal.Decimal) providerRecord {
	return providerRecord{
		Code:             code,
		Adapter:          adapter,
		DisplayName:      norm.displayName,
		Enabled:          enabled,
		AcceptingNew:     acceptingNew,
		BaseURL:          norm.baseURL,
		SubmitPath:       norm.submitPath,
		APIPath:          norm.apiPath,
		Methods:          norm.methods,
		DefaultMethod:    norm.defaultMethod,
		AllowPrivateHost: norm.allowPrivateHost,
		MinAmount:        minAmt,
		FeeFixed:         feeFixed,
		FeePercent:       feePercent,
		Creds:            creds,
	}
}

func trialBuildAdapter(s *Service, p providerRecord) error {
	_, err := s.adapter(p)
	if err == nil {
		return nil
	}
	msg := strings.TrimPrefix(err.Error(), p.Adapter+": ")
	field := "base_url"
	if strings.Contains(msg, "Store ID") || strings.Contains(msg, "Client ID") {
		field = "merchant_id"
	}
	return errs.Invalid(map[string]string{field: "渠道配置校验未通过：" + msg})
}

// ListProviders 列出全部渠道及统计。
func (s *Service) ListProviders(ctx context.Context) ([]ProviderView, error) {
	base, err := s.notifyBase(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	site, err := s.settings.Site(ctx)
	if err != nil {
		return nil, err
	}
	today, err := s.repo.ProviderTodayTotals(ctx, site.Timezone)
	if err != nil {
		return nil, err
	}
	rates, err := s.repo.ProviderSuccessRates(ctx, s.now().Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	lastEvents, err := s.repo.ProviderLastEvents(ctx)
	if err != nil {
		return nil, err
	}
	todayByProvider := map[uuid.UUID]decimal.Decimal{}
	for _, a := range today {
		todayByProvider[a.ProviderID] = todayByProvider[a.ProviderID].Add(a.Amount)
	}
	out := make([]ProviderView, 0, len(rows))
	for _, m := range rows {
		v, err := s.buildProviderView(m, base, todayByProvider[m.ID], rates, lastEvents)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) buildProviderView(
	m model.Provider,
	notifyBase string,
	today decimal.Decimal,
	rates map[uuid.UUID]float64,
	lastEvents map[uuid.UUID]time.Time,
) (ProviderView, error) {
	methods := m.Methods
	if methods == nil {
		methods = []string{}
	}
	var rate *float64
	if r, ok := rates[m.ID]; ok {
		rate = &r
	}
	var last *time.Time
	if t, ok := lastEvents[m.ID]; ok {
		last = &t
	}
	var callbackURL string
	if m.Adapter != AdapterOffline {
		callbackURL = notifyURL(notifyBase, m.Code)
	}
	var hasWebhookSecret bool
	if m.Adapter == AdapterBTCPay {
		creds, err := s.openCredentials(m.ID, m.CredentialsSealed)
		if err == nil {
			hasWebhookSecret = creds.WebhookSecret != ""
		}
	}
	return ProviderView{
		ID:               m.ID,
		Code:             m.Code,
		Adapter:          m.Adapter,
		DisplayName:      m.DisplayName,
		Enabled:          m.Enabled,
		AcceptingNew:     m.AcceptingNew,
		HasCredentials:   len(m.CredentialsSealed) > 0,
		BaseURL:          m.BaseURL,
		SubmitPath:       m.SubmitPath,
		APIPath:          m.APIPath,
		Methods:          methods,
		DefaultMethod:    m.DefaultMethod,
		AllowPrivateHost: m.AllowPrivateHost,
		MinAmount:        m.MinAmount,
		FeeFixed:         m.FeeFixed,
		FeePercent:       m.FeePercent,
		HasWebhookSecret: hasWebhookSecret,
		NotifyURL:        callbackURL,
		Today:            today,
		SuccessRate24h:   rate,
		LastCallbackAt:   last,
	}, nil
}

// CreateProvider 新建在线渠道（易支付、BTCPay、CoinPayments）。
func (s *Service) CreateProvider(ctx context.Context, in CreateProviderInput) (ProviderWriteResult, error) {
	code := strings.TrimSpace(in.Code)
	adapter := strings.TrimSpace(in.Adapter)
	norm, fields := normalizeSettings(in.ProviderSettings, adapter, s.cfg.AllowPrivateHosts)
	if !providerCodePattern.MatchString(code) {
		fields["code"] = "编码需为 2–32 位小写字母、数字、- 或 _，以字母开头"
	} else if code == OfflineProviderCode {
		fields["code"] = "offline 是系统内置渠道的编码"
	}
	if !slices.Contains(OnlineAdapters, adapter) {
		fields["adapter"] = "只支持易支付（epay）、BTCPay（btcpay）、CoinPayments（coinpayments）"
	}
	if norm.merchantID == "" {
		fields["merchant_id"] = "必填"
	}
	if norm.key == "" {
		fields["key"] = "必填"
	}
	if adapter == AdapterBTCPay && norm.webhookSecret == "" {
		fields["webhook_secret"] = "必填"
	}
	if len(fields) > 0 {
		return ProviderWriteResult{}, errs.Invalid(fields)
	}
	creds := credentials{MerchantID: norm.merchantID, Key: norm.key, WebhookSecret: norm.webhookSecret}
	if err := trialBuildAdapter(s, recordFromNormalized(code, adapter, norm, creds, true, false, norm.minAmount, norm.feeFixed, norm.feePercent)); err != nil {
		return ProviderWriteResult{}, err
	}
	var out ProviderWriteResult
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		id := uuid.Must(uuid.NewV7())
		sealed, err := s.sealCredentials(id, creds)
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		p := &model.Provider{
			ID:                id,
			Code:              code,
			Adapter:           adapter,
			DisplayName:       norm.displayName,
			CredentialsSealed: sealed,
			BaseURL:           norm.baseURL,
			SubmitPath:        norm.submitPath,
			APIPath:           norm.apiPath,
			Methods:           norm.methods,
			DefaultMethod:     norm.defaultMethod,
			AllowPrivateHost:  norm.allowPrivateHost,
			MinAmount:         norm.minAmount,
			FeeFixed:          norm.feeFixed,
			FeePercent:        norm.feePercent,
			Enabled:           true,
			AcceptingNew:      false,
		}
		if err := s.repo.InsertProvider(ctx, p); err != nil {
			if errors.Is(err, paymentrepo.ErrDuplicate) {
				return errs.Newf(errs.Conflict, "渠道编码已存在，请换一个")
			}
			return err
		}
		rec := recordFromNormalized(code, adapter, norm, creds, true, false, norm.minAmount, norm.feeFixed, norm.feePercent)
		rec.ID = id
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "payment_provider.create",
			ResourceType: "payment_provider",
			ResourceID:   &id,
			Data: providerAuditSnapshot(rec, map[string]any{
				"code": code, "adapter": adapter, "credentials_changed": true,
			}),
		}); err != nil {
			return err
		}
		out = ProviderWriteResult{ID: id, Code: code, CredentialsChanged: true}
		return nil
	})
	if err != nil {
		return ProviderWriteResult{}, err
	}
	return out, nil
}

// UpdateProvider 编辑已有在线渠道。
func (s *Service) UpdateProvider(ctx context.Context, code string, in ProviderSettings) (ProviderWriteResult, error) {
	code = strings.TrimSpace(code)
	var out ProviderWriteResult
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		m, ok, err := s.repo.LockProviderByCode(ctx, code)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		if m.Code == OfflineProviderCode || m.Adapter == AdapterOffline {
			return errs.Newf(errs.Conflict, "系统内置渠道不可编辑")
		}
		if !slices.Contains(OnlineAdapters, m.Adapter) {
			return errs.Newf(errs.Conflict, "这类渠道不支持在后台编辑")
		}
		norm, fields := normalizeSettings(in, m.Adapter, s.cfg.AllowPrivateHosts)
		if len(fields) > 0 {
			return errs.Invalid(fields)
		}
		before, err := s.record(m)
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		creds := before.Creds
		if norm.merchantID != "" {
			creds.MerchantID = norm.merchantID
		}
		if norm.key != "" {
			creds.Key = norm.key
		}
		if norm.webhookSecret != "" {
			creds.WebhookSecret = norm.webhookSecret
		}
		missing := map[string]string{}
		// 改了站点地址必须重填密钥（同 settings 改 SMTP 服务器须重填口令）：查单时密钥以明文出现在发往该地址的 URL 里，
		// 不能让只改地址的人把现有密钥带去别的站点
		if norm.baseURL != before.BaseURL && norm.key == "" {
			missing["key"] = "改了站点地址，需要重新填写密钥"
		}
		if creds.MerchantID == "" {
			missing["merchant_id"] = "该渠道还没有商户号，需填写"
		}
		if creds.Key == "" {
			missing["key"] = "该渠道还没有密钥，需填写"
		}
		if m.Adapter == AdapterBTCPay && creds.WebhookSecret == "" {
			missing["webhook_secret"] = "该渠道还没有 Webhook 密钥，需填写"
		}
		if len(missing) > 0 {
			return errs.Invalid(missing)
		}
		minAmt := before.MinAmount
		if norm.minAmountSet {
			minAmt = norm.minAmount
		}
		feeFixed := before.FeeFixed
		if norm.feeFixedSet {
			feeFixed = norm.feeFixed
		}
		feePercent := before.FeePercent
		if norm.feePercentSet {
			feePercent = norm.feePercent
		}
		after := recordFromNormalized(m.Code, m.Adapter, norm, creds, m.Enabled, m.AcceptingNew, minAmt, feeFixed, feePercent)
		after.ID = m.ID
		if err := trialBuildAdapter(s, after); err != nil {
			return err
		}
		credsChanged := creds.MerchantID != before.Creds.MerchantID || creds.Key != before.Creds.Key || creds.WebhookSecret != before.Creds.WebhookSecret
		updates := map[string]any{
			"display_name":       norm.displayName,
			"base_url":           norm.baseURL,
			"submit_path":        norm.submitPath,
			"api_path":           norm.apiPath,
			"methods":            norm.methods,
			"default_method":     norm.defaultMethod,
			"allow_private_host": norm.allowPrivateHost,
			"min_amount":         minAmt,
			"fee_fixed":          feeFixed,
			"fee_percent":        feePercent,
		}
		if credsChanged {
			sealed, err := s.sealCredentials(m.ID, creds)
			if err != nil {
				return errs.Wrap(errs.Internal, err)
			}
			updates["credentials_sealed"] = sealed
		}
		if _, err := s.repo.UpdateProviderFields(ctx, m.ID, updates); err != nil {
			return err
		}
		afterRec := after
		afterRec.Enabled = m.Enabled
		afterRec.AcceptingNew = m.AcceptingNew
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "payment_provider.update",
			ResourceType: "payment_provider",
			ResourceID:   &m.ID,
			Data: map[string]any{
				"before":              providerAuditSnapshot(before, nil),
				"after":               providerAuditSnapshot(afterRec, map[string]any{"credentials_changed": credsChanged}),
				"credentials_changed": credsChanged,
			},
		}); err != nil {
			return err
		}
		out = ProviderWriteResult{ID: m.ID, Code: m.Code, CredentialsChanged: credsChanged}
		return nil
	})
	if err != nil {
		return ProviderWriteResult{}, err
	}
	return out, nil
}

// ToggleProvider 启停渠道与是否收新单。
func (s *Service) ToggleProvider(ctx context.Context, code string, enabled, acceptingNew bool) error {
	code = strings.TrimSpace(code)
	return s.tx.Do(ctx, func(ctx context.Context) error {
		m, ok, err := s.repo.LockProviderByCode(ctx, code)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		before := map[string]any{"enabled": m.Enabled, "accepting_new": m.AcceptingNew}
		if _, err := s.repo.UpdateProviderFields(ctx, m.ID, map[string]any{
			"enabled":       enabled,
			"accepting_new": acceptingNew,
		}); err != nil {
			return err
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       "payment_provider.toggle",
			ResourceType: "payment_provider",
			ResourceID:   &m.ID,
			Data: map[string]any{
				"before": before,
				"after":  map[string]any{"enabled": enabled, "accepting_new": acceptingNew},
			},
		})
	})
}
