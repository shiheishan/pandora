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
	keySiteURL,
}

// siteLockKeys 是改站点设置时要锁的行：本组各键，外加改主域名时一起写的 site.url_previous 与 site.url_previous_until；按 key 顺序加锁。
var siteLockKeys = append(append([]string{}, siteKeys...), keySiteURLPrevious, keySiteURLPrevUntil)

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
	// 主域名照存的原样给（写入时已规整）；链接用的生效值另经 SiteURL 读，那里会把手工改坏的值按空
	siteURL, err := decodeOr(vals, keySiteURL, "")
	if err != nil {
		return Site{}, err
	}
	return Site{
		Name:           name,
		Timezone:       tz,
		SEOKeywords:    kw,
		SEODescription: desc,
		AllowIndexing:  allow,
		URL:            siteURL,
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
	// URL 是主域名：空串 = 恢复用配置的 public_base_url。见 site_url.go
	URL *string
}

// SetSite 在锁住本组各行的事务里合并、校验并写入站点设置，并发改不同字段不会互相覆盖。
// 改主域名时，校验与连通核对在事务外先做（不在事务里联网）；改动另写一条 site.url_changed 审计（新旧值，域名不是秘密），
// 同时把改之前的生效值记进 site.url_previous；提交后失效主域名快照并通知监听方。
func (s *Service) SetSite(ctx context.Context, u SiteUpdate) error {
	var newURL *string
	if u.URL != nil {
		norm, err := s.prepareSiteURL(ctx, *u.URL)
		if err != nil {
			return err
		}
		newURL = &norm
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, siteLockKeys)
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
		urlChanged := newURL != nil && *newURL != old.URL
		if len(changes) == 0 && !urlChanged {
			return nil
		}
		// 先落库再登记提交后的动作：没有事务时 AfterCommit 立即执行，监听方要读到新值
		otherChanged := len(changes) > 0
		prevEffective := strings.TrimRight(s.effectiveSiteURL(old.URL), "/")
		if urlChanged {
			changes = append(changes,
				change{key: keySiteURL, value: *newURL},
				change{key: keySiteURLPrevious, value: prevEffective},
				change{key: keySiteURLPrevUntil, value: s.now().Add(siteURLPreviousFor).UTC()},
			)
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		if otherChanged {
			if err := s.writeAudit(ctx, auditSiteChanged, auditResourceSettings, map[string]any{
				"group":  "site",
				"before": b,
				"after":  a,
			}); err != nil {
				return err
			}
		}
		if urlChanged {
			if err := s.writeAudit(ctx, auditSiteURLChanged, auditResourceSettings, map[string]any{
				"group":            "site",
				"before":           old.URL,
				"after":            *newURL,
				"effective_before": prevEffective,
				"effective_after":  strings.TrimRight(s.effectiveSiteURL(*newURL), "/"),
			}); err != nil {
				return err
			}
			s.siteURLChanged(ctx)
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
