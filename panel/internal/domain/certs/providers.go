package certs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/challenge"
)

// DNS 提供方（用户 10-08 定首发三家）。每家的凭据字段都必填、都只写不读；PATCH 里缺席的字段
// 保留库里的值。以后加提供方：这里登记字段，实现 dnsProvider，迁移放宽 provider 的 CHECK。
const (
	ProviderCloudflare   = "cloudflare"
	ProviderAliDNS       = "alidns"
	ProviderTencentCloud = "tencentcloud"
)

// providerFields 是各提供方的凭据字段（JSON 键）；第一个字段的末四位作为界面上的提示。
var providerFields = map[string][]string{
	ProviderCloudflare:   {"api_token"},
	ProviderAliDNS:       {"access_key_id", "access_key_secret"},
	ProviderTencentCloud: {"secret_id", "secret_key"},
}

// checkRecordLabel 是「能写 TXT」校验用的记录名（zone 下一级），建好立刻删掉。
const checkRecordLabel = "_pandora-check"

// dnsProvider 是一家 DNS 提供方的两件事：预检（列 zone，按需再建删一条 TXT）与 lego 的 DNS-01 提供方。
type dnsProvider interface {
	check(ctx context.Context, zone string, write bool) (checkResult, error)
	lego(propagation, polling time.Duration) (challenge.Provider, error)
}

// checkResult 是预检看到的情况；Warnings 是给界面的提示（不挡签发）。
type checkResult struct {
	VisibleZones int
	Warnings     []string
}

// credentialError 是提供方明确拒绝了凭据（鉴权失败、看不到 zone、没有写权限）。用到它的证书停在
// blocked_credential，不再去碰 CA；凭据更新并校验通过后自动恢复。网络错误、5xx 不是它，按普通失败退避。
type credentialError struct {
	msg   string
	cause error
}

func (e *credentialError) Error() string { return e.msg }
func (e *credentialError) Unwrap() error { return e.cause }

func credentialRejected(cause error, format string, args ...any) error {
	return &credentialError{msg: fmt.Sprintf(format, args...), cause: cause}
}

// newDNSProvider 按提供方装配；secret 已经过 validateSecret。
func (s *Service) newDNSProvider(provider string, secret map[string]string) (dnsProvider, error) {
	switch provider {
	case ProviderCloudflare:
		base := s.opts.CloudflareBaseURL
		if base == "" {
			base = cloudflareAPI
		}
		client := s.opts.ProviderHTTPClient
		if client == nil {
			client = &http.Client{Timeout: 30 * time.Second}
		}
		return &cloudflareProvider{token: secret["api_token"], base: strings.TrimRight(base, "/"),
			custom: s.opts.CloudflareBaseURL != "", client: client}, nil
	case ProviderAliDNS:
		return &aliDNSProvider{keyID: secret["access_key_id"], keySecret: secret["access_key_secret"]}, nil
	case ProviderTencentCloud:
		return &tencentProvider{secretID: secret["secret_id"], secretKey: secret["secret_key"]}, nil
	}
	return nil, fmt.Errorf("unknown dns provider %q", provider)
}

// mergeSecret 把这次提交的字段并进原有的凭据：缺席的字段保留原值，给了空串报错（字段都必填），
// 不认识的字段报错。返回的错误按字段名给表单。
func mergeSecret(provider string, old, in map[string]string) (map[string]string, map[string]string) {
	fields, ok := providerFields[provider]
	if !ok {
		return nil, map[string]string{"provider": "不支持的 DNS 提供方"}
	}
	out := map[string]string{}
	bad := map[string]string{}
	for k := range in {
		if !containsString(fields, k) {
			bad["secret."+k] = "这个提供方没有这个字段"
		}
	}
	for _, f := range fields {
		v, given := in[f]
		v = strings.TrimSpace(v)
		switch {
		case given && v == "":
			bad["secret."+f] = "不能为空"
		case given && len(v) > 512:
			bad["secret."+f] = "太长"
		case given:
			out[f] = v
		case old[f] != "":
			out[f] = old[f]
		default:
			bad["secret."+f] = "必填"
		}
	}
	if len(bad) > 0 {
		return nil, bad
	}
	return out, nil
}

// secretHint 是界面上显示的末四位（取每家的第一个字段：令牌或密钥 ID）。
func secretHint(provider string, secret map[string]string) string {
	v := secret[providerFields[provider][0]]
	if len(v) <= 4 {
		return ""
	}
	return v[len(v)-4:]
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func checkRecordValue() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "pandora-check-" + hex.EncodeToString(b)
}

// zoneMatches 比对提供方返回的 zone 名（可能带末尾点、大小写不一）。
func zoneMatches(got, zone string) bool {
	return strings.EqualFold(strings.TrimSuffix(got, "."), zone)
}

// providerErrorText 把提供方错误压成一行、截断，写进凭据的 verify_error 与证书的 last_error。
func providerErrorText(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.Join(strings.Fields(err.Error()), " ")
	return truncate(msg, 400)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

var errZoneNotFound = errors.New("zone not visible")
