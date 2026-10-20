package service

// 订阅域名分数的场景用例（miniredis，不连库）：拉取与探测按时间线交替写，核对某地区被封后读分多快降下来。
// 钉住 10-20 复审阻塞 1：偏移写入时按上一级当前读分夹、读分逐级夹，细分级不攒「看不见的余量」。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// scenarioRound 是场景用例的一轮间隔：比探测去重键的 TTL（30 分钟）多 1 秒，不卡在过期边界上
// （真实 Redis 剩余 TTL 为 0 时还不算过期，miniredis 会删；多 1 秒两边一致）。
const scenarioRound = 30*time.Minute + time.Second

// scenarioClock 让 miniredis 的键过期时间跟着测试里的 now 走（探测的去重、上报人集合、净变动都靠 TTL）。
type scenarioClock struct {
	mr  *miniredis.Miniredis
	cur time.Time
}

// to 把时钟推进到 at（只往前走）。
func (c *scenarioClock) to(at time.Time) {
	if at.After(c.cur) {
		c.mr.FastForward(at.Sub(c.cur))
		c.cur = at
	}
}

// readOne 按 region 读 base 的分数。
func readOne(t *testing.T, d *domainScores, base string, region domainRegion, at time.Time) float64 {
	t.Helper()
	m, ok := d.Read(context.Background(), []string{base}, region, at)
	if !ok {
		t.Fatal("Read 应可用")
	}
	return m[base]
}

// pullFrom 让 n 个凭据（prefix0 … prefix{n-1}）在 at 从 region 经 base 各成功拉取一次，并立即冲刷写完。
func pullFrom(t *testing.T, d *domainScores, base string, region domainRegion, prefix string, n int, at time.Time) {
	t.Helper()
	for i := 0; i < n; i++ {
		d.NoteSuccess(context.Background(), base, fmt.Sprintf("%s%d", prefix, i), func() domainRegion { return region }, at)
	}
	drainScores(t, d, at)
}

// probeFailFrom 让 accts 在 at 从 region 各报一次 base 连不上（每人每次一张新票据）。
func probeFailFrom(t *testing.T, d *domainScores, base string, region domainRegion, accts []string, tag string, at time.Time) {
	t.Helper()
	for _, acct := range accts {
		if _, err := d.ApplyProbe(context.Background(), acct, tag+"-"+acct, region, []probeSignal{{BaseURL: base, OK: false}}, at); err != nil {
			t.Fatal(err)
		}
	}
}

// 完成标准 (1)：全国分 3；广东联通每小时 50 个凭据经 A 拉取（每半小时 25 个），广东电信 5 个账号每 30 分钟各报一次 A 连不上。
// 广东联通的拉取会把省偏移顶住，但省偏移按全国分夹、最多到 0，不会攒出余量；广东电信的失败落在省|运营商偏移上，
// 每一轮都让广东电信读到的分数下降，到第 2 小时降到 2 以下；广东联通读到的一直是 3。
// 改前（偏移各自夹 ±2.9、读分最后才夹）省偏移会被顶到 +2.9，省|运营商压到 −2.9 读分也还是 3。
func TestDomainScoresOneISPBlockedInBusyProvince(t *testing.T) {
	mr, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	a := "https://a.example.test"
	gdUnicom := domainRegion{Province: "广东", ISP: ispUnicom}
	gdTelecom := domainRegion{Province: "广东", ISP: ispTelecom}
	start := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC) // 整点：0、30 分在一个小时桶，60、90 分在下一个，120 分再下一个
	clock := &scenarioClock{mr: mr, cur: start}
	setLevel(t, rdb, a, "*", 3, start)
	accts := []string{"u1", "u2", "u3", "u4", "u5"}

	prev := domainScoreMax
	for r := 0; r <= 4; r++ {
		at := start.Add(time.Duration(r) * scenarioRound)
		clock.to(at)
		probeFailFrom(t, d, a, gdTelecom, accts, fmt.Sprintf("n%d", r), at)
		// 前半小时、后半小时各一批凭据，同一个凭据隔一小时再拉一次
		pullFrom(t, d, a, gdUnicom, fmt.Sprintf("c%d-", r%2), 25, at)

		unicom := readOne(t, d, a, gdUnicom, at)
		telecom := readOne(t, d, a, gdTelecom, at)
		if !almost(unicom, domainScoreMax, 1e-3) {
			t.Fatalf("第 %d 分钟：广东联通读 A = %v，要仍是 3", r*30, unicom)
		}
		if telecom > prev-0.1 {
			t.Fatalf("第 %d 分钟：广东电信读 A = %v，上一轮 %v，每一轮都该降下来", r*30, telecom, prev)
		}
		prev = telecom
	}
	if prev > 2 {
		t.Fatalf("第 2 小时：广东电信读 A = %v，要 ≤ 2", prev)
	}
}

// 完成标准 (2)：全省被封、5 个账号报失败，第 1 小时内广东读分就下降（全国分由别省的拉取维持在 3）。
// 两种起点：一是封之前广东一直有大量拉取（偏移按全国分夹，始终是 0）；
// 二是全国分被别处拉高之后留下的余量（省、省|运营商偏移都在 +2.9）：失败写入时先把余量夹掉，
// 改前要 4–5 小时读分才开始降。
func TestDomainScoresProvinceBlockedDropsWithinHour(t *testing.T) {
	gdUnicom := domainRegion{Province: "广东", ISP: ispUnicom}
	gdTelecom := domainRegion{Province: "广东", ISP: ispTelecom}
	bj := domainRegion{Province: "北京", ISP: ispUnicom}
	accts := []string{"u1", "u2", "u3", "u4", "u5"}
	start := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)

	t.Run("封之前广东拉取多", func(t *testing.T) {
		mr, rdb := testkit.Redis(t)
		d := newDomainScores(rdb)
		a := "https://a.example.test"
		clock := &scenarioClock{mr: mr, cur: start}
		for h := 0; h < 3; h++ {
			at := start.Add(time.Duration(h) * time.Hour)
			clock.to(at)
			pullFrom(t, d, a, gdTelecom, "gdt", 30, at)
			pullFrom(t, d, a, gdUnicom, "gdu", 30, at)
			pullFrom(t, d, a, bj, "bj", 30, at)
		}
		blocked := start.Add(3 * time.Hour)
		clock.to(blocked)
		before := readOne(t, d, a, gdUnicom, blocked)
		if national := readOne(t, d, a, domainRegion{}, blocked); !almost(before, national, 1e-3) {
			t.Fatalf("封之前广东联通读 A = %v，应与全国 %v 相同（省偏移按全国分夹，没有余量）", before, national)
		}

		// 封了以后广东没有成功拉取了；5 个账号报失败，北京照常拉取把全国分拉回 3
		probeFailFrom(t, d, a, gdTelecom, accts, "n0", blocked)
		pullFrom(t, d, a, bj, "bj", 30, blocked)
		if got := readOne(t, d, a, bj, blocked); !almost(got, domainScoreMax, 1e-3) {
			t.Fatalf("北京读 A = %v，要 3", got)
		}
		if got := readOne(t, d, a, gdUnicom, blocked); got > 2.75 {
			t.Fatalf("封后第一轮广东联通读 A = %v，要降到 2.7 左右", got)
		}
		if got := readOne(t, d, a, gdTelecom, blocked); got > 2.45 {
			t.Fatalf("封后第一轮广东电信读 A = %v，要降到 2.4 左右", got)
		}
	})

	t.Run("全国拉高后留下的余量", func(t *testing.T) {
		mr, rdb := testkit.Redis(t)
		d := newDomainScores(rdb)
		a := "https://a.example.test"
		clock := &scenarioClock{mr: mr, cur: start}
		setLevel(t, rdb, a, "*", 3, start)
		setLevel(t, rdb, a, "广东", 2.9, start)
		setLevel(t, rdb, a, "广东|电信", 2.9, start)

		for r := 0; r < 2; r++ {
			at := start.Add(time.Duration(r) * scenarioRound)
			clock.to(at)
			probeFailFrom(t, d, a, gdTelecom, accts, fmt.Sprintf("n%d", r), at)
			pullFrom(t, d, a, bj, fmt.Sprintf("bj%d-", r), 30, at)
		}
		at := start.Add(scenarioRound)
		if got := readOne(t, d, a, bj, at); !almost(got, domainScoreMax, 1e-3) {
			t.Fatalf("北京读 A = %v，要 3", got)
		}
		if got := readOne(t, d, a, gdUnicom, at); got > 2.95 {
			t.Fatalf("第 30 分钟广东联通读 A = %v，余量应已夹掉、读分开始下降", got)
		}
		if got := readOne(t, d, a, gdTelecom, at); got > 2.75 {
			t.Fatalf("第 30 分钟广东电信读 A = %v，余量应已夹掉、读分开始下降", got)
		}
	})
}
