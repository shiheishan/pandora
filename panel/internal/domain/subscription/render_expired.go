package subscription

// 过期订阅的提示配置（用户 2026-10-07 规则 1）。
//
// 令牌有效、订阅已过期时，订阅里不再给任何真实节点，只给一条提示：名字就是
// 「已于 X 到期，续费后更新订阅即可恢复」。客户端照常导入成功，节点列表里只剩这一条，
// 用户一眼看到原因，而不是对着伪装 404 以为站点挂了。续费后客户端下次更新订阅
// （响应头把更新间隔压到 1 小时），真实节点原样回来，链接不变。
//
// 三种格式都必须是客户端能加载的合法配置：
//
//	Clash      一个指向 127.0.0.1 的 socks5 占位代理，代理组只含它，规则全部走这个组
//	sing-box   一个指向 127.0.0.1 的 socks 占位出站，外加一个只含它的 selector：图形客户端
//	           只在「组」里显示出站名，没有组用户看不到这句提示；route.final 指向它
//	URI        一条 ss:// 分享链接，提示写在 remark（#）里
//
// 占位地址是本机 1 号端口：连上去立即被拒，不会把流量送到任何地方。
// 这里不碰 render_singbox.go 的模板（w5retain 在改），自己出一份最小配置。

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	expiredPlaceholderHost = "127.0.0.1"
	expiredPlaceholderPort = 1
	expiredGroupName       = "节点选择"
)

// ExpiredNotice 是提示节点的名字：「已于 2026-10-07 14:32 到期，续费后更新订阅即可恢复」。
// 时刻按 loc（用户时区，未设跟随站点时区）显示到分钟。
func ExpiredNotice(periodEnd time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return "已于 " + periodEnd.In(loc).Format("2006-01-02 15:04") + " 到期，续费后更新订阅即可恢复"
}

// RenderExpired 按格式渲染只含一条提示节点的订阅。返回内容与 Content-Type。
func RenderExpired(f Format, notice string) ([]byte, string) {
	switch f {
	case FormatClash, FormatClashPremium:
		return renderExpiredClash(notice), "text/yaml; charset=utf-8"
	case FormatSingbox:
		return renderExpiredSingbox(notice), "application/json; charset=utf-8"
	default:
		return renderExpiredURI(notice), "text/plain; charset=utf-8"
	}
}

func renderExpiredClash(notice string) []byte {
	var b strings.Builder
	b.WriteString("mixed-port: 7890\nallow-lan: false\nmode: rule\nlog-level: info\n\n")
	b.WriteString("proxies:\n")
	// socks5 是 Meta 与 Premium 两系内核都认的类型
	b.WriteString("  - " + inlineYAML(map[string]any{
		"name": notice, "type": "socks5",
		"server": expiredPlaceholderHost, "port": expiredPlaceholderPort, "udp": false,
	}) + "\n")
	b.WriteString("\nproxy-groups:\n")
	b.WriteString("  - {name: " + expiredGroupName + ", type: select, proxies: [" +
		strconv.Quote(notice) + "]}\n")
	b.WriteString("\nrules:\n  - MATCH," + expiredGroupName + "\n")
	return []byte(b.String())
}

func renderExpiredSingbox(notice string) []byte {
	cfg := map[string]any{
		"log": map[string]any{"level": "warn"},
		"outbounds": []map[string]any{
			{"type": "selector", "tag": expiredGroupName, "outbounds": []string{notice}},
			{"type": "socks", "tag": notice, "server": expiredPlaceholderHost,
				"server_port": expiredPlaceholderPort, "version": "5"},
		},
		"route": map[string]any{"final": expiredGroupName},
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return []byte("{}")
	}
	return body
}

func renderExpiredURI(notice string) []byte {
	// SIP002：userinfo 是 method:password 的 URL 安全 base64（不补等号）
	userinfo := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:expired"))
	line := "ss://" + userinfo + "@" + expiredPlaceholderHost + ":" +
		strconv.Itoa(expiredPlaceholderPort) + "#" + url.PathEscape(notice)
	return []byte(base64.StdEncoding.EncodeToString([]byte(line)))
}
