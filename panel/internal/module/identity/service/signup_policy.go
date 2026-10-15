package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
)

// checkEmailPolicy 按注册设置检查邮箱：后缀白名单、Gmail 别名限制（#12 #52）。email 已规整成小写。
func checkEmailPolicy(cfg settingssvc.Signup, email string) error {
	at := strings.LastIndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		return nil
	}
	domain := email[at+1:]
	local := email[:at]

	if cfg.EmailSuffixWhitelistEnabled {
		allowed := false
		for _, suf := range cfg.EmailSuffixes {
			if strings.EqualFold(suf, domain) {
				allowed = true
				break
			}
		}
		if !allowed {
			msg := "暂不支持这个邮箱后缀，可以用：" + formatSuffixHint(cfg.EmailSuffixes)
			return errs.Invalid(map[string]string{"email": msg})
		}
	}

	if cfg.GmailAliasBlock {
		d := strings.ToLower(domain)
		if d == "gmail.com" || d == "googlemail.com" {
			if strings.Contains(local, ".") || strings.Contains(local, "+") {
				return errs.Invalid(map[string]string{"email": "不支持 Gmail 别名地址，请去掉邮箱名里的点和加号"})
			}
		}
	}
	return nil
}

func formatSuffixHint(suffixes []string) string {
	if len(suffixes) == 0 {
		return ""
	}
	n := len(suffixes)
	if n > 10 {
		n = 10
	}
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = suffixes[i]
	}
	hint := strings.Join(parts, "、")
	if len(suffixes) > 10 {
		hint += "等"
	}
	return hint
}

// limitRegisterIP 注册第 1 步按来源 IP 限额（#53，后台可调，缺省每 IP 每分钟 10 次）；没开或拿不到 IP 时不限。
func (s *Service) limitRegisterIP(ctx context.Context, cfg settingssvc.Signup) error {
	if !cfg.IPLimitEnabled {
		return nil
	}
	ip := reqmeta.From(ctx).IP
	if ip == "" {
		return nil
	}
	rule := ratelimit.PerWindow(
		fmt.Sprintf("reg_ip_%d_%d", cfg.IPLimitCount, cfg.IPLimitWindowMinutes),
		cfg.IPLimitCount,
		time.Duration(cfg.IPLimitWindowMinutes)*time.Minute,
	).StrictMode()
	return s.limit(ctx, rule, ip)
}
