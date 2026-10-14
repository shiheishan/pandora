package service

import (
	"context"
	"errors"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/identity/model"
	"github.com/aegispanel/aegis/internal/module/identity/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// toUser 把 model.User 转成对外的 User。
func toUser(u model.User) User {
	return User{
		ID:        u.ID,
		Email:     u.Email,
		Status:    u.Status,
		CreatedAt: u.CreatedAt,
	}
}

func toProfile(u model.User) Profile {
	return Profile{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Status:      u.Status,
		CreatedAt:   u.CreatedAt,
	}
}

func requireAudience(audience string) error {
	if audience != authn.AudiencePublic && audience != authn.AudienceAdmin {
		return errs.Newf(errs.BadRequest, "未知的登录域")
	}
	return nil
}

// Authenticate 校验邮箱与口令。邮箱不存在时做一次 dummyVerify 对齐耗时；邮箱不存在或口令不对都返回
// ErrInvalidCredentials，账号停用或封禁返回 ErrAccountInactive（对外同一文案）。账号存在时，
// 出错也带回 User（只给 auth 写失败审计和计数用，不代表登录成功）。参数过时的哈希静默重算；
// last_login_at 不在这里记（口令对不等于登录成功），由 auth 在会话建成后调 TouchLastLogin。
func (s *Service) Authenticate(ctx context.Context, audience, email, password string) (User, error) {
	if err := requireAudience(audience); err != nil {
		return User{}, err
	}
	email = normalizeEmail(email)

	var cred repo.Credential
	var found bool
	if looksLikeEmail(email) {
		var qerr error
		cred, found, qerr = s.repo.CredentialByEmail(ctx, email)
		if qerr != nil {
			return User{}, qerr
		}
	}

	release, err := s.hasher.acquire(ctx, audience)
	if err != nil {
		return User{}, err
	}

	if !found || cred.PHC == nil {
		dummyVerify(password)
		release()
		u := User{}
		if found {
			u = toUser(cred.User)
		}
		return u, ErrInvalidCredentials
	}

	ok, rehash, err := verifyPHC(password, *cred.PHC)
	if err != nil {
		release()
		return User{}, errs.Wrap(errs.Internal, err)
	}
	if !ok {
		release()
		return toUser(cred.User), ErrInvalidCredentials
	}
	if cred.Status != model.UserActive {
		release()
		return toUser(cred.User), ErrAccountInactive
	}

	var newPHC string
	if rehash {
		if h, herr := hashPHC(password); herr == nil {
			newPHC = h
		}
	}
	release()

	if newPHC != "" {
		if _, err := s.repo.RehashPassword(ctx, cred.ID, *cred.PHC, newPHC); err != nil {
			plog.From(ctx).Warn("静默升级口令哈希失败", zap.Error(err))
		}
	}

	return toUser(cred.User), nil
}

// TouchLastLogin 记最近登录时间；auth 在登录或快捷登录的会话建成后调用。
func (s *Service) TouchLastLogin(ctx context.Context, userID uuid.UUID) error {
	if err := s.repo.TouchLastLogin(ctx, userID); err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	return nil
}

// VerifyPassword 校验某个已知用户的口令（auth 的重认证用）：不对返回 ErrInvalidCredentials。
func (s *Service) VerifyPassword(ctx context.Context, audience string, userID uuid.UUID, password string) error {
	if err := requireAudience(audience); err != nil {
		return err
	}

	cred, found, err := s.repo.CredentialByID(ctx, userID)
	if err != nil {
		return err
	}

	release, err := s.hasher.acquire(ctx, audience)
	if err != nil {
		return err
	}

	if !found || cred.PHC == nil {
		dummyVerify(password)
		release()
		return ErrInvalidCredentials
	}

	ok, _, err := verifyPHC(password, *cred.PHC)
	if err != nil {
		release()
		return errs.Wrap(errs.Internal, err)
	}
	if !ok {
		release()
		return ErrInvalidCredentials
	}
	if cred.Status != model.UserActive {
		release()
		return ErrAccountInactive
	}
	release()
	return nil
}

// Get 读一个用户；不存在回 404。
func (s *Service) Get(ctx context.Context, id uuid.UUID) (User, error) {
	u, ok, err := s.repo.UserByID(ctx, id)
	if err != nil {
		return User{}, err
	}
	if !ok {
		return User{}, errs.New(errs.NotFound)
	}
	return toUser(u), nil
}

// GetMany 批量读用户（一条 SQL），不存在的 id 不在结果里。
func (s *Service) GetMany(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]User, error) {
	rows, err := s.repo.UsersByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]User, len(rows))
	for _, u := range rows {
		out[u.ID] = toUser(u)
	}
	return out, nil
}

// FindByEmail 按邮箱读用户（给命令行用）；不存在回 404。
func (s *Service) FindByEmail(ctx context.Context, email string) (User, error) {
	email = normalizeEmail(email)
	u, ok, err := s.repo.UserByEmail(ctx, email)
	if err != nil {
		return User{}, err
	}
	if !ok {
		return User{}, errs.New(errs.NotFound)
	}
	return toUser(u), nil
}

// Profile 读 /me 要的账户资料；不存在回 401（会话还在、人没了，按未登录处理）。
func (s *Service) Profile(ctx context.Context, id uuid.UUID) (Profile, error) {
	u, ok, err := s.repo.UserByID(ctx, id)
	if err != nil {
		return Profile{}, err
	}
	if !ok {
		return Profile{}, errs.New(errs.Unauthorized)
	}
	return toProfile(u), nil
}

// CreateUser 建一个有口令的用户（给命令行建管理员用）：口令按后台人员的 12 位规则校验；邮箱已存在回 409。
func (s *Service) CreateUser(ctx context.Context, email, password string) (User, error) {
	email = normalizeEmail(email)
	if !ValidEmail(email) {
		return User{}, errEmailInvalid()
	}
	if err := validatePassword(password, true); err != nil {
		return User{}, err
	}

	release, err := s.hasher.acquire(ctx, authn.AudienceAdmin)
	if err != nil {
		return User{}, err
	}
	phc, err := hashPHC(password)
	release()
	if err != nil {
		return User{}, errs.Wrap(errs.Internal, err)
	}

	now := s.now()
	u := model.User{
		Email:           email,
		Status:          model.UserActive,
		EmailVerifiedAt: &now,
	}

	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.InsertUser(ctx, &u); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.Newf(errs.Conflict, "该邮箱已注册")
			}
			return err
		}
		if err := s.repo.UpsertPassword(ctx, u.ID, phc); err != nil {
			return err
		}
		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionCreated,
			ResourceType: ResourceUser,
			ResourceID:   &u.ID,
			Data:         map[string]any{"via": "cli"},
		})
	})
	if err != nil {
		return User{}, err
	}
	return toUser(u), nil
}

// ChangePassword 改当前登录用户的口令：校验当前口令（15 分钟错 5 次后一律 429），成功后吊销其它会话；
// 门户保留当前会话，后台连当前会话一起吊销（前端随后要求重新登录）。
func (s *Service) ChangePassword(ctx context.Context, oldPassword, newPassword string) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}

	keep := p.SessionID
	if p.Audience == authn.AudienceAdmin {
		keep = ""
	}
	audience := p.Audience
	if audience == "" {
		audience = authn.AudiencePublic
	}

	if oldPassword == "" || newPassword == "" {
		return errs.Invalid(map[string]string{
			"old_password": "当前密码必填",
			"new_password": "新密码必填",
		})
	}

	// 先占试错名额再验：并发请求也最多 5 个能进入校验，超出直接 429、不进哈希闸。
	if err := s.claimPasswordAttempt(ctx, p.UserID); err != nil {
		return err
	}

	phc, ok, err := s.repo.PasswordHash(ctx, p.UserID)
	if err != nil {
		return err
	}

	release, err := s.hasher.acquire(ctx, audience)
	if err != nil {
		return err
	}

	passwordOK := false
	if !ok {
		dummyVerify(oldPassword)
	} else {
		var verr error
		passwordOK, _, verr = verifyPHC(oldPassword, phc)
		if verr != nil {
			release()
			return errs.Wrap(errs.Internal, verr)
		}
	}

	if !passwordOK {
		release()
		_ = s.tx.Do(ctx, func(ctx context.Context) error {
			return s.writeAudit(ctx, auditsvc.Event{
				Action:       ActionPasswordChanged,
				ResourceType: ResourceUser,
				ResourceID:   &p.UserID,
				Outcome:      auditsvc.OutcomeFailure,
				Data:         map[string]any{"error_code": "invalid_password"},
			})
		})
		return ErrWrongCurrentPassword
	}
	s.clearPasswordAttempts(ctx, p.UserID)

	if newPassword == oldPassword {
		release()
		return errs.Newf(errs.BadRequest, "新密码不能与当前密码相同")
	}

	staff, err := s.isStaff(ctx, p.UserID)
	if err != nil {
		release()
		return err
	}
	if err := validatePassword(newPassword, staff); err != nil {
		release()
		return err
	}

	newPHC, err := hashPHC(newPassword)
	if err != nil {
		release()
		return errs.Wrap(errs.Internal, err)
	}
	release()

	return s.tx.Do(ctx, func(ctx context.Context) error {
		cur, locked, err := s.repo.LockPasswordHash(ctx, p.UserID)
		if err != nil {
			return err
		}
		if !locked || cur != phc {
			return errs.New(errs.Conflict)
		}
		changed, err := s.repo.ReplacePassword(ctx, p.UserID, phc, newPHC)
		if err != nil {
			return err
		}
		if !changed {
			return errs.New(errs.Conflict)
		}

		revoker, err := s.needRevoker()
		if err != nil {
			return err
		}
		if err := revoker.RevokeUser(ctx, p.UserID, keep); err != nil {
			return err
		}

		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionPasswordChanged,
			ResourceType: ResourceUser,
			ResourceID:   &p.UserID,
			Data: map[string]any{
				"sessions_revoked": true,
				"kept_current":     keep != "",
			},
		})
	})
}
