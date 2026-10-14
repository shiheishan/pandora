package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/module/identity/model"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const inviteCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// MyInvite 返回用户自己的邀请码（没有就生成一个）与邀请来的人（最多 100 个，邮箱打码）。
func (s *Service) MyInvite(ctx context.Context, userID uuid.UUID) (Invite, []Invitee, error) {
	var inv model.InviteCode
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		row, ok, err := s.repo.ActiveInviteOf(ctx, userID)
		if err != nil {
			return err
		}
		if ok {
			inv = row
			return nil
		}
		if _, _, err := s.repo.LockUser(ctx, userID); err != nil {
			return err
		}
		row, ok, err = s.repo.ActiveInviteOf(ctx, userID)
		if err != nil {
			return err
		}
		if ok {
			inv = row
			return nil
		}
		for attempt := 0; attempt < 5; attempt++ {
			code, err := newInviteCode()
			if err != nil {
				return errs.Wrap(errs.Internal, err)
			}
			row, inserted, err := s.repo.InsertOwnerInvite(ctx, userID, code)
			if err != nil {
				return err
			}
			if inserted {
				inv = row
				return nil
			}
			row, ok, err = s.repo.ActiveInviteOf(ctx, userID)
			if err != nil {
				return err
			}
			if ok {
				inv = row
				return nil
			}
		}
		return errs.Wrap(errs.Internal, errors.New("生成邀请码失败"))
	})
	if err != nil {
		return Invite{}, nil, err
	}

	rows, err := s.repo.Invitees(ctx, userID, inviteesLimit)
	if err != nil {
		return Invite{}, nil, err
	}
	invitees := make([]Invitee, 0, len(rows))
	for _, r := range rows {
		invitees = append(invitees, Invitee{
			Email:    maskEmail(r.Email),
			BoundAt:  r.BoundAt,
			RiskFlag: r.RiskFlag,
		})
	}
	return Invite{
		Code:      inv.Code,
		Invited:   inv.UsedCount,
		MaxUses:   inv.MaxUses,
		CreatedAt: inv.CreatedAt,
	}, invitees, nil
}

// MyInviteCodes 列出用户的全部个人邀请码（active 在前）与可同时持有的上限。
func (s *Service) MyInviteCodes(ctx context.Context, userID uuid.UUID) (MyInviteCodes, error) {
	cfg, err := s.settings.Signup(ctx)
	if err != nil {
		return MyInviteCodes{}, err
	}
	rows, err := s.repo.OwnerInvites(ctx, userID, inviteCodesListMax)
	if err != nil {
		return MyInviteCodes{}, err
	}
	codes := make([]InviteCodeView, 0, len(rows))
	for _, row := range rows {
		codes = append(codes, InviteCodeView{
			Code:      row.Code,
			Status:    row.Status,
			Invited:   row.UsedCount,
			Visits:    row.VisitCount,
			MaxUses:   row.MaxUses,
			CreatedAt: row.CreatedAt,
		})
	}
	return MyInviteCodes{
		Codes:    codes,
		MaxCodes: cfg.InviteMaxCodes,
	}, nil
}

// CreateInviteCode 门户用户再生成一个邀请码；active 的已到上限回 409。
func (s *Service) CreateInviteCode(ctx context.Context, userID uuid.UUID) (InviteCodeView, error) {
	cfg, err := s.settings.Signup(ctx)
	if err != nil {
		return InviteCodeView{}, err
	}
	var created model.InviteCode
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		u, ok, err := s.repo.LockUser(ctx, userID)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		if u.Status != StatusActive {
			return errs.Newf(errs.Conflict, "账号当前不可用")
		}
		n, err := s.repo.CountActiveOwnerInvites(ctx, userID)
		if err != nil {
			return err
		}
		if n >= int64(cfg.InviteMaxCodes) {
			return errs.Newf(errs.Conflict, fmt.Sprintf("最多同时持有 %d 个邀请码", cfg.InviteMaxCodes))
		}
		for attempt := 0; attempt < 5; attempt++ {
			code, err := newInviteCode()
			if err != nil {
				return errs.Wrap(errs.Internal, err)
			}
			row, inserted, err := s.repo.InsertOwnerInvite(ctx, userID, code)
			if err != nil {
				return err
			}
			if inserted {
				created = row
				return nil
			}
		}
		return errs.Wrap(errs.Internal, errors.New("生成邀请码失败"))
	})
	if err != nil {
		return InviteCodeView{}, err
	}
	return InviteCodeView{
		Code:      created.Code,
		Status:    created.Status,
		Invited:   created.UsedCount,
		Visits:    created.VisitCount,
		MaxUses:   created.MaxUses,
		CreatedAt: created.CreatedAt,
	}, nil
}

// RecordInviteVisit 记一次邀请链接访问：同一 IP 对同一个码 24 小时内只计一次；不存 IP 原文。
// 码不存在、不是 active、拿不到 IP、Redis 出错都静默返回 nil（访问量只是参考，不能挡住打开页面）。
func (s *Service) RecordInviteVisit(ctx context.Context, code string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) < 4 || len(code) > 32 {
		return nil
	}
	for i := 0; i < len(code); i++ {
		if !strings.ContainsRune(inviteCodeAlphabet, rune(code[i])) {
			return nil
		}
	}
	ip := reqmeta.From(ctx).IP
	if ip == "" || s.rdb == nil {
		return nil
	}
	sum := s.keys.HMAC(crypto.InfoHMACIdentity, []byte("iv:"+ip+"|"+code))
	key := "iv:" + hex.EncodeToString(sum[:16])
	first, err := s.rdb.SetNX(ctx, key, 1, inviteVisitWindow).Result()
	if err != nil {
		plog.From(ctx).Warn("记录邀请访问失败", zap.Error(err))
		return nil
	}
	if !first {
		return nil
	}
	if err := s.repo.IncInviteVisit(ctx, code); err != nil {
		plog.From(ctx).Warn("记录邀请访问失败", zap.Error(err))
		return nil
	}
	return nil
}

func newInviteCode() (string, error) {
	out := make([]byte, 8)
	buf := make([]byte, 1)
	for i := 0; i < 8; {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if buf[0] >= 248 {
			continue
		}
		out[i] = inviteCodeAlphabet[buf[0]%31]
		i++
	}
	return string(out), nil
}

func maskEmail(e string) string {
	at := strings.IndexByte(e, '@')
	if at <= 1 {
		return "***"
	}
	local := e[:at]
	domain := e[at:]
	n := utf8.RuneCountInString(local)
	if n <= 2 {
		r, _ := utf8.DecodeRuneInString(local)
		return string(r) + "***" + domain
	}
	var i int
	var prefix []rune
	for _, r := range local {
		if i >= 2 {
			break
		}
		prefix = append(prefix, r)
		i++
	}
	return string(prefix) + "***" + domain
}
