package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// secretKeys 只能经环境变量或 *_FILE 提供。
var secretKeys = []string{
	"database.url",
	"database.migration_url",
	"redis.url",
	"auth.public_jwt_secret",
	"auth.public_jwt_previous_secret",
	"auth.admin_jwt_secret",
	"auth.admin_jwt_previous_secret",
	"secrets.master_key",
}

// Load 从可选配置文件与 PANDORA_ 环境变量加载并校验配置。
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetEnvPrefix("PANDORA")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	defaults(v)

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, err
		}
	}

	for _, key := range secretKeys {
		if v.InConfig(key) {
			return nil, fmt.Errorf(
				"秘密 %s 不能写在配置文件里，请用环境变量 %s 或 %s_FILE",
				key, envVarName(key), envVarName(key),
			)
		}
	}

	for _, key := range secretKeys {
		val, err := resolveSecret(key)
		if err != nil {
			return nil, err
		}
		if val != "" {
			v.Set(key, val)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// IsProduction 是否为生产环境。
func (c *Config) IsProduction() bool {
	return c.Env == EnvProduction
}

func defaults(v *viper.Viper) {
	// 缺省按生产处理（失败即关闭）：dev_code 回显、开发日志这些只在显式设了 development / test 时才开
	v.SetDefault("env", EnvProduction)
	v.SetDefault("public_base_url", "http://127.0.0.1:9000")

	for _, side := range []string{"public", "admin"} {
		prefix := "http." + side + "."
		addr := "127.0.0.1:9000"
		if side == "admin" {
			addr = "127.0.0.1:9001"
		}
		v.SetDefault(prefix+"addr", addr)
		v.SetDefault(prefix+"read_header_timeout", "5s")
		v.SetDefault(prefix+"read_timeout", "30s")
		v.SetDefault(prefix+"write_timeout", "30s")
		v.SetDefault(prefix+"idle_timeout", "120s")
		v.SetDefault(prefix+"request_timeout", "25s")
		v.SetDefault(prefix+"max_concurrency", 512)
		v.SetDefault(prefix+"pprof_addr", "")
	}

	v.SetDefault("grpc.node.addr", ":9443")
	v.SetDefault("grpc.node.public_host", "")
	v.SetDefault("grpc.node.public_port", 0)

	v.SetDefault("database.url", "")
	v.SetDefault("database.migration_url", "")
	v.SetDefault("database.max_conns", 30)
	v.SetDefault("database.min_conns", 2)

	v.SetDefault("redis.url", "")
	v.SetDefault("redis.pool_size", 32)
	v.SetDefault("redis.op_timeout", "500ms")

	v.SetDefault("worker.concurrency", 8)
	v.SetDefault("worker.shutdown_timeout", "30s")
	v.SetDefault("worker.pprof_addr", "")

	v.SetDefault("auth.access_ttl", "15m")
	v.SetDefault("auth.public_session_ttl", "720h")
	v.SetDefault("auth.admin_session_ttl", "168h")
	v.SetDefault("auth.admin_idle_timeout", "2h")
	v.SetDefault("auth.public_jwt_secret", "")
	v.SetDefault("auth.public_jwt_previous_secret", "")
	v.SetDefault("auth.admin_jwt_secret", "")
	v.SetDefault("auth.admin_jwt_previous_secret", "")
	v.SetDefault("auth.password_hash.public_concurrency", 2)
	v.SetDefault("auth.password_hash.admin_concurrency", 1)
	v.SetDefault("auth.password_hash.queue_timeout", "5s")

	v.SetDefault("ratelimit.ip_per_min", 120)
	v.SetDefault("ratelimit.account_per_min", 300)
	v.SetDefault("ratelimit.auth_per_min", 10)

	v.SetDefault("secrets.master_key", "")

	v.SetDefault("log.level", "info")
	v.SetDefault("log.file", "")
	v.SetDefault("log.max_size_mb", 64)
	v.SetDefault("log.max_backups", 14)
	v.SetDefault("log.max_age_days", 30)
	v.SetDefault("log.compress", true)

	v.SetDefault("subscription.singbox.geosite_url_prefix", "")
	v.SetDefault("subscription.singbox.geoip_url_prefix", "")
	v.SetDefault("subscription.singbox.block_ads", false)

	v.SetDefault("pdnd.dist_dir", "/opt/pandora/pdnd-dist")
}

func envVarName(configKey string) string {
	return "PANDORA_" + strings.ToUpper(strings.ReplaceAll(configKey, ".", "_"))
}

func resolveSecret(configKey string) (string, error) {
	envName := envVarName(configKey)
	if v, ok := os.LookupEnv(envName); ok {
		return v, nil
	}
	fileEnv := envName + "_FILE"
	path, ok := os.LookupEnv(fileEnv)
	if !ok {
		return "", nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("秘密文件 %s 权限过宽，组或其他用户可读", path)
	}
	b, err := os.ReadFile(path) //nolint:gosec // 路径来自运维设置的 *_FILE 环境变量，读它就是本意
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
