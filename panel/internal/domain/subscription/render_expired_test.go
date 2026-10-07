package subscription

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestExpiredNoticeUsesLocationToTheMinute(t *testing.T) {
	end := time.Date(2026, 10, 7, 6, 32, 59, 0, time.UTC)
	shanghai := time.FixedZone("CST", 8*3600)
	if got := ExpiredNotice(end, shanghai); got != "已于 2026-10-07 14:32 到期，续费后更新订阅即可恢复" {
		t.Fatalf("notice=%q", got)
	}
	if got := ExpiredNotice(end, nil); !strings.Contains(got, "2026-10-07 06:32") {
		t.Fatalf("nil location must fall back to UTC: %q", got)
	}
}

// 过期提示的三种格式：只有一条提示节点、指向本机占位地址，组里只有它。
// 真实客户端内核的离线校验（sing-box check、mihomo YAML 解析）见 subscription-e2e skill，
// 交付报告里贴了结果；这里钉结构。
func TestRenderExpiredHasOnlyTheNotice(t *testing.T) {
	notice := ExpiredNotice(time.Date(2026, 10, 7, 6, 32, 0, 0, time.UTC), time.UTC)

	for _, f := range []Format{FormatClash, FormatClashPremium} {
		body, ct := RenderExpired(f, notice)
		y := string(body)
		if ct != "text/yaml; charset=utf-8" || strings.Count(y, "\n  - {") != 2 ||
			!strings.Contains(y, `name: "`+notice+`"`) || !strings.Contains(y, `type: "socks5"`) ||
			!strings.Contains(y, `server: "127.0.0.1"`) ||
			!strings.Contains(y, `{name: 节点选择, type: select, proxies: ["`+notice+`"]}`) ||
			!strings.Contains(y, "MATCH,节点选择") {
			t.Fatalf("%s expired config:\n%s", f, y)
		}
	}

	body, ct := RenderExpired(FormatSingbox, notice)
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
		Route     struct {
			Final string `json:"final"`
		} `json:"route"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil || ct != "application/json; charset=utf-8" {
		t.Fatalf("sing-box expired config err=%v ct=%s\n%s", err, ct, body)
	}
	if len(cfg.Outbounds) != 2 || cfg.Outbounds[0]["type"] != "selector" ||
		cfg.Outbounds[1]["type"] != "socks" || cfg.Outbounds[1]["tag"] != notice ||
		cfg.Outbounds[1]["server"] != "127.0.0.1" || cfg.Route.Final != "节点选择" {
		t.Fatalf("sing-box expired outbounds=%+v final=%s", cfg.Outbounds, cfg.Route.Final)
	}
	if members, _ := cfg.Outbounds[0]["outbounds"].([]any); len(members) != 1 || members[0] != notice {
		t.Fatalf("selector must hold only the notice: %+v", cfg.Outbounds[0])
	}

	body, ct = RenderExpired(FormatURI, notice)
	raw, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil || ct != "text/plain; charset=utf-8" {
		t.Fatalf("uri body err=%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	u, err := url.Parse(lines[0])
	if len(lines) != 1 || err != nil || u.Scheme != "ss" || u.Hostname() != "127.0.0.1" || u.Fragment != notice {
		t.Fatalf("uri lines=%q err=%v", lines, err)
	}
}
