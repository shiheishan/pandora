package service

// 订阅域名探测的单元测试（不连库）：票据签发与验签、组指纹、ApplyProbe 的防操纵规则（成功与失败都要 3 个账号）。
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
		n, err := d.ApplyProbe(ctx, acct, "n-"+acct, 5*time.Minute, []probeSignal{{BaseURL: a, OK: true}}, now)
		if err != nil || n != 0 {
			t.Fatalf("%s 报成功 = (%d, %v)，要 (0, nil)", acct, n, err)
		}
	}
	if rdb.HExists(ctx, domainScoreKey, a).Val() {
		t.Fatal("a 不该有分数记录")
	}
	// 第三个不同账号：成功开始加分
	n, err := d.ApplyProbe(ctx, "u3", "n-u3", 5*time.Minute, []probeSignal{{BaseURL: a, OK: true}}, now)
	if err != nil || n != 1 {
		t.Fatalf("u3 报成功 = (%d, %v)，要 (1, nil)", n, err)
	}
	scores, ok := d.Read(ctx, []string{a}, now)
	if !ok || !almost(scores[a], 1.1, 1e-3) {
		t.Fatalf("a = %v（ok=%v），要 1.1", scores[a], ok)
	}

	// 同一 nonce 再报（换账号、换域名）：票据一次性，整次上报不生效，连上报人集合都不进
	n, err = d.ApplyProbe(ctx, "u4", "n-u3", 5*time.Minute, []probeSignal{{BaseURL: b, OK: true}}, now)
	if err != nil || n != 0 {
		t.Fatalf("同票据第二次 = (%d, %v)，要 (0, nil)", n, err)
	}
	if rdb.HExists(ctx, domainScoreKey, b).Val() {
		t.Fatal("b 不该有分数记录")
	}
	if rdb.Exists(ctx, probeRedisName(probeOKKeyPrefix, b, probeHourBucket(now))).Val() != 0 {
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
		if n, err := d.ApplyProbe(ctx, acct, "n-"+acct, time.Minute, []probeSignal{{BaseURL: a, OK: true}}, now); err != nil || n != 0 {
			t.Fatalf("%s = (%d, %v)，要 0", acct, n, err)
		}
	}
	if n, err := d.ApplyProbe(ctx, "u3", "n3", time.Minute, []probeSignal{{BaseURL: a, OK: true}}, now); err != nil || n != 1 {
		t.Fatalf("u3 第一次 = (%d, %v)，要 1", n, err)
	}
	// 同一账号同一域名 30 分钟内换票据再报：去重不生效
	if n, err := d.ApplyProbe(ctx, "u3", "n4", time.Minute, []probeSignal{{BaseURL: a, OK: true}}, now); err != nil || n != 0 {
		t.Fatalf("u3 第二次 = (%d, %v)，要 0", n, err)
	}
	// 过了去重窗口再报（仍在同一小时桶，集合里已有 3 人）：生效
	mr.FastForward(31 * time.Minute)
	now = now.Add(31 * time.Minute)
	if n, err := d.ApplyProbe(ctx, "u3", "n5", time.Minute, []probeSignal{{BaseURL: a, OK: true}}, now); err != nil || n != 1 {
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
		n, err := d.ApplyProbe(ctx, acct, "n-"+acct, time.Minute, []probeSignal{{BaseURL: a, OK: false}}, now)
		if err != nil || n != 0 {
			t.Fatalf("%s 报失败 = (%d, %v)，要 (0, nil)", acct, n, err)
		}
	}
	if rdb.HExists(ctx, domainScoreKey, a).Val() {
		t.Fatal("a 不该有分数记录")
	}

	// 第三个不同账号：失败开始扣分
	n, err := d.ApplyProbe(ctx, "u3", "n-u3", time.Minute, []probeSignal{{BaseURL: a, OK: false}}, now)
	if err != nil || n != 1 {
		t.Fatalf("u3 报失败 = (%d, %v)，要 (1, nil)", n, err)
	}
	scores, ok := d.Read(ctx, []string{a}, now)
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
		n, err := d.ApplyProbe(ctx, acct, "nf-"+acct, time.Minute, []probeSignal{{BaseURL: b, OK: false}}, now)
		if err != nil || n != wantN[i] {
			t.Fatalf("%s 报 b 失败 = (%d, %v)，要 %d", acct, n, err, wantN[i])
		}
	}
	scores, ok := d.Read(ctx, []string{b}, now)
	if !ok || !almost(scores[b], 0.5, 1e-3) {
		t.Fatalf("b = %v（ok=%v），要 0.5（封顶）", scores[b], ok)
	}

	// 成功：u1..u8 依次报 c：前两个不到 3 人不计，u3..u7 各 +0.1，u8 被封顶挡下
	wantS := []int{0, 0, 1, 1, 1, 1, 1, 0}
	for i, acct := range []string{"u1", "u2", "u3", "u4", "u5", "u6", "u7", "u8"} {
		n, err := d.ApplyProbe(ctx, acct, "ns-"+acct, time.Minute, []probeSignal{{BaseURL: c, OK: true}}, now)
		if err != nil || n != wantS[i] {
			t.Fatalf("%s 报 c 成功 = (%d, %v)，要 %d", acct, n, err, wantS[i])
		}
	}
	scores, ok = d.Read(ctx, []string{c}, now)
	if !ok || !almost(scores[c], 1.5, 1e-3) {
		t.Fatalf("c = %v（ok=%v），要 1.5（封顶）", scores[c], ok)
	}

	// 推到下一个小时桶：上报人集合与净变动额度都重置，又要凑够 3 人
	mr.FastForward(time.Hour)
	now = now.Add(time.Hour)
	wantNew := []int{0, 0, 1}
	for i, acct := range []string{"u9", "u10", "u11"} {
		n, err := d.ApplyProbe(ctx, acct, "ns-"+acct, time.Minute, []probeSignal{{BaseURL: c, OK: true}}, now)
		if err != nil || n != wantNew[i] {
			t.Fatalf("新桶 %s 报 c = (%d, %v)，要 %d", acct, n, err, wantNew[i])
		}
	}
}

func TestApplyProbeNilSafe(t *testing.T) {
	ctx := context.Background()
	now := probeTestNow
	sig := []probeSignal{{BaseURL: "https://a.example.test", OK: true}}
	if n, err := (*domainScores)(nil).ApplyProbe(ctx, "u1", "n1", time.Minute, sig, now); n != 0 || err != nil {
		t.Fatalf("nil 接收者 = (%d, %v)，要 (0, nil)", n, err)
	}
	if n, err := newDomainScores(nil).ApplyProbe(ctx, "u1", "n1", time.Minute, sig, now); n != 0 || err != nil {
		t.Fatalf("rdb nil = (%d, %v)，要 (0, nil)", n, err)
	}
	if n, err := newDomainScores(nil).ApplyProbe(ctx, "u1", "n1", time.Minute, nil, now); n != 0 || err != nil {
		t.Fatalf("signals 空 = (%d, %v)，要 (0, nil)", n, err)
	}
}
