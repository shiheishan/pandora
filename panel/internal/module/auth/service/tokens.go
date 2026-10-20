package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strconv"
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

// refreshSecret 是会话 sid 第 gen 代的 refresh 随机串：HMAC(主密钥派生, sid ‖ 0 ‖ seed ‖ 0 ‖ gen)，32 字节 base64url。
// seed 是会话建立时的随机值（Redis 的 rt_seed，base64url 不含 0 字节，拼接无歧义）：只有主密钥、没有这条会话的种子算不出任何一代，
// 只有 Redis、没有主密钥同样算不出。每一代都可重算，所以 30 秒宽限内拿上一代来能算出当前那一枚原样再发；
// 任何一代签发过的旧值再出现都能认出真伪，判重放（不管中间被轮换了几次）。
func (s *Service) refreshSecret(sid, seed string, gen uint64) string {
	msg := sid + "\x00" + seed + "\x00" + strconv.FormatUint(gen, 10)
	return base64.RawURLEncoding.EncodeToString(s.keys.HMAC(crypto.InfoHMACRefresh, []byte(msg)))
}

// refreshAuthentic 核对随机串确是本服务给这条会话（sid 与种子）第 gen 代签发的（常数时间比较）；没有种子一律不认。
func (s *Service) refreshAuthentic(sid, seed string, gen uint64, secret string) bool {
	if seed == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(s.refreshSecret(sid, seed, gen))) == 1
}

// hashSecret 返回随机串的 SHA-256（hex）；Redis 里只存它。
func hashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// formatRefresh 拼出 refresh cookie 的值：<sid>.<代数>.<随机串>。
func formatRefresh(sid string, gen uint64, secret string) string {
	return sid + "." + strconv.FormatUint(gen, 10) + "." + secret
}

// parseRefresh 拆 refresh cookie；sid 必须是 UUID、代数是规范的十进制、随机串必须解出 32 字节。只核格式，真伪用 refreshAuthentic。
func parseRefresh(raw string) (sid string, gen uint64, secret string, ok bool) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", 0, "", false
	}
	sid, genStr, secret := parts[0], parts[1], parts[2]
	if _, err := uuid.Parse(sid); err != nil {
		return "", 0, "", false
	}
	gen, err := strconv.ParseUint(genStr, 10, 64)
	if err != nil || strconv.FormatUint(gen, 10) != genStr {
		return "", 0, "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(b) != secretBytes {
		return "", 0, "", false
	}
	return sid, gen, secret, true
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
	seed, err := newSecret()
	if err != nil {
		return sessionRecord{}, "", errs.Wrap(errs.Internal, err)
	}
	secret := s.refreshSecret(sid, seed, 0)
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
		RTSeed:    seed,
		RTCur:     hashSecret(secret),
		Epoch:     epoch,
	}
	if reauth {
		rec.ReauthAt = now
	}
	return rec, formatRefresh(sid, 0, secret), nil
}
