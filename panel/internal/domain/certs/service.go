// Package certs 是节点证书的面板集中签发（P2）：DNS 凭据、ACME 设置与账号、证书资源，
// 以及 aegis-admin 里的签发 worker（租约认领 → 凭据预检 → 本地限额对账 → lego DNS-01 → 落库）。
//
// 设计稿：节点证书设计稿 §1–§2、§4.1（10-07 / 10-08 用户定案）。范围：
//   - DNS-01 只做 Cloudflare、阿里云 DNS、腾讯云 DNSPod 三家（lego 只 import 这三个提供方）；
//   - 不做 IP 证书、HTTP-01、TLS-ALPN-01；
//   - 备用 CA（ZeroSSL，要 EAB）可选、默认关；
//   - 到期与失败只在后台显示（列表的 summary 给横幅用），不发 Telegram、不发邮件。
//
// 秘密（DNS 凭据、ACME 账号私钥、证书私钥、ZeroSSL EAB HMAC）只以信封密文落库，AAD 绑定租户与行；
// 读接口的结构体里没有任何密文字段。证书私钥是 PKCS#8 DER，给 P3 的 HPKE 下发直接用。
package certs

import (
	"crypto/x509"
	"log/slog"
	"net/http"
	"time"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// Sealer 加解密落库的秘密；*crypto.Envelope 满足它。
type Sealer interface {
	Seal(plaintext, aad []byte) ([]byte, error)
	Open(sealed, aad []byte) ([]byte, error)
}

// Options 是装配项。生产只用前两项（来自 platform/config 的非生产目录覆盖）；
// 其余只给测试注入（模拟的 DNS 提供方 API、进程内 DNS 服务器、更短的超时）。
type Options struct {
	// DirectoryOverride 非空时所有签发都打到这个 ACME 目录（ca=custom）
	DirectoryOverride string
	// TrustedRoots 是 ACME 目录 HTTPS 证书的信任根；nil 用系统根
	TrustedRoots *x509.CertPool

	// ZeroSSLDirectory 覆盖备用 CA 的目录地址（测试指向第二个 pebble）
	ZeroSSLDirectory string
	// CloudflareBaseURL 覆盖 Cloudflare API 地址（测试指向 httptest）
	CloudflareBaseURL string
	// ProviderHTTPClient 是调 Cloudflare API 的 HTTP 客户端；nil 用缺省
	ProviderHTTPClient *http.Client
	// RecursiveNameservers 非空时 lego 查 SOA 与传播检查都问这几台（测试的进程内 DNS），
	// 并且不再去问权威服务器
	RecursiveNameservers []string
	// PropagationTimeout / PollingInterval 覆盖 DNS 传播检查的时限与间隔
	PropagationTimeout time.Duration
	PollingInterval    time.Duration
	// Now 覆盖时钟（只影响到期等级等纯计算；排程一律用数据库的 now()）
	Now func() time.Time
	// Log 是 lego 的日志去处；nil 用 slog.Default
	Log *slog.Logger
}

// Service 是证书域的入口：后台 CRUD 与 worker 共用。
type Service struct {
	pool   *db.Pool
	sealer Sealer
	opts   Options
}

// NewService 装配证书服务。
func NewService(pool *db.Pool, sealer Sealer, opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.ZeroSSLDirectory == "" {
		opts.ZeroSSLDirectory = zeroSSLDirectory
	}
	if opts.PropagationTimeout <= 0 {
		opts.PropagationTimeout = 4 * time.Minute
	}
	if opts.PollingInterval <= 0 {
		opts.PollingInterval = 10 * time.Second
	}
	configureLegoLog(opts.Log)
	return &Service{pool: pool, sealer: sealer, opts: opts}
}

// Actor 是写操作的主体：后台管理员或 worker 自己（system）。
type Actor struct {
	Kind string // admin / system
	ID   string
}

func (a Actor) auditID() *string {
	if a.ID == "" {
		return nil
	}
	id := a.ID
	return &id
}

func (a Actor) kind() string {
	if a.Kind == "" {
		return "system"
	}
	return a.Kind
}
