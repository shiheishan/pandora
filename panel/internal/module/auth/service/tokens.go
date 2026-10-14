package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/google/uuid"
)

// 认证方式（会话与令牌里的 amr）。
const (
	amrPassword   = "password"
	amrQuickLogin = "quick_login"
	amrMailLink   = "mail_link"
)

const (
	secretBytes   = 32
	maxUserAgent  = 256
	quickLoginTTL = 60 * time.Second
	mailLinkTTL   = 15 * time.Minute
)

// newSecret 生成 32 字节随机串（base64url，无填充）。
func newSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashSecret 返回随机串的 SHA-256（hex）；Redis 里只存它。
func hashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// formatRefresh 拼出 refresh cookie 的值：<sid>.<随机串>。
func formatRefresh(sid, secret string) string {
	return sid + "." + secret
}

// parseRefresh 拆 refresh cookie；sid 必须是 UUID、随机串必须解出 32 字节。
func parseRefresh(raw string) (sid, secret string, ok bool) {
	sid, secret, found := strings.Cut(raw, ".")
	if !found {
		return "", "", false
	}
	if _, err := uuid.Parse(sid); err != nil {
		return "", "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(b) != secretBytes {
		return "", "", false
	}
	return sid, secret, true
}

// newSessionID 生成会话 ID（UUIDv7 字符串）。
func newSessionID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// normalizeEmail 小写并去掉首尾空白（与 identity 的口径一致）。
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// emailHash 是邮箱的 HMAC（hex），用于失败计数与失败审计，Redis 与审计里不出现明文邮箱。
func (s *Service) emailHash(email string) string {
	return hex.EncodeToString(s.keys.HMAC(crypto.InfoHMACIdentity, []byte(normalizeEmail(email))))
}

// ipHash 是请求来源 IP 的 HMAC（hex）；没有 IP（命令行）时为空串。
func (s *Service) ipHash(ctx context.Context) string {
	ip := reqmeta.From(ctx).IP
	if ip == "" {
		return ""
	}
	return hex.EncodeToString(s.keys.HMAC(crypto.InfoHMACIP, []byte(ip)))
}

// userAgent 取请求的 UA，去 NUL、非法 UTF-8，截到 256 字节。
func userAgent(ctx context.Context) string {
	ua := strings.ToValidUTF8(strings.ReplaceAll(reqmeta.From(ctx).UserAgent, "\x00", ""), "�")
	if len(ua) <= maxUserAgent {
		return ua
	}
	ua = ua[:maxUserAgent]
	for len(ua) > 0 && !utf8.ValidString(ua) {
		ua = ua[:len(ua)-1]
	}
	return ua
}

// issueAccess 为会话签发本域的访问令牌，返回令牌与有效秒数。
func (s *Service) issueAccess(aud string, uid uuid.UUID, sid string, amr []string, now time.Time) (string, int, error) {
	iss := s.issuer(aud)
	if iss == nil {
		return "", 0, errs.Newf(errs.Internal, "未知的登录域")
	}
	tok, _, err := iss.Sign(uid.String(), sid, amr, now)
	if err != nil {
		return "", 0, errs.Wrap(errs.Internal, err)
	}
	return tok, int(iss.TTL() / time.Second), nil
}

// newSession 准备一个新会话（还没写进 Redis）：epoch 必须是校验口令之前读到的纪元。
// reauth 为 true 时 reauth_at 记为 now（口令登录算一次重认证）。返回会话记录与 refresh cookie 值。
func (s *Service) newSession(ctx context.Context, uid uuid.UUID, aud string, amr []string,
	epoch string, reauth bool, now time.Time) (sessionRecord, string, error) {
	sid, err := newSessionID()
	if err != nil {
		return sessionRecord{}, "", errs.Wrap(errs.Internal, err)
	}
	secret, err := newSecret()
	if err != nil {
		return sessionRecord{}, "", errs.Wrap(errs.Internal, err)
	}
	rec := sessionRecord{
		ID:        sid,
		UserID:    uid,
		Audience:  aud,
		AMR:       amr,
		CreatedAt: now,
		LastSeen:  now,
		AbsExp:    now.Add(s.sessionTTL(aud)),
		UserAgent: userAgent(ctx),
		IPHash:    s.ipHash(ctx),
		RTCur:     hashSecret(secret),
		Epoch:     epoch,
	}
	if reauth {
		rec.ReauthAt = now
	}
	return rec, formatRefresh(sid, secret), nil
}
