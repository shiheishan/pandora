package service

import (
	"strings"
	"testing"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

func TestParseBotCommand(t *testing.T) {
	cases := []struct {
		input   string
		wantCmd string
		wantArg string
	}{
		{"/traffic", "traffic", ""},
		{"  /Traffic@my_bot  ", "traffic", ""},
		{"/start ABCD2345", "start", "ABCD2345"},
		{"/sub@bot foo bar", "sub", "foo bar"},
		{"普通文本", "", "普通文本"},
		{"   hello world   ", "", "hello world"},
		{"/UNKNOWN", "unknown", ""},
	}

	for _, tc := range cases {
		cmd, arg := parseBotCommand(tc.input)
		if cmd != tc.wantCmd || arg != tc.wantArg {
			t.Errorf("parseBotCommand(%q) = (%q, %q), want (%q, %q)", tc.input, cmd, arg, tc.wantCmd, tc.wantArg)
		}
	}
}

func TestStartText(t *testing.T) {
	ex := settingssvc.TelegramExtras{}
	if got := startText(ex); got != tgDefaultStart {
		t.Fatalf("want default %q, got %q", tgDefaultStart, got)
	}

	ex.StartMessage = "欢迎来到测试站点！"
	if got := startText(ex); got != "欢迎来到测试站点！" {
		t.Fatalf("want custom start text, got %q", got)
	}
}

func TestHelpText(t *testing.T) {
	ex := settingssvc.TelegramExtras{SubCommand: false}
	h1 := helpText(ex)
	if strings.Contains(h1, "/sub") {
		t.Fatalf("expected helpText without /sub, got %s", h1)
	}
	if !strings.Contains(h1, "/traffic") || !strings.Contains(h1, "/unbind") {
		t.Fatalf("expected /traffic and /unbind, got %s", h1)
	}

	ex.SubCommand = true
	h2 := helpText(ex)
	if !strings.Contains(h2, "/sub") {
		t.Fatalf("expected helpText with /sub, got %s", h2)
	}
}

func TestFormatTrafficReply(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, loc)

	// 空订阅
	if got := formatTrafficReply(nil, loc); got != tgNoLiveSub {
		t.Fatalf("want tgNoLiveSub, got %q", got)
	}

	// 1. 不限量（Limit 为 nil）
	label := "办公专用"
	subUnlimited := subsvc.Summary{
		Subscription: subsvc.Subscription{
			PeriodEnd: now.Add(24 * time.Hour),
			Label:     &label,
		},
		PlanName: "无限套餐",
		Quotas: []subsvc.Quota{
			{
				Metric:   subsvc.MetricTraffic,
				Limit:    nil,
				Consumed: 10 * 1024 * 1024 * 1024,
			},
		},
	}
	res1 := formatTrafficReply([]subsvc.Summary{subUnlimited}, loc)
	if !strings.Contains(res1, "「无限套餐」 · 办公专用") {
		t.Errorf("title mismatch in: %s", res1)
	}
	if !strings.Contains(res1, "流量：已用 10.0 GB（不限量）") {
		t.Errorf("unlimited traffic format mismatch in: %s", res1)
	}
	if !strings.Contains(res1, "到期：2026-10-15 12:00") {
		t.Errorf("expiry mismatch in: %s", res1)
	}

	// 2. 有配额 + 本期赠送 10 GB：剩余 = 100 + 10 − 40，共 = 已用 + 剩余
	limit := int64(100 * 1024 * 1024 * 1024)
	rem := int64(70 * 1024 * 1024 * 1024)
	subWithBonus := subsvc.Summary{
		Subscription: subsvc.Subscription{
			PeriodEnd: now.Add(48 * time.Hour),
		},
		PlanName: "主力套餐",
		Quotas: []subsvc.Quota{
			{
				Metric:    subsvc.MetricTraffic,
				Limit:     &limit,
				Consumed:  40 * 1024 * 1024 * 1024,
				Remaining: &rem,
			},
		},
	}
	res2 := formatTrafficReply([]subsvc.Summary{subWithBonus}, loc)
	if !strings.Contains(res2, "「主力套餐」") {
		t.Errorf("title mismatch in: %s", res2)
	}
	if !strings.Contains(res2, "流量：已用 40.0 GB / 共 110.0 GB，剩余 70.0 GB") {
		t.Errorf("traffic format mismatch in: %s", res2)
	}
	if !strings.Contains(res2, "到期：2026-10-16 12:00") {
		t.Errorf("expiry mismatch in: %s", res2)
	}

	// 3. 两份合并（空行隔开）
	res3 := formatTrafficReply([]subsvc.Summary{subUnlimited, subWithBonus}, loc)
	blocks := strings.Split(res3, "\n\n")
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks separated by double newline, got %d:\n%s", len(blocks), res3)
	}
}
