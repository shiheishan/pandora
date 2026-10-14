package service

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/payment/model"
	paymentrepo "github.com/aegispanel/aegis/internal/module/payment/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	defaultMinAmount = decimal.RequireFromString("1.00")
	maxMinAmount     = decimal.RequireFromString("1000.00")
)

const (
	defaultSubmitPath = "/submit.php"
	defaultAPIPath    = "/api.php"
)

var (
	providerCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)
	providerPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{0,127}$`)
)

// normalized 是校验后的渠道可写字段。
type normalized struct {
	displayName, baseURL, submitPath, apiPath string
	methods                                   []string
	defaultMethod                             string
	allowPrivateHost                          bool
	minAmount                                 decimal.Decimal
	minAmountSet                              bool
	merchantID, key                           string
}

func containsNUL(s string) bool {
	return strings.ContainsRune(s, 0)
}

func normalizeSettings(in ProviderSettings, allowPrivate bool) (normalized, map[string]string) {
	fields := map[string]string{}
	out := normalized{
		displayName: strings.TrimSpace(in.DisplayName),
		merchantID:  strings.TrimSpace(in.MerchantID),
		key:         strings.TrimSpace(in.Key),
	}
	if containsNUL(out.displayName) {
		fields["display_name"] = "不能包含控制字符"
	} else if n := utf8.RuneCountInString(out.displayName); n < 1 || n > 40 {
		fields["display_name"] = "名称需为 1–40 个字"
	}

	baseURL := strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if containsNUL(in.BaseURL) {
		fields["base_url"] = "不能包含控制字符"
	} else if u, err := url.Parse(baseURL); baseURL == "" || err != nil || u.Host == "" ||
		(u.Scheme != "https" && u.Scheme != "http") || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(baseURL, "?") {
		fields["base_url"] = "请填写完整的站点地址，如 https://pay.example.com"
	} else if u.Scheme != "https" && !in.AllowPrivateHost {
		fields["base_url"] = "站点地址必须使用 https"
	}
	if in.AllowPrivateHost && !allowPrivate {
		fields["allow_private_host"] = "生产环境不允许内网或 http 地址"
	}

	submitPath := strings.TrimSpace(in.SubmitPath)
	if submitPath == "" {
		submitPath = defaultSubmitPath
	} else if containsNUL(in.SubmitPath) {
		fields["submit_path"] = "不能包含控制字符"
	} else if !providerPathPattern.MatchString(submitPath) || strings.HasPrefix(submitPath, "//") {
		fields["submit_path"] = "路径需以 / 开头，只含字母、数字与 . _ ~ / -"
	}
	apiPath := strings.TrimSpace(in.APIPath)
	if apiPath == "" {
		apiPath = defaultAPIPath
	} else if containsNUL(in.APIPath) {
		fields["api_path"] = "不能包含控制字符"
	} else if !providerPathPattern.MatchString(apiPath) || strings.HasPrefix(apiPath, "//") {
		fields["api_path"] = "路径需以 / 开头，只含字母、数字与 . _ ~ / -"
	}

	methods := make([]string, 0, len(in.Methods))
	for _, m := range in.Methods {
		m = strings.TrimSpace(m)
		if !slices.Contains(EpayMethods, m) {
			fields["methods"] = "支付方式只能从支付宝、微信支付中选"
			continue
		}
		if !slices.Contains(methods, m) {
			methods = append(methods, m)
		}
	}
	if len(methods) == 0 && fields["methods"] == "" {
		fields["methods"] = "至少选一种支付方式"
	}
	defaultMethod := strings.TrimSpace(in.DefaultMethod)
	if defaultMethod == "" && len(methods) > 0 {
		defaultMethod = methods[0]
	}
	if fields["methods"] == "" && !slices.Contains(methods, defaultMethod) {
		fields["default_method"] = "默认方式必须是已勾选的方式之一"
	}

	if containsNUL(out.merchantID) {
		fields["merchant_id"] = "不能包含控制字符"
	} else if utf8.RuneCountInString(out.merchantID) > 64 {
		fields["merchant_id"] = "商户号最多 64 个字符"
	}
	if containsNUL(out.key) {
		fields["key"] = "不能包含控制字符"
	} else if utf8.RuneCountInString(out.key) > 256 {
		fields["key"] = "密钥最多 256 个字符"
	}

	if in.MinAmount.IsZero() {
		out.minAmountSet = false
		out.minAmount = defaultMinAmount
	} else {
		out.minAmountSet = true
		out.minAmount = in.MinAmount
		if money.Check(out.minAmount) != nil || out.minAmount.LessThan(decimal.RequireFromString("0.01")) || out.minAmount.GreaterThan(maxMinAmount) {
			fields["min_amount"] = "最低付款额需在 ¥0.01 到 ¥1000.00 之间"
		}
	}

	out.baseURL = baseURL
	out.submitPath = submitPath
	out.apiPath = apiPath
	out.methods = methods
	out.defaultMethod = defaultMethod
	out.allowPrivateHost = in.AllowPrivateHost
	return out, fields
}

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
		"enabled":            p.Enabled,
		"accepting_new":      p.AcceptingNew,
	}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

func recordFromNormalized(code, adapter string, norm normalized, creds credentials, enabled, acceptingNew bool, minAmt decimal.Decimal) providerRecord {
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
		Creds:            creds,
	}
}

func trialBuildAdapter(s *Service, p providerRecord) error {
	_, err := s.adapter(p)
	if err == nil {
		return nil
	}
	msg := strings.TrimPrefix(err.Error(), p.Adapter+": ")
	return errs.Invalid(map[string]string{"base_url": "渠道配置校验未通过：" + msg})
}

// ListProviders 列出全部渠道及统计。
func (s *Service) ListProviders(ctx context.Context) ([]ProviderView, error) {
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
	todayByProvider := map[uuid.UUID][]paymentrepo.ProviderAmount{}
	for _, a := range today {
		todayByProvider[a.ProviderID] = append(todayByProvider[a.ProviderID], a)
	}
	out := make([]ProviderView, 0, len(rows))
	for _, m := range rows {
		v, err := buildProviderView(m, todayByProvider[m.ID], rates, lastEvents)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func buildProviderView(
	m model.Provider,
	today []paymentrepo.ProviderAmount,
	rates map[uuid.UUID]float64,
	lastEvents map[uuid.UUID]time.Time,
) (ProviderView, error) {
	methods := m.Methods
	if methods == nil {
		methods = []string{}
	}
	currencies := m.Currencies
	if currencies == nil {
		currencies = []string{}
	}
	todayMap := make(map[money.Currency]decimal.Decimal)
	for _, a := range today {
		c, err := money.ParseCurrency(a.Currency)
		if err != nil {
			return ProviderView{}, err
		}
		todayMap[c] = a.Amount
	}
	var rate *float64
	if r, ok := rates[m.ID]; ok {
		rate = &r
	}
	var last *time.Time
	if t, ok := lastEvents[m.ID]; ok {
		last = &t
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
		Currencies:       currencies,
		SubmitPath:       m.SubmitPath,
		APIPath:          m.APIPath,
		Methods:          methods,
		DefaultMethod:    m.DefaultMethod,
		AllowPrivateHost: m.AllowPrivateHost,
		MinAmount:        m.MinAmount,
		Today:            todayMap,
		SuccessRate24h:   rate,
		LastCallbackAt:   last,
	}, nil
}

// CreateProvider 新建易支付渠道。
func (s *Service) CreateProvider(ctx context.Context, in CreateProviderInput) (ProviderWriteResult, error) {
	code := strings.TrimSpace(in.Code)
	adapter := strings.TrimSpace(in.Adapter)
	norm, fields := normalizeSettings(in.ProviderSettings, s.cfg.AllowPrivateHosts)
	if !providerCodePattern.MatchString(code) {
		fields["code"] = "编码需为 2–32 位小写字母、数字、- 或 _，以字母开头"
	} else if code == OfflineProviderCode {
		fields["code"] = "offline 是系统内置渠道的编码"
	}
	if adapter != AdapterEpay {
		fields["adapter"] = "只支持易支付（epay）"
	}
	if norm.merchantID == "" {
		fields["merchant_id"] = "必填"
	}
	if norm.key == "" {
		fields["key"] = "必填"
	}
	if len(fields) > 0 {
		return ProviderWriteResult{}, errs.Invalid(fields)
	}
	creds := credentials{MerchantID: norm.merchantID, Key: norm.key}
	if err := trialBuildAdapter(s, recordFromNormalized(code, adapter, norm, creds, true, false, norm.minAmount)); err != nil {
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
			Currencies:        []string{"CNY"},
			BaseURL:           norm.baseURL,
			SubmitPath:        norm.submitPath,
			APIPath:           norm.apiPath,
			Methods:           norm.methods,
			DefaultMethod:     norm.defaultMethod,
			AllowPrivateHost:  norm.allowPrivateHost,
			MinAmount:         norm.minAmount,
			Enabled:           true,
			AcceptingNew:      false,
		}
		if err := s.repo.InsertProvider(ctx, p); err != nil {
			if errors.Is(err, paymentrepo.ErrDuplicate) {
				return errs.Newf(errs.Conflict, "渠道编码已存在，请换一个")
			}
			return err
		}
		rec := recordFromNormalized(code, adapter, norm, creds, true, false, norm.minAmount)
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

// UpdateProvider 编辑已有易支付渠道。
func (s *Service) UpdateProvider(ctx context.Context, code string, in ProviderSettings) (ProviderWriteResult, error) {
	code = strings.TrimSpace(code)
	norm, fields := normalizeSettings(in, s.cfg.AllowPrivateHosts)
	if len(fields) > 0 {
		return ProviderWriteResult{}, errs.Invalid(fields)
	}
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
		if m.Adapter != AdapterEpay {
			return errs.Newf(errs.Conflict, "这类渠道不支持在后台编辑")
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
		if len(missing) > 0 {
			return errs.Invalid(missing)
		}
		minAmt := before.MinAmount
		if norm.minAmountSet {
			minAmt = norm.minAmount
		}
		after := recordFromNormalized(m.Code, m.Adapter, norm, creds, m.Enabled, m.AcceptingNew, minAmt)
		after.ID = m.ID
		if err := trialBuildAdapter(s, after); err != nil {
			return err
		}
		credsChanged := creds.MerchantID != before.Creds.MerchantID || creds.Key != before.Creds.Key
		updates := map[string]any{
			"display_name":       norm.displayName,
			"base_url":           norm.baseURL,
			"submit_path":        norm.submitPath,
			"api_path":           norm.apiPath,
			"methods":            norm.methods,
			"default_method":     norm.defaultMethod,
			"allow_private_host": norm.allowPrivateHost,
			"min_amount":         minAmt,
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
