package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// 本文件由主 Agent 定稿：identity 对外的类型，以及 identity 需要、由别的模块实现的接口（app 装配时注入）。
// 改这里要同时改实现方（auth、notify），先和主 Agent 对齐。

// User 是 identity 对其他模块暴露的用户视图。
type User struct {
	ID        uuid.UUID
	Email     string
	Status    string
	CreatedAt time.Time
}

// SessionRevoker 由 auth 实现。在业务事务里调用：先在 Redis 吊销该用户的会话（exceptSessionID 非空时保留它），
// Redis 失败返回 errs.Unavailable 让调用方事务回滚；提交后实现方自己再扫一遍（tx.AfterCommit），并推进凭据纪元。
type SessionRevoker interface {
	RevokeUser(ctx context.Context, userID uuid.UUID, exceptSessionID string) error
}

// AdminGuard 由 auth 实现。停用或封禁用户前在调用方事务里调用：该用户是最后一个有效管理员时返回 errs.Conflict。
type AdminGuard interface {
	EnsureNotLastAdmin(ctx context.Context, userID uuid.UUID) error
}

// VerificationMailer 由 notify 实现。在调用方事务里写一条投递记录，提交后入队发送。
type VerificationMailer interface {
	SendToAddress(ctx context.Context, templateCode, address string, vars map[string]string, userID *uuid.UUID) error
}

// StaffGuard 由 auth 实现（管理员名单归 auth）。后台不分角色，管理员之间对等；identity 用它做两件事：
// 管理员的口令至少 12 位；管理员不能走门户的自助找回密码。
type StaffGuard interface {
	// IsStaff 报告用户是否在管理员名单里（不看账号状态，口令策略宁可多要求几位）。
	IsStaff(ctx context.Context, userID uuid.UUID) (bool, error)
}
