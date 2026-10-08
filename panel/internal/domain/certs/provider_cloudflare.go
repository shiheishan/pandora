package certs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
)

const cloudflareAPI = "https://api.cloudflare.com/client/v4"

// cloudflareProvider 用一个 API 令牌（权限 Zone·Zone·Read + Zone·DNS·Edit）。
//
// 预检只调列 zone 的接口：既验证令牌（401/403 即拒绝），又确认看得到这个 zone，还能数出令牌
// 一共看得到几个 zone（多于 1 个提示不是最小权限）。用户令牌与账户令牌都能调它，所以不调
// /user/tokens/verify（账户令牌在那里会报无效）。
type cloudflareProvider struct {
	token  string
	base   string
	custom bool // base 是测试注入的地址
	client *http.Client
}

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		TotalCount int `json:"total_count"`
	} `json:"result_info"`
}

type cfZone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

// cfStatusError 是 Cloudflare 回的非 2xx。
type cfStatusError struct {
	status int
	detail string
}

func (e *cfStatusError) Error() string {
	return fmt.Sprintf("Cloudflare API %d: %s", e.status, e.detail)
}

func (p *cloudflareProvider) do(ctx context.Context, method, path string, query url.Values, body any) (*cfEnvelope, error) {
	u := p.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Cloudflare API 不可达: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env cfEnvelope
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode/100 != 2 || !env.Success {
		detail := http.StatusText(resp.StatusCode)
		if len(env.Errors) > 0 {
			detail = fmt.Sprintf("%d %s", env.Errors[0].Code, env.Errors[0].Message)
		}
		return nil, &cfStatusError{status: resp.StatusCode, detail: detail}
	}
	return &env, nil
}

// cfAuthRejected 判断是不是凭据问题：401/403，或 400 带鉴权类错误码（6003/6111/9106/9109/10000）。
func cfAuthRejected(err error) (*cfStatusError, bool) {
	se, ok := err.(*cfStatusError)
	if !ok {
		return nil, false
	}
	if se.status == http.StatusUnauthorized || se.status == http.StatusForbidden {
		return se, true
	}
	if se.status == http.StatusBadRequest {
		for _, code := range []string{"6003 ", "6111 ", "9106 ", "9109 ", "10000 "} {
			if len(se.detail) >= len(code) && se.detail[:len(code)] == code {
				return se, true
			}
		}
	}
	return se, false
}

func (p *cloudflareProvider) check(ctx context.Context, zone string, write bool) (checkResult, error) {
	var res checkResult
	env, err := p.do(ctx, http.MethodGet, "/zones", url.Values{"name": {zone}, "per_page": {"50"}}, nil)
	if err != nil {
		if se, auth := cfAuthRejected(err); auth {
			return res, credentialRejected(err, "Cloudflare 拒绝了这个令牌（%s）", se.detail)
		}
		return res, err
	}
	var zones []cfZone
	_ = json.Unmarshal(env.Result, &zones)
	var found *cfZone
	for i := range zones {
		if zoneMatches(zones[i].Name, zone) {
			found = &zones[i]
		}
	}
	if found == nil {
		return res, credentialRejected(errZoneNotFound,
			"令牌看不到 zone %s：确认令牌的 Zone Resources 包含它，权限有 Zone·Zone·Read", zone)
	}
	if len(found.Permissions) > 0 && !containsString(found.Permissions, "#dns_records:edit") {
		res.Warnings = append(res.Warnings, "令牌在这个 zone 上似乎没有 DNS 编辑权限（Zone·DNS·Edit）")
	}
	all, err := p.do(ctx, http.MethodGet, "/zones", url.Values{"per_page": {"50"}}, nil)
	if err != nil {
		return res, err
	}
	if all.ResultInfo != nil {
		res.VisibleZones = all.ResultInfo.TotalCount
	} else {
		var list []cfZone
		_ = json.Unmarshal(all.Result, &list)
		res.VisibleZones = len(list)
	}
	if !write {
		return res, nil
	}
	created, err := p.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(found.ID)+"/dns_records", nil, map[string]any{
		"type": "TXT", "name": checkRecordLabel + "." + zone, "content": `"` + checkRecordValue() + `"`, "ttl": 120,
		"comment": "pandora certificate credential check",
	})
	if err != nil {
		if se, auth := cfAuthRejected(err); auth {
			return res, credentialRejected(err, "令牌不能在 zone %s 里写 TXT 记录（%s）：需要 Zone·DNS·Edit", zone, se.detail)
		}
		return res, err
	}
	var rec struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(created.Result, &rec)
	if rec.ID != "" {
		if _, err := p.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(found.ID)+"/dns_records/"+url.PathEscape(rec.ID), nil, nil); err != nil {
			res.Warnings = append(res.Warnings, "校验用的 TXT 记录 "+checkRecordLabel+"."+zone+" 没删掉，请手动删除")
		}
	}
	return res, nil
}

func (p *cloudflareProvider) lego(propagation, polling time.Duration) (challenge.Provider, error) {
	cfg := &cloudflare.Config{
		AuthToken:          p.token,
		TTL:                120,
		PropagationTimeout: propagation,
		PollingInterval:    polling,
		HTTPClient:         p.client,
	}
	if p.custom {
		cfg.BaseURL = p.base
	}
	return cloudflare.NewDNSProviderConfig(cfg)
}
