package certs

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/acme"
	"github.com/go-acme/lego/v4/acme/api"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	legolog "github.com/go-acme/lego/v4/log"
	"github.com/go-acme/lego/v4/registration"
)

// CA 取值（certificate_versions.ca / acme_accounts.ca）。
const (
	CALetsEncrypt        = "letsencrypt"
	CALetsEncryptStaging = "letsencrypt_staging"
	CAZeroSSL            = "zerossl"
	CACustom             = "custom"

	zeroSSLDirectory = "https://acme.zerossl.com/v2/DV90"
)

// caTarget 是一次签发要找的 CA。
type caTarget struct {
	CA        string
	Directory string
	eabKID    string
	eabHMAC   string
}

// primaryTarget 是首选 CA：非生产的目录覆盖 > 测试环境的 Let's Encrypt staging > Let's Encrypt。
func (s *Service) primaryTarget(cfg acmeConfig) caTarget {
	switch {
	case s.opts.DirectoryOverride != "":
		return caTarget{CA: CACustom, Directory: s.opts.DirectoryOverride}
	case cfg.UseStaging:
		return caTarget{CA: CALetsEncryptStaging, Directory: lego.LEDirectoryStaging}
	}
	return caTarget{CA: CALetsEncrypt, Directory: lego.LEDirectoryProduction}
}

// fallbackTarget 是备用 CA（ZeroSSL，要 EAB）；没启用或没配齐时为 nil（用户 10-08 定默认关）。
func (s *Service) fallbackTarget(cfg acmeConfig) *caTarget {
	if !cfg.ZeroSSLEnabled || cfg.ZeroSSLEABKID == "" || cfg.eabHMAC == "" {
		return nil
	}
	return &caTarget{CA: CAZeroSSL, Directory: s.opts.ZeroSSLDirectory, eabKID: cfg.ZeroSSLEABKID, eabHMAC: cfg.eabHMAC}
}

// targetForCA 找回签出某个版本的 CA（ARI 要问签它的那家）。
func (s *Service) targetForCA(ca string, cfg acmeConfig) (caTarget, bool) {
	switch ca {
	case CACustom:
		return caTarget{CA: CACustom, Directory: s.opts.DirectoryOverride}, s.opts.DirectoryOverride != ""
	case CALetsEncryptStaging:
		return caTarget{CA: ca, Directory: lego.LEDirectoryStaging}, true
	case CALetsEncrypt:
		return caTarget{CA: ca, Directory: lego.LEDirectoryProduction}, true
	case CAZeroSSL:
		return caTarget{CA: ca, Directory: s.opts.ZeroSSLDirectory}, true
	}
	return caTarget{}, false
}

//------------------------------------------------------------------------------
// lego 的装配
//------------------------------------------------------------------------------

var legoLogOnce sync.Once

// configureLegoLog 把 lego 的包级日志转到 slog（只装一次）。lego 的 Fatal 只在它自己的命令行里用，
// 这里一律当错误记，不退出进程。
func configureLegoLog(log *slog.Logger) {
	legoLogOnce.Do(func() { legolog.Logger = legoLogger{log: log} })
}

type legoLogger struct{ log *slog.Logger }

func (l legoLogger) Fatal(args ...any)   { l.log.Error("lego", "msg", fmt.Sprint(args...)) }
func (l legoLogger) Fatalln(args ...any) { l.log.Error("lego", "msg", fmt.Sprint(args...)) }
func (l legoLogger) Fatalf(format string, args ...any) {
	l.log.Error("lego", "msg", fmt.Sprintf(format, args...))
}
func (l legoLogger) Print(args ...any)                 { l.emit(fmt.Sprint(args...)) }
func (l legoLogger) Println(args ...any)               { l.emit(fmt.Sprint(args...)) }
func (l legoLogger) Printf(format string, args ...any) { l.emit(fmt.Sprintf(format, args...)) }

// emit 按 lego 自己打的前缀分级：[WARN] → Warn，[INFO] → Info，其余 Debug。
func (l legoLogger) emit(msg string) {
	switch {
	case strings.HasPrefix(msg, "[WARN] "):
		l.log.Warn("lego", "msg", strings.TrimPrefix(msg, "[WARN] "))
	case strings.HasPrefix(msg, "[INFO] "):
		l.log.Info("lego", "msg", strings.TrimPrefix(msg, "[INFO] "))
	default:
		l.log.Debug("lego", "msg", msg)
	}
}

// legoUser 是 lego 要的账号接口。
type legoUser struct {
	email string
	key   crypto.PrivateKey
	uri   string
}

func (u *legoUser) GetEmail() string { return u.email }
func (u *legoUser) GetRegistration() *registration.Resource {
	if u.uri == "" {
		return nil
	}
	return &registration.Resource{URI: u.uri}
}
func (u *legoUser) GetPrivateKey() crypto.PrivateKey { return u.key }

// retryRecorder 记下 CA 回 429 / 503 时的 Retry-After：lego 的错误里不带这个头，退避要用它。
type retryRecorder struct {
	base http.RoundTripper
	mu   sync.Mutex
	last time.Duration
}

func (r *retryRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.base.RoundTrip(req)
	if err == nil && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) {
		if d, perr := api.ParseRetryAfter(resp.Header.Get("Retry-After")); perr == nil && d > 0 {
			r.mu.Lock()
			r.last = d
			r.mu.Unlock()
		}
	}
	return resp, err
}

func (r *retryRecorder) retryAfter() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// acmeHTTPClient 是找 CA 的 HTTP 客户端：缺省传输（含代理环境），信任根可被非生产覆盖。
func (s *Service) acmeHTTPClient(rec *retryRecorder) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: s.opts.TrustedRoots}
	tr.ResponseHeaderTimeout = 30 * time.Second
	rec.base = tr
	return &http.Client{Timeout: 2 * time.Minute, Transport: rec}
}

func (s *Service) newLegoClient(target caTarget, user *legoUser, keyType string, rec *retryRecorder) (*lego.Client, error) {
	cfg := &lego.Config{
		CADirURL:   target.Directory,
		User:       user,
		UserAgent:  "pandora-panel",
		HTTPClient: s.acmeHTTPClient(rec),
		Certificate: lego.CertificateConfig{
			KeyType: legoKeyType(keyType),
			Timeout: 90 * time.Second,
		},
	}
	return lego.NewClient(cfg)
}

func legoKeyType(k string) certcrypto.KeyType {
	if k == KeyRSA2048 {
		return certcrypto.RSA2048
	}
	return certcrypto.EC256
}

func newPrivateKey(keyType string) (crypto.Signer, error) {
	if keyType == KeyRSA2048 {
		return rsa.GenerateKey(rand.Reader, 2048)
	}
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// registerAccount 在 CA 注册一个新账号（同意服务条款；ZeroSSL 带 EAB），返回账号私钥与 URL。
func (s *Service) registerAccount(target caTarget, email string) (crypto.Signer, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", err
	}
	user := &legoUser{email: email, key: key}
	client, err := s.newLegoClient(target, user, KeyECDSAP256, &retryRecorder{})
	if err != nil {
		return nil, "", err
	}
	var reg *registration.Resource
	if target.eabKID != "" {
		reg, err = client.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{
			TermsOfServiceAgreed: true, Kid: target.eabKID, HmacEncoded: target.eabHMAC})
	} else {
		reg, err = client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	}
	if err != nil {
		return nil, "", err
	}
	if reg == nil || !strings.HasPrefix(reg.URI, "https://") {
		return nil, "", errors.New("CA 没有返回账号地址")
	}
	return key, reg.URI, nil
}

// issueRequest 是一次签发的全部输入。
type issueRequest struct {
	target      caTarget
	accountKey  crypto.PrivateKey
	accountURL  string
	email       string
	provider    dnsProvider
	identifiers []string
	keyType     string
	replaces    string
}

// issued 是签出来的证书：私钥是 PKCS#8 DER（P3 的 HPKE 下发直接用）。
type issued struct {
	keyDER      []byte
	chainPEM    []byte
	leaf        *x509.Certificate
	chainSHA256 []byte
	pubSHA256   []byte
	certURL     string
	ariCertID   string
}

// obtain 跑一次 lego DNS-01 签发。lego 没有 context：调用方在另一个协程里跑它，停机时不等。
func (s *Service) obtain(req issueRequest, rec *retryRecorder) (*issued, error) {
	user := &legoUser{email: req.email, key: req.accountKey, uri: req.accountURL}
	client, err := s.newLegoClient(req.target, user, req.keyType, rec)
	if err != nil {
		return nil, err
	}
	prov, err := req.provider.lego(s.opts.PropagationTimeout, s.opts.PollingInterval)
	if err != nil {
		return nil, err
	}
	var dnsOpts []dns01.ChallengeOption
	if len(s.opts.RecursiveNameservers) > 0 {
		dnsOpts = append(dnsOpts, dns01.AddRecursiveNameservers(s.opts.RecursiveNameservers),
			dns01.DisableAuthoritativeNssPropagationRequirement(), dns01.RecursiveNSsPropagationRequirement())
	}
	if err := client.Challenge.SetDNS01Provider(prov, dnsOpts...); err != nil {
		return nil, err
	}
	key, err := newPrivateKey(req.keyType)
	if err != nil {
		return nil, err
	}
	obtainReq := certificate.ObtainRequest{Domains: req.identifiers, PrivateKey: key, Bundle: true,
		ReplacesCertID: req.replaces}
	// 带 replaces 的单被 CA 以 alreadyReplaced 拒绝时，lego 自己会去掉 replaces 重下一单（RFC 9773 §5）
	res, err := client.Certificate.Obtain(obtainReq)
	if err != nil {
		return nil, err
	}
	return buildIssued(key, res.Certificate, res.CertURL)
}

func buildIssued(key crypto.Signer, chain []byte, certURL string) (*issued, error) {
	block, _ := pem.Decode(chain)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("CA 返回的证书链不是 PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析叶子证书: %w", err)
	}
	pub, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return nil, err
	}
	leafPub, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil || !bytes.Equal(pub, leafPub) {
		return nil, errors.New("CA 返回的证书与私钥不匹配")
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	chainSum := sha256.Sum256(chain)
	pubSum := sha256.Sum256(pub)
	ari, _ := certificate.MakeARICertID(leaf)
	return &issued{keyDER: der, chainPEM: chain, leaf: leaf, chainSHA256: chainSum[:], pubSHA256: pubSum[:],
		certURL: certURL, ariCertID: ari}, nil
}

// renewalInfo 问 CA 这张证书的 ARI 续期窗口。CA 不支持 ARI 时回 api.ErrNoARI。
func (s *Service) renewalInfo(target caTarget, leaf *x509.Certificate) (*certificate.RenewalInfoResponse, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	client, err := s.newLegoClient(target, &legoUser{key: key}, KeyECDSAP256, &retryRecorder{})
	if err != nil {
		return nil, err
	}
	return client.Certificate.GetRenewalInfo(certificate.RenewalInfoRequest{Cert: leaf})
}

// serialHex 是证书序列号的十六进制小写写法（certificate_versions.serial）。
func serialHex(leaf *x509.Certificate) string {
	return hex.EncodeToString(leaf.SerialNumber.Bytes())
}

// acmeFailure 是一次签发失败的归类。
type acmeFailure struct {
	code        string
	detail      string
	rateLimited bool
	retryAfter  time.Duration
}

// classifyFailure 把 lego 的错误归成退避用的类别：CA 限流（读 Retry-After，不计入连续失败）、
// DNS 提供方出错、其余 ACME 错误。
func classifyFailure(err error, rec *retryRecorder) acmeFailure {
	f := acmeFailure{code: "acme_error", detail: providerErrorText(err)}
	var prob *acme.ProblemDetails
	if errors.As(err, &prob) && (prob.Type == acme.RateLimitedErr || prob.HTTPStatus == http.StatusTooManyRequests) {
		f.code, f.rateLimited = "rate_limited", true
		f.retryAfter = rec.retryAfter()
		return f
	}
	msg := err.Error()
	for _, p := range []string{"cloudflare:", "alicloud:", "tencentcloud:"} {
		if strings.Contains(msg, p) {
			f.code = "dns_provider_error"
			return f
		}
	}
	if strings.Contains(msg, "propagation") || strings.Contains(msg, "time limit exceeded") {
		f.code = "dns_propagation_timeout"
	}
	return f
}
