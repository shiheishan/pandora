package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// Site 读取站点级设置。
func (s *Service) Site(ctx context.Context) (Site, error) {
	keys := []string{keySiteName, keySiteTimezone}
	vals, err := s.store.Values(ctx, keys)
	if err != nil {
		return Site{}, err
	}
	name, err := decodeOr(vals, keySiteName, defaultSiteName)
	if err != nil {
		return Site{}, err
	}
	tz, err := decodeOr(vals, keySiteTimezone, defaultSiteTimezone)
	if err != nil {
		return Site{}, err
	}
	return Site{Name: name, Timezone: tz}, nil
}

// SiteUpdate 是站点设置的部分更新：nil 字段保持原值。
type SiteUpdate struct {
	Name     *string
	Timezone *string
}

// SetSite 在锁住两行的事务里合并、校验并写入站点设置，并发改不同字段不会互相覆盖。
func (s *Service) SetSite(ctx context.Context, u SiteUpdate) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		keys := []string{keySiteName, keySiteTimezone}
		vals, err := s.store.LockValues(ctx, keys)
		if err != nil {
			return err
		}
		var old Site
		if old.Name, err = decodeOr(vals, keySiteName, defaultSiteName); err != nil {
			return err
		}
		if old.Timezone, err = decodeOr(vals, keySiteTimezone, defaultSiteTimezone); err != nil {
			return err
		}
		v := old
		if u.Name != nil {
			v.Name = strings.TrimSpace(*u.Name)
		}
		if u.Timezone != nil {
			v.Timezone = *u.Timezone
		}
		if err := validateSite(v); err != nil {
			return err
		}

		before := map[string]any{
			keySiteName:     old.Name,
			keySiteTimezone: old.Timezone,
		}
		after := map[string]any{
			keySiteName:     v.Name,
			keySiteTimezone: v.Timezone,
		}
		changes, b, a := diff(before, after)
		if len(changes) == 0 {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		if err := s.writeAudit(ctx, auditSiteChanged, auditResourceSettings, map[string]any{
			"group":  "site",
			"before": b,
			"after":  a,
		}); err != nil {
			return err
		}
		s.siteChanged(ctx)
		return nil
	})
}

func validateSite(v Site) error {
	fields := map[string]string{}
	if v.Name == "" {
		fields["name"] = "站点名必填"
	} else if utf8.RuneCountInString(v.Name) > 40 {
		fields["name"] = "站点名最多 40 个字"
	}
	if !validTimezone(v.Timezone) {
		fields["timezone"] = "不是有效的时区"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

func validTimezone(name string) bool {
	if name == "" || name == "Local" || len(name) > 64 {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}
