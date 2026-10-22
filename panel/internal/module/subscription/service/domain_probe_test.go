package service

// 订阅域名探测的单元测试（不连库）：票据签发与验签、组指纹、ApplyProbe 的防操纵规则
// （成功与失败对称；全国一级 3 个账号、省与省|运营商 5 个；按级别分开计）。
// 连库的用例在 domain_probe_pg_test.go。

import (
	"bytes"
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// probeTestNow 是探测测试共用的固定时刻（整点后 10 分钟，避免恰好跨小时桶）。
var probeTestNow = time.Date(2026, 10, 14, 8, 10, 0, 0, time.UTC)

// newProbeDelivery 用主密钥（32 个 fill 字节）造分发依赖与空 service，给 signProbeTicket / parseProbeTicket 用；
// 同时返回 keys，造坏票据时手工签名。
func newProbeDelivery(t *testing.T, fill byte) (*Delivery, *Service, *crypto.Keys) {
	t.Helper()
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return &Delivery{Hasher: keys}, &Service{}, keys
}

// signProbePayload 用同一把 keys 手工签一段 payload：造坏票据时用，确保拒绝原因就是那一项。
func signProbePayload(t *testing.T, keys *crypto.Keys, payload string) string {
	t.Helper()
	mac := keys.HMAC(crypto.InfoHMACSubProbe, []byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac)
}

func TestProbeTicketRoundTrip(t *testing.T) {
	d, s, _ := newProbeDelivery(t, 0x03)
	now := probeTestNow
	uid := uuid.New()
	exp := now.Add(5 * time.Minute)
	ticket := s.signProbeTicket(d, probeTicket{
		UserID: uid, GroupFP: "abcdef0123456789", Exp: exp, Nonce: "0123456789abcdef",
	})
	got, err := s.parseProbeTicket(d, ticket, now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.UserID != uid || got.GroupFP != "abcdef0123456789" ||
		got.Exp.Unix() != exp.Unix() || got.Nonce != "0123456789abcdef" {
		t.Fatalf("round trip: %+v", got)
	}
}

func TestProbeTicketRejects(t *testing.T) {
	d, s, keys := newProbeDelivery(t, 0x03)
	otherD, otherS, _ := newProbeDelivery(t, 0x04) // 换一把主密钥
	now := probeTestNow
	uid := uuid.New()
	fp := "abcdef0123456789"
	expStr := strconv.FormatInt(now.Add(time.Minute).Unix(), 10)

	ok := s.signProbeTicket(d, probeTicket{UserID: uid, GroupFP: fp, Exp: now.Add(5 * time.Minute), Nonce: "0123456789abcdef"})

	// 改 payload 一个字符后重新 base64，签名段原样保留：HMAC 对不上
	parts := strings.SplitN(ok, ".", 2)
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 0x01
	tampered := base64.RawURLEncoding.EncodeToString(payload) + "." + parts[1]

	cases := []struct{ name, raw string }{
		{"在 Exp 那一刻", s.signProbeTicket(d, probeTicket{UserID: uid, GroupFP: fp, Exp: now, Nonce: "0123456789abcdef"})},
		{"payload 被改", tampered},
		{"别的密钥签的", otherS.signProbeTicket(otherD, probeTicket{UserID: uid, GroupFP: fp, Exp: now.Add(5 * time.Minute), Nonce: "0123456789abcdef"})},
		{"版本不是 v1", signProbePayload(t, keys, "v2|"+uid.String()+"|"+fp+"|"+expStr+"|0123456789abcdef")},
		{"有效期超过上限", s.signProbeTicket(d, probeTicket{UserID: uid, GroupFP: fp, Exp: now.Add(probeTicketTTL).Add(61 * time.Second), Nonce: "0123456789abcdef"})},
		{"nonce 不是十六进制", signProbePayload(t, keys, "v1|"+uid.String()+"|"+fp+"|"+expStr+"|gggggggggggggggg")},
		{"nonce 长度不对", signProbePayload(t, keys, "v1|"+uid.String()+"|"+fp+"|"+expStr+"|abc")},
		{"没有点号", base64.RawURLEncoding.EncodeToString([]byte("v1|no-dot"))},
		{"三段", ok + ".x"},
		{"超过 512 字符", strings.Repeat("a", probeTicketMaxLen+1)},
		{"空串", ""},
	}
	for _, c := range cases {
		if _, perr := s.parseProbeTicket(d, c.raw, now); errs.As(perr).Code != errs.Unauthorized {
			t.Fatalf("%s: 要 Unauthorized，得 %v", c.name, perr)
		}
	}
}

func TestGroupFingerprint(t *testing.T) {
	if groupFingerprint(nil) != "" {
		t.Fatal("nil 组指纹要为空串")
	}
	g := &DomainGroup{ID: uuid.New(), Domains: []Domain{
		{BaseURL: "https://a.example.test"}, {BaseURL: "https://b.example.test"},
	}}
	fp := groupFingerprint(g)
	if len(fp) != 16 {
		t.Fatalf("长度 %d，要 16", len(fp))
	}
	if fp != groupFingerprint(g) {
		t.Fatal("同一组两次要相同")
	}

	changed := *g
	changed.ID = uuid.New()
	if groupFingerprint(&changed) == fp {
		t.Fatal("改组 id 指纹要变")
	}

	added := *g
	added.Domains = append(append([]Domain{}, g.Domains...), Domain{BaseURL: "https://c.example.test"})
	if groupFingerprint(&added) == fp {
		t.Fatal("加一个域名指纹要变")
	}

	swapped := *g
	swapped.Domains = []Domain{g.Domains[1], g.Domains[0]}
	if groupFingerprint(&swapped) == fp {
		t.Fatal("调换顺序指纹要变")
	}
}

func TestApplyProbeSuccessNeedsThreeAccountsAndTicketOnce(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	now := probeTestNow
	a := "https://a.example.test"
	b := "https://b.example.test"

	// 前两个账号报成功：不到 3 人，不加分（防单个账号把某域名顶高）
	for _, acct := range []string{"u1", "u2"} {
		n, err := d.ApplyProbe(ctx, acct, "n-"+acct, domainRegion{}, []probeSignal{{BaseURL: a, OK: true}}, now)
		if err != nil || n != 0 {
			t.Fatalf("%s 报成功 = (%d, %v)，要 (0, nil)", acct, n, err)
		}
	}
	if rdb.HExists(ctx, domainScoreKeyFor(a), domainScoreFieldAll).Val() {
		t.Fatal("a 不该有分数记录")
	}
	// 第三个不同账号：成功开始加分
	n, err := d.ApplyProbe(ctx, "u3", "n-u3", domainRegion{}, []probeSignal{{BaseURL: a, OK: true}}, now)
	if err != nil || n != 1 {
		t.Fatalf("u3 报成功 = (%d, %v)，要 (1, nil)", n, err)
	}
	scores, ok := d.Read(ctx, []string{a}, domainRegion{}, now)
	if !ok || !almost(scores[a], 1.1, 1e-3) {
		t.Fatalf("a = %v（ok=%v），要 1.1", scores[a], ok)
	}

	// 同一 nonce 再报（换账号、换域名）：票据一次性，整次上报不生效，连上报人集合都不进
	n, err = d.ApplyProbe(ctx, "u4", "n-u3", domainRegion{}, []probeSignal{{BaseURL: b, OK: true}}, now)
	if err != nil || n != 0 {
		t.Fatalf("同票据第二次 = (%d, %v)，要 (0, nil)", n, err)
	}
	if rdb.HExists(ctx, domainScoreKeyFor(b), domainScoreFieldAll).Val() {
		t.Fatal("b 不该有分数记录")
	}
	if rdb.Exists(ctx, probeLevelName(probeOKKeyPrefix, b, domainScoreFieldAll, probeHourBucket(now))).Val() != 0 {
		t.Fatal("被票据一次性挡下的上报不该进上报人集合")
	}
}

func TestApplyProbeDedupePerAccount(t *testing.T) {
	mr, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	now := probeTestNow
	a := "https://a.example.test"

	// 先凑够两个别的账号，让 u3 的成功能生效
	for _, acct := range []string{"u1", "u2"} {
		if n, err := d.ApplyProbe(ctx, acct, "n-"+acct, domainRegion{}, []probeSignal{{BaseURL: a, OK: true}}, now); err != nil || n != 0 {
			t.Fatalf("%s = (%d, %v)，要 0", acct, n, err)
		}
	}
	if n, err := d.ApplyProbe(ctx, "u3", "n3", domainRegion{}, []probeSignal{{BaseURL: a, OK: true}}, now); err != nil || n != 1 {
		t.Fatalf("u3 第一次 = (%d, %v)，要 1", n, err)
	}
	// 同一账号同一域名 30 分钟内换票据再报：去重不生效
	if n, err := d.ApplyProbe(ctx, "u3", "n4", domainRegion{}, []probeSignal{{BaseURL: a, OK: true}}, now); err != nil || n != 0 {
		t.Fatalf("u3 第二次 = (%d, %v)，要 0", n, err)
	}
	// 过了去重窗口再报（仍在同一小时桶，集合里已有 3 人）：生效
	mr.FastForward(31 * time.Minute)
	now = now.Add(31 * time.Minute)
	if n, err := d.ApplyProbe(ctx, "u3", "n5", domainRegion{}, []probeSignal{{BaseURL: a, OK: true}}, now); err != nil || n != 1 {
		t.Fatalf("31 分钟后 = (%d, %v)，要 1", n, err)
	}
}

func TestApplyProbeFailureNeedsThreeAccounts(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	now := probeTestNow
	a := "https://a.example.test"

	// 前两个账号报失败：不到 3 人，不扣分
	for _, acct := range []string{"u1", "u2"} {
		n, err := d.ApplyProbe(ctx, acct, "n-"+acct, domainRegion{}, []probeSignal{{BaseURL: a, OK: false}}, now)
		if err != nil || n != 0 {
			t.Fatalf("%s 报失败 = (%d, %v)，要 (0, nil)", acct, n, err)
		}
	}
	if rdb.HExists(ctx, domainScoreKeyFor(a), domainScoreFieldAll).Val() {
		t.Fatal("a 不该有分数记录")
	}

	// 第三个不同账号：失败开始扣分
	n, err := d.ApplyProbe(ctx, "u3", "n-u3", domainRegion{}, []probeSignal{{BaseURL: a, OK: false}}, now)
	if err != nil || n != 1 {
		t.Fatalf("u3 报失败 = (%d, %v)，要 (1, nil)", n, err)
	}
	scores, ok := d.Read(ctx, []string{a}, domainRegion{}, now)
	if !ok || !almost(scores[a], 0.7, 1e-3) {
		t.Fatalf("a = %v（ok=%v），要 0.7", scores[a], ok)
	}
}

func TestApplyProbeNetCap(t *testing.T) {
	mr, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	now := probeTestNow
	b := "https://b.example.test"
	c := "https://c.example.test"

	// 失败：u1..u5 依次报 b，净变动封顶 −0.5
	wantN := []int{0, 0, 1, 1, 0}
	for i, acct := range []string{"u1", "u2", "u3", "u4", "u5"} {
		n, err := d.ApplyProbe(ctx, acct, "nf-"+acct, domainRegion{}, []probeSignal{{BaseURL: b, OK: false}}, now)
		if err != nil || n != wantN[i] {
			t.Fatalf("%s 报 b 失败 = (%d, %v)，要 %d", acct, n, err, wantN[i])
		}
	}
	scores, ok := d.Read(ctx, []string{b}, domainRegion{}, now)
	if !ok || !almost(scores[b], 0.5, 1e-3) {
		t.Fatalf("b = %v（ok=%v），要 0.5（封顶）", scores[b], ok)
	}

	// 成功：u1..u8 依次报 c：前两个不到 3 人不计，u3..u7 各 +0.1，u8 被封顶挡下
	wantS := []int{0, 0, 1, 1, 1, 1, 1, 0}
	for i, acct := range []string{"u1", "u2", "u3", "u4", "u5", "u6", "u7", "u8"} {
		n, err := d.ApplyProbe(ctx, acct, "ns-"+acct, domainRegion{}, []probeSignal{{BaseURL: c, OK: true}}, now)
		if err != nil || n != wantS[i] {
			t.Fatalf("%s 报 c 成功 = (%d, %v)，要 %d", acct, n, err, wantS[i])
		}
	}
	scores, ok = d.Read(ctx, []string{c}, domainRegion{}, now)
	if !ok || !almost(scores[c], 1.5, 1e-3) {
		t.Fatalf("c = %v（ok=%v），要 1.5（封顶）", scores[c], ok)
	}

	// 推到下一个小时桶：上报人集合与净变动额度都重置，又要凑够 3 人
	mr.FastForward(time.Hour)
	now = now.Add(time.Hour)
	wantNew := []int{0, 0, 1}
	for i, acct := range []string{"u9", "u10", "u11"} {
		n, err := d.ApplyProbe(ctx, acct, "ns-"+acct, domainRegion{}, []probeSignal{{BaseURL: c, OK: true}}, now)
		if err != nil || n != wantNew[i] {
			t.Fatalf("新桶 %s 报 c = (%d, %v)，要 %d", acct, n, err, wantNew[i])
		}
	}
}

func TestApplyProbeNilSafe(t *testing.T) {
	ctx := context.Background()
	now := probeTestNow
	sig := []probeSignal{{BaseURL: "https://a.example.test", OK: true}}
	if n, err := (*domainScores)(nil).ApplyProbe(ctx, "u1", "n1", domainRegion{}, sig, now); n != 0 || err != nil {
		t.Fatalf("nil 接收者 = (%d, %v)，要 (0, nil)", n, err)
	}
	if n, err := newDomainScores(nil).ApplyProbe(ctx, "u1", "n1", domainRegion{}, sig, now); n != 0 || err != nil {
		t.Fatalf("rdb nil = (%d, %v)，要 (0, nil)", n, err)
	}
	if n, err := newDomainScores(nil).ApplyProbe(ctx, "u1", "n1", domainRegion{}, nil, now); n != 0 || err != nil {
		t.Fatalf("signals 空 = (%d, %v)，要 (0, nil)", n, err)
	}
}

// probeRun 让 accts 依次从 region 对 base 报一次（各用自己的票据），核对每次真正改动分数的域名数。
func probeRun(t *testing.T, d *domainScores, region domainRegion, base string, ok bool, accts []string, want []int, now time.Time) {
	t.Helper()
	for i, acct := range accts {
		n, err := d.ApplyProbe(context.Background(), acct, "n-"+base+"-"+acct, region, []probeSignal{{BaseURL: base, OK: ok}}, now)
		if err != nil || n != want[i] {
			t.Fatalf("%s 从 %+v 报 %s（成功=%v）= (%d, %v)，要 %d", acct, region, base, ok, n, err, want[i])
		}
	}
}

// probeRead 按 region 读 base 的分数并核对。
func probeRead(t *testing.T, d *domainScores, base string, region domainRegion, want float64, now time.Time) {
	t.Helper()
	scores, ok := d.Read(context.Background(), []string{base}, region, now)
	if !ok || !almost(scores[base], want, 1e-3) {
		t.Fatalf("%+v 读 %s = %v（ok=%v），要 %v", region, base, scores[base], ok, want)
	}
}

// 某省某运营商的失败只压那里的偏移：别的省、别的运营商只受全国分影响；别的省的成功不碰这个省的偏移。
func TestApplyProbeRegionIsolated(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	now := probeTestNow
	fj := domainRegion{Province: "福建", ISP: ispTelecom}
	gd := domainRegion{Province: "广东", ISP: ispTelecom}
	a := "https://a.example.test"
	setLevel(t, rdb, a, "*", 2, now)

	// 福建电信 5 个账号报失败：全国第 3、4 个起扣（−0.3、−0.2 封顶）；第 5 个凑够细分级门槛，省与省|运营商偏移各 −0.3
	probeRun(t, d, fj, a, false, []string{"u1", "u2", "u3", "u4", "u5"}, []int{0, 0, 1, 1, 1}, now)
	probeRead(t, d, a, domainRegion{}, 1.5, now)
	probeRead(t, d, a, fj, 0.9, now)
	probeRead(t, d, a, domainRegion{Province: "福建", ISP: ispUnicom}, 1.2, now)
	probeRead(t, d, a, gd, 1.5, now)

	// 广东电信 5 个账号报成功：全国第 3 个起各 +0.1（同一小时净变动额度与失败共用）；广东两级偏移各 +0.1
	probeRun(t, d, gd, a, true, []string{"u6", "u7", "u8", "u9", "u10"}, []int{0, 0, 1, 1, 1}, now)
	probeRead(t, d, a, domainRegion{}, 1.8, now)
	probeRead(t, d, a, gd, 2.0, now)
	probeRead(t, d, a, fj, 1.2, now)
	probeRead(t, d, a, domainRegion{Province: "福建", ISP: ispUnicom}, 1.5, now)
	probeRead(t, d, a, domainRegion{Province: "浙江", ISP: ispMobile}, 1.8, now)
}

// 门槛按级别分开数：3 个账号分散在两个省，全国一级凑够，细分级都不够、不写。
func TestApplyProbeLevelThresholdsSeparate(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	now := probeTestNow
	fj := domainRegion{Province: "福建", ISP: ispTelecom}
	gd := domainRegion{Province: "广东", ISP: ispTelecom}
	a := "https://a.example.test"

	probeRun(t, d, fj, a, false, []string{"u1", "u2"}, []int{0, 0}, now)
	probeRun(t, d, gd, a, false, []string{"u3"}, []int{1}, now)
	if !rdb.HExists(ctx, domainScoreKeyFor(a), domainScoreFieldAll).Val() {
		t.Fatal("全国 field 应有分数")
	}
	for _, field := range []string{"福建", "福建|电信", "广东", "广东|电信"} {
		if rdb.HExists(ctx, domainScoreKeyFor(a), field).Val() {
			t.Fatalf("field %q 不该存在", field)
		}
	}
	probeRead(t, d, a, fj, 0.7, now)
}

// 完成标准 (e)：细分级（省、省|运营商）4 个账号不计分，第 5 个才计；全国一级第 3 个就计。成功与失败同样。
func TestApplyProbeRegionalNeedsFiveAccounts(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	ctx := context.Background()
	now := probeTestNow
	fj := domainRegion{Province: "福建", ISP: ispTelecom}
	accts := []string{"u1", "u2", "u3", "u4", "u5"}
	cases := []struct {
		base     string
		ok       bool
		national float64 // 第 5 个之后的全国分
		local    float64 // 第 5 个之后福建电信读到的分
	}{
		{"https://ok.example.test", true, 1.3, 1.5},    // 全国 +0.1×3；两级偏移各 +0.1
		{"https://fail.example.test", false, 1.5, 0.9}, // 起点 2：全国 −0.3、−0.2 封顶；两级偏移各 −0.3
	}
	for _, c := range cases {
		if !c.ok {
			setLevel(t, rdb, c.base, "*", 2, now)
		}
		probeRun(t, d, fj, c.base, c.ok, accts[:4], []int{0, 0, 1, 1}, now)
		for _, field := range []string{"福建", "福建|电信"} {
			if rdb.HExists(ctx, domainScoreKeyFor(c.base), field).Val() {
				t.Fatalf("%s：4 个账号时细分级 %q 不该有记录", c.base, field)
			}
		}
		probeRun(t, d, fj, c.base, c.ok, accts[4:], []int{1}, now)
		for _, field := range []string{"福建", "福建|电信"} {
			if !rdb.HExists(ctx, domainScoreKeyFor(c.base), field).Val() {
				t.Fatalf("%s：第 5 个账号后细分级 %q 应有记录", c.base, field)
			}
		}
		probeRead(t, d, c.base, domainRegion{}, c.national, now)
		probeRead(t, d, c.base, fj, c.local, now)
	}
}

// 细分级的净变动封顶也按级别分开：每一级相对上一级每小时最多偏 0.5。
func TestApplyProbeRegionNetCapPerLevel(t *testing.T) {
	_, rdb := testkit.Redis(t)
	d := newDomainScores(rdb)
	now := probeTestNow
	fj := domainRegion{Province: "福建", ISP: ispTelecom}
	b := "https://b.example.test"
	setLevel(t, rdb, b, "*", 2.5, now)

	// 全国：第 3 个 −0.3、第 4 个 −0.2 后封顶；细分级：第 5 个 −0.3、第 6 个 −0.2 后封顶；第 7 个什么都不动
	probeRun(t, d, fj, b, false, []string{"u1", "u2", "u3", "u4", "u5", "u6", "u7"}, []int{0, 0, 1, 1, 1, 1, 0}, now)
	probeRead(t, d, b, domainRegion{}, 2.0, now)
	probeRead(t, d, b, domainRegion{Province: "福建", ISP: ispUnicom}, 1.5, now)
	probeRead(t, d, b, fj, 1.0, now)
}

func TestPortalOrigin(t *testing.T) {
	cases := map[string]string{
		"https://Portal.Example.test/":        "https://portal.example.test",
		"https://portal.example.test:443/app": "https://portal.example.test",
		"http://portal.example.test:80":       "http://portal.example.test",
		"https://portal.example.test:8443":    "https://portal.example.test:8443",
		"http://portal.example.test:443":      "http://portal.example.test:443",
		" https://portal.example.test ":       "https://portal.example.test",
		"https://[::1]:443":                   "https://[::1]",
		"ftp://portal.example.test":           "",
		"portal.example.test":                 "",
		"https://u:p@portal.example.test":     "",
		"":                                    "",
	}
	for in, want := range cases {
		if got := portalOrigin(in); got != want {
			t.Errorf("portalOrigin(%q)=%q want %q", in, got, want)
		}
	}
}
