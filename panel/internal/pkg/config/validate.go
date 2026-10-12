package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

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

	if c.Database.MaxConns < 2 || c.Database.MaxConns > 500 {
		errs = append(errs, fmt.Errorf("database.max_conns 须在 [2, 500]，当前 %d", c.Database.MaxConns))
	}
	if c.Database.MinConns < 0 || c.Database.MinConns > c.Database.MaxConns {
		errs = append(errs, fmt.Errorf("database.min_conns 须在 [0, max_conns]，当前 %d", c.Database.MinConns))
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
	if c.RateLimit.AccountPerMin < 1 {
		errs = append(errs, errors.New("ratelimit.account_per_min 至少为 1"))
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

	return errors.Join(errs...)
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

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "::1", "localhost":
		return true
	default:
		return false
	}
}
