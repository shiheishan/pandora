package service

import (
	"context"
	"net/url"
	"strings"
)

// 站点对外根地址：入口页 canonical、og:url 用。全仓读它只经 SiteURL，以后「主域名」进后台设置时只改这里。

// UsePublicBaseURL 注入配置里的 public_base_url（启动装配时调用，不并发安全）。是配置值，不是端口，不进 MissingPorts。
func (s *Service) UsePublicBaseURL(u string) { s.publicBaseURL = u }

// SiteURL 返回站点对外根地址，规整成以 / 结尾（如 https://panel.example.test/）；没配或不是 http(s) 绝对地址时为空。
func (s *Service) SiteURL(_ context.Context) string {
	u := strings.TrimSpace(s.publicBaseURL)
	if u == "" {
		return ""
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	if parsed.Host == "" {
		return ""
	}
	path := strings.TrimRight(parsed.Path, "/")
	return scheme + "://" + parsed.Host + path + "/"
}
