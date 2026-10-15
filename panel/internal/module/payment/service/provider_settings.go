package service

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	coinpayments "github.com/aegispanel/aegis/internal/module/payment/provider/coinpayments"
	"github.com/aegispanel/aegis/internal/pkg/money"
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
	feeFixed, feePercent                      decimal.Decimal
	feeFixedSet, feePercentSet                bool
	merchantID, key, webhookSecret            string
}

func containsNUL(s string) bool {
	return strings.ContainsRune(s, 0)
}

func normalizeSettings(in ProviderSettings, adapter string, allowPrivate bool) (normalized, map[string]string) {
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
	if adapter == AdapterCoinPayments && baseURL == "" {
		baseURL = coinpayments.DefaultBaseURL
	}
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

	submitPath := ""
	apiPath := ""
	methods := []string{}
	defaultMethod := ""
	if adapter == AdapterEpay {
		submitPath = strings.TrimSpace(in.SubmitPath)
		if submitPath == "" {
			submitPath = defaultSubmitPath
		} else if containsNUL(in.SubmitPath) {
			fields["submit_path"] = "不能包含控制字符"
		} else if !providerPathPattern.MatchString(submitPath) || strings.HasPrefix(submitPath, "//") {
			fields["submit_path"] = "路径需以 / 开头，只含字母、数字与 . _ ~ / -"
		}
		apiPath = strings.TrimSpace(in.APIPath)
		if apiPath == "" {
			apiPath = defaultAPIPath
		} else if containsNUL(in.APIPath) {
			fields["api_path"] = "不能包含控制字符"
		} else if !providerPathPattern.MatchString(apiPath) || strings.HasPrefix(apiPath, "//") {
			fields["api_path"] = "路径需以 / 开头，只含字母、数字与 . _ ~ / -"
		}

		methods = make([]string, 0, len(in.Methods))
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
		defaultMethod = strings.TrimSpace(in.DefaultMethod)
		if defaultMethod == "" && len(methods) > 0 {
			defaultMethod = methods[0]
		}
		if fields["methods"] == "" && !slices.Contains(methods, defaultMethod) {
			fields["default_method"] = "默认方式必须是已勾选的方式之一"
		}
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

	out.webhookSecret = strings.TrimSpace(in.WebhookSecret)
	if containsNUL(out.webhookSecret) {
		fields["webhook_secret"] = "不能包含控制字符"
	} else if utf8.RuneCountInString(out.webhookSecret) > 256 {
		fields["webhook_secret"] = "Webhook 密钥最多 256 个字符"
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

	if in.FeeFixed != nil {
		out.feeFixedSet = true
		out.feeFixed = *in.FeeFixed
		if !ValidFeeFixed(out.feeFixed) {
			fields["fee_fixed"] = "固定手续费需在 ¥0 到 ¥1000.00 之间，最多两位小数"
		}
	}
	if in.FeePercent != nil {
		out.feePercentSet = true
		out.feePercent = *in.FeePercent
		if !ValidFeePercent(out.feePercent) {
			fields["fee_percent"] = "比例手续费需在 0 到 100 之间，最多两位小数"
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
