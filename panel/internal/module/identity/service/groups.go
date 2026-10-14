package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/identity/model"
	"github.com/aegispanel/aegis/internal/module/identity/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

var groupCodeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ListGroups 列出全部用户组（含组内用户数），按建组时间。
func (s *Service) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.repo.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Group, 0, len(rows))
	for _, r := range rows {
		desc := ""
		if r.Description != nil {
			desc = *r.Description
		}
		out = append(out, Group{
			ID:          r.ID,
			Code:        r.Code,
			Name:        r.Name,
			Description: desc,
			Users:       r.Users,
		})
	}
	return out, nil
}

// AdminListGroups 是后台用户组列表：ListGroups 再补引用计数与把组列入仅限名单的节点池（经 OpsGroupUsage，一次调用）。
func (s *Service) AdminListGroups(ctx context.Context) ([]Group, error) {
	out, err := s.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	gu, err := s.needGroupUsage()
	if err != nil {
		return nil, err
	}
	usage, err := gu.GroupUsage(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		u, ok := usage[out[i].ID]
		if !ok {
			out[i].ExclusivePools = []PoolRef{}
			continue
		}
		out[i].Plans = u.Plans
		out[i].Prices = u.Prices
		out[i].Coupons = u.Coupons
		if u.ExclusivePools == nil {
			out[i].ExclusivePools = []PoolRef{}
		} else {
			out[i].ExclusivePools = u.ExclusivePools
		}
	}
	return out, nil
}

// CreateGroup 新建用户组；code 为空时由名称生成；code 重复回 409。
func (s *Service) CreateGroup(ctx context.Context, code, name, description string) (uuid.UUID, error) {
	code, name, descPtr, err := validateGroup(code, name, description)
	if err != nil {
		return uuid.Nil, err
	}
	g := model.UserGroup{Code: code, Name: name, Description: descPtr}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.InsertGroup(ctx, &g); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.Newf(errs.Conflict, "这个分组标识已存在")
			}
			return err
		}
		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionUserGroupSaved,
			ResourceType: ResourceUserGroup,
			ResourceID:   &g.ID,
			Data:         map[string]any{"code": code, "name": name},
		})
	})
	if err != nil {
		return uuid.Nil, err
	}
	return g.ID, nil
}

// UpdateGroup 改用户组的名称与说明（code 不可改）；不存在回 404。
func (s *Service) UpdateGroup(ctx context.Context, id uuid.UUID, name, description string) error {
	name, descPtr, err := validateGroupNameDesc(name, description)
	if err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		ok, err := s.repo.UpdateGroup(ctx, id, name, descPtr)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionUserGroupSaved,
			ResourceType: ResourceUserGroup,
			ResourceID:   &id,
			Data:         map[string]any{"name": name},
		})
	})
}

// DeleteGroup 删除一个没有用户、也没被别处引用的用户组；不存在回 404。
func (s *Service) DeleteGroup(ctx context.Context, id uuid.UUID) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		ok, err := s.repo.LockGroup(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		n, err := s.repo.GroupUserCount(ctx, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return errs.Newf(errs.Conflict, "这个分组下还有用户，先把他们移出去")
		}
		ok, err = s.repo.DeleteGroup(ctx, id)
		if err != nil {
			if errors.Is(err, repo.ErrForeignKey) {
				return errs.Newf(errs.Conflict, "这个分组还被价格、优惠券或节点池引用，先解除")
			}
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionUserGroupDeleted,
			ResourceType: ResourceUserGroup,
			ResourceID:   &id,
		})
	})
}

func validateGroup(code, name, description string) (string, string, *string, error) {
	name, descPtr, err := validateGroupNameDesc(name, description)
	if err != nil {
		return "", "", nil, err
	}
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		code = groupSlug(name)
	}
	if !groupCodeRe.MatchString(code) {
		return "", "", nil, errs.Invalid(map[string]string{"code": "标识只能用小写字母、数字、- 和 _，最多 64 位"})
	}
	return code, name, descPtr, nil
}

func validateGroupNameDesc(name, description string) (string, *string, error) {
	name, ok := cleanText(name)
	if !ok || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 64 {
		return "", nil, errs.Invalid(map[string]string{"name": "分组名必填，最多 64 字"})
	}
	desc, ok := cleanText(description)
	if !ok || utf8.RuneCountInString(desc) > 500 {
		return "", nil, errs.Invalid(map[string]string{"description": "说明最多 500 字"})
	}
	var descPtr *string
	if desc != "" {
		descPtr = &desc
	}
	return name, descPtr, nil
}

func groupSlug(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == ' ' || c == '-' || c == '_':
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		buf := make([]byte, 4)
		if _, err := rand.Read(buf); err != nil {
			return "group"
		}
		s = "group-" + hex.EncodeToString(buf)
	}
	if len(s) > 64 {
		s = strings.TrimRight(s[:64], "-")
	}
	return s
}
