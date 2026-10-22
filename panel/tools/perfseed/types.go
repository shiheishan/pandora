// perfseed 是性能测试的造数工具：在按 install.sh 装好的空面板上造出服务器、节点、套餐、用户、订阅与历史数据。
//
// 三个子命令按顺序跑（见 README.md）：
//
//	infra     经后台 API 建用户组、节点池、服务器与节点，签接入令牌，写 tokens、nodesim.env、admin.env、infra.json
//	activate  经后台 API 让节点上线，建并发布套餐，写 catalog.json
//	users     用属主角色直连库，一个事务批量写用户、订阅、订阅链接凭据、历史订单与账本、按天用量、审计，写 users.csv
//
// 本文件只放几个文件共用的类型与常量；各子命令的实现分别在 infra.go、activate.go、users.go。
package main

import "time"

// out-dir 下的文件名（一律 0600）。
const (
	fileTokens     = "tokens"
	fileNodesimEnv = "nodesim.env"
	fileUsersCSV   = "users.csv"
	fileAdminEnv   = "admin.env"
	fileInfra      = "infra.json"
	fileCatalog    = "catalog.json"
)

// perfseed 造的用户邮箱：perf-00001@example.com …（%05d，从 1 起）。幂等判断按这个前缀与域名。
const (
	userEmailPrefix = "perf-"
	userEmailDomain = "@example.com"
)

// ---- 子命令参数（main.go 解析与校验，各 run 函数使用） ----

// adminOpts 是走后台 API 的两个子命令共用的参数。
type adminOpts struct {
	AdminURL          string // 后台基地址，不带 /v1，去掉末尾的 /，如 http://127.0.0.1:9001
	AdminEmail        string
	AdminPasswordFile string
	OutDir            string
}

// infraOpts 是 infra 子命令的参数。
type infraOpts struct {
	adminOpts
	Servers int // 服务器台数（每台 1 个节点），缺省 200
}

// activateOpts 是 activate 子命令的参数。
type activateOpts struct {
	adminOpts
}

// usersOpts 是 users 子命令的参数。
type usersOpts struct {
	DSNFile          string     // 属主角色 pandora_owner 的连接串文件（裸 DSN，或含 PANDORA_DATABASE_MIGRATION_URL= 的 deploy.env）
	SecretsEnv       string     // 面板的 secrets.env（取 PANDORA_SECRETS_MASTER_KEY 与 PANDORA_REDIS_URL），缺省 /etc/pandora/secrets.env
	OutDir           string     // 读 catalog.json、infra.json，写 users.csv
	Users            int        // 用户数，缺省 5000
	UserPasswordFile string     // 所有用户共用的口令
	ExpireAt         *time.Time // 非空时把 ExpireCount 份生效中的订阅的到期时间设到这一刻（整点到期突发用）
	ExpireCount      int
	HistoryDays      int    // 按天用量往回造多少天，缺省 30
	ExpiredPercent   int    // 已过期用户的百分比，缺省 10
	Seed             uint64 // 随机种子，缺省 1（同一种子造出同样的时间线与用量；uuid 与令牌仍是随机的）
}

// ---- infra.json / catalog.json ----

// InfraState 是 infra 的进度与结果（out-dir/infra.json）。每建完一个对象就整份重写一次，
// 中途失败重跑时跳过已建的；TokensDone 为真表示 infra 已经做完。activate 在上线每个节点后同样重写它。
type InfraState struct {
	GroupID    string        `json:"group_id"`
	PoolID     string        `json:"pool_id"`
	Cipher     string        `json:"cipher"`
	Servers    []ServerState `json:"servers"`
	TokensDone bool          `json:"tokens_done"`
}

// ServerState 是一台服务器与它上面唯一的节点。
type ServerState struct {
	Name           string `json:"name"` // s0001…，也是令牌文件里的名字
	ServerID       string `json:"server_id"`
	NodeID         string `json:"node_id"`
	NodeRowVersion int64  `json:"node_row_version"`
	Activated      bool   `json:"activated"`
}

// Catalog 是 activate 建好的套餐（out-dir/catalog.json）。
type Catalog struct {
	Plans []CatalogPlan `json:"plans"`
}

// CatalogPlan 是一个已发布的套餐。
type CatalogPlan struct {
	Code      string         `json:"code"`
	Name      string         `json:"name"`
	PlanID    string         `json:"plan_id"`
	VersionID string         `json:"version_id"`
	TrafficGB int64          `json:"traffic_gb"` // 0 = 不限量
	Prices    []CatalogPrice `json:"prices"`
}

// CatalogPrice 是套餐的一档价格。
type CatalogPrice struct {
	PriceID         string `json:"price_id"`
	BillingInterval string `json:"billing_interval"`
	IntervalCount   int    `json:"interval_count"`
	UnitAmountCents int64  `json:"unit_amount_cents"`
}

// ---- users 的生成输入与输出（gen.go 生成，dbwrite.go 写库） ----

// PlanSpec 是 users 从库里读回的一档套餐（价格取月付、每 1 个月那一档）。
type PlanSpec struct {
	PlanID           string
	VersionID        string
	PriceID          string
	Name             string // plans.name
	VersionNo        int    // plan_versions.version
	Amount           string // 两位小数，如 "15.00"
	Interval         string // 只支持 "month"
	IntervalCount    int    // 只支持 1
	Quotas           []QuotaDef
	EntitlementsJSON string // order_items.snapshot_entitlements（JSON 数组）
	QuotasJSON       string // order_items.snapshot_quotas（JSON 数组）
}

// QuotaDef 是套餐版本的一条配额定义（quota_definitions）。
type QuotaDef struct {
	Metric string
	Limit  *int64 // 空 = 不限
	Unit   string
	Period string // 只支持 cycle 与 total
}

// GenInput 是生成器的输入。
type GenInput struct {
	Now               time.Time      // UTC，截断到微秒
	Loc               *time.Location // 站点时区（settings 的 site.timezone，缺省 UTC），按天用量的日期按它算
	Users             int
	ExpiredCount      int        // 末尾这么多用户的订阅已过期
	ExpireAt          *time.Time // 非空时前 ExpireCount 个用户的订阅到期时间改成它
	ExpireCount       int
	HistoryDays       int
	Seed              uint64
	GroupID           string // 用户都放进这个用户组
	OfflineProviderID string // payment_providers 里 code = 'offline' 那一行
	Plans             []PlanSpec
}

// Credential 是给一份订阅签发的订阅链接凭据（令牌原文只进 users.csv）。
type Credential struct {
	Token  string // base64url 无填充的 32 字节随机值
	Hash   []byte // sha256(Token)
	Prefix string // Token[:8]
	Sealed []byte // 主密钥派生的封存器封存的 Token，AAD 是 subsvc.CredentialAAD(订阅 id)
}

// CredentialIssuer 给一份订阅签发凭据（users.go 用主密钥实现，测试里用假的）。
type CredentialIssuer func(subscriptionID string) (Credential, error)

// Seed 是生成器的全部输出：每张表一组行。uuid 一律是小写带连字符的字符串。
// 不在这里出现的派生行（user_passwords、order_reservation_events、ledger_entries）由 dbwrite.go 按这里的行推出来。
type Seed struct {
	PasswordPHC   string // 所有用户共用的 Argon2id PHC 串（users.go 填）
	Users         []UserRow
	Subscriptions []SubscriptionRow
	Credentials   []CredentialRow
	Quotas        []QuotaRow
	Orders        []OrderRow
	OrderItems    []OrderItemRow
	Reservations  []ReservationRow
	Payments      []PaymentRow
	PaymentEvents []PaymentEventRow
	LedgerTxns    []LedgerTxnRow
	SubEvents     []SubEventRow
	ResetLogs     []ResetLogRow
	Usage         []UsageRow
	Audits        []AuditRow
	CSV           []CSVRow // 与 Users 同序
}

// UserRow → users（status active，display_name 空）。
type UserRow struct {
	ID              string
	Email           string
	GroupID         string
	EmailVerifiedAt time.Time
	CreatedAt       time.Time // updated_at 同值
}

// SubscriptionRow → subscriptions（source_order_id 先空，写完订单后按新购单回填；node_uid 由库生成；label 空）。
type SubscriptionRow struct {
	ID            string
	UserID        string
	PlanID        string
	PlanVersionID string
	PriceID       string
	Status        string // active / expired
	PeriodStart   time.Time
	PeriodEnd     time.Time
	Amount        string
	Interval      string
	IntervalCount int
	ProxyUUID     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CredentialRow → subscription_credentials（status active）。
type CredentialRow struct {
	ID             string
	SubscriptionID string
	UserID         string
	TokenHash      []byte
	TokenPrefix    string
	TokenEncrypted []byte
	CreatedAt      time.Time
}

// QuotaRow → quota_balances。
type QuotaRow struct {
	ID             string
	SubscriptionID string
	Metric         string
	Period         string
	PeriodStart    time.Time
	PeriodEnd      *time.Time // total 为空
	Limit          *int64
	Consumed       int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// OrderRow → orders：status fulfilled，subtotal = total = payable = paid = Amount，折扣、抵扣、免零头、余额抵扣都是 0，
// state_version 3（待付 → 已付 → 已履约），fulfilled_at = updated_at = PaidAt，created_by 与 manual_reason 空。
type OrderRow struct {
	ID             string
	OrderNo        string
	UserID         string
	Kind           string // new / renewal
	Amount         string
	SubscriptionID string
	CreatedAt      time.Time
	ExpiresAt      time.Time // CreatedAt + 30 分钟
	PaidAt         time.Time
}

// OrderItemRow → order_items（quantity 1，unit_amount = line_amount = Amount，snapshot_traffic_bytes 空）。
type OrderItemRow struct {
	ID               string
	OrderID          string
	PriceID          string
	PlanID           string
	PlanVersionID    string
	Name             string
	VersionNo        int
	Interval         string
	IntervalCount    int
	EntitlementsJSON string
	QuotasJSON       string
	Amount           string
	CreatedAt        time.Time
}

// ReservationRow → order_reservations（state captured）；dbwrite 另推两条 order_reservation_events：
// reserve（null → held，actor user = UserID，HeldAt）与 capture（held → captured，actor payment，CapturedAt）。
type ReservationRow struct {
	ID         string
	OrderID    string
	UserID     string
	ExpiresAt  time.Time
	HeldAt     time.Time
	CapturedAt time.Time
}

// PaymentRow → payments（provider = offline 渠道，method offline，fee 0，status succeeded，payment_intent_id 空）。
type PaymentRow struct {
	ID                string
	OrderID           string
	UserID            string
	ProviderPaymentID string // "offline:" + 线下凭证号
	Amount            string
	PaidAt            time.Time
}

// PaymentEventRow → payment_events（provider = offline，event_type payment.succeeded，signature_verified，processed）。
type PaymentEventRow struct {
	ProviderEventID   string // "offline:" + 订单 id + ":" + 线下凭证号
	ProviderPaymentID string
	RawPayload        string    // JSON 对象
	ReceivedAt        time.Time // processed_at 同值
}

// LedgerTxnRow → ledger_transactions（kind order_paid，source_type order，memo 空，actor system）；
// dbwrite 另推两条分录：借 channel_cash(offline) Amount，贷 platform_revenue(main) Amount。
type LedgerTxnRow struct {
	ID         string
	OrderID    string
	Amount     string
	OccurredAt time.Time
}

// SubEventRow → subscription_events。
type SubEventRow struct {
	ID             string
	SubscriptionID string
	EventType      string  // activated / renewed / expired
	FromStatus     *string // activated 为空
	ToStatus       string
	ActorKind      string  // payment / scheduler（都不带 actor_id）
	OrderID        *string // activated、renewed 带订单
	Payload        string  // JSON 对象
	OccurredAt     time.Time
}

// ResetLogRow → traffic_reset_logs（consumed_after 0，actor_id 与 note 空）。
type ResetLogRow struct {
	ID             string
	SubscriptionID string
	UserID         string
	Metric         string
	Reason         string // renewal（cycle 行滚进新一期时 subscription 写的就是 renewal）
	ConsumedBefore int64
	CreatedAt      time.Time
}

// UsageRow → subscription_usage_daily。
type UsageRow struct {
	ID             string
	SubscriptionID string
	Day            time.Time // 站点时区的自然日，用 UTC 零点表示
	Bytes          int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// AuditRow → audit_events（id 由库生成）。
type AuditRow struct {
	ActorKind     string  // user / system
	ActorID       *string // user 时为用户 id
	ActorAudience *string // user 时为 "public"
	AuthContext   *string // user 时为 "session"
	Action        string
	ResourceType  string
	ResourceID    string
	Data          string // JSON 对象
	OccurredAt    time.Time
}

// CSVRow 是 users.csv 的一行（口令列由写出方统一填）。
type CSVRow struct {
	UserID   string
	Email    string
	SubToken string
}
