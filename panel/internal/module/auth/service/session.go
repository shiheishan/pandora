package service

import (
	"context"
	"crypto/subtle"
	"math"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// principal 从 ctx 取指定域的登录主体；未登录或域不符时返回 errSessionInvalid。
func principal(ctx context.Context, aud string) (authn.Principal, error) {
	p, ok := authn.From(ctx)
	if !ok || p.Audience != aud {
		return authn.Principal{}, errSessionInvalid
	}
	return p, nil
}

// sameHash 常量时间比较两个哈希串是否相同。
func sameHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func sessionResourceID(sid string) (*uuid.UUID, error) {
	id, err := uuid.Parse(sid)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// Authenticate 校验 Bearer 访问令牌并返回请求主体（中间件调用）。
func (s *Service) Authenticate(ctx context.Context, audience, bearer string) (authn.Principal, error) {
	iss := s.issuer(audience)
	if iss == nil {
		return authn.Principal{}, errs.Newf(errs.Internal, "未知的登录域")
	}
	now := s.now()
	claims, err := iss.Verify(bearer, now)
	if err != nil {
		return authn.Principal{}, errSessionInvalid
	}
	uid, err := uuid.Parse(claims.Subject)
	if err != nil {
		return authn.Principal{}, errSessionInvalid
	}
	if _, err := uuid.Parse(claims.SessionID); err != nil {
		return authn.Principal{}, errSessionInvalid
	}
	st, ok, err := s.sessions.check(ctx, claims.SessionID, uid, audience, now, s.idleTimeout(audience))
	if err != nil {
		return authn.Principal{}, errs.Wrap(errs.Unavailable, err)
	}
	if !ok {
		return authn.Principal{}, errSessionInvalid
	}
	return authn.Principal{
		UserID:    uid,
		SessionID: claims.SessionID,
		Audience:  audience,
		AMR:       st.AMR,
		ReauthAt:  st.ReauthAt,
	}, nil
}

// Refresh 用 refresh cookie 换新 access；必要时轮换 refresh。
func (s *Service) Refresh(ctx context.Context, audience, raw string) (Tokens, error) {
	if s.issuer(audience) == nil {
		return Tokens{}, errs.Newf(errs.Internal, "未知的登录域")
	}
	sid, secret, ok := parseRefresh(raw)
	if !ok {
		return Tokens{}, errSessionInvalid
	}
	h := hashSecret(secret)

	for round := 0; round < 2; round++ {
		now := s.now()
		st, found, err := s.sessions.readRefresh(ctx, sid)
		if err != nil {
			return Tokens{}, errs.Wrap(errs.Unavailable, err)
		}
		if !found || st.Audience != audience {
			return Tokens{}, errSessionInvalid
		}

		var mode string
		var expect string
		var newHash string
		var newSecretPlain string
		if sameHash(h, st.RTCur) {
			mode = refreshModeRotate
			expect = h
			sec, err := newSecret()
			if err != nil {
				return Tokens{}, errs.Wrap(errs.Internal, err)
			}
			newSecretPlain = sec
			newHash = hashSecret(sec)
		} else if st.RTPrev != "" && sameHash(h, st.RTPrev) && now.Sub(st.RTRotatedAt) <= refreshGrace {
			mode = refreshModeGrace
			expect = h
		} else {
			sidUUID, parseErr := uuid.Parse(sid)
			if parseErr != nil {
				return Tokens{}, errSessionInvalid
			}
			actor := st.UserID
			err := s.tx.Do(ctx, func(ctx context.Context) error {
				if _, err := s.sessions.revokeOne(ctx, sid, st.UserID, audience); err != nil {
					return errs.Wrap(errs.Unavailable, err)
				}
				return s.audit.Write(ctx, auditsvc.Event{
					Action:       "session.refresh_reuse",
					ResourceType: "session",
					ResourceID:   &sidUUID,
					ActorID:      &actor,
					Audience:     audience,
					Outcome:      auditsvc.OutcomeDenied,
				})
			})
			if err != nil {
				return Tokens{}, err
			}
			return Tokens{}, errSessionInvalid
		}

		status, ss, err := s.sessions.rotate(ctx, sid, st.UserID, audience, now, s.idleTimeout(audience), mode, expect, newHash)
		if err != nil {
			return Tokens{}, errs.Wrap(errs.Unavailable, err)
		}
		if status == rotateConflict {
			continue
		}
		if status == rotateInvalid {
			return Tokens{}, errSessionInvalid
		}
		access, exp, err := s.issueAccess(audience, st.UserID, sid, ss.AMR, now)
		if err != nil {
			return Tokens{}, err
		}
		tok := Tokens{
			AccessToken:    access,
			ExpiresIn:      exp,
			UserID:         st.UserID,
			RefreshExpires: ss.AbsExp,
		}
		if mode == refreshModeRotate {
			tok.Refresh = formatRefresh(sid, newSecretPlain)
		}
		return tok, nil
	}
	return Tokens{}, errSessionInvalid
}

// Logout 吊销会话；找不到会话也视为成功（幂等）。
func (s *Service) Logout(ctx context.Context, audience, rawRefresh string) error {
	if p, ok := authn.From(ctx); ok && p.Audience == audience {
		return s.tx.Do(ctx, func(ctx context.Context) error {
			revoked, err := s.sessions.revokeOne(ctx, p.SessionID, p.UserID, audience)
			if err != nil {
				return errs.Wrap(errs.Unavailable, err)
			}
			if !revoked {
				return nil
			}
			sid, err := sessionResourceID(p.SessionID)
			if err != nil {
				return nil
			}
			return s.audit.Write(ctx, auditsvc.Event{
				Action:       "user.logout",
				ResourceType: "session",
				ResourceID:   sid,
			})
		})
	}

	if rawRefresh == "" {
		return nil
	}
	sid, secret, ok := parseRefresh(rawRefresh)
	if !ok {
		return nil
	}
	h := hashSecret(secret)
	st, found, err := s.sessions.readRefresh(ctx, sid)
	if err != nil {
		return errs.Wrap(errs.Unavailable, err)
	}
	if !found || st.Audience != audience {
		return nil
	}
	match := sameHash(h, st.RTCur) || (st.RTPrev != "" && sameHash(h, st.RTPrev))
	if !match {
		return nil
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		revoked, err := s.sessions.revokeOne(ctx, sid, st.UserID, audience)
		if err != nil {
			return errs.Wrap(errs.Unavailable, err)
		}
		if !revoked {
			return nil
		}
		resID, err := sessionResourceID(sid)
		if err != nil {
			return nil
		}
		actor := st.UserID
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       "user.logout",
			ResourceType: "session",
			ResourceID:   resID,
			ActorID:      &actor,
			Audience:     audience,
		})
	})
}

// ListSessions 返回门户当前用户的有效会话列表（最多 50 条）。
func (s *Service) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	p, err := principal(ctx, authn.AudiencePublic)
	if err != nil {
		return nil, err
	}
	recs, err := s.sessions.list(ctx, p.UserID, authn.AudiencePublic, s.now())
	if err != nil {
		return nil, errs.Wrap(errs.Unavailable, err)
	}
	if len(recs) > 50 {
		recs = recs[:50]
	}
	out := make([]SessionInfo, 0, len(recs))
	for _, rec := range recs {
		out = append(out, SessionInfo{
			ID:         rec.ID,
			Current:    rec.ID == p.SessionID,
			UserAgent:  rec.UserAgent,
			CreatedAt:  rec.CreatedAt,
			LastSeenAt: rec.LastSeen,
			ExpiresAt:  rec.AbsExp,
		})
	}
	return out, nil
}

// RevokeSession 门户用户踢掉自己的一条会话（不能踢当前会话）。
func (s *Service) RevokeSession(ctx context.Context, sid string) error {
	p, err := principal(ctx, authn.AudiencePublic)
	if err != nil {
		return err
	}
	target, err := uuid.Parse(sid)
	if err != nil {
		return errs.New(errs.NotFound)
	}
	if sid == p.SessionID {
		return errs.Newf(errs.ValidationFailed, "这是你当前正在使用的会话。要退出当前设备请直接点退出登录")
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		revoked, err := s.sessions.revokeOne(ctx, sid, p.UserID, authn.AudiencePublic)
		if err != nil {
			return errs.Wrap(errs.Unavailable, err)
		}
		if !revoked {
			return errs.New(errs.NotFound)
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       "session.revoked_by_user",
			ResourceType: "session",
			ResourceID:   &target,
			Data:         map[string]any{"reason": "user_revoked"},
		})
	})
}

// IssueQuickLogin 为当前门户会话签发一次性快捷登录链接。
func (s *Service) IssueQuickLogin(ctx context.Context) (QuickLoginLink, error) {
	p, err := principal(ctx, authn.AudiencePublic)
	if err != nil {
		return QuickLoginLink{}, err
	}
	token, err := newSecret()
	if err != nil {
		return QuickLoginLink{}, errs.Wrap(errs.Internal, err)
	}
	now := s.now()
	ok, err := s.sessions.issueQuickLogin(ctx, p.SessionID, p.UserID, hashSecret(token), quickLoginTTL)
	if err != nil {
		return QuickLoginLink{}, errs.Wrap(errs.Unavailable, err)
	}
	if !ok {
		return QuickLoginLink{}, errs.Newf(errs.Unauthorized, "当前登录状态无法签发快捷登录链接")
	}
	return QuickLoginLink{
		Token:     token,
		ExpiresAt: now.Add(quickLoginTTL),
		ExpiresIn: int(quickLoginTTL / time.Second),
	}, nil
}

const (
	reauthLimit  = 5                // 窗口内允许的重认证口令试错次数
	reauthWindow = 15 * time.Minute // 试错计数窗口（与改密一致）
)

func reauthFailureKey(uid uuid.UUID) string { return "rf:" + uid.String() }

// Reauth 后台用当前口令刷新重认证时间并签发新 access（不换 refresh）。
func (s *Service) Reauth(ctx context.Context, password string) (Tokens, error) {
	p, err := principal(ctx, authn.AudienceAdmin)
	if err != nil {
		return Tokens{}, err
	}
	if password == "" {
		return Tokens{}, errs.Invalid(map[string]string{"password": "请输入当前密码"})
	}

	// 试错上限：先原子占名额再验口令，并发请求绕不过；验对后清零
	key := reauthFailureKey(p.UserID)
	n, ttl, err := s.sessions.claimAttempt(ctx, key, reauthWindow)
	if err != nil {
		return Tokens{}, errs.Wrap(errs.Unavailable, err)
	}
	if n > reauthLimit {
		retry := int(math.Ceil(ttl.Seconds()))
		if retry < 1 {
			retry = int(reauthWindow / time.Second)
		}
		e := errs.Newf(errs.RateLimited, "密码错误次数过多，请稍后再试")
		e.RetryAfterSeconds = retry
		return Tokens{}, e
	}

	err = s.users.VerifyPassword(ctx, authn.AudienceAdmin, p.UserID, password)
	if err == nil {
		if cerr := s.sessions.clearFailures(ctx, key); cerr != nil {
			plog.From(ctx).Warn("清除重认证试错计数失败", zap.Error(cerr))
		}
	} else if errs.As(err).Code != errs.Unauthorized {
		// 口令校验没有结论（繁忙、故障）：退回名额
		if rerr := s.sessions.releaseAttempt(ctx, key); rerr != nil {
			plog.From(ctx).Warn("退回重认证试错名额失败", zap.Error(rerr))
		}
	}
	if err != nil {
		if errs.As(err).Code == errs.Unauthorized {
			_ = s.tx.Do(ctx, func(ctx context.Context) error {
				if werr := s.audit.Write(ctx, auditsvc.Event{
					Action:       "user.reauthenticated",
					ResourceType: "user",
					ResourceID:   &p.UserID,
					Outcome:      auditsvc.OutcomeFailure,
					Data:         map[string]any{"reason": "invalid_password"},
				}); werr != nil {
					plog.From(ctx).Warn("重认证失败审计写入失败", zap.Error(werr))
				}
				return nil
			})
			return Tokens{}, errs.Newf(errs.Unauthorized, "密码不正确")
		}
		return Tokens{}, err
	}

	now := s.now()
	p2 := p
	p2.ReauthAt = now
	if err := s.tx.Do(authn.With(ctx, p2), func(ctx context.Context) error {
		ok, err := s.sessions.stampReauth(ctx, p.SessionID, p.UserID, now)
		if err != nil {
			return errs.Wrap(errs.Unavailable, err)
		}
		if !ok {
			return errs.Newf(errs.Unauthorized, "会话已失效，请重新登录")
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       "user.reauthenticated",
			ResourceType: "user",
			ResourceID:   &p.UserID,
		})
	}); err != nil {
		return Tokens{}, err
	}

	access, exp, err := s.issueAccess(authn.AudienceAdmin, p.UserID, p.SessionID, p.AMR, now)
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{
		AccessToken: access,
		ExpiresIn:   exp,
		UserID:      p.UserID,
	}, nil
}

// Permissions 返回用户当前生效的权限码列表。
func (s *Service) Permissions(ctx context.Context, userID uuid.UUID) ([]string, error) {
	perms, err := s.store.Permissions(ctx, userID)
	if err != nil {
		return nil, errs.Wrap(errs.Internal, err)
	}
	if perms == nil {
		return []string{}, nil
	}
	return perms, nil
}

// AdminProfile 返回后台当前登录用户的资料与权限。
func (s *Service) AdminProfile(ctx context.Context) (AdminProfile, error) {
	p, err := principal(ctx, authn.AudienceAdmin)
	if err != nil {
		return AdminProfile{}, err
	}
	u, err := s.users.Get(ctx, p.UserID)
	if err != nil {
		if errs.As(err).Code == errs.NotFound {
			return AdminProfile{}, errSessionInvalid
		}
		return AdminProfile{}, err
	}
	roles, err := s.store.UserRoles(ctx, p.UserID)
	if err != nil {
		return AdminProfile{}, errs.Wrap(errs.Internal, err)
	}
	perms, err := s.Permissions(ctx, p.UserID)
	if err != nil {
		return AdminProfile{}, err
	}
	refs := make([]RoleRef, 0, len(roles))
	for _, r := range roles {
		refs = append(refs, RoleRef{Code: r.Code, Name: r.Name})
	}
	return AdminProfile{
		UserID:      p.UserID,
		Email:       u.Email,
		Roles:       refs,
		Permissions: perms,
		Reauthed:    p.RecentlyReauthed(s.now()),
	}, nil
}
