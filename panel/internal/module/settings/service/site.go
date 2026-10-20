package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// siteKeys 是站点设置这一组的全部键（读与加锁共用）。
var siteKeys = []string{
	keySiteName,
	keySiteTimezone,
	keySiteSEOKeywords,
	keySiteSEODescription,
	keySiteAllowIndexing,
}

// decodeSite 按缺省值解出站点设置。
func decodeSite(vals map[string]json.RawMessage) (Site, error) {
	name, err := decodeOr(vals, keySiteName, defaultSiteName)
	if err != nil {
		return Site{}, err
	}
	tz, err := decodeOr(vals, keySiteTimezone, defaultSiteTimezone)
	if err != nil {
		return Site{}, err
	}
	kw, err := decodeOr(vals, keySiteSEOKeywords, "")
	if err != nil {
		return Site{}, err
	}
	desc, err := decodeOr(vals, keySiteSEODescription, "")
	if err != nil {
		return Site{}, err
	}
	allow, err := decodeOr(vals, keySiteAllowIndexing, DefaultSiteAllowIndexing)
	if err != nil {
		return Site{}, err
	}
	return Site{
		Name:           name,
		Timezone:       tz,
		SEOKeywords:    kw,
		SEODescription: desc,
		AllowIndexing:  allow,
	}, nil
}

// Site 读取站点级设置。
func (s *Service) Site(ctx context.Context) (Site, error) {
	vals, err := s.store.Values(ctx, siteKeys)
	if err != nil {
		return Site{}, err
	}
	return decodeSite(vals)
}

// SiteUpdate 是站点设置的部分更新：nil 字段保持原值。
type SiteUpdate struct {
	Name           *string
	Timezone       *string
	SEOKeywords    *string
	SEODescription *string
	AllowIndexing  *bool
}

// SetSite 在锁住本组各行的事务里合并、校验并写入站点设置，并发改不同字段不会互相覆盖。
func (s *Service) SetSite(ctx context.Context, u SiteUpdate) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, siteKeys)
		if err != nil {
			return err
		}
		old, err := decodeSite(vals)
		if err != nil {
			return err
		}
		v := old
		if u.Name != nil {
			v.Name = strings.TrimSpace(*u.Name)
		}
		if u.Timezone != nil {
			v.Timezone = *u.Timezone
		}
		if u.SEOKeywords != nil {
			v.SEOKeywords = strings.TrimSpace(*u.SEOKeywords)
		}
		if u.SEODescription != nil {
			v.SEODescription = strings.TrimSpace(*u.SEODescription)
		}
		if u.AllowIndexing != nil {
			v.AllowIndexing = *u.AllowIndexing
		}
		if err := validateSite(v); err != nil {
			return err
		}

		before := map[string]any{
			keySiteName:           old.Name,
			keySiteTimezone:       old.Timezone,
			keySiteSEOKeywords:    old.SEOKeywords,
			keySiteSEODescription: old.SEODescription,
			keySiteAllowIndexing:  old.AllowIndexing,
		}
		after := map[string]any{
			keySiteName:           v.Name,
			keySiteTimezone:       v.Timezone,
			keySiteSEOKeywords:    v.SEOKeywords,
			keySiteSEODescription: v.SEODescription,
			keySiteAllowIndexing:  v.AllowIndexing,
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

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
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
	if utf8.RuneCountInString(v.SEOKeywords) > SiteSEOKeywordsMax {
		fields["seo_keywords"] = "关键词最多 200 个字"
	} else if hasControl(v.SEOKeywords) {
		fields["seo_keywords"] = "关键词不能包含换行或控制字符"
	}
	if utf8.RuneCountInString(v.SEODescription) > SiteSEODescriptionMax {
		fields["seo_description"] = "站点描述最多 160 个字"
	} else if hasControl(v.SEODescription) {
		fields["seo_description"] = "站点描述不能包含换行或控制字符"
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
