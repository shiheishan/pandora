package service

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"golang.org/x/crypto/argon2"
	"golang.org/x/sync/semaphore"
)

// argonParams 是 Argon2id 的代价参数。参数照旧：OWASP 低内存档 19 MiB / 2 次迭代，
// 首发机器内存小，64 MiB × 并发登录会把内存打满。参数内嵌在 PHC 串里，调强后随登录逐步重算。
type argonParams struct {
	memory      uint32 // KiB
	iterations  uint32
	parallelism uint8
	saltLength  uint32
	keyLength   uint32
}

var defaultArgon = argonParams{memory: 19 * 1024, iterations: 2, parallelism: 1, saltLength: 16, keyLength: 32}

// passwordHasher 是 Argon2id 的两道并发闸：public 与 admin 各一个，排队有超时。
// 计算一律在事务外进行：排队的请求不占数据库连接，持名额的请求不等锁。
type passwordHasher struct {
	public       *semaphore.Weighted
	admin        *semaphore.Weighted
	queueTimeout time.Duration
}

func newPasswordHasher(c config.PasswordHash) *passwordHasher {
	pub, adm, qt := c.PublicConcurrency, c.AdminConcurrency, c.QueueTimeout
	if pub < 1 {
		pub = 2
	}
	if adm < 1 {
		adm = 1
	}
	if qt <= 0 {
		qt = 5 * time.Second
	}
	return &passwordHasher{
		public:       semaphore.NewWeighted(int64(pub)),
		admin:        semaphore.NewWeighted(int64(adm)),
		queueTimeout: qt,
	}
}

// acquire 等 audience 对应闸门的一个名额，返回归还函数；等不到回 503。
func (h *passwordHasher) acquire(ctx context.Context, audience string) (func(), error) {
	sem := h.public
	if audience == authn.AudienceAdmin {
		sem = h.admin
	}
	wctx, cancel := context.WithTimeout(ctx, h.queueTimeout)
	defer cancel()
	if err := sem.Acquire(wctx, 1); err != nil {
		return nil, ErrPasswordHashBusy
	}
	return func() { sem.Release(1) }, nil
}

// hashPHC 计算 PHC 格式的 Argon2id 串。调用方须持有名额。
func hashPHC(password string) (string, error) {
	p := defaultArgon
	salt := make([]byte, p.saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐值: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.iterations, p.memory, p.parallelism, p.keyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.iterations, p.parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifyPHC 恒定时间比较口令与 PHC 串；needsRehash 表示该串的参数弱于当前缺省，应在登录后静默升级。
// 调用方须持有名额。
func verifyPHC(password, phc string) (ok, needsRehash bool, err error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, false, errors.New("不是合法的 argon2id PHC 串")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false, errors.New("argon2 版本不匹配")
	}
	var p argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.iterations, &p.parallelism); err != nil {
		return false, false, errors.New("无法解析 argon2 参数")
	}
	if p.memory == 0 || p.iterations == 0 || p.parallelism == 0 {
		return false, false, errors.New("argon2 参数非法")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, false, errors.New("盐值不是合法 base64")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 1024 {
		return false, false, errors.New("哈希不是合法 base64")
	}
	got := argon2.IDKey([]byte(password), salt, p.iterations, p.memory, p.parallelism, uint32(len(want))) //nolint:gosec // 上面已限制 len(want) ≤ 1024
	ok = subtle.ConstantTimeCompare(got, want) == 1
	needsRehash = p.memory < defaultArgon.memory || p.iterations < defaultArgon.iterations
	return ok, needsRehash, nil
}

// dummyVerify 在账号不存在时付出与一次真实校验相同的代价（IAM-006）。调用方须持有名额。
func dummyVerify(password string) {
	p := defaultArgon
	salt := make([]byte, p.saltLength)
	_ = argon2.IDKey([]byte(password), salt, p.iterations, p.memory, p.parallelism, p.keyLength)
}

// 口令策略：只有长度下限、上限与「同时含字母和数字」。不做大写加符号这类组合规则（NIST SP 800-63B）。
// 后台人员（StaffGuard.IsStaff）至少 12 位：看账号不看入口，管理员从门户改密也一样。
const (
	passwordMinRunes      = 8
	staffPasswordMinRunes = 12
	passwordMaxBytes      = 256
)

// validatePassword 校验新口令，错误挂在 password 字段上（与前端的错误落位一致）。
func validatePassword(password string, staff bool) error {
	fail := func(msg string) error { return errs.Invalid(map[string]string{"password": msg}) }
	n := utf8.RuneCountInString(password)
	if n < passwordMinRunes {
		return fail("密码至少需要 8 个字符")
	}
	if len(password) > passwordMaxBytes {
		return fail("密码过长")
	}
	if strings.ContainsRune(password, 0) || !utf8.ValidString(password) {
		return fail("密码含有不支持的字符")
	}
	var hasLetter, hasDigit bool
	for _, r := range password {
		hasLetter = hasLetter || unicode.IsLetter(r)
		hasDigit = hasDigit || unicode.IsDigit(r)
	}
	if !hasLetter || !hasDigit {
		return fail("密码必须同时包含字母和数字")
	}
	if staff && n < staffPasswordMinRunes {
		return fail("管理员密码至少需要 12 个字符")
	}
	return nil
}
