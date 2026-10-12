package service

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/module/identity/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
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
