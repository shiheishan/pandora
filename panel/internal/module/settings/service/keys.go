package service

// 设置键与缺省值。缺行即取缺省值；新增一组键就在这里加常量、在对应文件加 X / SetX。
const (
	keySiteName           = "site.name"               // string，1–40 字
	keySiteTimezone       = "site.timezone"           // string，IANA 时区名
	keySiteSEOKeywords    = "site.seo_keywords"       // string，最多 200 字，不含换行
	keySiteSEODescription = "site.seo_description"    // string，最多 160 字，不含换行；空则入口页用主题标语
	keySiteAllowIndexing  = "site.allow_indexing"     // bool，缺省 DefaultSiteAllowIndexing
	keySiteURL            = "site.url"                // string，主域名 scheme://host[:port]，空 = 用配置的 public_base_url（site_url.go）
	keySiteURLPrevious    = "site.url_previous"       // string，上一次改主域名之前的生效值，改 site.url 时一起写
	keySiteURLPrevUntil   = "site.url_previous_until" // 时间（RFC 3339），上一个生效值认到什么时候，改 site.url 时一起写

	keyRegistrationMode  = "auth.registration_mode"  // string：closed / invite_only / open
	keyEmailVerification = "auth.email_verification" // bool

	keyMailHost        = "mail.smtp_host"     // string
	keyMailPort        = "mail.smtp_port"     // int，1–65535
	keyMailEncryption  = "mail.encryption"    // string：ssl / tls / none
	keyMailUsername    = "mail.smtp_username" // string
	keyMailPassword    = "mail.smtp_password" //nolint:gosec // 这是设置键名，不是口令；值经 Sealer 封存后存，AAD 为键名
	keyMailFromAddress = "mail.from_address"  // string，空或邮箱
	keyMailFromName    = "mail.from_name"     // string
)

// 缺省值。
const (
	defaultSiteName = "Pandora"
	// 站点时区缺省北京时间（用户 10-21 定）：用户多在中国大陆，用量切日、收入趋势按它算
	defaultSiteTimezone = "Asia/Shanghai"

	// 新装缺省开放注册；紧急关闭用 auth.registration 开关。
	defaultRegistrationMode = RegistrationOpen
	// 缺省不验证邮箱：没配 SMTP 就开验证等于把注册入口焊死。
	defaultEmailVerification = false

	defaultMailPort       = 465
	defaultMailEncryption = MailEncryptionSSL
)

// DefaultSiteAllowIndexing 是「允许搜索引擎收录门户首页」的缺省值：关。全仓只在这里定义，入口页与 robots.txt 读不到设置时也用它。
const DefaultSiteAllowIndexing = false

// 站点 SEO 文字的长度上限（按字符数）。
const (
	SiteSEOKeywordsMax    = 200
	SiteSEODescriptionMax = 160
)

// 注册方式。
const (
	RegistrationClosed     = "closed"
	RegistrationInviteOnly = "invite_only"
	RegistrationOpen       = "open"
)

// SMTP 加密方式：ssl 是 465 直连 TLS，tls 是 587 先明文再 STARTTLS，none 不加密。
const (
	MailEncryptionSSL  = "ssl"
	MailEncryptionTLS  = "tls"
	MailEncryptionNone = "none"
)

// Site 是站点级设置（原 tenants 表的站点名、时区）。
type Site struct {
	Name           string
	Timezone       string
	SEOKeywords    string // 门户入口页 meta keywords
	SEODescription string // 门户入口页 meta description；空时入口页用主题标语
	AllowIndexing  bool   // 是否允许搜索引擎收录门户首页
	URL            string // 后台存的主域名（scheme://host[:port]，不带末尾 /）；空 = 用配置的 public_base_url。生效值读 SiteURL
}

// Registration 是存下来的注册设置。实际生效的策略还要看 auth.registration 开关，见 RegistrationPolicy。
type Registration struct {
	Mode              string
	EmailVerification bool
}

// Mail 是 SMTP 发信设置。Password 是解封后的明文，只给发信用；后台展示只看 HasPassword。
type Mail struct {
	Host        string
	Port        int
	Encryption  string
	Username    string
	Password    string
	HasPassword bool
	FromAddress string
	FromName    string
}

// Configured 报告发信配置是否齐全（服务器、端口、发件地址都有）。
func (m Mail) Configured() bool {
	return m.Host != "" && m.Port > 0 && m.FromAddress != ""
}

// MailUpdate 是 SetMail 的输入。Password 为 nil 表示不改，指向空串表示清除，其余为新口令。
type MailUpdate struct {
	Host        string
	Port        int
	Encryption  string
	Username    string
	Password    *string
	FromAddress string
	FromName    string
}

// 降级开关码。
const (
	SwitchAuthLogin           = "auth.login"
	SwitchSubscriptionRenewal = "subscription.renewal"
	SwitchClientConfigSync    = "client.config_sync"
	SwitchAuthRegistration    = "auth.registration"
	SwitchBillingCheckout     = "billing.checkout"
	SwitchGiftCardRedeem      = "marketing.giftcard.redeem"
	SwitchNotifyEmail         = "notify.email"
	// SwitchAdminWrites 关闭时后台进入只读模式。
	SwitchAdminWrites = "admin.writes"
)

// SeedSwitches 是迁移 00002 种下的开关（值为 essential）；测试对照迁移与本表。
var SeedSwitches = map[string]bool{
	SwitchAuthLogin:           true,
	SwitchSubscriptionRenewal: true,
	SwitchClientConfigSync:    true,
	SwitchAuthRegistration:    false,
	SwitchBillingCheckout:     false,
	SwitchGiftCardRedeem:      false,
	SwitchNotifyEmail:         false,
	SwitchAdminWrites:         false,
}

// Switch 是一个降级开关；Enabled 为 true 表示功能可用。
type Switch struct {
	Code      string
	Enabled   bool
	Essential bool
	Reason    *string
}

// 审计动作。
const (
	auditSiteChanged         = "site.settings_changed"
	auditSiteURLChanged      = "site.url_changed"
	auditRegistrationChanged = "registration.settings_changed"
	auditMailChanged         = "mail.settings_changed"
	auditSwitchToggle        = "feature_switch.toggle"

	auditResourceSettings = "settings"
	auditResourceSwitch   = "feature_switch"
)
