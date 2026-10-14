package service

import "time"

// ActionReferrerChanged 是后台改邀请人的审计动作。
const ActionReferrerChanged = "user.referrer_changed"

// inviteCodesListMax 是门户邀请页最多列出的个人邀请码个数。
const inviteCodesListMax = 50

// inviteVisitWindow 是同一 IP 对同一邀请码的访问去重窗口。
const inviteVisitWindow = 24 * time.Hour

// referrerChainMaxDepth 是改邀请人时往上查环的最大层数。假设：生效邀请链不会超过 100 层。
// 注册时的绑定只能指向更早注册的人，天然无环；后台改动都在全局咨询锁里先查环再写，所以图始终无环，
// 这里的层数只是给递归查询兜底。链真超过 100 层时，更远处的环会漏查。
const referrerChainMaxDepth = 100

// InviteCodeView 是门户邀请页里的一个个人邀请码。
type InviteCodeView struct {
	Code      string
	Status    string // active / exhausted / disabled
	Invited   int    // 用这个码注册的人数（used_count）
	Visits    int64  // 访问量（按 IP 去重）
	MaxUses   *int
	CreatedAt time.Time
}

// MyInviteCodes 是门户邀请页的码列表与上限。
type MyInviteCodes struct {
	Codes    []InviteCodeView // 永不为 nil
	MaxCodes int              // 一人最多同时持有的 active 码
}
