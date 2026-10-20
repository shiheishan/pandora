package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// 本文件由块负责人定稿：后台「安全与运维 › 溯源总览」用到的对外类型、取值与端口（实现在 trace.go）。
// 两路数据：订阅拉取日志（subscription 实现 TraceFetches）与审计（audit 的 TraceIPs / TraceUsers）。
// 两路的 IP 哈希用的盐不同（拉取 InfoHMACSubAudit、审计 InfoHMACIP），不能按哈希对上：
// 各自在自己的 service 里解封出明文 IP，identity 按明文合并；补齐「另一路的数」时把明文 IP / 用户 id 传过去，
// 由对方自己算哈希。改这里要同时改实现方（subscription/service/trace_reads.go、audit/service/trace_reads.go）。

// 时间窗（查询参数 window 的取值）。30 天受拉取日志 31 天保留期限制。
const (
	TraceWindow24h = "24h"
	TraceWindow7d  = "7d"
	TraceWindow30d = "30d"
)

// 订阅更新排行的排序（查询参数 sort）；「地区数」由前端在本页里排。
const (
	TraceSubSortFetches  = "fetches"
	TraceSubSortIPs      = "ips"
	TraceSubSortUAKinds  = "ua_kinds"
	TraceSubSortFailures = "failures"
	TraceSubSortLast     = "last"
)

// 账号综合排行的排序（查询参数 sort），缺省 linked_ips。
const (
	TraceAccSortLinkedIPs = "linked_ips"
	TraceAccSortLogins    = "logins"
	TraceAccSortLoginIPs  = "login_ips"
	TraceAccSortFetchIPs  = "fetch_ips"
	TraceAccSortSharedIPs = "shared_ips"
	TraceAccSortUAKinds   = "ua_kinds"
	TraceAccSortDCHits    = "datacenter_hits"
)

// 规模上限。
const (
	// TraceRankLimit 是每张排行最多给几行
	TraceRankLimit = 100
	// TraceScanIPs 是概览与 IP 排行每一路最多取的 IP 数（按次数降序）；超过时独立 IP 数按估算给
	TraceScanIPs = 5000
	// TraceExtraIPs 是 IP 排行里每一路拿去对方补齐的头部 IP 数
	TraceExtraIPs = 200
	// TraceUserIPs 是每个用户每一路最多带回的 IP 数（按次数降序），用来算地区数、机房次数、两路去重
	TraceUserIPs = 100
	// TraceIPAccounts 是每个 IP 每一路最多带回的账号 id 数
	TraceIPAccounts = 20
	// TraceRevealTTL 是「显示」令牌的有效期
	TraceRevealTTL = 2 * time.Hour
)

// ---- 拉取这一路（subscription 实现）----

// TraceFetches 由 subscription 实现（subscription/service/trace_reads.go），启动装配时经 UseTraceFetches 注入。
// 两个方法各是一条 SQL：对时间窗内的拉取日志做一次扫描，先按排序取头部，再把 Extra* 指定的 IP / 用户一并带回；
// TraceFetchUsers 在 Limit = 0 时只补查 UserIDs（按凭据过滤的窄 SQL）。
type TraceFetches interface {
	TraceFetchIPs(ctx context.Context, q TraceFetchIPQuery) (TraceFetchIPSet, error)
	TraceFetchUsers(ctx context.Context, q TraceFetchUserQuery) ([]TraceFetchUser, error)
}

// TraceFetchIPQuery 是按来源 IP 聚合拉取日志的条件。
type TraceFetchIPQuery struct {
	// Since 是时间窗起点（不含）
	Since time.Time
	// Limit 是按拉取次数降序取的头部 IP 数（0 = 只要 ExtraIPs）
	Limit int
	// ExtraIPs 是不论排名都要带回的明文 IP（subscription 自己算哈希；窗内没有拉取的不在结果里）
	ExtraIPs []string
}

// TraceFetchIPSet 是按来源 IP 聚合的拉取日志。三个总数是整个时间窗的（不受 Limit 影响），
// 只在结果至少有一行时可信（窗内没有拉取时为 0）。
type TraceFetchIPSet struct {
	// DistinctIPs 是窗内不同来源 IP 数（按哈希，含未认证失败的采样行）
	DistinctIPs int
	// Fetches 是窗内拉取总行数；Failures 是其中 result 不是 ok 的
	Fetches  int
	Failures int
	// Rows 先是头部（次数降序、最近出现降序），再是只因 ExtraIPs 带回的
	Rows []TraceFetchIP
}

// TraceFetchIP 是一个来源 IP 在窗内的拉取统计。
type TraceFetchIP struct {
	// IP 是解封后的明文（解不开为空串）
	IP      string
	Fetches int
	// UAKinds 是不同 UA 原文数（按 ua_hash）
	UAKinds int
	// Accounts 是经凭据认出的不同用户数（未认证失败不算）；AccountIDs 最多 TraceIPAccounts 个
	Accounts   int
	AccountIDs []uuid.UUID
	Last       time.Time
}

// TraceFetchUserQuery 是按用户聚合拉取日志的条件（只算认出凭据的拉取）。
type TraceFetchUserQuery struct {
	Since time.Time
	// Sort 是 fetches / ips / ua_kinds / failures / last / shared_ips，都按降序；不认识的按 fetches
	Sort string
	// Limit 是按 Sort 取的头部用户数（0 = 只要 UserIDs）
	Limit int
	// UserIDs 是不论排名都要带回的用户（窗内没有拉取的不在结果里）
	UserIDs []uuid.UUID
}

// TraceFetchUser 是一个用户在窗内的拉取统计。
type TraceFetchUser struct {
	UserID   uuid.UUID
	Fetches  int
	Failures int
	// DistinctIPs 是不同来源 IP 数；UAKinds 是不同 UA 原文数；Families 是客户端家族（升序去重）
	DistinctIPs int
	UAKinds     int
	Families    []string
	// SharedIPs 是窗内也被别的用户的订阅拉取过的来源 IP 数
	SharedIPs int
	Last      time.Time
	// IPs 是最多 TraceUserIPs 个来源（次数降序），IP 为明文
	IPs []TraceIPCount
}

// TraceIPCount 是一个用户在一个来源 IP 上的次数；Shared 为 true 表示窗内同一路里别的账号也用过这个 IP。
type TraceIPCount struct {
	IP     string
	Count  int
	Shared bool
}

// ---- identity 对外（handler 用）----

// TraceIPReport 是概览与 IP 访问排行（同一次计算：两路各取头部 TraceScanIPs 个 IP 合并）。
type TraceIPReport struct {
	Window   string
	Since    time.Time
	Overview TraceOverview
	IPs      []TraceIPRow
}

// TraceOverview 是时间窗内的概览。
type TraceOverview struct {
	// DistinctIPs 是两路按明文合并后的独立 IP 数；任一路超过 TraceScanIPs 个 IP 时是估算，DistinctIPsApprox 为 true
	DistinctIPs       int
	DistinctIPsApprox bool
	// Fetches / FetchFailures 是拉取总次数与其中失败的（含未认证失败的采样行）
	Fetches       int
	FetchFailures int
	// Logins 是登录成功次数（user.login）；LoginFailures 是登录失败审计条数（同一邮箱或 IP 窗口内只记一条）
	Logins        int
	LoginFailures int
	// AnalyzedIPs 是参与归属地统计的 IP 数（两路头部合并），DatacenterIPs 是其中机房 IP 数
	AnalyzedIPs   int
	DatacenterIPs int
	// TopRegions 最多 5 个，按次数降序
	TopRegions []TraceRegion
}

// TraceRegion 是一个来源地区（国家 + 省 / 州；判不出为「未知」）。
type TraceRegion struct {
	Region string
	IPs    int
	Hits   int
}

// TraceIPRow 是 IP 访问排行的一行；IP 只给打码后的形式，明文经 Token 调 TraceReveal 取。
type TraceIPRow struct {
	// Token 是「显示」令牌（明文解不开时为空串）
	Token       string
	IPMasked    string
	Geo         string
	NetworkKind string
	// Hits = Fetches + Events（审计里 actor 为 user / anonymous 的事件）
	Hits    int
	Fetches int
	Events  int
	// Accounts 是两路账号合并去重后的数；任一路的账号超过 TraceIPAccounts 时 AccountsCapped 为 true，
	// Accounts 取「合并数」与两路各自的数中最大的（至少这么多）
	Accounts       int
	AccountsCapped bool
	// UAKinds 是两路不同 UA 数之和（登录 UA 与客户端 UA 几乎不会相同，不去重）
	UAKinds int
	Last    time.Time
	// Users 按邮箱升序，最多 2 × TraceIPAccounts 个，永不为 nil
	Users []TraceUserRef
}

// TraceUserRef 是排行里的一个账号。
type TraceUserRef struct {
	ID     uuid.UUID
	Email  string
	Status string
}

// TraceSubscriberRow 是订阅更新排行的一行。
type TraceSubscriberRow struct {
	UserID   uuid.UUID
	Email    string
	Status   string
	Fetches  int
	Failures int
	IPs      int
	UAKinds  int
	// Families 永不为 nil
	Families []string
	// Regions 是来源 IP（最多 TraceUserIPs 个）的不同地区数
	Regions int
	Last    time.Time
}

// TraceAccountRow 是账号综合排行的一行（两路合并）。
type TraceAccountRow struct {
	UserID uuid.UUID
	Email  string
	Status string
	// LinkedIPs 是登录等行为 IP 与拉取 IP 按明文合并去重后的数
	LinkedIPs int
	Logins    int
	LoginIPs  int
	FetchIPs  int
	// SharedIPs 是同一路里也被别的账号用过的 IP 数（两路合并去重）
	SharedIPs int
	// UAKinds 是两路不同 UA 数之和
	UAKinds int
	// DatacenterHits 是来自机房 IP 的事件与拉取次数
	DatacenterHits int
	Last           time.Time
}

// TraceSubscriberReport 是订阅更新排行（Window、Sort 是规整后的取值）。
type TraceSubscriberReport struct {
	Window string
	Since  time.Time
	Sort   string
	Rows   []TraceSubscriberRow
}

// TraceAccountReport 是账号综合排行（Window、Sort 是规整后的取值）。
type TraceAccountReport struct {
	Window string
	Since  time.Time
	Sort   string
	Rows   []TraceAccountRow
}

// TraceReveal 是「显示」解出的明文 IP 与归属地。
type TraceReveal struct {
	IP          string
	Geo         string
	NetworkKind string
}

// UseTraceFetches 只在启动装配时调用（不并发安全）。
func (s *Service) UseTraceFetches(t TraceFetches) { s.traceFetches = t }
