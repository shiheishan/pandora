// Package config 定义 pandora 的全部运行配置，由 viper 从配置文件与 PANDORA_ 前缀的环境变量加载。
//
// 秘密（Secret 类型的字段）只能来自环境变量或 *_FILE 指向的文件，写在配置文件里会启动失败。
// 后续模块需要的配置段在各自迁移时追加到这里。
package config

import "time"

// Env 取值。
const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvProduction  = "production"
)

// Secret 是不会被打印出来的字符串。
type Secret string

func (Secret) String() string { return "***" }

// MarshalText 让日志与 JSON 输出里只出现占位符。
func (Secret) MarshalText() ([]byte, error) { return []byte("***"), nil }

// Reveal 返回原值，只在真正使用秘密的地方调用。
func (s Secret) Reveal() string { return string(s) }

type Config struct {
	Env           string    `mapstructure:"env"`
	PublicBaseURL string    `mapstructure:"public_base_url"`
	HTTP          HTTP      `mapstructure:"http"`
	GRPC          GRPC      `mapstructure:"grpc"`
	Database      Database  `mapstructure:"database"`
	Redis         Redis     `mapstructure:"redis"`
	Worker        Worker    `mapstructure:"worker"`
	Auth          Auth      `mapstructure:"auth"`
	RateLimit     RateLimit `mapstructure:"ratelimit"`
	Secrets       Secrets   `mapstructure:"secrets"`
	Log           Log       `mapstructure:"log"`
}

type HTTP struct {
	Public HTTPServer `mapstructure:"public"`
	Admin  HTTPServer `mapstructure:"admin"`
}

type HTTPServer struct {
	Addr              string        `mapstructure:"addr"`
	ReadHeaderTimeout time.Duration `mapstructure:"read_header_timeout"`
	ReadTimeout       time.Duration `mapstructure:"read_timeout"`
	WriteTimeout      time.Duration `mapstructure:"write_timeout"`
	IdleTimeout       time.Duration `mapstructure:"idle_timeout"`
	// RequestTimeout 是普通请求的 ctx 期限；SSE 端点豁免。
	RequestTimeout time.Duration `mapstructure:"request_timeout"`
	// MaxConcurrency 是同时处理的请求上限，超出回 503。
	MaxConcurrency int    `mapstructure:"max_concurrency"`
	PprofAddr      string `mapstructure:"pprof_addr"`
}

type GRPC struct {
	Node GRPCServer `mapstructure:"node"`
}

type GRPCServer struct {
	Addr string `mapstructure:"addr"`
}

type Database struct {
	URL Secret `mapstructure:"url"`
	// MigrationURL 只在 migrate 子命令里使用（表属主角色）。
	MigrationURL Secret `mapstructure:"migration_url"`
	MaxConns     int    `mapstructure:"max_conns"`
	MinConns     int    `mapstructure:"min_conns"`
}

type Redis struct {
	URL      Secret `mapstructure:"url"`
	PoolSize int    `mapstructure:"pool_size"`
	// OpTimeout 同时用作读、写、拨号、等连接池的超时，并打开 ContextTimeoutEnabled。
	OpTimeout time.Duration `mapstructure:"op_timeout"`
}

type Worker struct {
	Concurrency     int           `mapstructure:"concurrency"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	PprofAddr       string        `mapstructure:"pprof_addr"`
}

type Auth struct {
	AccessTTL        time.Duration `mapstructure:"access_ttl"`
	PublicSessionTTL time.Duration `mapstructure:"public_session_ttl"`
	AdminSessionTTL  time.Duration `mapstructure:"admin_session_ttl"`
	AdminIdleTimeout time.Duration `mapstructure:"admin_idle_timeout"`
	// 两个域各一把当前密钥，可选一把上一代密钥（只验不签），用于轮换。
	PublicJWTSecret         Secret       `mapstructure:"public_jwt_secret"`
	PublicJWTPreviousSecret Secret       `mapstructure:"public_jwt_previous_secret"`
	AdminJWTSecret          Secret       `mapstructure:"admin_jwt_secret"`
	AdminJWTPreviousSecret  Secret       `mapstructure:"admin_jwt_previous_secret"`
	PasswordHash            PasswordHash `mapstructure:"password_hash"`
}

type PasswordHash struct {
	PublicConcurrency int           `mapstructure:"public_concurrency"`
	AdminConcurrency  int           `mapstructure:"admin_concurrency"`
	QueueTimeout      time.Duration `mapstructure:"queue_timeout"`
}

type RateLimit struct {
	IPPerMin      int `mapstructure:"ip_per_min"`
	AccountPerMin int `mapstructure:"account_per_min"`
	AuthPerMin    int `mapstructure:"auth_per_min"`
}

type Secrets struct {
	// MasterKey 是 base64 编码、至少 32 字节的根密钥，只经 HKDF 派生子密钥使用。
	MasterKey Secret `mapstructure:"master_key"`
}

type Log struct {
	Level string `mapstructure:"level"`
	// File 为空时只写 stderr。
	File       string `mapstructure:"file"`
	MaxSizeMB  int    `mapstructure:"max_size_mb"`
	MaxBackups int    `mapstructure:"max_backups"`
	MaxAgeDays int    `mapstructure:"max_age_days"`
	Compress   bool   `mapstructure:"compress"`
}
