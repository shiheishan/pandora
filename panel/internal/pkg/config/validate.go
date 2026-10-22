package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var grpcNodePublicHostDNS = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// Validate 收集全部校验问题并一并返回。
func (c *Config) Validate() error {
	var errs []error

	switch c.Env {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("env 必须是 development、test 或 production"))
	}

	if c.Database.URL.Reveal() == "" {
		errs = append(errs, errors.New("database.url 必填"))
	}
	if c.Redis.URL.Reveal() == "" {
		errs = append(errs, errors.New("redis.url 必填"))
	}

	mk := c.Secrets.MasterKey.Reveal()
	if mk == "" {
		errs = append(errs, errors.New("secrets.master_key 必填"))
	} else {
		decoded, err := base64.StdEncoding.DecodeString(mk)
		if err != nil {
			errs = append(errs, fmt.Errorf("secrets.master_key 必须是有效的 base64: %w", err))
		} else if len(decoded) < 32 {
			errs = append(errs, fmt.Errorf("secrets.master_key 解码后至少 32 字节，当前 %d", len(decoded)))
		}
	}

	pub := c.Auth.PublicJWTSecret.Reveal()
	adm := c.Auth.AdminJWTSecret.Reveal()
	if pub == "" {
		errs = append(errs, errors.New("auth.public_jwt_secret 必填"))
	} else if len(pub) < 32 {
		errs = append(errs, fmt.Errorf("auth.public_jwt_secret 至少 32 字节"))
	}
	if adm == "" {
		errs = append(errs, errors.New("auth.admin_jwt_secret 必填"))
	} else if len(adm) < 32 {
		errs = append(errs, fmt.Errorf("auth.admin_jwt_secret 至少 32 字节"))
	}
	if pub != "" && adm != "" && pub == adm {
		errs = append(errs, errors.New("auth.public_jwt_secret 与 auth.admin_jwt_secret 不能相同"))
	}

	for _, pair := range []struct {
		name string
		val  string
	}{
		{"auth.public_jwt_previous_secret", c.Auth.PublicJWTPreviousSecret.Reveal()},
		{"auth.admin_jwt_previous_secret", c.Auth.AdminJWTPreviousSecret.Reveal()},
	} {
		if pair.val == "" {
			continue
		}
		if len(pair.val) < 32 {
			errs = append(errs, fmt.Errorf("%s 非空时至少 32 字节", pair.name))
		}
		if pair.val == pub || pair.val == adm {
			errs = append(errs, fmt.Errorf("%s 不能与当前 JWT 密钥相同", pair.name))
		}
	}

	if c.Auth.AccessTTL < time.Minute || c.Auth.AccessTTL > time.Hour {
		errs = append(errs, fmt.Errorf("auth.access_ttl 须在 [1m, 1h]，当前 %v", c.Auth.AccessTTL))
	}
	if c.Auth.PublicSessionTTL < time.Hour {
		errs = append(errs, fmt.Errorf("auth.public_session_ttl 至少 1h"))
	} else if c.Auth.PublicSessionTTL < c.Auth.AccessTTL {
		errs = append(errs, errors.New("auth.public_session_ttl 不能小于 auth.access_ttl"))
	}
	if c.Auth.AdminSessionTTL < time.Hour {
		errs = append(errs, fmt.Errorf("auth.admin_session_ttl 至少 1h"))
	} else if c.Auth.AdminSessionTTL < c.Auth.AccessTTL {
		errs = append(errs, errors.New("auth.admin_session_ttl 不能小于 auth.access_ttl"))
	}
	if c.Auth.AdminIdleTimeout < 5*time.Minute {
		errs = append(errs, fmt.Errorf("auth.admin_idle_timeout 至少 5m"))
	}

	errs = append(errs, validateHTTPServer("http.public", c.HTTP.Public)...)
	errs = append(errs, validateHTTPServer("http.admin", c.HTTP.Admin)...)
	errs = append(errs, validateGRPCNode(c.Env, c.GRPC.Node)...)
	if c.Env == EnvProduction {
		// 引擎只信 nginx 覆写的 X-Real-IP：监听若不在回环上，任何客户端都能伪造来源 IP，按 IP 的限流与审计聚合失效
		for field, addr := range map[string]string{"http.public.addr": c.HTTP.Public.Addr, "http.admin.addr": c.HTTP.Admin.Addr} {
			if host, _, err := net.SplitHostPort(addr); err == nil && !isLoopbackHost(host) {
				errs = append(errs, fmt.Errorf("production 环境下 %s 必须绑回环地址（前面是 nginx）", field))
			}
		}
	}

	if c.Database.MaxConns < 2 || c.Database.MaxConns > 500 {
		errs = append(errs, fmt.Errorf("database.max_conns 须在 [2, 500]，当前 %d", c.Database.MaxConns))
	}

	if c.Redis.PoolSize < 1 {
		errs = append(errs, errors.New("redis.pool_size 至少为 1"))
	}
	if c.Redis.OpTimeout < 50*time.Millisecond || c.Redis.OpTimeout > 5*time.Second {
		errs = append(errs, fmt.Errorf("redis.op_timeout 须在 [50ms, 5s]，当前 %v", c.Redis.OpTimeout))
	}

	if c.Worker.Concurrency < 1 {
		errs = append(errs, errors.New("worker.concurrency 至少为 1"))
	}
	if err := validatePprofAddr("worker.pprof_addr", c.Worker.PprofAddr); err != nil {
		errs = append(errs, err)
	}
	if err := validateMetricsAddr("worker.metrics_addr", c.Worker.MetricsAddr); err != nil {
		errs = append(errs, err)
	}
	if err := validateMetricsAddr("observability.metrics_addr", c.Observability.MetricsAddr); err != nil {
		errs = append(errs, err)
	}

	ph := c.Auth.PasswordHash
	if ph.PublicConcurrency < 1 || ph.PublicConcurrency > 16 {
		errs = append(errs, fmt.Errorf("auth.password_hash.public_concurrency 须在 [1, 16]"))
	}
	if ph.AdminConcurrency < 1 || ph.AdminConcurrency > 16 {
		errs = append(errs, fmt.Errorf("auth.password_hash.admin_concurrency 须在 [1, 16]"))
	}
	if ph.QueueTimeout <= 0 || ph.QueueTimeout > 15*time.Second {
		errs = append(errs, fmt.Errorf("auth.password_hash.queue_timeout 须在 (0, 15s]"))
	}

	if c.RateLimit.IPPerMin < 1 {
		errs = append(errs, errors.New("ratelimit.ip_per_min 至少为 1"))
	}
	if c.RateLimit.AuthPerMin < 1 {
		errs = append(errs, errors.New("ratelimit.auth_per_min 至少为 1"))
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("log.level 必须是 debug、info、warn 或 error"))
	}

	if c.Env == EnvProduction && !strings.HasPrefix(c.PublicBaseURL, "https://") {
		errs = append(errs, errors.New("production 环境下 public_base_url 必须以 https:// 开头"))
	}

	for _, f := range []struct{ name, val string }{
		{"subscription.singbox.geosite_url_prefix", c.Subscription.Singbox.GeositeURLPrefix},
		{"subscription.singbox.geoip_url_prefix", c.Subscription.Singbox.GeoIPURLPrefix},
	} {
		if err := validateRuleSetPrefix(f.name, f.val); err != nil {
			errs = append(errs, err)
		}
	}

	if c.Pdnd.DistDir == "" || !path.IsAbs(c.Pdnd.DistDir) {
		errs = append(errs, errors.New("pdnd.dist_dir 必须是绝对路径"))
	}
	if c.Backup.Dir != "" && !path.IsAbs(c.Backup.Dir) {
		errs = append(errs, errors.New("backup.dir 必须是绝对路径（或留空表示不探测）"))
	}

	return errors.Join(errs...)
}

// validateGRPCNode 校验节点 gRPC：addr 必须是 host:port 且端口 1–65535；public_host 为空只在非生产允许，
// 非空时必须是 IP 字面量或合法 DNS 名（不带端口、协议、路径）；public_port 在 0–65535。
func validateGRPCNode(env string, g GRPCServer) []error {
	var errs []error
	_, portStr, err := net.SplitHostPort(g.Addr)
	if err != nil {
		errs = append(errs, fmt.Errorf("grpc.node.addr 必须是 host:port，端口 1–65535"))
	} else {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			errs = append(errs, fmt.Errorf("grpc.node.addr 必须是 host:port，端口 1–65535"))
		}
	}
	if g.PublicHost == "" {
		if env == EnvProduction {
			errs = append(errs, errors.New("production 环境下 grpc.node.public_host 必填（节点连面板用的主机名或 IP）"))
		}
	} else {
		h := g.PublicHost
		if net.ParseIP(h) == nil {
			if len(h) > 253 || !grpcNodePublicHostDNS.MatchString(h) {
				errs = append(errs, fmt.Errorf("grpc.node.public_host 必须是 IP 或 DNS 名（不带端口与协议）"))
			}
		}
	}
	if g.PublicPort < 0 || g.PublicPort > 65535 {
		errs = append(errs, fmt.Errorf("grpc.node.public_port 须在 [0, 65535]"))
	}
	return errs
}

func validateHTTPServer(prefix string, s HTTPServer) []error {
	var errs []error
	if s.Addr == "" {
		errs = append(errs, fmt.Errorf("%s.addr 必填", prefix))
	}
	if s.MaxConcurrency < 1 {
		errs = append(errs, fmt.Errorf("%s.max_concurrency 至少为 1", prefix))
	}
	if s.RequestTimeout <= 0 {
		errs = append(errs, fmt.Errorf("%s.request_timeout 必须大于 0", prefix))
	}
	if err := validatePprofAddr(prefix+".pprof_addr", s.PprofAddr); err != nil {
		errs = append(errs, err)
	}
	return errs
}

func validatePprofAddr(field, addr string) error {
	if addr == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s 无法解析: %w", field, err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("%s 的 host 必须是回环地址 (127.0.0.1、::1 或 localhost)", field)
	}
	return nil
}

// validateMetricsAddr 校验指标端点地址：空表示不开；否则必须是 host:port，端口 1–65535，
// host 是回环（127.0.0.1、::1、localhost）或私网 IP 字面量（RFC 1918、IPv6 ULA fc00::/7）。
// 不收空主机、0.0.0.0、公网 IP 与其他主机名：指标里有路由与负载信息，不对公网开放。
func validateMetricsAddr(field, addr string) error {
	if addr == "" {
		return nil
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s 无法解析: %w", field, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("%s 的端口须在 1–65535", field)
	}
	if isLoopbackHost(host) {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		return nil
	}
	return fmt.Errorf("%s 的 host 必须是回环或私网地址（127.0.0.1、::1、10.x、172.16–31.x、192.168.x、fc00::/7）", field)
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "::1", "localhost":
		return true
	default:
		return false
	}
}

// validateRuleSetPrefix 校验规则集地址前缀：客户端直接拿它拼文件名下载，写错整份配置起不来。
// 只收 https、带主机、不带账号、查询与片段、以 / 结尾；空值表示用缺省。
func validateRuleSetPrefix(field, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/") ||
		strings.ContainsAny(raw, "\"\\ \t\r\n") {
		return fmt.Errorf("%s 必须是以 / 结尾的 https:// 地址前缀（不带查询与片段）", field)
	}
	return nil
}
