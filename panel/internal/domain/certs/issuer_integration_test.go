package certs

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/crypto"
)

// 不连数据库的签发集成测试：进程内 pebble + 进程内 DNS + 三家提供方的模拟 API，
// 走真实的 lego 提供方、预检与签发路径。数据库那一半（租约、落库、退避）在 worker 的 PG18 用例里。

func testSealer(t *testing.T) *crypto.Envelope {
	t.Helper()
	env, err := crypto.NewEnvelope([]byte("certs-test-envelope-key-32bytes!"))
	if err != nil {
		t.Fatal(err)
	}
	return env
}

type issueEnv struct {
	dns *fakeDNS
	ca  *fakeCA
	cf  *fakeProvider
	ali *fakeProvider
	tc  *fakeProvider
	svc *Service
}

const (
	testZone      = "example.com"
	testOtherZone = "example.net"
	cfToken       = "cf-test-token-0001"
	aliKeyID      = "LTAItestkey0001"
	aliKeySecret  = "ali-test-secret"
	tcSecretID    = "AKIDtestsecret0001"
	tcSecretKey   = "tc-test-key"
)

func newIssueEnv(t *testing.T, opts Options) *issueEnv {
	t.Helper()
	e := &issueEnv{dns: startDNS(t, testZone, testOtherZone)}
	e.ca = startCA(t, e.dns.addr, false)
	e.cf = newFakeProvider(e.dns, cfToken, testZone, testOtherZone)
	e.ali = newFakeProvider(e.dns, aliKeyID, testZone)
	e.tc = newFakeProvider(e.dns, tcSecretID, testZone)
	useTencent(t, e.tc.tencent())
	if aliMITM {
		useAliDNS(t, e.ali.alidns())
	}
	opts.DirectoryOverride = e.ca.dir
	opts.TrustedRoots = e.ca.roots
	opts.CloudflareBaseURL = fakeCloudflareBase
	opts.ProviderHTTPClient = inMemoryClient(e.cf.cloudflare())
	opts.RecursiveNameservers = []string{e.dns.addr}
	opts.PropagationTimeout = 30 * time.Second
	opts.PollingInterval = 100 * time.Millisecond
	opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	e.svc = NewService(nil, testSealer(t), opts)
	return e
}

func (e *issueEnv) provider(t *testing.T, name string, secret map[string]string) dnsProvider {
	t.Helper()
	p, err := e.svc.newDNSProvider(name, secret)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func goodSecret(provider string) map[string]string {
	switch provider {
	case ProviderAliDNS:
		return map[string]string{"access_key_id": aliKeyID, "access_key_secret": aliKeySecret}
	case ProviderTencentCloud:
		return map[string]string{"secret_id": tcSecretID, "secret_key": tcSecretKey}
	}
	return map[string]string{"api_token": cfToken}
}

func badSecret(provider string) map[string]string {
	switch provider {
	case ProviderAliDNS:
		return map[string]string{"access_key_id": "LTAIwrong", "access_key_secret": "x"}
	case ProviderTencentCloud:
		return map[string]string{"secret_id": "AKIDwrong", "secret_key": "x"}
	}
	return map[string]string{"api_token": "wrong-token"}
}

func (e *issueEnv) fake(provider string) *fakeProvider {
	switch provider {
	case ProviderAliDNS:
		return e.ali
	case ProviderTencentCloud:
		return e.tc
	}
	return e.cf
}

func (e *issueEnv) issue(t *testing.T, provider string, ids []string, replaces string) (*issued, error) {
	t.Helper()
	target := caTarget{CA: CACustom, Directory: e.ca.dir}
	key, url, err := e.svc.registerAccount(target, "")
	if err != nil {
		t.Fatalf("register account: %v", err)
	}
	return e.issueWith(provider, ids, replaces, key, url)
}

func (e *issueEnv) issueWith(provider string, ids []string, replaces string, key any, url string) (*issued, error) {
	p, _ := e.svc.newDNSProvider(provider, goodSecret(provider))
	return e.svc.obtain(issueRequest{target: caTarget{CA: CACustom, Directory: e.ca.dir}, accountKey: key,
		accountURL: url, provider: p, identifiers: ids, keyType: KeyECDSAP256, replaces: replaces}, &retryRecorder{})
}

// 三家提供方：预检（读 + 写 TXT 并删掉）、凭据错拒绝、缺写权限拒绝、DNS-01 签出通配符证书且 TXT 都清掉
func TestProvidersPreflightAndIssue(t *testing.T) {
	for _, provider := range []string{ProviderCloudflare, ProviderAliDNS, ProviderTencentCloud} {
		t.Run(provider, func(t *testing.T) {
			e := newIssueEnv(t, Options{})
			if provider == ProviderAliDNS {
				useAliDNS(t, e.ali.alidns()) // 非 Linux 在这里跳过
			}
			ctx := context.Background()
			fake := e.fake(provider)

			res, err := e.provider(t, provider, goodSecret(provider)).check(ctx, testZone, true)
			if err != nil {
				t.Fatalf("check with good credential: %v", err)
			}
			if provider == ProviderCloudflare && res.VisibleZones != 2 {
				t.Fatalf("visible zones = %d, want 2", res.VisibleZones)
			}
			if _, writes, live := fake.count(); writes != 1 || live != 0 || e.dns.txtCount() != 0 {
				t.Fatalf("write check must create and delete exactly one TXT: writes=%d live=%d dns=%d", writes, live, e.dns.txtCount())
			}

			var credErr *credentialError
			if _, err := e.provider(t, provider, badSecret(provider)).check(ctx, testZone, false); !errors.As(err, &credErr) {
				t.Fatalf("bad credential must be rejected as a credential error, got %v", err)
			}
			if _, err := e.provider(t, provider, goodSecret(provider)).check(ctx, "example.org", false); !errors.As(err, &credErr) {
				t.Fatalf("invisible zone must be a credential error, got %v", err)
			}
			fake.readOnly = true
			if _, err := e.provider(t, provider, goodSecret(provider)).check(ctx, testZone, false); err != nil {
				t.Fatalf("read-only credential passes the read check: %v", err)
			}
			if _, err := e.provider(t, provider, goodSecret(provider)).check(ctx, testZone, true); !errors.As(err, &credErr) {
				t.Fatalf("read-only credential must fail the write check, got %v", err)
			}
			fake.readOnly = false

			iss, err := e.issue(t, provider, []string{"*.example.com", "example.com"}, "")
			if err != nil {
				t.Fatalf("issue via %s: %v", provider, err)
			}
			if !slices.Contains(iss.leaf.DNSNames, "*.example.com") || !slices.Contains(iss.leaf.DNSNames, "example.com") {
				t.Fatalf("leaf SANs = %v", iss.leaf.DNSNames)
			}
			if _, err := x509.ParsePKCS8PrivateKey(iss.keyDER); err != nil {
				t.Fatalf("private key must be PKCS#8 DER: %v", err)
			}
			if iss.ariCertID == "" || len(iss.chainSHA256) != 32 || len(iss.pubSHA256) != 32 {
				t.Fatalf("issued metadata incomplete: ari=%q", iss.ariCertID)
			}
			if _, _, live := fake.count(); live != 0 || e.dns.txtCount() != 0 {
				t.Fatalf("challenge TXT records must be cleaned up: provider=%d dns=%d", live, e.dns.txtCount())
			}
		})
	}
}

// 续期带 ARI replaces；CA 说「已经被替换过」时不带 replaces 再下一单；ARI 窗口查得到
func TestIssueRenewalWithARIReplaces(t *testing.T) {
	e := newIssueEnv(t, Options{})
	target := caTarget{CA: CACustom, Directory: e.ca.dir}
	key, url, err := e.svc.registerAccount(target, "")
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"node1.example.com"}
	first, err := e.issueWith(ProviderCloudflare, ids, "", key, url)
	if err != nil {
		t.Fatal(err)
	}
	info, err := e.svc.renewalInfo(target, first.leaf)
	if err != nil || info.SuggestedWindow.Start.IsZero() || !info.SuggestedWindow.End.After(info.SuggestedWindow.Start) {
		t.Fatalf("ARI window = %+v, err=%v", info, err)
	}
	second, err := e.issueWith(ProviderCloudflare, ids, first.ariCertID, key, url)
	if err != nil {
		t.Fatalf("renewal with replaces: %v", err)
	}
	_, orders, replaces := e.ca.stats()
	if orders != 2 || replaces[1] != first.ariCertID {
		t.Fatalf("renewal must send replaces=%q, got orders=%d replaces=%q", first.ariCertID, orders, replaces)
	}
	if second.leaf.SerialNumber.Cmp(first.leaf.SerialNumber) == 0 {
		t.Fatal("renewal returned the same certificate")
	}
	// 同一张旧证书再被 replaces 一次：CA 回 alreadyReplaced（409），lego 去掉 replaces 重下一单，照样签出
	if _, err := e.issueWith(ProviderCloudflare, ids, first.ariCertID, key, url); err != nil {
		t.Fatalf("already-replaced retry: %v", err)
	}
	if _, orders, replaces = e.ca.stats(); orders != 4 || replaces[2] != first.ariCertID || replaces[3] != "" {
		t.Fatalf("already-replaced must be retried without replaces: orders=%d replaces=%q", orders, replaces)
	}
}

// CA 回 429：错误归为限流，Retry-After 记下来做退避，不计入连续失败
func TestIssueRateLimitedRecordsRetryAfter(t *testing.T) {
	e := newIssueEnv(t, Options{})
	target := caTarget{CA: CACustom, Directory: e.ca.dir}
	key, url, err := e.svc.registerAccount(target, "")
	if err != nil {
		t.Fatal(err)
	}
	e.ca.set429("120")
	p, _ := e.svc.newDNSProvider(ProviderCloudflare, goodSecret(ProviderCloudflare))
	rec := &retryRecorder{}
	_, err = e.svc.obtain(issueRequest{target: target, accountKey: key, accountURL: url, provider: p,
		identifiers: []string{"rl.example.com"}, keyType: KeyECDSAP256}, rec)
	if err == nil {
		t.Fatal("429 must fail the order")
	}
	f := classifyFailure(err, rec)
	if !f.rateLimited || f.code != "rate_limited" || f.retryAfter != 120*time.Second {
		t.Fatalf("classification = %+v", f)
	}
	if _, writes, _ := e.cf.count(); writes != 0 {
		t.Fatalf("rate-limited new order must not touch DNS, writes=%d", writes)
	}
}

// 备用 CA 要 EAB：带上 KID / HMAC 才能注册
func TestRegisterWithExternalAccountBinding(t *testing.T) {
	e := newIssueEnv(t, Options{})
	zs := startCA(t, e.dns.addr, true)
	target := caTarget{CA: CAZeroSSL, Directory: zs.dir}
	if _, _, err := e.svc.registerAccount(target, "ops@example.com"); err == nil {
		t.Fatal("registration without EAB must fail on an EAB-only CA")
	}
	target.eabKID, target.eabHMAC = zs.eabKeyID, zs.eabHMACB64
	if _, url, err := e.svc.registerAccount(target, "ops@example.com"); err != nil || url == "" {
		t.Fatalf("EAB registration: url=%q err=%v", url, err)
	}
}
