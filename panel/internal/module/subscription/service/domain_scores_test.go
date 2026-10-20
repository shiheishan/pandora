package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
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

// 读分 = 全国分 + 省偏移 + 省|运营商偏移：只有自己那一级的偏移参与，别的地区、别的运营商不参与。
func TestDomainScoresReadSumsLevels(t *testing.T) {
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
		t.Fatalf("福建电信读 a = %v，要 1.5 − 0.5 + 1 = 2", scores[a])
	}
	scores, _ = d.Read(ctx, []string{a}, domainRegion{Province: "福建", ISP: ispUnicom}, t0)
	if !almost(scores[a], 1, 1e-3) {
		t.Fatalf("福建联通读 a = %v，要 1.5 − 0.5 = 1", scores[a])
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

	// 偏移向 0 衰减：6 小时后 a 的全国分 1.25、两级偏移 −0.25 与 0.5
	scores, _ = d.Read(ctx, []string{a}, fj, t0.Add(6*time.Hour))
	if !almost(scores[a], 1.5, 1e-3) {
		t.Fatalf("6 小时后福建电信读 a = %v，要 1.25 − 0.25 + 0.5 = 1.5", scores[a])
	}

	// 偏移本身夹在 [−2.9, 2.9]，读分再夹进 [0.1, 3]
	if err := d.Apply(ctx, map[string]map[string]float64{b: {"福建": 10, "福建|电信": -10}}, t0); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for field, want := range map[string]float64{"福建": domainOffsetMax, "福建|电信": domainOffsetMin} {
		v, at, ok := parseScore(rdb.HGet(ctx, domainScoreKeyFor(b), field).Val())
		if !ok || !at.Equal(t0) || !almost(v, want, 1e-9) {
			t.Fatalf("b[%s] = (%v, %v, %v)，要 %v", field, v, at, ok, want)
		}
	}
	scores, _ = d.Read(ctx, []string{b}, domainRegion{Province: "福建", ISP: ispUnicom}, t0)
	if !almost(scores[b], domainScoreMax, 1e-3) {
		t.Fatalf("福建联通读 b = %v，要夹到上限 3", scores[b])
	}
	scores, _ = d.Read(ctx, []string{b}, fj, t0)
	if !almost(scores[b], 1, 1e-3) {
		t.Fatalf("福建电信读 b = %v，要 1 + 2.9 − 2.9 = 1", scores[b])
	}
}

// 细分级超过 24 小时没写的偏移读与写都当 0；全国分不受新鲜度约束（与分桶前一致）。
func TestDomainScoresStaleOffsetsAreZero(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	a := "https://a.example.test"
	b := "https://b.example.test"
	c := "https://c.example.test"
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
	if !ok || !almost(scores[a], 2, 1e-3) {
		t.Fatalf("福建电信读 a = %v（ok=%v），要 1.5 + 0.5（省偏移衰减一半）+ 0（过期）= 2", scores[a], ok)
	}

	// 过期的负偏移不会被一次新信号「复活」：按 0 起步再加
	if err := d.Apply(ctx, map[string]map[string]float64{b: {"福建": -2.9}}, old); err != nil {
		t.Fatalf("Apply b 旧: %v", err)
	}
	if err := d.Apply(ctx, map[string]map[string]float64{b: {"*": -0.5, "福建": 0.05}}, t0); err != nil {
		t.Fatalf("Apply b: %v", err)
	}
	if v, _, _ := parseScore(rdb.HGet(ctx, domainScoreKeyFor(b), "福建").Val()); !almost(v, 0.05, 1e-9) {
		t.Fatalf("b 省偏移 = %v，要 0.05（过期按 0 起步）", v)
	}
	scores, ok = d.Read(ctx, []string{b}, fj, t0)
	if !ok || !almost(scores[b], 0.55, 1e-3) {
		t.Fatalf("福建电信读 b = %v（ok=%v），要 0.5 + 0.05 = 0.55", scores[b], ok)
	}

	// 全国分 25 小时前写的照样参与（衰减后 1 + 1×0.5^(25/6)）
	if err := d.Apply(ctx, map[string]map[string]float64{c: {"*": 1}}, old); err != nil {
		t.Fatalf("Apply c: %v", err)
	}
	want := 1 + math.Pow(0.5, 25.0/6)
	scores, ok = d.Read(ctx, []string{c}, fj, t0)
	if !ok || !almost(scores[c], want, 1e-3) {
		t.Fatalf("福建电信读 c = %v（ok=%v），要 %v", scores[c], ok, want)
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
	// 全国 3 个凭据 +0.15；c1 在福建电信，省与省|运营商偏移各 +0.05
	waitScore(t, d, a, at, 1.15)
	waitScoreIn(t, d, a, fj, at, 1.25)
	waitScoreIn(t, d, a, domainRegion{Province: "福建", ISP: ispUnicom}, at, 1.20)

	if rdb.TTL(ctx, domainScoreKeyFor(a)).Val() <= 0 {
		t.Fatal("分数 hash 应有 TTL")
	}
}

// setLevel 直接写某域名某一级的存储值（测试用，造指定时刻、指定值的状态）。
func setLevel(t *testing.T, rdb *redis.Client, base, field string, v float64, at time.Time) {
	t.Helper()
	if err := rdb.HSet(context.Background(), domainScoreKeyFor(base), field, fmt.Sprintf("%.4f|%d", v, at.UnixMilli())).Err(); err != nil {
		t.Fatal(err)
	}
}

// drainScores 立即冲刷攒下的成功信号并等后台写完（测试用）。
func drainScores(t *testing.T, d *domainScores, at time.Time) {
	t.Helper()
	waitFlushIdle(t, d)
	d.mu.Lock()
	d.lastFlush = at.Add(-domainScoreFlushEvery)
	d.maybeFlushLocked(context.Background(), at)
	d.mu.Unlock()
	waitFlushIdle(t, d)
}

// waitFlushIdle 等后台写结束，最多 2 秒。
func waitFlushIdle(t *testing.T, d *domainScores) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for d.flushing.Load() {
		if time.Now().After(deadline) {
			t.Fatal("后台写 2 秒没结束")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 完成标准 (a)：任何一次成功信号（拉取 +0.05、探测成功）都不会让该域名在任何地区读到的分数比写入前低。
// 覆盖细分级在上下限、已过期的负偏移（写入时按 0 起步，读也按 0，不会被「复活」）、没有记录。
func TestDomainScoresSuccessNeverLowers(t *testing.T) {
	a := "https://a.example.test"
	fj := domainRegion{Province: "福建", ISP: ispTelecom}
	fjUnicom := domainRegion{Province: "福建", ISP: ispUnicom}
	gd := domainRegion{Province: "广东", ISP: ispMobile}
	readRegions := []domainRegion{fj, fjUnicom, gd, {}}
	stale := probeTestNow.Add(-25 * time.Hour)
	fresh := probeTestNow.Add(-time.Hour)
	type level struct {
		field string
		v     float64
		at    time.Time
	}
	states := map[string][]level{
		"没有记录":      nil,
		"全国上限、偏移上限": {{"*", 3, fresh}, {"福建", 2.9, fresh}, {"福建|电信", 2.9, fresh}},
		"全国下限、偏移下限": {{"*", 0.1, fresh}, {"福建", -2.9, fresh}, {"福建|电信", -2.9, fresh}},
		"过期的负偏移":    {{"*", 2, fresh}, {"福建", -2.9, stale}, {"福建|电信", -2.9, stale}},
		"新旧混合":      {{"*", 1.5, stale}, {"福建", 1, fresh}, {"福建|电信", -2, stale}, {"福建|联通", -1, fresh}},
	}
	for name, levels := range states {
		for _, from := range []domainRegion{fj, fjUnicom, {}} {
			for _, probe := range []bool{false, true} {
				_, rdb := testkit.Redis(t)
				d := newDomainScores(rdb)
				ctx := context.Background()
				now := probeTestNow
				for _, l := range levels {
					setLevel(t, rdb, a, l.field, l.v, l.at)
				}
				before := readAll(t, d, a, readRegions, now)
				if probe {
					// 每一级都凑够人数（本账号是第 5 个），让这次成功在三级都生效
					for _, field := range from.fields() {
						seedProbeLevel(t, rdb, a, field, now, "o1", "o2", "o3", "o4")
					}
					if _, err := d.ApplyProbe(ctx, "acct", "n-"+name, from, []probeSignal{{BaseURL: a, OK: true}}, now); err != nil {
						t.Fatal(err)
					}
				} else if err := d.Apply(ctx, expandBuckets(map[scoreBucket]float64{{BaseURL: a, Region: from}: scorePullSuccess}), now); err != nil {
					t.Fatal(err)
				}
				after := readAll(t, d, a, readRegions, now)
				for i, reg := range readRegions {
					if after[i] < before[i]-1e-9 {
						t.Fatalf("%s：从 %+v 来的成功（探测=%v）让 %+v 读到的分数从 %v 降到 %v", name, from, probe, reg, before[i], after[i])
					}
				}
			}
		}
	}
}

// readAll 按每个地区各读一次 base 的分数。
func readAll(t *testing.T, d *domainScores, base string, regions []domainRegion, now time.Time) []float64 {
	t.Helper()
	out := make([]float64, len(regions))
	for i, reg := range regions {
		m, ok := d.Read(context.Background(), []string{base}, reg, now)
		if !ok {
			t.Fatal("Read 应可用")
		}
		out[i] = m[base]
	}
	return out
}

// seedProbeLevel 往 base 某一级当前小时桶的成功上报人集合里预先放几个别的账号。
func seedProbeLevel(t *testing.T, rdb *redis.Client, base, field string, now time.Time, accts ...string) {
	t.Helper()
	members := make([]any, len(accts))
	for i, a := range accts {
		members[i] = a
	}
	if err := rdb.SAdd(context.Background(), probeLevelName(probeOKKeyPrefix, base, field, probeHourBucket(now)), members...).Err(); err != nil {
		t.Fatal(err)
	}
}

// 完成标准 (c)，审查失败场景一：A、B 同权重、全国分相同，某省某运营商有一个凭据经 A 成功拉取一次，
// A 在那里读到的分数不低于 B（改前 A 掉到 1.05、B 还是 3）；全国分不在上限时 A 严格高于 B。
func TestDomainScoresLocalSuccessDoesNotLoseToNational(t *testing.T) {
	for _, national := range []float64{3, 2} {
		_, rdb := testkit.Redis(t)
		d := newDomainScores(rdb)
		ctx := context.Background()
		a := "https://a.example.test"
		b := "https://b.example.test"
		px := domainRegion{Province: "青海", ISP: ispBroadcast}
		setLevel(t, rdb, a, "*", national, t0)
		setLevel(t, rdb, b, "*", national, t0)

		d.NoteSuccess(ctx, a, "c1", func() domainRegion { return px }, t0)
		drainScores(t, d, t0.Add(time.Second))

		at := t0.Add(time.Second)
		m, ok := d.Read(ctx, []string{a, b}, px, at)
		if !ok {
			t.Fatal("Read 应可用")
		}
		if m[a] < m[b]-1e-9 {
			t.Fatalf("全国分 %v：青海广电读 A = %v，B = %v，A 不该输给没数据的 B", national, m[a], m[b])
		}
		if national < domainScoreMax && m[a] <= m[b] {
			t.Fatalf("全国分 %v：A = %v 应严格高于 B = %v", national, m[a], m[b])
		}
		if national == 2 && !almost(m[a], 2.15, 1e-3) {
			t.Fatalf("A = %v，要 2.05 + 0.05 + 0.05 = 2.15", m[a])
		}
	}
}

// 完成标准 (b)(d)，审查失败场景二：单个凭据用某地区的 IP 每小时经同一域名拉取（一小时里拉很多次也只记一次）。
// 一小时内它能让该地区读到的分数比父级多出的不超过一次拉取信号 0.05；之后每小时每级至多再 +0.05、向 0 衰减，
// 稳态不超过 0.05 / (1 − 0.5^(1/6)) ≈ 0.46；拉取只会加分，当地读到的分数不会低于全国。
func TestDomainScoresSingleCredentialBounded(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	high := "https://high.example.test" // 全国 3：攻击者想把它在当地压低
	low := "https://low.example.test"   // 全国 0.1：攻击者想把它在当地顶高
	px := domainRegion{Province: "青海", ISP: ispBroadcast}
	prov := domainRegion{Province: "青海", ISP: ispTelecom} // 只读到全国 + 省偏移
	setLevel(t, rdb, high, "*", 3, t0)
	setLevel(t, rdb, low, "*", 0.1, t0)
	locate := func() domainRegion { return px }

	steady := scorePullSuccess / (1 - math.Pow(0.5, 1.0/6))
	decayHour := math.Pow(0.5, 1.0/6)
	prevPX, prevP := 0.0, 0.0
	for h := 0; h < 48; h++ {
		hourStart := t0.Add(time.Duration(h) * time.Hour)
		for i := 0; i < 6; i++ {
			at := hourStart.Add(time.Duration(i) * 10 * time.Minute)
			d.NoteSuccess(ctx, high, "c1", locate, at)
			d.NoteSuccess(ctx, low, "c1", locate, at)
		}
		drainScores(t, d, hourStart.Add(55*time.Minute))
		measure := hourStart.Add(59 * time.Minute)

		for _, base := range []string{high, low} {
			m := map[domainRegion]float64{}
			for _, reg := range []domainRegion{px, prov, {}} {
				s, _ := d.Read(ctx, []string{base}, reg, measure)
				m[reg] = s[base]
			}
			// 拉取只会加分：当地读到的不低于省、省不低于全国（改前当地会从 3 掉到约 1.05）
			if m[px] < m[prov]-1e-9 || m[prov] < m[domainRegion{}]-1e-9 {
				t.Fatalf("第 %d 小时 %s：省|运营商 %v、省 %v、全国 %v，单凭据的拉取不该把当地压低", h, base, m[px], m[prov], m[domainRegion{}])
			}
			if h == 0 && (m[px]-m[prov] > scorePullSuccess+1e-3 || m[prov]-m[domainRegion{}] > scorePullSuccess+1e-3) {
				t.Fatalf("第一小时 %s：省|运营商 %v、省 %v、全国 %v，相邻两级不该差出 0.05 以上", base, m[px], m[prov], m[domainRegion{}])
			}
		}

		offPX := offsetAt(t, rdb, low, "青海|广电", measure)
		offP := offsetAt(t, rdb, low, "青海", measure)
		if offPX-prevPX*decayHour > scorePullSuccess+1e-3 || offP-prevP*decayHour > scorePullSuccess+1e-3 {
			t.Fatalf("第 %d 小时：省|运营商偏移 %v→%v、省偏移 %v→%v，单凭据一小时不该多出 0.05 以上", h, prevPX, offPX, prevP, offP)
		}
		if offPX > steady+1e-3 || offP > steady+1e-3 {
			t.Fatalf("第 %d 小时：偏移 %v / %v 超过稳态上界 %v", h, offPX, offP, steady)
		}
		prevPX, prevP = offPX, offP
	}
}

// offsetAt 读某域名某细分级的偏移并衰减到 now；没有记录回 0。
func offsetAt(t *testing.T, rdb *redis.Client, base, field string, now time.Time) float64 {
	t.Helper()
	raw, err := rdb.HGet(context.Background(), domainScoreKeyFor(base), field).Result()
	if errors.Is(err, redis.Nil) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	v, at, ok := parseScore(raw)
	if !ok {
		t.Fatalf("%s[%s] 存储值 %q 解析不了", base, field, raw)
	}
	return decayOffset(v, at, now)
}

// pipeline 读遇到 Redis 失败：回 (nil, false)，调用方按基础权重（scoreOf 对 nil map 回中性分）。
func TestDomainScoresReadRedisError(t *testing.T) {
	mr, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	a := "https://a.example.test"
	setLevel(t, rdb, a, "*", 3, t0)
	mr.SetError("ERR 测试注入的故障")
	m, ok := d.Read(context.Background(), []string{a, "https://b.example.test"}, domainRegion{Province: "福建", ISP: ispTelecom}, t0)
	if m != nil || ok {
		t.Fatalf("Redis 失败时 Read = (%v, %v)，要 (nil, false)", m, ok)
	}
	if got := scoreOf(m, a); got != domainScoreNeutral {
		t.Fatalf("读失败后 scoreOf = %v，要中性分", got)
	}
}

// 多 key 脚本失败：Apply 回错误；冲刷在后台跑，只打 Warn、不 panic，这一批丢掉，恢复后照常记分。
func TestDomainScoresApplyErrorOnlyWarns(t *testing.T) {
	mr, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	core, logs := observer.New(zap.WarnLevel)
	ctx := plog.With(context.Background(), zap.New(core))
	a := "https://a.example.test"
	b := "https://b.example.test"
	fj := domainRegion{Province: "福建", ISP: ispTelecom}

	mr.SetError("ERR 测试注入的故障")
	if err := d.Apply(ctx, map[string]map[string]float64{a: {"*": 0.1}, b: {"*": 0.1, "福建": 0.1}}, t0); err == nil {
		t.Fatal("Redis 失败时 Apply 应回错误")
	}
	d.NoteSuccess(ctx, a, "c1", func() domainRegion { return fj }, t0)
	d.NoteSuccess(ctx, b, "c1", func() domainRegion { return fj }, t0)
	d.NoteSuccess(ctx, a, "c2", nil, t0.Add(11*time.Second)) // 到点冲刷，后台写失败
	waitFlushIdle(t, d)
	if n := logs.FilterMessage("订阅域名成功分数批量写入失败（丢一批只影响分数增速）").Len(); n != 1 {
		t.Fatalf("批量写失败的 Warn 条数 = %d，要 1", n)
	}

	mr.SetError("")
	if rdb.Exists(context.Background(), domainScoreKeyFor(a), domainScoreKeyFor(b)).Val() != 0 {
		t.Fatal("失败的那一批不该留下分数")
	}
	d.NoteSuccess(ctx, a, "c3", nil, t0.Add(22*time.Second)) // 恢复后照常攒批、冲刷
	waitScore(t, d, a, t0.Add(22*time.Second), 1.05)
}
