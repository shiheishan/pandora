package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

//------------------------------------------------------------------------------
// sing-box 订阅模板（w5retain）：规则集地址前缀与广告拦截开关
//------------------------------------------------------------------------------

// SingboxTemplate 是 sing-box 订阅附带的路由模板的可配项。前缀为空表示用订阅渲染的缺省值
// （SagerNet 公开发布的 rule-set 分支），由 subscription 包决定，这里不重复写一份地址。
type SingboxTemplate struct {
	// GeositeURLPrefix 来自 AEGIS_SINGBOX_GEOSITE_URL_PREFIX，后面拼 geosite-cn.srs 等文件名。
	GeositeURLPrefix string
	// GeoIPURLPrefix 来自 AEGIS_SINGBOX_GEOIP_URL_PREFIX，后面拼 geoip-cn.srs。
	GeoIPURLPrefix string
	// BlockAds 来自 AEGIS_SINGBOX_BLOCK_ADS（true / false，缺省 false）。
	BlockAds bool
}

func loadSingboxTemplate() (SingboxTemplate, error) {
	var t SingboxTemplate
	var err error
	if t.GeositeURLPrefix, err = ruleSetURLPrefix("AEGIS_SINGBOX_GEOSITE_URL_PREFIX"); err != nil {
		return t, err
	}
	if t.GeoIPURLPrefix, err = ruleSetURLPrefix("AEGIS_SINGBOX_GEOIP_URL_PREFIX"); err != nil {
		return t, err
	}
	if raw := strings.TrimSpace(env("AEGIS_SINGBOX_BLOCK_ADS", "")); raw != "" {
		if t.BlockAds, err = strconv.ParseBool(raw); err != nil {
			return t, fmt.Errorf("AEGIS_SINGBOX_BLOCK_ADS must be true or false")
		}
	}
	return t, nil
}

// ruleSetURLPrefix 读一个规则集地址前缀：只收 https、带主机、不带查询与片段、以 / 结尾
// （订阅客户端直接拿它拼文件名去下载，写错了整份配置起不来）。空值表示用缺省。
func ruleSetURLPrefix(name string) (string, error) {
	raw := strings.TrimSpace(env(name, ""))
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/") ||
		strings.ContainsAny(raw, "\"\\ \t\r\n") {
		return "", fmt.Errorf("%s must be an https:// URL prefix ending with / (no query or fragment)", name)
	}
	return raw, nil
}
