package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// t0 是测试共用的固定时刻。
var t0 = time.Unix(1700000000, 0).UTC()

// almost 报告两个浮点是否在 eps 内相等。
func almost(a, b, eps float64) bool { return math.Abs(a-b) < eps }

func TestDecayScore(t *testing.T) {
	at := t0
	if got := decayScore(3, at, at.Add(6*time.Hour)); !almost(got, 2, 1e-9) {
		t.Fatalf("score 3 过 6 小时 = %v，要 2", got)
	}
	if got := decayScore(3, at, at.Add(12*time.Hour)); !almost(got, 1.5, 1e-9) {
		t.Fatalf("score 3 过 12 小时 = %v，要 1.5", got)
	}
	if got := decayScore(0.1, at, at.Add(6*time.Hour)); !almost(got, 0.55, 1e-9) {
		t.Fatalf("score 0.1 过 6 小时 = %v，要 0.55", got)
	}
	// at 晚于 now 按 0 处理：返回原值
	if got := decayScore(2.5, at, at.Add(-time.Hour)); !almost(got, 2.5, 1e-9) {
		t.Fatalf("at 晚于 now = %v，要原值 2.5", got)
	}
}

func TestClampScore(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{-1, domainScoreMin},
		{5, domainScoreMax},
		{math.NaN(), domainScoreNeutral},
		{1.2, 1.2},
	}
	for _, c := range cases {
		if got := clampScore(c.in); !almost(got, c.want, 1e-9) {
			t.Fatalf("clampScore(%v) = %v，要 %v", c.in, got, c.want)
		}
	}
}

func TestScoreOf(t *testing.T) {
	scores := map[string]float64{"https://a.example.test": 2.5}
	if got := scoreOf(scores, "https://b.example.test"); got != domainScoreNeutral {
		t.Fatalf("缺省 = %v，要 %v", got, domainScoreNeutral)
	}
	if got := scoreOf(scores, "https://a.example.test"); !almost(got, 2.5, 1e-9) {
		t.Fatalf("有值 = %v，要 2.5", got)
	}
}

func TestParseScore(t *testing.T) {
	score, at, ok := parseScore("1.5000|1700000000000")
	if !ok || !almost(score, 1.5, 1e-9) || !at.Equal(time.UnixMilli(1700000000000)) {
		t.Fatalf("parseScore 合法值 = (%v, %v, %v)", score, at, ok)
	}
	for _, bad := range []string{"x|1", "1.5", ""} {
		if _, _, ok := parseScore(bad); ok {
			t.Fatalf("parseScore(%q) 应失败", bad)
		}
	}
}

func TestDomainScoresNilSafe(t *testing.T) {
	var d *domainScores
	m, ok := d.Read(context.Background(), []string{"https://a.example.test"}, t0)
	if m != nil || ok {
		t.Fatalf("nil 接收者 Read = (%v, %v)，要 (nil, false)", m, ok)
	}
	if err := d.Apply(context.Background(), map[string]float64{"https://a.example.test": 1}, t0); err != nil {
		t.Fatalf("nil 接收者 Apply = %v，要 nil", err)
	}
	d.NoteSuccess(context.Background(), "https://a.example.test", "c1", t0) // 不 panic

	nd := newDomainScores(nil)
	if m, ok := nd.Read(context.Background(), []string{"https://a.example.test"}, t0); m != nil || ok {
		t.Fatalf("rdb nil 的 Read = (%v, %v)，要 (nil, false)", m, ok)
	}
	if err := nd.Apply(context.Background(), map[string]float64{"https://a.example.test": 1}, t0); err != nil {
		t.Fatalf("rdb nil 的 Apply = %v，要 nil", err)
	}
	nd.NoteSuccess(context.Background(), "https://a.example.test", "c1", t0) // 不 panic
}

func TestDomainScoresApplyAndRead(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	a := "https://a.example.test"
	b := "https://b.example.test"
	c := "https://c.example.test"

	if err := d.Apply(ctx, map[string]float64{a: 0.5, b: -5}, t0); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	scores, ok := d.Read(ctx, []string{a, b, c}, t0)
	if !ok {
		t.Fatal("Read 应可用")
	}
	if !almost(scores[a], 1.5, 1e-3) {
		t.Fatalf("a = %v，要 1.5", scores[a])
	}
	if !almost(scores[b], domainScoreMin, 1e-3) {
		t.Fatalf("b = %v，要 %v（下限）", scores[b], domainScoreMin)
	}
	if !almost(scores[c], domainScoreNeutral, 1e-3) {
		t.Fatalf("c = %v，要 %v（没记录）", scores[c], domainScoreNeutral)
	}

	// 半衰期后衰减：1.5 → 1.25
	if scores, _ = d.Read(ctx, []string{a}, t0.Add(6*time.Hour)); !almost(scores[a], 1.25, 1e-3) {
		t.Fatalf("6 小时后 a = %v，要 1.25", scores[a])
	}

	// 再加 10 分：夹到上限
	if err := d.Apply(ctx, map[string]float64{a: 10}, t0.Add(6*time.Hour)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if scores, _ = d.Read(ctx, []string{a}, t0.Add(6*time.Hour)); !almost(scores[a], domainScoreMax, 1e-3) {
		t.Fatalf("a = %v，要 %v（上限）", scores[a], domainScoreMax)
	}
}

func TestDomainScoresNoteSuccessBatches(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	a := "https://a.example.test"

	// 前两次只开始计时、攒量，不写 Redis
	d.NoteSuccess(ctx, a, "c1", t0)
	d.NoteSuccess(ctx, a, "c2", t0)
	if scores, ok := d.Read(ctx, []string{a}, t0); !ok || !almost(scores[a], 1, 1e-3) {
		t.Fatalf("攒批期间 a = (%v, %v)，要 1", scores[a], ok)
	}

	// 攒满 domainScoreFlushEvery 后触发后台写（三个不同凭据 ×0.05）
	d.NoteSuccess(ctx, a, "c3", t0.Add(11*time.Second))
	flushedAt := t0.Add(11 * time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for {
		scores, ok := d.Read(ctx, []string{a}, flushedAt)
		if ok && almost(scores[a], 1.15, 1e-3) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("后台写没生效：a = %v（ok=%v），要 1.15", scores[a], ok)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 同一凭据对同一域名在一小时内连记两次只算一次（换一个域名，不和上面共用 hash 里的分数）
	b := "https://b.example.test"
	d2 := newDomainScores(rdb)
	d2.NoteSuccess(ctx, b, "same", t0)                     // 记一次、开始计时
	d2.NoteSuccess(ctx, b, "same", t0.Add(11*time.Second)) // 去重不加量，但到点触发冲刷
	waitScore(t, d2, b, t0.Add(11*time.Second), 1.05)

	// 一小时之后再记会再算
	c := "https://c.example.test"
	d3 := newDomainScores(rdb)
	d3.NoteSuccess(ctx, c, "same", t0)
	d3.NoteSuccess(ctx, c, "same", t0.Add(domainScoreCredWindow+time.Second)) // 过了去重窗口，加量并触发冲刷
	waitScore(t, d3, c, t0.Add(domainScoreCredWindow+time.Second), 1.10)
}

// waitScore 轮询最多 2 秒，等后台批量写让 url 的分数到 want。
func waitScore(t *testing.T, d *domainScores, url string, at time.Time, want float64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		scores, ok := d.Read(context.Background(), []string{url}, at)
		if ok && almost(scores[url], want, 1e-3) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s = %v（ok=%v），要 %v", url, scores[url], ok, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDomainScoresReadFlushes(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	a := "https://a.example.test"

	d.NoteSuccess(ctx, a, "c1", t0)
	d.NoteSuccess(ctx, a, "c2", t0.Add(5*time.Second))
	readAt := t0.Add(11 * time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for {
		scores, ok := d.Read(ctx, []string{a}, readAt)
		if ok && almost(scores[a], 1.10, 1e-3) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Read 冲刷后 a = %v（ok=%v），要 1.10", scores[a], ok)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
