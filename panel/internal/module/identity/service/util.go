package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// normalizeEmail 去空白并转小写；库里是 citext，判重本来就不分大小写，这里只为哈希与限流键稳定。
func normalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// looksLikeEmail 只做最低限度的形状检查：有且只有一个有意义的 @，域名带点，没有空白与控制字符，不超过 254 字节。
func looksLikeEmail(s string) bool {
	if len(s) < 3 || len(s) > 254 {
		return false
	}
	at := strings.LastIndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return strings.Contains(s[at+1:], ".")
}

// errEmailInvalid 是邮箱格式不对的 422。
func errEmailInvalid() error {
	return errs.Invalid(map[string]string{"email": "邮箱格式不正确"})
}

// newToken 生成 n 字节熵的 URL-safe 随机令牌。
func newToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机令牌: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newNumericCode 生成 n 位数字验证码，拒绝采样消除取模偏差。
func newNumericCode(n int) (string, error) {
	out := make([]byte, n)
	buf := make([]byte, 1)
	for i := 0; i < n; {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("生成验证码: %w", err)
		}
		if buf[0] >= 250 {
			continue
		}
		out[i] = '0' + buf[0]%10
		i++
	}
	return string(out), nil
}

// validNumericCode 只收恰好 n 位 ASCII 数字。
func validNumericCode(code string, n int) bool {
	if len(code) != n {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return true
}

// hashToken 是高熵令牌的 sha256（注册令牌）。
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// targetHash 是验证码收件目标（规整后的邮箱）的 HMAC。
func (s *Service) targetHash(email string) []byte {
	return s.keys.HMAC(crypto.InfoHMACIdentity, []byte("vc-target:"+normalizeEmail(email)))
}

// codeHash 是验证码的 HMAC：6 位数字只有一百万种，不能只做 sha256。
func (s *Service) codeHash(code string) []byte {
	return s.keys.HMAC(crypto.InfoHMACIdentity, []byte("vc-code:"+code))
}

// ipHash 是来源 IP 的 HMAC（与审计同一个子密钥）；ip 为空时返回 nil。
func (s *Service) ipHash(ip string) []byte {
	if ip == "" {
		return nil
	}
	return s.keys.HMAC(crypto.InfoHMACIP, []byte(ip))
}

// 按账号维度的严格限流（Redis 故障回 503）；按 IP、网段的在 handler 的路由中间件上。
var (
	ruleRegisterEmail      = ratelimit.PerWindow("reg_email", 5, 10*time.Minute).StrictMode()
	ruleRegisterInvite     = ratelimit.PerWindow("reg_invite", 20, 10*time.Minute).StrictMode()
	ruleRegisterToken      = ratelimit.PerWindow("reg_token", 6, 10*time.Minute).StrictMode()
	ruleResetStartEmail    = ratelimit.PerWindow("pwreset_email", 3, 15*time.Minute).StrictMode()
	ruleResetCompleteEmail = ratelimit.PerWindow("pwreset_complete_email", 10, 15*time.Minute).StrictMode()
)

// limit 按 subject（邮箱、邀请码、令牌）限流；键是 HMAC，Redis 里不出现明文。
func (s *Service) limit(ctx context.Context, rule ratelimit.Rule, subject string) error {
	if s.limiter == nil {
		return nil
	}
	sum := s.keys.HMAC(crypto.InfoHMACIdentity, []byte("rl:"+rule.Name+":"+subject))
	return s.limiter.Allow(ctx, rule, hex.EncodeToString(sum[:16]))
}

// startFloor 是注册与找回第 1 步的最短耗时：账号存在时多写几行、多排一封信，
// 不存在时什么都不写，两者的耗时差用这个下限抹平（IAM-006）。
const startFloor = 300 * time.Millisecond

// padSince 睡到 start 之后至少 d；ctx 取消时提前返回。
func padSince(ctx context.Context, start time.Time, d time.Duration) {
	left := d - time.Since(start)
	if left <= 0 {
		return
	}
	t := time.NewTimer(left)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// 改密时当前口令的试错上限：同一账号 15 分钟窗口内错满 5 次，之后（含口令正确）一律 429，
// 直到窗口过去。拿到会话的人不能借改密接口在线猜当前口令。计数在 Redis，故障回 503。
const (
	passwordFailLimit  = 5
	passwordFailWindow = 15 * time.Minute
)

func passwordFailKey(userID uuid.UUID) string { return "pwfail:" + userID.String() }

// checkPasswordFailures 在校验当前口令之前调用：已错满就回 429 并带 Retry-After。
func (s *Service) checkPasswordFailures(ctx context.Context, userID uuid.UUID) error {
	key := passwordFailKey(userID)
	n, err := s.rdb.Get(ctx, key).Int()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if err != nil {
		return errs.Wrap(errs.Unavailable, err)
	}
	if n < passwordFailLimit {
		return nil
	}
	e := errs.Newf(errs.RateLimited, "当前密码错误次数过多，请 15 分钟后再试")
	e.RetryAfterSeconds = int(passwordFailWindow / time.Second)
	if ttl, err := s.rdb.TTL(ctx, key).Result(); err == nil && ttl > 0 {
		e.RetryAfterSeconds = int(math.Ceil(ttl.Seconds()))
	}
	return e
}

// recordPasswordFailure 记一次当前口令错误；窗口从第一次错误起算。
func (s *Service) recordPasswordFailure(ctx context.Context, userID uuid.UUID) error {
	key := passwordFailKey(userID)
	pipe := s.rdb.TxPipeline()
	pipe.Incr(ctx, key)
	pipe.ExpireNX(ctx, key, passwordFailWindow)
	if _, err := pipe.Exec(ctx); err != nil {
		return errs.Wrap(errs.Unavailable, err)
	}
	return nil
}

// cleanText 规整来自请求的自由文本：去首尾空白；含 NUL 或非法 UTF-8 时 ok 为 false（调用方回 422）。
func cleanText(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if strings.ContainsRune(s, 0) || !utf8.ValidString(s) {
		return "", false
	}
	return s, true
}
