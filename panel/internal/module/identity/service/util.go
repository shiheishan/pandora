package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/google/uuid"
	"go.uber.org/zap"
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

// ValidEmail 判断能否作为收信地址：不含 CR/LF、不超过 254 字节，且 net/mail 能解析成不带显示名、与原串一致的裸地址
// （规则与 notify 的发信校验一致，挡掉 a..b@x.com、a,b@x.com、a@b@x.com 这类发不出信的地址）。调用方先 TrimSpace。
func ValidEmail(address string) bool {
	if address == "" || len(address) > 254 || strings.ContainsAny(address, "\r\n") {
		return false
	}
	a, err := mail.ParseAddress(address)
	return err == nil && a.Name == "" && a.Address == address
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
		return errs.Wrap(errs.Internal, errNotWired)
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

// 改密时当前口令的试错上限：同一账号 15 分钟窗口内最多 5 次进入校验，超出（含口令正确）一律 429，
// 直到窗口过去；校验通过后清零。拿到会话的人不能借改密接口在线猜当前口令。计数在 Redis，故障回 503。
const (
	passwordFailLimit  = 5
	passwordFailWindow = 15 * time.Minute
)

func passwordFailKey(userID uuid.UUID) string { return "pwfail:" + userID.String() }

// claimPasswordAttempt 在校验当前口令之前先占一个名额（先占再验，并发请求也最多 5 个进入校验）：
// 超出上限回 429 并带 Retry-After（剩余窗口），不进哈希闸。
func (s *Service) claimPasswordAttempt(ctx context.Context, userID uuid.UUID) error {
	if s.rdb == nil {
		return errs.Wrap(errs.Internal, errNotWired)
	}
	n, ttl, err := ratelimit.NewCounter(s.rdb).Incr(ctx, passwordFailKey(userID), passwordFailWindow, 0, 0)
	if err != nil {
		return errs.Wrap(errs.Unavailable, fmt.Errorf("改密试错计数: %w", err))
	}
	if n <= passwordFailLimit {
		return nil
	}
	e := errs.Newf(errs.RateLimited, "当前密码错误次数过多，请 15 分钟后再试")
	e.RetryAfterSeconds = int(passwordFailWindow / time.Second)
	if ttl > 0 {
		e.RetryAfterSeconds = int(ttl / time.Second)
	}
	return e
}

// clearPasswordAttempts 在当前口令校验通过后清零计数；失败只记日志（计数留着最多让本人多等一个窗口）。
func (s *Service) clearPasswordAttempts(ctx context.Context, userID uuid.UUID) {
	if err := ratelimit.NewCounter(s.rdb).Reset(ctx, passwordFailKey(userID)); err != nil {
		plog.From(ctx).Warn("清零改密试错计数失败", zap.Error(err))
	}
}

// cleanText 规整来自请求的自由文本：去首尾空白；含 NUL 或非法 UTF-8 时 ok 为 false（调用方回 422）。
func cleanText(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if strings.ContainsRune(s, 0) || !utf8.ValidString(s) {
		return "", false
	}
	return s, true
}
