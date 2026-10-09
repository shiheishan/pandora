package certs

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNormalizeIdentifiers(t *testing.T) {
	got, msg := normalizeIdentifiers([]string{" Example.COM. ", "*.example.com", "example.com", "", "a.example.net"})
	if msg != "" || !slices.Equal(got, []string{"*.example.com", "a.example.net", "example.com"}) {
		t.Fatalf("normalize = %v %q", got, msg)
	}
	for _, bad := range [][]string{
		nil, {""}, {"192.0.2.1"}, {"[2001:db8::1]"}, {"com"}, {"*.com"}, {"*.co.uk"}, {"a.*.example.com"},
		{"-a.example.com"}, {"a_b.example.com"}, {"exa mple.com"}, {strings.Repeat("a", 64) + ".example.com"},
	} {
		if _, msg := normalizeIdentifiers(bad); msg == "" {
			t.Errorf("%q must be rejected", bad)
		}
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = fmt.Sprintf("n%d.example.com", i)
	}
	if _, msg := normalizeIdentifiers(many); msg == "" {
		t.Fatal("more than 20 identifiers must be rejected")
	}
	if registeredDomain("*.a.b.example.co.uk") != "example.co.uk" || registeredDomain("node.example.com") != "example.com" {
		t.Fatal("registered domain must be eTLD+1")
	}
	if !hasWildcard([]string{"a.example.com", "*.example.com"}) || hasWildcard([]string{"a.example.com"}) {
		t.Fatal("wildcard detection")
	}
	if !underZone("*.a.example.com", "example.com") || underZone("example.net", "example.com") || underZone("badexample.com", "example.com") {
		t.Fatal("zone containment")
	}
	if z, msg := normalizeZone(" Example.com. "); z != "example.com" || msg != "" {
		t.Fatalf("zone = %q %q", z, msg)
	}
	for _, bad := range []string{"", "*.example.com", "com", "192.0.2.1"} {
		if _, msg := normalizeZone(bad); msg == "" {
			t.Errorf("zone %q must be rejected", bad)
		}
	}
}

// 到期等级按寿命缩放（设计稿 §1.4 的表）
func TestExpiryLevelScalesWithLifetime(t *testing.T) {
	day := 24 * time.Hour
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	at := func(life, remaining time.Duration) ExpiryLevel {
		na := now.Add(remaining)
		nb := na.Add(-life)
		return expiryLevel(&nb, &na, now)
	}
	for _, tc := range []struct {
		life, remaining time.Duration
		want            ExpiryLevel
	}{
		{90 * day, 15 * day, ExpiryOK},
		{90 * day, 13 * day, ExpiryWarning},
		{90 * day, 2 * day, ExpiryCritical},
		{45 * day, 12 * day, ExpiryOK},      // 45 天：黄线是 11.25 天
		{45 * day, 11 * day, ExpiryWarning}, //
		{160 * time.Hour, 41 * time.Hour, ExpiryOK},
		{160 * time.Hour, 39 * time.Hour, ExpiryWarning},  // 6.7 天：黄 < 40 小时
		{160 * time.Hour, 15 * time.Hour, ExpiryCritical}, // 红 < 16 小时
		{90 * day, -time.Minute, ExpiryExpired},
	} {
		if got := at(tc.life, tc.remaining); got != tc.want {
			t.Errorf("life=%s remaining=%s: %s, want %s", tc.life, tc.remaining, got, tc.want)
		}
	}
	if expiryLevel(nil, nil, now) != ExpiryNone {
		t.Fatal("no certificate yet is none")
	}
	if alertRank(ExpiryWarning) != 1 || alertRank(ExpiryExpired) != 2 || alertRank(ExpiryOK) != 0 {
		t.Fatal("alert ranks")
	}
	nb := now
	if got := defaultRenewAfter(nb, nb.Add(90*day)); !got.Equal(nb.Add(60 * day)) {
		t.Fatalf("renew after = %s, want 60 days in", got.Sub(nb))
	}
	for n, want := range map[int]time.Duration{1: time.Hour, 2: 2 * time.Hour, 3: 4 * time.Hour, 6: 24 * time.Hour, 20: 24 * time.Hour} {
		if got := failureBackoff(n); got != want {
			t.Errorf("backoff(%d) = %s, want %s", n, got, want)
		}
	}
}

func TestCheckLocalLimits(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	newOnes := func(n int, ids []string, renewal bool) []recentIssuance {
		out := make([]recentIssuance, n)
		for i := range out {
			out[i] = recentIssuance{identifiers: ids, isRenewal: renewal, createdAt: now.Add(-time.Duration(n-i) * time.Hour)}
		}
		return out
	}
	// 每注册域每周 50 张：49 张放行，50 张拦下，退避到第 1 张满 7 天
	var recent []recentIssuance
	for i := range 49 {
		recent = append(recent, recentIssuance{identifiers: []string{fmt.Sprintf("n%d.example.com", i)},
			createdAt: now.Add(-time.Duration(50-i) * time.Hour)})
	}
	if d := checkLocalLimits(recent, []string{"new.example.com"}, false, false, now); d.blocked {
		t.Fatalf("49 issued must still allow one more: %+v", d)
	}
	recent = append(recent, recentIssuance{identifiers: []string{"n49.example.com"}, createdAt: now.Add(-time.Hour)})
	d := checkLocalLimits(recent, []string{"new.example.com"}, false, false, now)
	if !d.blocked || !d.retryAt.Equal(now.Add(-50*time.Hour).Add(limitWindow)) || !strings.Contains(d.reason, "example.com") {
		t.Fatalf("50 issued must block until the oldest ages out: %+v", d)
	}
	// 别的注册域、续期（标识集合不变）、ARI replaces 都不受这一条影响
	if d := checkLocalLimits(recent, []string{"new.example.net"}, false, false, now); d.blocked {
		t.Fatal("other registered domain must not be blocked")
	}
	if d := checkLocalLimits(recent, []string{"new.example.com"}, true, false, now); d.blocked {
		t.Fatal("renewals do not count against the per-domain limit")
	}
	// 续期不计入每域新证书：50 张续期不拦首次签发
	if d := checkLocalLimits(newOnes(50, []string{"r.example.com"}, true), []string{"x.example.com"}, false, false, now); d.blocked {
		t.Fatal("renewals must not count as new certificates")
	}
	// 完全相同的标识集合每周 5 张：续期也拦，ARI replaces 不拦
	same := newOnes(5, []string{"a.example.com"}, true)
	if d := checkLocalLimits(same, []string{"a.example.com"}, true, false, now); !d.blocked {
		t.Fatal("five identical sets must block a sixth")
	}
	if d := checkLocalLimits(same, []string{"a.example.com"}, true, true, now); d.blocked {
		t.Fatal("ARI-coordinated renewals are exempt from all limits")
	}
	if d := checkLocalLimits(newOnes(4, []string{"a.example.com"}, true), []string{"a.example.com"}, true, false, now); d.blocked {
		t.Fatal("four identical sets allow a fifth")
	}
}

func TestMergeSecretKeepsAbsentFields(t *testing.T) {
	old := map[string]string{"access_key_id": "LTAIold0001", "access_key_secret": "s-old"}
	got, bad := mergeSecret(ProviderAliDNS, old, map[string]string{"access_key_secret": " s-new "})
	if bad != nil || got["access_key_id"] != "LTAIold0001" || got["access_key_secret"] != "s-new" {
		t.Fatalf("merge = %v %v", got, bad)
	}
	if _, bad := mergeSecret(ProviderAliDNS, old, map[string]string{"access_key_secret": ""}); bad["secret.access_key_secret"] == "" {
		t.Fatal("explicit empty secret must be rejected")
	}
	if _, bad := mergeSecret(ProviderCloudflare, nil, map[string]string{"api_token": "t", "zone_token": "z"}); bad["secret.zone_token"] == "" {
		t.Fatal("unknown secret field must be rejected")
	}
	if _, bad := mergeSecret(ProviderTencentCloud, nil, map[string]string{"secret_id": "AKID"}); bad["secret.secret_key"] == "" {
		t.Fatal("missing required field on create must be rejected")
	}
	if _, bad := mergeSecret("route53", nil, nil); bad["provider"] == "" {
		t.Fatal("unknown provider must be rejected")
	}
	if secretHint(ProviderCloudflare, map[string]string{"api_token": "aaaa-1234"}) != "1234" ||
		secretHint(ProviderTencentCloud, map[string]string{"secret_id": "abc"}) != "" {
		t.Fatal("hint is the last four characters of the first field, empty for short values")
	}
}

// 读形状里不能有任何密文或私钥字段（JSON 键名）
func TestReadShapesCarryNoSecrets(t *testing.T) {
	for _, v := range []any{DNSCredential{}, Certificate{}, Version{}, Order{}, ACMESettings{}, VerifyResult{},
		CertificateList{}, CertificateDetail{}} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"sealed", "secret\"", "private_key", "token\"", "hmac\"", "api_token", "access_key"} {
			if strings.Contains(string(raw), banned) {
				t.Errorf("%s JSON contains %q: %s", reflect.TypeOf(v).Name(), banned, raw)
			}
		}
	}
}

func TestClassifyFailureAndWindow(t *testing.T) {
	if f := classifyFailure(errors.New("cloudflare: failed to create TXT record: 403"), &retryRecorder{}); f.code != "dns_provider_error" || f.rateLimited {
		t.Fatalf("dns provider error = %+v", f)
	}
	if f := classifyFailure(errors.New("acme: error: 403 :: urn:ietf:params:acme:error:unauthorized"), &retryRecorder{}); f.code != "acme_error" {
		t.Fatalf("acme error = %+v", f)
	}
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	start, end := now.Add(time.Hour), now.Add(3*time.Hour)
	for range 50 {
		if got := pickInWindow(start, end, now); got.Before(start) || !got.Before(end) {
			t.Fatalf("pick %s outside [%s, %s)", got, start, end)
		}
	}
	if got := pickInWindow(now.Add(-2*time.Hour), now.Add(-time.Hour), now); !got.Equal(now) {
		t.Fatal("a window in the past renews now")
	}
}

// 阿里云 SDK 的错误类型不一：值类型、指针类型、只有文本，都要认出凭据错误
func TestAliAuthRejectedRecognisesSDKErrors(t *testing.T) {
	text := errors.New("SDKError:\n   StatusCode: 404\n   Code: InvalidAccessKeyId.NotFound\n   Message: code: 404, Specified access key is not found.")
	if detail, ok := aliAuthRejected(fmt.Errorf("alicloud: %w", text)); !ok || !strings.HasPrefix(detail, "InvalidAccessKeyId.NotFound") {
		t.Fatalf("text-only SDK error: %q %v", detail, ok)
	}
	if _, ok := aliAuthRejected(errors.New("SDKError:\n   Code: InternalError\n")); ok {
		t.Fatal("server errors are not credential errors")
	}
	if _, ok := aliAuthRejected(errors.New("dial tcp: timeout")); ok {
		t.Fatal("network errors are not credential errors")
	}
}
