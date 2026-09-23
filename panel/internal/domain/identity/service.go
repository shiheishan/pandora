// Package identity 实现注册、验证与登录。
//
// 对应 IAM-001..IAM-006。本包最需要小心的是 IAM-006：
// 「不能通过正文、状态码或明显时间差确认账号存在」。
// 这条要求贯穿注册、登录、找回密码三个入口，任何一处提前返回都会破功。
package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/token"
)

type transactionRunner interface {
	InTx(context.Context, db.Scope, func(pgx.Tx) error) error
}

type Service struct {
	pool       transactionRunner
	issuer     *token.Issuer
	refreshTTL time.Duration
	hashSalt   []byte
	// devMode 为 true 时在响应里回带验证码，便于本地联调；生产必须为 false。
	devMode bool
}

func NewService(pool *db.Pool, issuer *token.Issuer, refreshTTL time.Duration, hashSalt []byte, devMode bool) *Service {
	return &Service{pool: pool, issuer: issuer, refreshTTL: refreshTTL, hashSalt: hashSalt, devMode: devMode}
}

//------------------------------------------------------------------------------
// 注册（IAM-001 / IAM-002）
//------------------------------------------------------------------------------

type StartRegistrationInput struct {
	Email      string
	InviteCode string
	IPHash     []byte
	// IP 是明文来源地址，供审计做风控画像
	IP        string
	UserAgent string
}

type StartRegistrationOutput struct {
	RegistrationToken string
	ExpiresAt         time.Time
	// DevCode 仅在开发模式下非空
	DevCode string
	// VerificationRequired 告诉前端要不要显示验证码输入框。
	// 由后台开关决定，前端不该自己猜
	VerificationRequired bool
}

// StartRegistration 开启注册事务并发送验证码。
//
// 无论邮箱是否已注册，本方法的外部行为完全一致：
// 都返回一个 registration_token 与相同的过期时间。
// 邮箱已存在时，实际发出的是「你的账号已存在，请直接登录」邮件而非验证码，
// 但调用方从 API 响应上分辨不出来 —— 这是 IAM-006 的正解。
// emailVerifyEnabled 读注册是否需要验证邮箱。
//
// 默认关：没配 SMTP 却开着验证，等于把注册入口焊死 —— 验证码发不出去，
// 谁也注册不了，而这个故障在日志里只表现为「注册量归零」。
// 要开验证的站点会在后台主动打开，那时 SMTP 也配好了。
func emailVerifyEnabled(ctx context.Context, tx pgx.Tx, tenantID string) (bool, error) {
	var on bool
	err := tx.QueryRow(ctx, `
		SELECT (value #>> '{}')::boolean
		  FROM system_settings
		 WHERE tenant_id = $1 AND key = 'auth.email_verification'
		 FOR SHARE`,
		tenantID).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return on, err
}

func (s *Service) StartRegistration(ctx context.Context, tenantID string, in StartRegistrationInput) (*StartRegistrationOutput, error) {
	email := normalizeEmail(in.Email)
	if !looksLikeEmail(email) {
		return nil, httpx.Invalid(map[string]string{"email": "邮箱格式不正确"})
	}

	regToken, err := crypto.NewToken(32)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	code, err := crypto.NewNumericCode(6)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	nonce, err := crypto.NewToken(16)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	// IAM-002：5–10 分钟失效
	expiresAt := time.Now().Add(10 * time.Minute)

	var alreadyExists bool
	var needVerify bool
	scope := db.Scope{TenantID: tenantID}

	err = s.pool.InTx(ctx, scope, func(tx pgx.Tx) error {
		_, inviteID, err := enforceRegistrationStartPolicy(ctx, tx, tenantID, in.InviteCode)
		if err != nil {
			return err
		}
		needVerify, err = emailVerifyEnabled(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM users WHERE tenant_id = $1 AND email = $2)`,
			tenantID, email).Scan(&alreadyExists); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO registration_sessions
				(tenant_id, token_hash, email, nonce, risk_context, expires_at, invite_code_id)
			VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::uuid)`,
			tenantID, crypto.HashToken(regToken), email,
			crypto.HashToken(nonce),
			map[string]any{"ip_hash": fmt.Sprintf("%x", in.IPHash), "ua": in.UserAgent},
			expiresAt, inviteID); err != nil {
			return err
		}

		// 邮箱已存在时不写验证码 —— 写了反而可能被用来劫持既有账号。
		// 关闭验证时也不写：没人会去核对它，留着只是让表长胖
		if !alreadyExists && needVerify {
			if _, err := tx.Exec(ctx, `
				INSERT INTO verification_codes
					(tenant_id, purpose, target_hash, code_hash, expires_at)
				VALUES ($1, 'email_verify', $2, $3, $4)`,
				tenantID,
				crypto.HashIdentifier(s.hashSalt, email),
				crypto.HashToken(code),
				expiresAt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, he
		}
		return nil, httpx.Internal(err)
	}

	out := &StartRegistrationOutput{
		RegistrationToken:    regToken,
		ExpiresAt:            expiresAt,
		VerificationRequired: needVerify,
	}
	if needVerify && s.devMode && !alreadyExists {
		out.DevCode = code
	}
	return out, nil
}

type CompleteRegistrationInput struct {
	RegistrationToken string
	Code              string
	Password          string
}

type CompleteRegistrationOutput struct {
	UserID string
	Email  string
}

// CompleteRegistration 校验验证码并创建账号。
func (s *Service) CompleteRegistration(ctx context.Context, tenantID string, in CompleteRegistrationInput) (*CompleteRegistrationOutput, error) {
	if err := validatePassword(in.Password); err != nil {
		return nil, err
	}

	scope := db.Scope{TenantID: tenantID}
	var out CompleteRegistrationOutput
	var rejectedAttempt bool

	err := s.pool.InTx(ctx, scope, func(tx pgx.Tx) error {
		registrationMode, err := enforceRegistrationCompletePolicy(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		// 取注册事务并加行锁，防止同一 token 被并发消费两次（IAM-001）
		var (
			sessionID     string
			email         string
			status        string
			attempts      int16
			boundInviteID string
		)
		err = tx.QueryRow(ctx, `
			SELECT id, email, status, attempts, COALESCE(invite_code_id::text, '')
			  FROM registration_sessions
			 WHERE tenant_id = $1 AND token_hash = $2 AND expires_at > now()
			 FOR UPDATE`,
			tenantID, crypto.HashToken(in.RegistrationToken)).Scan(
			&sessionID, &email, &status, &attempts, &boundInviteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRegistrationUnavailable
		}
		if err != nil {
			return err
		}
		if status != "pending" {
			return ErrRegistrationUnavailable
		}
		if attempts >= 5 {
			return ErrRegistrationUnavailable
		}
		if registrationMode == RegistrationModeInviteOnly && boundInviteID == "" {
			return ErrRegistrationUnavailable
		}

		// Only the invite identity persisted at start can authorize completion.
		boundInvite, err := lockBoundInvite(ctx, tx, tenantID, boundInviteID)
		if err != nil {
			return ErrRegistrationUnavailable
		}

		// 关闭邮箱验证时跳过验证码这一段。
		//
		// 跳过的只是「核对验证码」。上面对 registration_sessions 的校验照跑，
		// 所以拿不到有效注册令牌的人依然建不了号 —— 关掉的是邮箱所有权证明，
		// 不是整条注册防线。
		verifyOn, err := emailVerifyEnabled(ctx, tx, tenantID)
		if err != nil {
			return err
		}

		var codeID string
		if verifyOn {
			err = tx.QueryRow(ctx, `
				SELECT id FROM verification_codes
				 WHERE tenant_id = $1 AND purpose = 'email_verify'
				   AND target_hash = $2 AND code_hash = $3
				   AND consumed_at IS NULL AND expires_at > now()
				 ORDER BY created_at DESC LIMIT 1
				 FOR UPDATE`,
				tenantID,
				crypto.HashIdentifier(s.hashSalt, email),
				crypto.HashToken(in.Code)).Scan(&codeID)
			if errors.Is(err, pgx.ErrNoRows) {
				tag, updateErr := tx.Exec(ctx,
					`UPDATE registration_sessions SET attempts = attempts + 1 WHERE id = $1`,
					sessionID)
				if updateErr != nil {
					return updateErr
				}
				if tag.RowsAffected() != 1 {
					return ErrRegistrationUnavailable
				}
				// Returning the public rejection from inside InTx would roll the
				// invalid-attempt counter back, so commit the counter first.
				rejectedAttempt = true
				return nil
			}
			if err != nil {
				return err
			}
		}

		phc, err := crypto.HashPassword(in.Password, crypto.DefaultArgon2Params())
		if err != nil {
			return err
		}

		// 创建用户。唯一约束是并发注册的最终仲裁者。
		var userID string
		err = tx.QueryRow(ctx, `
			INSERT INTO users (tenant_id, email, email_verified_at, status)
			VALUES ($1, $2, CASE WHEN $3::bool THEN now() ELSE NULL END, 'active')
			RETURNING id`,
			tenantID, email, verifyOn).Scan(&userID)
		if db.IsUniqueViolation(err) {
			return ErrRegistrationUnavailable
		}
		if err != nil {
			return err
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO user_passwords (user_id, tenant_id, phc) VALUES ($1, $2, $3)`,
			userID, tenantID, phc); err != nil {
			return err
		}

		if codeID != "" {
			if _, err := tx.Exec(ctx,
				`UPDATE verification_codes SET consumed_at = now() WHERE id = $1`,
				codeID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx,
			`UPDATE registration_sessions SET status = 'consumed', consumed_at = now() WHERE id = $1`,
			sessionID); err != nil {
			return err
		}

		// 邀请绑定放在最后：前面任何一步失败都轮不到它，
		// 而它失败时整个注册回滚 —— 用户会看到「邀请码无效」并可以改了重试，
		// 好过默默建了个没有推荐人的号
		if err := consumeBoundInvite(ctx, tx, tenantID, userID, boundInvite); err != nil {
			return ErrRegistrationUnavailable
		}

		// Audit is the final write. audit.Write acquires its own advisory lock;
		// taking more business row/FK locks after it would widen deadlock paths.
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &userID,
			Action: "user.registered", ResourceType: "user", ResourceID: &userID,
			APIDomain: "public", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
		}); err != nil {
			return err
		}

		if err := plugin.EmitUserRegistered(ctx, tx, tenantID, userID, email); err != nil {
			return err
		}

		out = CompleteRegistrationOutput{UserID: userID, Email: email}
		return nil
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, he
		}
		return nil, httpx.Internal(err)
	}
	if rejectedAttempt {
		return nil, ErrRegistrationUnavailable
	}
	return &out, nil
}

//------------------------------------------------------------------------------
// 登录（IAM-005 / IAM-006）
//------------------------------------------------------------------------------

type LoginInput struct {
	Email    string
	Password string
	IPHash   []byte
	// IP 是明文来源地址，供审计做风控画像
	IP        string
	UserAgent string
	// Audience 决定本次会话属于哪个 API 域（EXT-001）。
	// 空值按 public 处理。admin 域会额外要求账号持有角色绑定，
	// 否则普通用户也能在管理域建立会话 —— 虽然拿不到任何权限，
	// 但白白多出一条可被撞库的入口，也污染管理域的会话审计。
	Audience string
}

type LoginOutput struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	UserID       string
	// Permissions 仅在 admin 域返回，供管理界面决定显示哪些入口。
	// 前端据此隐藏按钮只是体验优化，真正的拦截在网关的 RequirePermission。
	Permissions []string
}

// Login 校验口令并建立会话。
//
// IAM-006 的实现要点：
//
//	· 用户不存在时也执行一次等价开销的 Argon2 计算（DummyVerify），消除时间差；
//	· 用户不存在、口令错误、账号被停用，对外都是同一个错误码与文案。
func (s *Service) Login(ctx context.Context, tenantID string, in LoginInput) (*LoginOutput, error) {
	email := normalizeEmail(in.Email)
	scope := db.Scope{TenantID: tenantID}

	audience := in.Audience
	if audience == "" {
		audience = "public"
	}
	switch audience {
	case "public", "admin":
	case "client":
		// CLIENT-AUTH-01 forbids turning a password login into a legacy
		// client session.  The dedicated client gateway will use Device Code
		// + proof-of-possession after the 00047 cutover; until then the route
		// remains closed.
		return nil, httpx.New(httpx.CodeBadRequest, "客户端不支持密码登录")
	default:
		return nil, httpx.New(httpx.CodeBadRequest, "未知的登录域")
	}

	var (
		userID      string
		phc         string
		status      string
		found       bool
		needsHash   bool
		permissions []string
	)

	err := s.pool.InTx(ctx, scope, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT u.id, u.status, p.phc
			  FROM users u
			  JOIN user_passwords p ON p.user_id = u.id
			 WHERE u.tenant_id = $1 AND u.email = $2`,
			tenantID, email).Scan(&userID, &status, &phc)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // found 保持 false，稍后统一处理
		}
		if err != nil {
			return err
		}
		found = true

		// 展开该用户当前生效的权限码（IAM-009 / IAM-010：过期的临时提权不算数）
		prows, err := tx.Query(ctx, `
			SELECT DISTINCT rp.permission_code
			  FROM role_bindings rb
			  JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
			  JOIN role_permissions rp ON rp.role_id = rb.role_id
			 WHERE rb.tenant_id = $1 AND rb.user_id = $2
			   AND (rb.expires_at IS NULL OR rb.expires_at > now())
			   AND ($3::text <> 'admin' OR
			        (rb.scope_type = 'tenant' AND rb.scope_id IS NULL))`,
			tenantID, userID, audience)
		if err != nil {
			return err
		}
		defer prows.Close()
		for prows.Next() {
			var code string
			if err := prows.Scan(&code); err != nil {
				return err
			}
			permissions = append(permissions, code)
		}
		return prows.Err()
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	invalid := httpx.New(httpx.CodeUnauthorized, "邮箱或密码不正确")

	if !found {
		// 关键：不提前返回，先付出与真实校验相同的计算代价
		crypto.DummyVerify(in.Password)
		return nil, invalid
	}

	ok, rehash, err := crypto.VerifyPassword(in.Password, phc)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	if !ok || status != "active" {
		return nil, invalid
	}
	// 管理域要求账号确实被授过权。口令正确但无任何角色时，
	// 对外仍返回与口令错误完全一致的响应（IAM-006），
	// 不让攻击者借此枚举出「哪些邮箱是管理员」。
	if audience == "admin" && len(permissions) == 0 {
		return nil, invalid
	}
	needsHash = rehash

	// 建立会话
	refreshToken, err := crypto.NewToken(32)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	var sessionID string
	now := time.Now()

	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO sessions
				(tenant_id, user_id, audience, user_agent, ip_hash, auth_methods,
				 last_reauth_at, expires_at)
			VALUES ($1, $2, $6, $3, $4, ARRAY['password'], now(), $5)
			RETURNING id`,
			tenantID, userID, in.UserAgent, in.IPHash,
			now.Add(s.refreshTTL), audience).Scan(&sessionID); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO refresh_tokens
				(tenant_id, session_id, user_id, token_hash, expires_at)
			VALUES ($1, $2, $3, $4, $5)`,
			tenantID, sessionID, userID, crypto.HashToken(refreshToken),
			now.Add(s.refreshTTL)); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx,
			`UPDATE users SET last_login_at = now() WHERE id = $1`, userID); err != nil {
			return err
		}

		// 参数被调强后，趁用户输入明文时静默升级
		if needsHash {
			if newPHC, herr := crypto.HashPassword(in.Password, crypto.DefaultArgon2Params()); herr == nil {
				_, _ = tx.Exec(ctx,
					`UPDATE user_passwords SET phc = $2, rotated_at = now() WHERE user_id = $1`,
					userID, newPHC)
			}
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &userID,
			Action: "user.login", ResourceType: "session", ResourceID: &sessionID,
			APIDomain: audience, Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx), SourceIPHash: in.IPHash, SourceIP: in.IP,
			UserAgent: in.UserAgent,
		})
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	access, err := s.issuer.Issue(token.Claims{
		Subject:   userID,
		TenantID:  tenantID,
		SessionID: sessionID,
		Kind:      "user",
		AuthMeth:  []string{"password"},
		ReauthAt:  now.Unix(),
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	out := &LoginOutput{
		AccessToken:  access,
		RefreshToken: refreshToken,
		ExpiresIn:    int(s.issuer.TTL() / time.Second),
		UserID:       userID,
	}
	if audience == "admin" {
		out.Permissions = permissions
	}
	return out, nil
}

//------------------------------------------------------------------------------
// 辅助
//------------------------------------------------------------------------------

func normalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 || len(s) > 254 {
		return false
	}
	return strings.Contains(s[at+1:], ".") && !strings.Contains(s, " ")
}

// validatePassword 只做长度下限。
// 不做「必须含大写+数字+符号」这类组合规则 —— NIST SP 800-63B 明确指出
// 这类规则会驱使用户选择 P@ssw0rd1 这样可预测的模式，实际强度反而更低。
// 真正有效的是长度下限 + 已泄露口令库比对（SEC-005，由风控侧完成）。
func validatePassword(p string) error {
	if err := crypto.ValidatePassword(p); err != nil {
		return httpx.Invalid(map[string]string{"password": err.Error()})
	}
	return nil
}
