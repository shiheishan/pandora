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
	keys := []string{keySiteName, keySiteTimezone, keySiteCurrency}
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
	cur, err := decodeOr(vals, keySiteCurrency, defaultSiteCurrency)
	if err != nil {
		return Site{}, err
	}
	return Site{Name: name, Timezone: tz, Currency: cur}, nil
}

// SetSite 写入站点级设置。
func (s *Service) SetSite(ctx context.Context, v Site) error {
	v.Name = strings.TrimSpace(v.Name)
	fields := map[string]string{}
	if v.Name == "" {
		fields["name"] = "站点名必填"
	} else if utf8.RuneCountInString(v.Name) > 40 {
		fields["name"] = "站点名最多 40 个字"
	}
	if !validTimezone(v.Timezone) {
		fields["timezone"] = "不是有效的时区"
	}
	if len(v.Currency) != 3 {
		fields["currency"] = "币种须为三位大写字母代码"
	} else {
		for _, r := range v.Currency {
			if r < 'A' || r > 'Z' {
				fields["currency"] = "币种须为三位大写字母代码"
				break
			}
		}
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		keys := []string{keySiteName, keySiteTimezone, keySiteCurrency}
		vals, err := s.store.LockValues(ctx, keys)
		if err != nil {
			return err
		}
		oldName, err := decodeOr(vals, keySiteName, defaultSiteName)
		if err != nil {
			return err
		}
		oldTZ, err := decodeOr(vals, keySiteTimezone, defaultSiteTimezone)
		if err != nil {
			return err
		}
		oldCur, err := decodeOr(vals, keySiteCurrency, defaultSiteCurrency)
		if err != nil {
			return err
		}
		before := map[string]any{
			keySiteName:     oldName,
			keySiteTimezone: oldTZ,
			keySiteCurrency: oldCur,
		}
		after := map[string]any{
			keySiteName:     v.Name,
			keySiteTimezone: v.Timezone,
			keySiteCurrency: v.Currency,
		}
		changes, b, a := diff(before, after)
		if len(changes) == 0 {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		return s.writeAudit(ctx, auditSiteChanged, auditResourceSettings, map[string]any{
			"group":  "site",
			"before": b,
			"after":  a,
		})
	})
}

func validTimezone(name string) bool {
	if name == "" || name == "Local" || len(name) > 64 {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}
