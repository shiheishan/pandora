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
	m, ok := d.Read(context.Background(), []string{"https://a.example.test"}, domainRegion{}, t0)
	if m != nil || ok {
		t.Fatalf("nil 接收者 Read = (%v, %v)，要 (nil, false)", m, ok)
	}
	if err := d.Apply(context.Background(), map[string]map[string]float64{
		"https://a.example.test": {domainScoreFieldAll: 1},
	}, t0); err != nil {
		t.Fatalf("nil 接收者 Apply = %v，要 nil", err)
	}
	d.NoteSuccess(context.Background(), "https://a.example.test", "c1", nil, t0) // 不 panic

	nd := newDomainScores(nil)
	if m, ok := nd.Read(context.Background(), []string{"https://a.example.test"}, domainRegion{}, t0); m != nil || ok {
		t.Fatalf("rdb nil 的 Read = (%v, %v)，要 (nil, false)", m, ok)
	}
	if err := nd.Apply(context.Background(), map[string]map[string]float64{
		"https://a.example.test": {domainScoreFieldAll: 1},
	}, t0); err != nil {
		t.Fatalf("rdb nil 的 Apply = %v，要 nil", err)
	}
	nd.NoteSuccess(context.Background(), "https://a.example.test", "c1", nil, t0) // 不 panic
}

func TestDomainScoresApplyAndRead(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	a := "https://a.example.test"
	b := "https://b.example.test"
	c := "https://c.example.test"

	if err := d.Apply(ctx, map[string]map[string]float64{
		a: {domainScoreFieldAll: 0.5},
		b: {domainScoreFieldAll: -5},
	}, t0); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	scores, ok := d.Read(ctx, []string{a, b, c}, domainRegion{}, t0)
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
	if scores, _ = d.Read(ctx, []string{a}, domainRegion{}, t0.Add(6*time.Hour)); !almost(scores[a], 1.25, 1e-3) {
		t.Fatalf("6 小时后 a = %v，要 1.25", scores[a])
	}

	// 再加 10 分：夹到上限
	if err := d.Apply(ctx, map[string]map[string]float64{a: {domainScoreFieldAll: 10}}, t0.Add(6*time.Hour)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if scores, _ = d.Read(ctx, []string{a}, domainRegion{}, t0.Add(6*time.Hour)); !almost(scores[a], domainScoreMax, 1e-3) {
		t.Fatalf("a = %v，要 %v（上限）", scores[a], domainScoreMax)
	}
}

func TestDomainScoresNoteSuccessBatches(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	a := "https://a.example.test"

	// 前两次只开始计时、攒量，不写 Redis
	d.NoteSuccess(ctx, a, "c1", nil, t0)
	d.NoteSuccess(ctx, a, "c2", nil, t0)
	if scores, ok := d.Read(ctx, []string{a}, domainRegion{}, t0); !ok || !almost(scores[a], 1, 1e-3) {
		t.Fatalf("攒批期间 a = (%v, %v)，要 1", scores[a], ok)
	}

	// 攒满 domainScoreFlushEvery 后触发后台写（三个不同凭据 ×0.05）
	d.NoteSuccess(ctx, a, "c3", nil, t0.Add(11*time.Second))
	flushedAt := t0.Add(11 * time.Second)
	waitScore(t, d, a, flushedAt, 1.15)

	// 同一凭据对同一域名在一小时内连记两次只算一次（换一个域名，不和上面共用 hash 里的分数）
	b := "https://b.example.test"
	d2 := newDomainScores(rdb)
	d2.NoteSuccess(ctx, b, "same", nil, t0)                     // 记一次、开始计时
	d2.NoteSuccess(ctx, b, "same", nil, t0.Add(11*time.Second)) // 去重不加量，但到点触发冲刷
	waitScore(t, d2, b, t0.Add(11*time.Second), 1.05)

	// 一小时之后再记会再算
	c := "https://c.example.test"
	d3 := newDomainScores(rdb)
	d3.NoteSuccess(ctx, c, "same", nil, t0)
	d3.NoteSuccess(ctx, c, "same", nil, t0.Add(domainScoreCredWindow+time.Second)) // 过了去重窗口，加量并触发冲刷
	waitScore(t, d3, c, t0.Add(domainScoreCredWindow+time.Second), 1.10)
}

// waitScore 轮询最多 2 秒，等后台批量写让 url 的分数到 want（全国一级）。
func waitScore(t *testing.T, d *domainScores, url string, at time.Time, want float64) {
	waitScoreIn(t, d, url, domainRegion{}, at, want)
}

// waitScoreIn 轮询最多 2 秒，按 region 读 url 的分数到 want。
func waitScoreIn(t *testing.T, d *domainScores, url string, region domainRegion, at time.Time, want float64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		scores, ok := d.Read(context.Background(), []string{url}, region, at)
		if ok && almost(scores[url], want, 1e-3) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s region=%+v = %v（ok=%v），要 %v", url, region, scores[url], ok, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDomainScoresReadFlushes(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	a := "https://a.example.test"

	d.NoteSuccess(ctx, a, "c1", nil, t0)
	d.NoteSuccess(ctx, a, "c2", nil, t0.Add(5*time.Second))
	readAt := t0.Add(11 * time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for {
		scores, ok := d.Read(ctx, []string{a}, domainRegion{}, readAt)
		if ok && almost(scores[a], 1.10, 1e-3) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Read 冲刷后 a = %v（ok=%v），要 1.10", scores[a], ok)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDomainScoresReadFallback(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	a := "https://a.example.test"
	b := "https://b.example.test"
	fj := domainRegion{Province: "福建", ISP: ispTelecom}

	if err := d.Apply(ctx, map[string]map[string]float64{
		a: {"*": 0.5, "福建": -0.5, "福建|电信": 1},
	}, t0); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	scores, ok := d.Read(ctx, []string{a, b}, fj, t0)
	if !ok {
		t.Fatal("Read 应可用")
	}
	if !almost(scores[a], 2, 1e-3) {
		t.Fatalf("fj 读 a = %v，要 2", scores[a])
	}
	scores, _ = d.Read(ctx, []string{a}, domainRegion{Province: "福建", ISP: ispUnicom}, t0)
	if !almost(scores[a], 0.5, 1e-3) {
		t.Fatalf("福建联通读 a = %v，要 0.5", scores[a])
	}
	scores, _ = d.Read(ctx, []string{a}, domainRegion{Province: "广东", ISP: ispTelecom}, t0)
	if !almost(scores[a], 1.5, 1e-3) {
		t.Fatalf("广东电信读 a = %v，要 1.5", scores[a])
	}
	scores, _ = d.Read(ctx, []string{a}, domainRegion{}, t0)
	if !almost(scores[a], 1.5, 1e-3) {
		t.Fatalf("零值读 a = %v，要 1.5", scores[a])
	}
	for _, reg := range []domainRegion{fj, {}, {Province: "广东", ISP: ispTelecom}} {
		scores, _ = d.Read(ctx, []string{b}, reg, t0)
		if !almost(scores[b], domainScoreNeutral, 1e-3) {
			t.Fatalf("未写 b region=%+v = %v，要 1", reg, scores[b])
		}
	}
}

func TestDomainScoresReadStaleFallsBack(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	a := "https://a.example.test"
	b := "https://b.example.test"
	fj := domainRegion{Province: "福建", ISP: ispTelecom}
	old := t0.Add(-25 * time.Hour)
	mid := t0.Add(-6 * time.Hour)

	if err := d.Apply(ctx, map[string]map[string]float64{a: {"福建|电信": 1}}, old); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := d.Apply(ctx, map[string]map[string]float64{a: {"福建": 1}}, mid); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := d.Apply(ctx, map[string]map[string]float64{a: {"*": 0.5}}, t0); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	scores, ok := d.Read(ctx, []string{a}, fj, t0)
	if !ok || !almost(scores[a], 1.5, 1e-3) {
		t.Fatalf("fj 读 a = %v（ok=%v），要 1.5", scores[a], ok)
	}

	if err := d.Apply(ctx, map[string]map[string]float64{b: {"福建": 1}}, old); err != nil {
		t.Fatalf("Apply b 省: %v", err)
	}
	if err := d.Apply(ctx, map[string]map[string]float64{b: {"*": -0.5}}, t0); err != nil {
		t.Fatalf("Apply b *: %v", err)
	}
	scores, ok = d.Read(ctx, []string{b}, fj, t0)
	if !ok || !almost(scores[b], 0.5, 1e-3) {
		t.Fatalf("fj 读 b = %v（ok=%v），要 0.5", scores[b], ok)
	}
}

func TestExpandBuckets(t *testing.T) {
	fj := domainRegion{Province: "福建", ISP: ispTelecom}
	fjUnicom := domainRegion{Province: "福建", ISP: ispUnicom}
	batch := map[scoreBucket]float64{
		{BaseURL: "a", Region: fj}:             0.1,
		{BaseURL: "a", Region: fjUnicom}:       0.05,
		{BaseURL: "a", Region: domainRegion{}}: 0.05,
		{BaseURL: "b", Region: domainRegion{}}: 2.5,
		{BaseURL: "b", Region: fj}:             2.5,
	}
	got := expandBuckets(batch)
	wantA := map[string]float64{
		"*":     0.2,
		"福建":    0.15,
		"福建|电信": 0.1,
		"福建|联通": 0.05,
	}
	for f, w := range wantA {
		if !almost(got["a"][f], w, 1e-9) {
			t.Fatalf("a[%s] = %v，要 %v", f, got["a"][f], w)
		}
	}
	if !almost(got["b"]["*"], 2.9, 1e-9) {
		t.Fatalf("b[*] = %v，要 2.9（夹回）", got["b"]["*"])
	}
	if !almost(got["b"]["福建"], 2.5, 1e-9) || !almost(got["b"]["福建|电信"], 2.5, 1e-9) {
		t.Fatalf("b 福建桶 = %v", got["b"])
	}
}

func TestDomainScoresNoteSuccessByRegion(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	a := "https://a.example.test"
	fj := domainRegion{Province: "福建", ISP: ispTelecom}
	calls := 0
	locate := func() domainRegion {
		calls++
		return fj
	}

	d.NoteSuccess(ctx, a, "c1", locate, t0)
	d.NoteSuccess(ctx, a, "c1", locate, t0.Add(time.Second)) // 同凭据去重
	d.NoteSuccess(ctx, a, "c2", nil, t0.Add(2*time.Second))
	d.NoteSuccess(ctx, a, "c3", nil, t0.Add(11*time.Second)) // 到点冲刷

	if calls != 1 {
		t.Fatalf("locate 调用次数 = %d，要 1", calls)
	}
	at := t0.Add(11 * time.Second)
	waitScore(t, d, a, at, 1.15)
	waitScoreIn(t, d, a, fj, at, 1.05)
	waitScoreIn(t, d, a, domainRegion{Province: "福建", ISP: ispUnicom}, at, 1.05)

	if rdb.TTL(ctx, domainScoreKeyFor(a)).Val() <= 0 {
		t.Fatal("分数 hash 应有 TTL")
	}
}
