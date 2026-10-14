package service

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestSubjectFromBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"首行非空", "第一行标题\n第二行", "第一行标题"},
		{"跳过空行", "\n\n  有效的行  ", "有效的行"},
		{"中文截断", strings.Repeat("测", 130), strings.Repeat("测", 120)},
		{"emoji按rune截断", "🎉" + strings.Repeat("a", 130), "🎉" + strings.Repeat("a", 119)},
		{"少于4字加前缀", "ab", "用户咨询：ab"},
		{"全空", "\n  \n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := subjectFromBody(c.body); got != c.want {
				t.Fatalf("subjectFromBody = %q，期望 %q", got, c.want)
			}
		})
	}
}

func TestNormalizeCreate(t *testing.T) {
	longBody := strings.Repeat("正", 10)
	in := CreateInput{Body: longBody}
	if err := normalizeCreate(&in); err != nil {
		t.Fatal(err)
	}
	if in.Subject == "" || in.Category != defaultCode {
		t.Fatalf("空标题/分类应补默认，得到 subject=%q category=%q", in.Subject, in.Category)
	}

	short := CreateInput{Subject: "标题够长", Body: strings.Repeat("短", 9)}
	err := normalizeCreate(&short)
	mustValidationField(t, err, "body")

	badCat := CreateInput{Subject: "标题够长", Body: longBody, Category: "nope"}
	err = normalizeCreate(&badCat)
	mustValidationField(t, err, "category")

	nul := CreateInput{Subject: "标题够长", Body: "十个字正文哈\x00"}
	err = normalizeCreate(&nul)
	mustValidationField(t, err, "body")
}

func TestNormalizeBodyAndReasonAndMacro(t *testing.T) {
	if _, err := normalizeBody(""); err == nil {
		t.Fatal("空正文应 422")
	}
	if _, err := normalizeBody("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeBody(strings.Repeat("b", bodyMax)); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeBody(strings.Repeat("b", bodyMax+1)); err == nil {
		t.Fatal("超上限应 422")
	}
	if _, err := normalizeBody("a\x00"); err == nil {
		t.Fatal("NUL 应 422")
	}

	if _, err := normalizeReason("reason", strings.Repeat("r", reasonMax)); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeReason("reason", strings.Repeat("r", reasonMax+1)); err == nil {
		t.Fatal("reason 超 500 应 422")
	}
	if _, err := normalizeReason("reason", "ok\x00"); err == nil {
		t.Fatal("reason NUL 应 422")
	}

	ok, err := normalizeMacro(MacroInput{Title: "标题", Body: "正文"})
	if err != nil {
		t.Fatal(err)
	}
	if ok.Title != "标题" {
		t.Fatal("应 trim")
	}
	_, err = normalizeMacro(MacroInput{Title: strings.Repeat("t", macroTitle+1), Body: "正文"})
	mustValidationField(t, err, "title")
	_, err = normalizeMacro(MacroInput{Title: "标题", Body: ""})
	mustValidationField(t, err, "body")
}

func TestPriorityForAndSLA(t *testing.T) {
	if priorityFor("billing") != PriorityHigh || priorityFor("account") != PriorityHigh {
		t.Fatal("billing/account 应为 high")
	}
	if priorityFor("abuse") != PriorityUrgent {
		t.Fatal("abuse 应为 urgent")
	}
	if priorityFor("general") != PriorityNormal {
		t.Fatal("其它应为 normal")
	}

	from := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	first, res := slaDue(PriorityUrgent, from)
	if !first.Equal(from.Add(1*time.Hour)) || !res.Equal(from.Add(8*time.Hour)) {
		t.Fatal("urgent SLA 不对")
	}
	first, res = slaDue(PriorityHigh, from)
	if !first.Equal(from.Add(4*time.Hour)) || !res.Equal(from.Add(24*time.Hour)) {
		t.Fatal("high SLA 不对")
	}
	first, res = slaDue(PriorityNormal, from)
	if !first.Equal(from.Add(12*time.Hour)) || !res.Equal(from.Add(72*time.Hour)) {
		t.Fatal("normal SLA 不对")
	}
	first, res = slaDue(PriorityLow, from)
	if !first.Equal(from.Add(24*time.Hour)) || !res.Equal(from.Add(168*time.Hour)) {
		t.Fatal("low SLA 不对")
	}
	first, res = slaDue("unknown", from)
	if !first.Before(res) {
		t.Fatal("首次响应应早于解决时限")
	}
	if !first.Equal(from.Add(12 * time.Hour)) {
		t.Fatal("未知优先级应按 normal")
	}
}

func TestEscalatedPriority(t *testing.T) {
	if escalatedPriority(PriorityLow) != PriorityHigh || escalatedPriority(PriorityNormal) != PriorityHigh {
		t.Fatal("low/normal 升级应为 high")
	}
	if escalatedPriority(PriorityUrgent) != PriorityUrgent {
		t.Fatal("urgent 不应降")
	}
}

func TestNewTicketNo(t *testing.T) {
	re := regexp.MustCompile(`^TK[0-9]{8}-[A-Z2-7]{8}$`)
	when := time.Date(2026, 10, 13, 15, 0, 0, 0, time.FixedZone("CST", 8*3600))
	no, err := newTicketNo(when)
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString(no) {
		t.Fatalf("工单号 %q 格式不对", no)
	}
	if !strings.HasPrefix(no, "TK20261013-") {
		t.Fatalf("日期应为 UTC 20261013，得到 %q", no)
	}
}

func TestCanTransition(t *testing.T) {
	if !CanTransition(StatusClosed, StatusPendingUser) {
		t.Fatal("closed → pending_user 应允许")
	}
	if CanTransition("bogus", StatusOpen) {
		t.Fatal("未知 from 应拒绝")
	}
}

func TestCategoriesCopy(t *testing.T) {
	var s Service
	first := s.Categories()
	if len(first) != 6 {
		t.Fatalf("应有 6 个分类，得到 %d", len(first))
	}
	want := []string{"general", "technical", "subscription", "billing", "account", "abuse"}
	for i, code := range want {
		if first[i].Code != code {
			t.Fatalf("顺序[%d]=%q 期望 %q", i, first[i].Code, code)
		}
	}
	first[0].Code = "mutated"
	second := s.Categories()
	if second[0].Code != "general" {
		t.Fatal("返回切片不应影响下次调用")
	}
}

func TestAgentStatusCols(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	resolved := now.Add(-time.Hour)

	t.Run("open→closed", func(t *testing.T) {
		cols := agentStatusCols(StatusOpen, StatusClosed, &resolved, nil, nil, nil, nil, PriorityNormal, now)
		if cols["status"] != StatusClosed {
			t.Fatal(cols)
		}
		if cols["closed_reason"] != ClosedByAgent {
			t.Fatal("closed_reason 应为 agent_closed")
		}
		if !mapValIsNil(cols, "closed_note") {
			t.Fatal("closed_note 应为 nil")
		}
		if cols["closed_at"] != now {
			t.Fatal("closed_at 应为 now")
		}
		if p, _ := cols["resolved_at"].(*time.Time); p == nil || !p.Equal(resolved) {
			t.Fatal("resolved_at 应保留传入值")
		}
	})

	t.Run("closed保留原因", func(t *testing.T) {
		closedAt := now.Add(-2 * time.Hour)
		reason := ClosedWithdrawn
		note := "x"
		cols := agentStatusCols(StatusClosed, StatusClosed, nil, &closedAt, nil, &reason, &note, PriorityNormal, now)
		if n, _ := cols["closed_note"].(*string); cols["closed_reason"] != ClosedWithdrawn || n == nil || *n != "x" {
			t.Fatal(cols)
		}
		if cols["closed_at"] != closedAt {
			t.Fatal("closed_at 应保留原值")
		}
	})

	t.Run("closed→resolved", func(t *testing.T) {
		cols := agentStatusCols(StatusClosed, StatusResolved, nil, nil, nil, nil, nil, PriorityNormal, now)
		if cols["resolved_at"] != now {
			t.Fatal("无 resolved_at 时应写 now")
		}
		if !mapValIsNil(cols, "closed_at") || !mapValIsNil(cols, "closed_reason") || !mapValIsNil(cols, "closed_note") {
			t.Fatal("应清空 closed 相关列")
		}
	})

	t.Run("normal→escalated", func(t *testing.T) {
		cols := agentStatusCols(StatusOpen, StatusEscalated, nil, nil, nil, nil, nil, PriorityNormal, now)
		if cols["priority"] != PriorityHigh || cols["escalated_at"] != now {
			t.Fatal(cols)
		}
		cols = agentStatusCols(StatusOpen, StatusEscalated, nil, nil, nil, nil, nil, PriorityUrgent, now)
		if cols["priority"] != PriorityUrgent {
			t.Fatal("urgent 应保持")
		}
	})

	t.Run("→pending_agent", func(t *testing.T) {
		cols := agentStatusCols(StatusOpen, StatusPendingAgent, nil, nil, nil, nil, nil, PriorityNormal, now)
		if !mapValIsNil(cols, "resolved_at") {
			t.Fatal("resolved_at 应为 nil")
		}
		if _, ok := cols["escalated_at"]; ok {
			t.Fatal("不应有 escalated_at 键")
		}
		if _, ok := cols["priority"]; ok {
			t.Fatal("不应有 priority 键")
		}
	})
}

func mustValidationField(t *testing.T, err error, field string) {
	t.Helper()
	e := errs.As(err)
	if e == nil || e.Code != errs.ValidationFailed {
		t.Fatalf("期望 validation_failed，得到 %v", err)
	}
	if e.Fields == nil || e.Fields[field] == "" {
		t.Fatalf("期望 fields[%s]，得到 %v", field, e.Fields)
	}
}

func mapValIsNil(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}
