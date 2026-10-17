package service

import (
	"time"

	"github.com/google/uuid"
)

// 本文件由块负责人定稿：后台用户画像、访问明细、同源 IP 聚类与复核用到的类型（实现在 ops.go）。
// identity 调这些方法拼后台页面；audit 只给按用户、按 IP 哈希的聚合读与复核写，不依赖任何业务模块。

// 聚类与复核参数。
const (
	// ClusterWindow 是同源 IP 聚类的时间窗：近 90 天
	ClusterWindow = 90 * 24 * time.Hour
	// ClusterNormalTTL 是「标记为正常」的有效期，到期后重新出现在默认列表
	ClusterNormalTTL = 30 * 24 * time.Hour
	// ClusterListLimit 是聚类列表最多给几个
	ClusterListLimit = 50
)

// 复核结论（与迁移 00033 的 CHECK 一致）。
const (
	ClusterNormal   = "normal"
	ClusterDisabled = "disabled"
)

// 后台查看明文来源 IP 的页面（审计 security.source_ip_viewed 的 data.view）。
const (
	ViewUserProfile = "user_profile"
	ViewAccessLog   = "access_log"
	ViewIPClusters  = "ip_clusters"
)

// Activity 是一个用户作为操作者的审计画像；IP 都是解封后的明文（解不开为空串），切片永不为 nil。
type Activity struct {
	// RegisteredIP 是注册（action = user.registered、操作者是本人）那条审计的来源 IP
	RegisteredIP string
	// Events 是最近 80 条
	Events []ActivityEvent
	// IPs 是最常用的 20 个来源（次数倒序、再最近出现倒序、再哈希升序）
	IPs []ActivityIP
}

// ActivityEvent 是画像里的一条审计。
type ActivityEvent struct {
	Action    string
	Outcome   string
	IP        string
	UserAgent string
	// Audience 是 public / admin（actor_audience），没有为空串
	Audience string
	At       time.Time
}

// ActivityIP 是按来源 IP 归并的统计。Accounts 是近 90 天这个 IP 上的用户账号数（actor_kind = user，含本人；
// 只有本人时为 1），AccountIDs 是这些账号（只有本人时为空切片）。
type ActivityIP struct {
	IP         string
	Count      int
	First      time.Time
	Last       time.Time
	Accounts   int
	AccountIDs []uuid.UUID
}

// AccessQuery 是访问明细里审计这一路的筛选。
type AccessQuery struct {
	// Include 非空时 action 要以其中之一开头；Exclude 里的前缀一个都不能命中（分类筛选用，前缀是代码里的常量）
	Include []string
	Exclude []string
	// IP 非空时按来源 IP 精确匹配（audit 自己算哈希）
	IP string
	// FilterActors 为 true 时只看 ActorIDs 这些操作者（ActorIDs 为空即一条都不要）
	FilterActors bool
	ActorIDs     []uuid.UUID
	// Outcome 是 ""（全部）、success / failure / denied / partial，或 error（= 非 success）
	Outcome string
	// Limit 是最多取几条（按发生时间倒序）
	Limit int
}

// AccessEvent 是访问明细里的一条审计；IP 是明文（解不开为空串）。
type AccessEvent struct {
	Action    string
	Outcome   string
	IP        string
	UserAgent string
	ActorID   *uuid.UUID
	At        time.Time
}

// Cluster 是近 90 天关联到多个用户账号（actor_kind = user）的一个来源 IP。
type Cluster struct {
	// Key 是来源 IP 哈希的 hex
	Key string
	// IP 是解封后的明文（解不开为空串）
	IP         string
	Accounts   int
	Events     int
	First      time.Time
	Last       time.Time
	AccountIDs []uuid.UUID
	Review     *ClusterReview
}

// ClusterReview 是一个聚类当前的复核结论。
type ClusterReview struct {
	Decision  string
	DecidedAt time.Time
	ExpiresAt *time.Time
}
