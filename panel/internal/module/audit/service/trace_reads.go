package service

import (
	"time"

	"github.com/google/uuid"
)

// 本文件的类型段由块负责人定稿：后台「溯源总览」要的两个审计聚合读（identity 的 trace.go 调用）。
// 只读 audit_events，不依赖任何业务模块；IP 在这里解封成明文交出去，按明文查询时在这里算 InfoHMACIP 哈希。
// 方法实现写在本文件类型段之后。

// ViewTrace 是溯源总览「显示」明文 IP 时审计 security.source_ip_viewed 的 data.view。
const ViewTrace = "trace"

// TraceIPQuery 是按来源 IP 聚合审计的条件。只算带 ip_hash、actor_kind 为 user 或 anonymous 的事件
// （后台管理员自己的操作不算，否则管理员的出口会排第一）。
type TraceIPQuery struct {
	// Since 是时间窗起点（不含）
	Since time.Time
	// Limit 是按事件数降序取的头部 IP 数（0 = 只要 ExtraIPs）
	Limit int
	// ExtraIPs 是不论排名都要带回的明文 IP（按 netip 规范写法算 InfoHMACIP 哈希；窗内没有事件的不在结果里）
	ExtraIPs []string
}

// TraceIPSet 是按来源 IP 聚合的审计。四个总数是整个时间窗的（不受 Limit 影响），
// 只在结果至少有一行时可信（窗内没有事件时为 0）。
type TraceIPSet struct {
	DistinctIPs   int
	Events        int
	Logins        int // action = user.login 且 outcome = success
	LoginFailures int // action = user.login_failed
	// Rows 先是头部（事件数降序、最近出现降序），再是只因 ExtraIPs 带回的
	Rows []TraceIP
}

// TraceIP 是一个来源 IP 在窗内的审计统计。
type TraceIP struct {
	// IP 是解封后的明文（解不开为空串）
	IP      string
	Events  int
	Logins  int
	UAKinds int // 不同 user_agent 数
	// Accounts 是 actor_kind = user 的不同操作者数；AccountIDs 最多 20 个
	Accounts   int
	AccountIDs []uuid.UUID
	Last       time.Time
}

// TraceUserQuery 是按用户聚合审计的条件（只算 actor_kind = user、带 ip_hash 的事件）。
type TraceUserQuery struct {
	Since time.Time
	// Sort 是 ips / logins / login_ips / shared_ips / ua_kinds，都按降序；不认识的按 ips
	Sort string
	// Limit 是按 Sort 取的头部用户数（0 = 只要 UserIDs）
	Limit int
	// UserIDs 是不论排名都要带回的用户（窗内没有事件的不在结果里）
	UserIDs []uuid.UUID
}

// TraceUser 是一个用户在窗内作为操作者的审计统计。
type TraceUser struct {
	UserID uuid.UUID
	Events int
	Logins int
	// LoginIPs 是有过登录成功的不同 IP 数；DistinctIPs 是全部事件的不同 IP 数
	LoginIPs    int
	DistinctIPs int
	UAKinds     int
	// SharedIPs 是窗内也有别的用户（actor_kind = user）出现过的 IP 数
	SharedIPs int
	Last      time.Time
	// IPs 最多 100 个（次数降序），IP 为明文
	IPs []TraceUserIP
}

// TraceUserIP 是一个用户在一个来源 IP 上的事件数；Shared 同 TraceUser.SharedIPs 的口径。
type TraceUserIP struct {
	IP     string
	Count  int
	Shared bool
}
