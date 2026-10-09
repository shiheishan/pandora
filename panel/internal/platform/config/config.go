// Package config 从环境变量加载配置。
//
// 设计取舍：不引入配置框架。三个网关的配置项有限且必须在启动时全部校验通过，
// 「缺一项就拒绝启动」比「运行时读到零值」安全得多（NFR-006：错误配置不能未经验证发布）。
package config

import (
	"encoding/base64"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Domain 是 API 令牌域：public、admin、node 各有一个网关，各域令牌互不相通（EXT-001）。
type Domain string

const (
	DomainPublic Domain = "public"
	DomainAdmin  Domain = "admin"
	DomainNode   Domain = "node"
)

type Config struct {
	Env string

	DatabaseURL string
	RedisURL    string

	PublicAddr string
	AdminAddr  string
	NodeAddr   string

	// PublicBaseURL 是用户门户的对外地址，用于拼支付回调地址与跳转地址。
	// 支付渠道必须能从公网回访到它，所以不能用 127.0.0.1。
	PublicBaseURL string

	// MasterKey 是信封加密的根密钥（SEC-010）。生产必须换成 KMS/Vault。
	MasterKey []byte

	// 每个域一把独立的令牌签名密钥。密钥不同 =
	// 拿 public 域的令牌去敲 admin 域，签名校验直接失败（EXT-001 验收）。
	JWTSecrets map[Domain][]byte

	// ConfigSigningSeed 是订阅配置与节点配置的 Ed25519 签名种子（CLI-004 / AGT-007）。
	ConfigSigningSeed []byte
	// PreviousConfigSigningSeed is only present during an explicit rolling
	// key transition. It certifies the current public key and must be removed
	// after all nodes have adopted the new key.
	PreviousConfigSigningSeed []byte

	// 限流默认档位（SEC-002）
	RateLimitPerIPPerMinute      int
	RateLimitPerAccountPerMinute int
	// RateLimitAuthPerMinute 是登录/注册这类认证入口的每 IP 每分钟上限（SEC-005）。
	// 默认值按生产收紧；集成测试会主动打撞库用例，需要在测试环境调高，
	// 否则测试自己就会把配额耗尽，后续断言全部被 429 污染。
	RateLimitAuthPerMinute int

	// 令牌有效期
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration

	ShutdownTimeout time.Duration

	// PprofAddrs 是各网关 pprof 诊断端口的监听地址（见 pprof.go），键为网关所属的域；
	// 未出现的网关不开 pprof。值已校验为回环 IP 字面量并规范化。
	PprofAddrs map[Domain]string

	// Deployment 是部署侧可缺省的项（备份目录、GeoIP、NativeCore 发布绑定）。
	Deployment

	// Runtime 是进程级资源上限（连接池、口令哈希并发），见 runtime.go。
	Runtime

	// NodePorts 是节点端口的保留表（node_ports.go）。
	NodePorts NodePorts

	// SingboxTemplate 是 sing-box 订阅模板的可配项（subscription_template.go）。
	SingboxTemplate SingboxTemplate

	// ACME 是节点证书签发的非生产目录覆盖（acme.go）。
	ACME ACME
}

func Load() (*Config, error) {
	environment, err := parseEnvironment(env("AEGIS_ENV", "development"))
	if err != nil {
		return nil, err
	}
	accessTokenTTL, err := strictEnvDuration("AEGIS_ACCESS_TOKEN_TTL", 30*24*time.Hour)
	if err != nil {
		return nil, err
	}
	refreshTokenTTL, err := strictEnvDuration("AEGIS_REFRESH_TOKEN_TTL", 30*24*time.Hour)
	if err != nil {
		return nil, err
	}
	if err := validateTokenTTLs(accessTokenTTL, refreshTokenTTL); err != nil {
		return nil, err
	}
	shutdownTimeout, err := strictPositiveEnvDuration("AEGIS_SHUTDOWN_TIMEOUT", 20*time.Second)
	if err != nil {
		return nil, err
	}
	rateLimitIP, err := strictPositiveEnvInt("AEGIS_RL_IP_PER_MIN", 120)
	if err != nil {
		return nil, err
	}
	rateLimitAccount, err := strictPositiveEnvInt("AEGIS_RL_ACCOUNT_PER_MIN", 300)
	if err != nil {
		return nil, err
	}
	rateLimitAuth, err := strictPositiveEnvInt("AEGIS_RL_AUTH_PER_MIN", 10)
	if err != nil {
		return nil, err
	}

	c := &Config{
		Env:             environment,
		DatabaseURL:     os.Getenv("AEGIS_DATABASE_URL"),
		RedisURL:        os.Getenv("AEGIS_REDIS_URL"),
		PublicAddr:      env("AEGIS_PUBLIC_ADDR", "127.0.0.1:9000"),
		AdminAddr:       env("AEGIS_ADMIN_ADDR", "127.0.0.1:9001"),
		NodeAddr:        env("AEGIS_NODE_ADDR", "127.0.0.1:9003"),
		PublicBaseURL:   strings.TrimRight(env("AEGIS_PUBLIC_BASE_URL", "http://127.0.0.1:9000"), "/"),
		JWTSecrets:      map[Domain][]byte{},
		AccessTokenTTL:  accessTokenTTL,
		RefreshTokenTTL: refreshTokenTTL,
		ShutdownTimeout: shutdownTimeout,

		RateLimitPerIPPerMinute:      rateLimitIP,
		RateLimitPerAccountPerMinute: rateLimitAccount,
		RateLimitAuthPerMinute:       rateLimitAuth,

		Deployment: loadDeployment(),
	}
	if c.PprofAddrs, err = loadPprofAddrs([]string{c.PublicAddr, c.AdminAddr, c.NodeAddr}); err != nil {
		return nil, err
	}
	if c.Runtime, err = loadRuntime(); err != nil {
		return nil, err
	}
	if c.NodePorts, err = loadNodePorts(c.PublicBaseURL); err != nil {
		return nil, err
	}
	if c.SingboxTemplate, err = loadSingboxTemplate(); err != nil {
		return nil, err
	}
	if c.ACME, err = loadACME(c.IsProduction()); err != nil {
		return nil, err
	}
	if c.IsProduction() {
		if _, err := c.CanonicalPublicOrigin(); err != nil {
			return nil, err
		}
	}

	var missing []string
	if c.DatabaseURL == "" {
		missing = append(missing, "AEGIS_DATABASE_URL")
	}
	if c.RedisURL == "" {
		missing = append(missing, "AEGIS_REDIS_URL")
	}

	err = nil
	if c.MasterKey, err = requireKey("AEGIS_MASTER_KEY", 32); err != nil {
		missing = append(missing, err.Error())
	}
	if c.ConfigSigningSeed, err = requireExactKey("AEGIS_CONFIG_SIGNING_SEED", 32); err != nil {
		missing = append(missing, err.Error())
	}
	if raw := strings.TrimSpace(os.Getenv("AEGIS_PREVIOUS_CONFIG_SIGNING_SEED")); raw != "" {
		previous, decodeErr := base64.StdEncoding.DecodeString(raw)
		if decodeErr != nil || len(previous) != 32 {
			missing = append(missing, "AEGIS_PREVIOUS_CONFIG_SIGNING_SEED must be base64 for exactly 32 bytes")
		} else {
			c.PreviousConfigSigningSeed = previous
		}
	}

	for _, d := range []Domain{DomainPublic, DomainAdmin} {
		name := "AEGIS_JWT_" + strings.ToUpper(string(d)) + "_SECRET"
		k, kerr := requireKey(name, 32)
		if kerr != nil {
			missing = append(missing, kerr.Error())
			continue
		}
		c.JWTSecrets[d] = k
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("配置不完整，拒绝启动：\n  - %s", strings.Join(missing, "\n  - "))
	}

	// 两把域密钥必须互不相同，否则 EXT-001 的隔离形同虚设。
	if err := assertDistinct(c.JWTSecrets); err != nil {
		return nil, err
	}
	if len(c.PreviousConfigSigningSeed) > 0 && string(c.PreviousConfigSigningSeed) == string(c.ConfigSigningSeed) {
		return nil, fmt.Errorf("AEGIS_PREVIOUS_CONFIG_SIGNING_SEED must differ from AEGIS_CONFIG_SIGNING_SEED")
	}

	return c, nil
}

func (c *Config) IsProduction() bool { return c.Env == "production" }

// CanonicalPublicOrigin returns the deployment-owned external origin used in
// generated root-install commands. Request headers are never authoritative.
//
// 生产环境的规则与 deploy/public-base-url.sh 的 pandora_valid_public_base_url、
// deploy/render-nginx.sh 的校验是同一条：https://<DNS 域名或公网 IPv4>，不带端口与路径。
// 三处共用 deploy/fixtures/public-base-url-cases.txt 这张用例表，改规则要三处同改。
func (c *Config) CanonicalPublicOrigin() (string, error) {
	base := strings.TrimRight(strings.TrimSpace(c.PublicBaseURL), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("invalid canonical public base URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && !c.IsProduction()) {
		// 这条错误会顺着 500 冒到管理员面前，所以必须写清楚该改哪里。
		//
		// 生产上真踩过：.env 里从来没有 AEGIS_PUBLIC_BASE_URL，默认值
		// http://127.0.0.1:9000 不是 HTTPS，于是「接入命令」按钮一按就是
		// 500 加一句「服务暂时不可用」——没人猜得到要去配一个环境变量。
		return "", fmt.Errorf(
			"AEGIS_PUBLIC_BASE_URL 必须是面板的 HTTPS 对外地址"+
				"（如 https://panel.example.com 或 https://公网IPv4），当前为 %q。"+
				"接入命令、支付回调、订阅链接都从它拼出来", base)
	}
	if c.IsProduction() {
		if !isPublicHostname(u.Hostname()) {
			return "", fmt.Errorf("AEGIS_PUBLIC_BASE_URL 生产环境必须是 DNS 域名或公网 IPv4（IPv6 地址请改用解析到它的域名），当前为 %q", u.Hostname())
		}
		// nginx 只在 443 上按这个地址出证书；带端口的地址边缘配置渲染不出来
		if strings.Contains(u.Host, ":") {
			return "", fmt.Errorf("AEGIS_PUBLIC_BASE_URL 生产环境不能带端口，当前为 %q", base)
		}
	}
	if strings.ContainsAny(base, "\"'`$\\\r\n") {
		return "", fmt.Errorf("canonical public base URL contains unsafe characters")
	}
	return base, nil
}

func assertDistinct(secrets map[Domain][]byte) error {
	seen := map[string]Domain{}
	for d, k := range secrets {
		fp := base64.StdEncoding.EncodeToString(k)
		if other, dup := seen[fp]; dup {
			return fmt.Errorf(
				"域 %s 与 %s 使用了相同的令牌签名密钥；这会让两域令牌可以互换（违反 EXT-001）",
				d, other)
		}
		seen[fp] = d
	}
	return nil
}

func requireKey(name string, wantLen int) ([]byte, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return nil, fmt.Errorf("%s 未设置", name)
	}
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%s 不是合法的 base64", name)
	}
	if len(k) < wantLen {
		return nil, fmt.Errorf("%s 长度 %d 字节，至少需要 %d 字节", name, len(k), wantLen)
	}
	return k, nil
}

func requireExactKey(name string, wantLen int) ([]byte, error) {
	k, err := requireKey(name, wantLen)
	if err != nil {
		return nil, err
	}
	if len(k) != wantLen {
		return nil, fmt.Errorf("%s 长度 %d 字节，必须恰好为 %d 字节", name, len(k), wantLen)
	}
	return k, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func strictPositiveEnvInt(k string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s 不是合法的整数: %w", k, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s 必须大于 0", k)
	}
	return n, nil
}

func strictPositiveEnvDuration(k string, def time.Duration) (time.Duration, error) {
	d, err := strictEnvDuration(k, def)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s 必须大于 0", k)
	}
	return d, nil
}

func parseEnvironment(raw string) (string, error) {
	environment := strings.ToLower(strings.TrimSpace(raw))
	switch environment {
	case "development", "test", "production":
		return environment, nil
	default:
		return "", fmt.Errorf("AEGIS_ENV 必须是 development、test 或 production，当前为 %q", raw)
	}
}

// isPublicHostname 判断生产环境对外地址的主机名：小写的 DNS 域名（至少两段、末段以字母开头，
// 不是 localhost），或公网 IPv4。IPv6 字面量一律不收：nginx 的 server_name 与 ACME 的
// HTTP-01 都按 IPv4 写，只有 IPv6 的机器用解析到它的域名。
func isPublicHostname(host string) bool {
	host = strings.ToLower(host)
	if addr, err := netip.ParseAddr(host); err == nil {
		return isPublicIPv4(addr)
	}
	if len(host) > 253 || strings.HasSuffix(host, ".localhost") {
		return false
	}
	return publicDNSName.MatchString(host)
}

// publicDNSName 与 public-base-url.sh、render-nginx.sh 的域名正则逐字相同。
var publicDNSName = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$`)

// nonPublicIPv4 是 IsGlobalUnicast / IsPrivate 之外仍不能作对外地址的 IPv4 段：
// 「本网络」、运营商级 NAT、IETF 协议分配、基准测试、保留段（含广播）。
// 文档段 TEST-NET-1/2/3 不在其中：测试夹具用它们，Let's Encrypt 会拒签，安装器兜底自签。
var nonPublicIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

func isPublicIPv4(addr netip.Addr) bool {
	if !addr.Is4() || !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	for _, p := range nonPublicIPv4 {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

func strictEnvDuration(k string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s 不是合法的时长: %w", k, err)
	}
	return d, nil
}

func validateTokenTTLs(access, refresh time.Duration) error {
	const maxTTL = 365 * 24 * time.Hour
	if access <= 0 || access > maxTTL {
		return fmt.Errorf("AEGIS_ACCESS_TOKEN_TTL 必须大于 0 且不超过 %s", maxTTL)
	}
	if refresh <= 0 || refresh > maxTTL {
		return fmt.Errorf("AEGIS_REFRESH_TOKEN_TTL 必须大于 0 且不超过 %s", maxTTL)
	}
	if access > refresh {
		return fmt.Errorf("AEGIS_ACCESS_TOKEN_TTL 不能大于 AEGIS_REFRESH_TOKEN_TTL")
	}
	return nil
}
