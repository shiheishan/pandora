// Package token 签发与校验访问令牌。
//
// 刻意不使用 JWT：
//
//	· JWT 的 alg 字段由令牌自身携带，历史上反复出现 alg=none 与 RS256→HS256 混淆漏洞；
//	· 本平台只需要「服务端签、服务端验」的对称场景，JWT 的灵活性全是负担。
//
// 这里的格式把算法固定在代码里，令牌无法影响验签方式。
//
// 格式：v1.<domain>.<base64url(payload)>.<base64url(hmac-sha256)>
// HMAC 覆盖 "v1.<domain>.<payload>" 全串，因此域名也被签名保护，
// 改域即改签名 —— 这是 EXT-001 的密码学落点。
package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrMalformed = errors.New("令牌格式非法")
	ErrSignature = errors.New("令牌签名校验失败")
	ErrExpired   = errors.New("令牌已过期")
	ErrAudience  = errors.New("令牌不属于当前 API 域")
)

// Claims 是令牌载荷。字段刻意精简：令牌只承载「你是谁」，
// 「你能做什么」每次请求实时从数据库算，避免权限变更后旧令牌仍然有效。
type Claims struct {
	Subject   string   `json:"sub"`           // user_id
	TenantID  string   `json:"tid"`           //
	SessionID string   `json:"sid,omitempty"` //
	DeviceID  string   `json:"did,omitempty"` // Client 域使用
	Audience  string   `json:"aud"`           // public / admin / client
	Kind      string   `json:"knd"`           // user / admin / device
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp"`
	AuthMeth  []string `json:"amr,omitempty"` // password / passkey / mfa_totp / sso
	ReauthAt  int64    `json:"rat,omitempty"` // 最近一次重认证时间（SEC-009）
}

type Issuer struct {
	secret   []byte
	domain   string
	ttl      time.Duration
	reauthOK time.Duration
}

// NewIssuer 为单个 API 域构造签发器。
// 每个域必须传入不同的 secret —— config.Load 已在启动时校验过这一点。
func NewIssuer(domain string, secret []byte, ttl time.Duration) *Issuer {
	return &Issuer{
		secret:   secret,
		domain:   domain,
		ttl:      ttl,
		reauthOK: 15 * time.Minute,
	}
}

// TTL 返回访问令牌有效期，供登录响应的 expires_in 与真实签发配置保持一致。
func (i *Issuer) TTL() time.Duration { return i.ttl }

func (i *Issuer) Issue(c Claims) (string, error) {
	now := time.Now()
	c.Audience = i.domain
	c.IssuedAt = now.Unix()
	c.ExpiresAt = now.Add(i.ttl).Unix()

	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("序列化令牌载荷: %w", err)
	}

	body := "v1." + i.domain + "." + base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + base64.RawURLEncoding.EncodeToString(i.sign(body)), nil
}

func (i *Issuer) Verify(raw string) (*Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return nil, ErrMalformed
	}

	// 先比域再验签：域不符时连 HMAC 都不必算，也不给计时侧信道留空间
	if parts[1] != i.domain {
		return nil, ErrAudience
	}

	body := parts[0] + "." + parts[1] + "." + parts[2]
	sig, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return nil, ErrMalformed
	}
	if !hmac.Equal(sig, i.sign(body)) {
		return nil, ErrSignature
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrMalformed
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, ErrMalformed
	}

	// 验签通过后仍要检查载荷里的 aud：
	// 防的是「将来某次重构不慎让两域共用了 secret」这种情形。
	if c.Audience != i.domain {
		return nil, ErrAudience
	}
	if time.Now().Unix() >= c.ExpiresAt {
		return nil, ErrExpired
	}
	return &c, nil
}

// ReauthedRecently 判断是否满足高风险动作的重认证时效（SEC-009）。
func (i *Issuer) ReauthedRecently(c *Claims) bool {
	if c.ReauthAt == 0 {
		return false
	}
	return time.Since(time.Unix(c.ReauthAt, 0)) <= i.reauthOK
}

func (i *Issuer) sign(body string) []byte {
	m := hmac.New(sha256.New, i.secret)
	m.Write([]byte(body))
	return m.Sum(nil)
}
