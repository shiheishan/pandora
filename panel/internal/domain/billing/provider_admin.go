package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/payment"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 后台建 / 改支付渠道。aegis-payctl 也走这一份写入，两边口径不会分叉。
//
// 凭据密文的 AAD 绑定渠道行 id（"payment_provider:<id>"，loadProvider 用同一个解开），
// 所以新建必须「先落行拿 id、再加密写回」，两步在同一个事务里。
// 保存前用已登记的适配器试构造一次（epay.New 会查 https、公网地址、商户号与密钥），
// 与运行时是同一个构造函数，校验口径不会两样；私网地址只在 devMode 下放行。
//
// 不变量：
//   - adapter 与 code 建后不可改：回调地址按 code 路由，历史支付按 id 认渠道；
//   - 系统内置的 offline 渠道只读；
//   - 商户号与密钥只写不读：编辑时留空 = 不改；审计只记「凭据是否变更」，不记明文也不记密文。
//
// 改完只清本进程的渠道缓存。public 网关各自缓存 5 分钟，最迟 5 分钟后结账页生效。

// EpayMethods 是易支付可勾选的方式（与门户 paymentMethodLabels 的键一致）。
var EpayMethods = []string{"alipay", "wxpay", "qqpay"}

// adminProviderAdapters 是后台能新建与编辑的适配器。demo_hmac 只给集成测试与本地联调用，
// 它的密钥为空时回落到主密钥，不能让后台随手建出来。
var adminProviderAdapters = map[string]bool{"epay": true}

var (
	providerCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)
	providerPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{0,127}$`)
)

const (
	defaultEpaySubmitPath = "/submit.php"
	defaultEpayAPIPath    = "/api.php"
)

// ProviderSettings 是建与改共用的可写字段。
type ProviderSettings struct {
	DisplayName      string
	BaseURL          string
	SubmitPath       string
	APIPath          string
	Methods          []string
	DefaultMethod    string
	AllowPrivateHost bool
	// MerchantID 与 Key 只写不读。新建时必填；编辑时留空表示沿用库里的那一份。
	MerchantID string
	Key        string
}

type CreateProviderInput struct {
	Code    string
	Adapter string
	ProviderSettings
	Enabled      bool
	AcceptingNew bool
}

type UpdateProviderInput struct {
	Code string
	ProviderSettings
	// Enabled 非空时一并改启用状态。只有 payctl 用；后台的启停走 toggle。
	Enabled *bool
}

// ProviderActor 是写入人：后台是 admin + 管理员 id，payctl 是 system、没有 id。
type ProviderActor struct {
	Kind string
	ID   string
}

type ProviderWriteResult struct {
	ID                 string
	Code               string
	CredentialsChanged bool
}

// normalizedProvider 是校验后的结果：config 只含本函数认识的键。
type normalizedProvider struct {
	displayName string
	config      map[string]any
	merchantID  string
	key         string
}

// normalizeProviderSettings 规整并校验可写字段，错误按字段收齐一次返回。
// 凭据在这里只规整、不判必填：新建与编辑的必填口径不同，由调用方判。
func normalizeProviderSettings(in ProviderSettings, devMode bool) (normalizedProvider, map[string]string) {
	fields := map[string]string{}
	out := normalizedProvider{
		displayName: strings.TrimSpace(in.DisplayName),
		merchantID:  strings.TrimSpace(in.MerchantID),
		key:         strings.TrimSpace(in.Key),
	}
	if n := utf8.RuneCountInString(out.displayName); n < 1 || n > 40 {
		fields["display_name"] = "名称需为 1–40 个字"
	}

	baseURL := strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if u, err := url.Parse(baseURL); baseURL == "" || err != nil || u.Host == "" ||
		(u.Scheme != "https" && u.Scheme != "http") || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(baseURL, "?") {
		fields["base_url"] = "请填写完整的站点地址，如 https://pay.example.com"
	} else if u.Scheme != "https" && !in.AllowPrivateHost {
		fields["base_url"] = "站点地址必须使用 https"
	}
	if in.AllowPrivateHost && !devMode {
		fields["allow_private_host"] = "生产环境不允许内网或 http 地址"
	}

	submitPath := strings.TrimSpace(in.SubmitPath)
	if submitPath == "" {
		submitPath = defaultEpaySubmitPath
	} else if !providerPathPattern.MatchString(submitPath) || strings.HasPrefix(submitPath, "//") {
		fields["submit_path"] = "路径需以 / 开头，只含字母、数字与 . _ ~ / -"
	}
	apiPath := strings.TrimSpace(in.APIPath)
	if apiPath == "" {
		apiPath = defaultEpayAPIPath
	} else if !providerPathPattern.MatchString(apiPath) || strings.HasPrefix(apiPath, "//") {
		fields["api_path"] = "路径需以 / 开头，只含字母、数字与 . _ ~ / -"
	}

	methods := make([]string, 0, len(in.Methods))
	for _, m := range in.Methods {
		m = strings.TrimSpace(m)
		if !slices.Contains(EpayMethods, m) {
			fields["methods"] = "支付方式只能从支付宝、微信支付、QQ 钱包中选"
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

	if utf8.RuneCountInString(out.merchantID) > 64 {
		fields["merchant_id"] = "商户号最多 64 个字符"
	}
	if utf8.RuneCountInString(out.key) > 256 {
		fields["key"] = "密钥最多 256 个字符"
	}

	out.config = map[string]any{
		"base_url":           baseURL,
		"submit_path":        submitPath,
		"api_path":           apiPath,
		"methods":            methods,
		"default_method":     defaultMethod,
		"allow_private_host": in.AllowPrivateHost,
	}
	return out, fields
}

// tryBuildProvider 用已登记的适配器试构造一次，失败按地址字段报回。
func (s *PaymentService) tryBuildProvider(code, adapter string, cfg map[string]any, creds payment.Credentials) error {
	_, err := s.factory.Build(payment.ProviderRecord{
		Code: code, Adapter: adapter, Enabled: true, AcceptingNew: true,
		Config: cfg, Credentials: creds,
	})
	if err != nil {
		msg := strings.TrimPrefix(err.Error(), adapter+": ")
		return httpx.Invalid(map[string]string{"base_url": "渠道配置校验未通过：" + msg})
	}
	return nil
}

func (s *PaymentService) sealProviderCredentials(providerID string, creds payment.Credentials) ([]byte, error) {
	if s.env == nil {
		return nil, errors.New("未配置信封加密，无法保存渠道凭据")
	}
	plain, err := payment.EncodeCredentials(creds)
	if err != nil {
		return nil, err
	}
	// AAD 绑定渠道 ID：把密文搬到另一条渠道记录上会解不开（与 loadProvider 同一个）
	return s.env.Seal(plain, []byte("payment_provider:"+providerID))
}

// providerAuditDigest 是审计里的渠道快照：只有非机密字段，凭据只记是否变更。
func providerAuditDigest(displayName string, cfg map[string]any, extra map[string]any) map[string]any {
	d := map[string]any{"display_name": displayName}
	for _, k := range []string{"base_url", "submit_path", "api_path", "methods", "default_method", "allow_private_host"} {
		if v, ok := cfg[k]; ok {
			d[k] = v
		}
	}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

func providerActorEntry(actor ProviderActor) (string, *string, string) {
	if actor.Kind == "admin" && actor.ID != "" {
		id := actor.ID
		return "admin", &id, "admin"
	}
	return "system", nil, ""
}

// CreateProvider 新建一个渠道。新建的渠道按调用方给的启用状态落库；
// 后台建的是「已启用、暂停收新单」，管理员确认无误后再在卡片上打开收单。
func (s *PaymentService) CreateProvider(ctx context.Context, tenantID string, actor ProviderActor, in CreateProviderInput) (*ProviderWriteResult, error) {
	code := strings.TrimSpace(in.Code)
	adapter := strings.TrimSpace(in.Adapter)
	norm, fields := normalizeProviderSettings(in.ProviderSettings, s.devMode)
	if !providerCodePattern.MatchString(code) {
		fields["code"] = "编码需为 2–32 位小写字母、数字、- 或 _，以字母开头"
	} else if code == OfflineProviderCode {
		fields["code"] = "offline 是系统内置渠道的编码"
	}
	if !adminProviderAdapters[adapter] {
		fields["adapter"] = "只支持易支付（epay）"
	}
	if norm.merchantID == "" {
		fields["merchant_id"] = "必填"
	}
	if norm.key == "" {
		fields["key"] = "必填"
	}
	if len(fields) > 0 {
		return nil, httpx.Invalid(fields)
	}
	creds := payment.Credentials{MerchantID: norm.merchantID, Key: norm.key}
	if err := s.tryBuildProvider(code, adapter, norm.config, creds); err != nil {
		return nil, err
	}
	configJSON, err := json.Marshal(norm.config)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	out := &ProviderWriteResult{Code: code, CredentialsChanged: true}
	actorKind, actorID, apiDomain := providerActorEntry(actor)
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor.ID}, func(tx pgx.Tx) error {
		// 先落行拿到 ID —— 凭据密文的 AAD 要绑定它
		if err := tx.QueryRow(ctx, `
			INSERT INTO payment_providers
				(tenant_id, code, adapter, display_name, supported_currencies,
				 config, enabled, accepting_new)
			VALUES ($1, $2, $3, $4, ARRAY['CNY']::app.currency_code[], $5, $6, $7)
			RETURNING id`,
			tenantID, code, adapter, norm.displayName, configJSON, in.Enabled, in.AcceptingNew,
		).Scan(&out.ID); err != nil {
			if db.IsUniqueViolation(err) {
				return httpx.New(httpx.CodeConflict, "渠道编码已存在，请换一个")
			}
			return err
		}
		sealed, err := s.sealProviderCredentials(out.ID, creds)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE payment_providers SET credentials_encrypted = $1, key_version = 1
			  WHERE tenant_id = $2 AND id = $3`, sealed, tenantID, out.ID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: actorKind, ActorID: actorID,
			Action: "payment_provider.create", ResourceType: "payment_provider", ResourceID: &out.ID,
			APIDomain: apiDomain, Outcome: "success",
			AfterDigest: providerAuditDigest(norm.displayName, norm.config, map[string]any{
				"code": code, "adapter": adapter, "enabled": in.Enabled,
				"accepting_new": in.AcceptingNew, "credentials_changed": true,
			}),
			RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		return nil, providerWriteError(err)
	}
	s.factory.Invalidate(tenantID, code)
	return out, nil
}

// UpdateProvider 编辑已有渠道。code 与 adapter 不可改；商户号、密钥留空表示不改。
func (s *PaymentService) UpdateProvider(ctx context.Context, tenantID string, actor ProviderActor, in UpdateProviderInput) (*ProviderWriteResult, error) {
	code := strings.TrimSpace(in.Code)
	norm, fields := normalizeProviderSettings(in.ProviderSettings, s.devMode)
	if len(fields) > 0 {
		return nil, httpx.Invalid(fields)
	}

	out := &ProviderWriteResult{Code: code}
	actorKind, actorID, apiDomain := providerActorEntry(actor)
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor.ID}, func(tx pgx.Tx) error {
		var (
			adapter, beforeName string
			configRaw, sealed   []byte
			beforeEnabled       bool
		)
		err := tx.QueryRow(ctx, `
			SELECT id, adapter, display_name, coalesce(config, '{}'::jsonb),
			       credentials_encrypted, enabled
			  FROM payment_providers
			 WHERE tenant_id = $1 AND code = $2
			 FOR UPDATE`, tenantID, code,
		).Scan(&out.ID, &adapter, &beforeName, &configRaw, &sealed, &beforeEnabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if code == OfflineProviderCode || adapter == OfflineProviderCode {
			return httpx.New(httpx.CodeConflict, "系统内置渠道不可编辑")
		}
		if !adminProviderAdapters[adapter] {
			return httpx.New(httpx.CodeConflict, "这类渠道不支持在后台编辑")
		}

		before := map[string]any{}
		if len(configRaw) > 0 {
			if err := json.Unmarshal(configRaw, &before); err != nil {
				return fmt.Errorf("渠道 %q 配置不是合法 JSON: %w", code, err)
			}
		}
		var current payment.Credentials
		if len(sealed) > 0 {
			if s.env == nil {
				return errors.New("未配置信封加密，无法读取渠道凭据")
			}
			plain, err := s.env.Open(sealed, []byte("payment_provider:"+out.ID))
			if err != nil {
				return fmt.Errorf("渠道 %q 凭据解密失败: %w", code, err)
			}
			if current, err = payment.DecodeCredentials(plain); err != nil {
				return fmt.Errorf("渠道 %q: %w", code, err)
			}
		}
		next := current
		if norm.merchantID != "" {
			next.MerchantID = norm.merchantID
		}
		if norm.key != "" {
			next.Key = norm.key
		}
		missing := map[string]string{}
		if next.MerchantID == "" {
			missing["merchant_id"] = "该渠道还没有商户号，需填写"
		}
		if next.Key == "" {
			missing["key"] = "该渠道还没有密钥，需填写"
		}
		if len(missing) > 0 {
			return httpx.Invalid(missing)
		}
		out.CredentialsChanged = next.MerchantID != current.MerchantID || next.Key != current.Key

		// 只覆盖本函数认识的键，库里别的键原样保留
		merged := make(map[string]any, len(before)+len(norm.config))
		for k, v := range before {
			merged[k] = v
		}
		for k, v := range norm.config {
			merged[k] = v
		}
		if err := s.tryBuildProvider(code, adapter, merged, next); err != nil {
			return err
		}
		configJSON, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		afterEnabled := beforeEnabled
		if in.Enabled != nil {
			afterEnabled = *in.Enabled
		}
		if _, err := tx.Exec(ctx,
			`UPDATE payment_providers SET display_name = $3, config = $4, enabled = $5
			  WHERE tenant_id = $1 AND id = $2`,
			tenantID, out.ID, norm.displayName, configJSON, afterEnabled); err != nil {
			return err
		}
		if out.CredentialsChanged {
			sealedNext, err := s.sealProviderCredentials(out.ID, next)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE payment_providers SET credentials_encrypted = $1, key_version = 1
				  WHERE tenant_id = $2 AND id = $3`, sealedNext, tenantID, out.ID); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: actorKind, ActorID: actorID,
			Action: "payment_provider.update", ResourceType: "payment_provider", ResourceID: &out.ID,
			APIDomain: apiDomain, Outcome: "success",
			BeforeDigest: providerAuditDigest(beforeName, before, map[string]any{"enabled": beforeEnabled}),
			AfterDigest: providerAuditDigest(norm.displayName, merged, map[string]any{
				"enabled": afterEnabled, "credentials_changed": out.CredentialsChanged,
			}),
			RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		return nil, providerWriteError(err)
	}
	s.factory.Invalidate(tenantID, code)
	return out, nil
}

func providerWriteError(err error) error {
	if httpErr := new(httpx.Error); errors.As(err, &httpErr) {
		return err
	}
	return httpx.Internal(err)
}

//------------------------------------------------------------------------------
// 渠道内的支付方式
//------------------------------------------------------------------------------

// providerMethods 是渠道允许的支付方式，口径与 PaymentMethods 的 SQL 一致：
// config.methods 是非空数组时用它，否则用 default_method，两者都没有时为空（交给渠道决定）。
func providerMethods(cfg map[string]any) []string {
	var out []string
	switch v := cfg["methods"].(type) {
	case []any:
		for _, m := range v {
			if s, ok := m.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = append(out, v...)
	}
	if len(out) > 0 {
		return out
	}
	if d := payment.ConfigString(cfg, "default_method"); d != "" {
		return []string{d}
	}
	return nil
}

// resolvePaymentMethod 把用户选的方式规整成实际下单的方式。
// 没选时取渠道默认（default_method 在方式里就用它，否则第一个）；选了不在方式里的拒绝。
// 渠道没配任何方式时只接受空串，由渠道自己决定。
func resolvePaymentMethod(cfg map[string]any, requested string) (string, error) {
	allowed := providerMethods(cfg)
	requested = strings.TrimSpace(requested)
	if requested == "" {
		if len(allowed) == 0 {
			return "", nil
		}
		if d := payment.ConfigString(cfg, "default_method"); slices.Contains(allowed, d) {
			return d, nil
		}
		return allowed[0], nil
	}
	if !slices.Contains(allowed, requested) {
		return "", httpx.Invalid(map[string]string{"method": "该渠道不支持所选支付方式"})
	}
	return requested, nil
}
