// [INPUT]: 依赖 router.go 的 Deps 与 NewRouter 里已挂 RequireAuth 的 /v1 分组，依赖 middleware 的权限/重认证/幂等链
// [OUTPUT]: 对外提供 registerUserBulkRoutes、registerTrafficResetRoutes、registerUserRoutes、registerUserGroupRoutes、registerDeviceLimitRoutes
// [POS]: api/admin 路由表的「用户批量运营、流量重置、用户状态与改密换链、用户组、设备数限制」段，由 NewRouter 按原注册顺序调用；处理器在 bulk_users.go / traffic_reset.go / handlers.go / usergroup.go / devices.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/middleware"
)

func registerUserBulkRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 用户批量运营 ---
	// 预览只要读权限：它是「发之前先看清影响面」，鼓励多用。
	// 导出会带走一份用户名单，生成账号会造出能登录的凭证，
	// 群发的信收不回来 —— 这三个都要写权限 + 近期重认证。
	r.With(middleware.RequirePermission("iam.user.read", d.Log)).
		Post("/users/bulk/preview", h.previewBulkUsers)
	r.With(
		middleware.RequirePermission("iam.user.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Get("/users/bulk/export", h.exportUsers)
	r.With(
		middleware.RequirePermission("iam.user.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "user_bulk_generate", d.Log),
	).Post("/users/bulk/generate", h.generateUsers)
	r.With(
		middleware.RequirePermission("ops.notification.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "user_bulk_mail", d.Log),
	).Post("/users/bulk/mail", h.sendBulkMail)
}

func registerTrafficResetRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 流量重置（XBD 的 traffic-reset）---
	// 手动重置直接改变用户可用额度，所以要写权限 + 近期重认证；
	// 查日志只要读权限 —— 客服排查「我流量怎么变了」时用得上。
	r.With(middleware.RequirePermission("metering.reset.read", d.Log)).
		Get("/traffic-resets", h.listTrafficResets)
	r.With(middleware.RequirePermission("metering.reset.read", d.Log)).
		Get("/traffic-resets/stats", h.trafficResetStats)
	r.With(middleware.RequirePermission("metering.reset.read", d.Log)).
		Get("/users/{id}/traffic-resets", h.userTrafficResetHistory)
	r.With(
		middleware.RequirePermission("metering.reset.write", d.Log),
		middleware.Idempotency(d.Pool, "traffic_manual_reset", d.Log),
	).Post("/users/{id}/traffic-reset", h.manualResetTraffic)
}

func registerUserRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 用户 ---
	r.With(middleware.RequirePermission("iam.user.read", d.Log)).
		Get("/users", h.listUsers)
	r.With(middleware.RequirePermission("iam.user.read", d.Log)).
		Get("/users/{id}", h.getUser)
	// 改用户状态是高风险动作：要写权限 + 近期重认证
	r.With(
		middleware.RequirePermission("iam.user.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/users/{id}/status", h.setUserStatus)
	// 下面两条与全网关一致：先权限、后重认证。反过来的话，没有权限的
	// 管理员会先被要求输一遍密码，然后才收到拒绝。
	r.With(
		// 改别人的密码属于「出事没法补救」那一类，保留重认证。
		middleware.RequirePermission("iam.user.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/users/{id}/reset-password", h.resetUserPassword)
	r.With(
		// 换掉别人的订阅链接会让他的客户端立刻断，同样保留重认证。
		middleware.RequirePermission("iam.user.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/subscriptions/{id}/rotate", h.rotateSubscriptionLink)
}

func registerUserGroupRoutes(r chi.Router, d Deps, h *handlers) {
	// 用户分组：套餐可见、专属价格、优惠券限定、公告定向都靠它
	r.With(middleware.RequirePermission("iam.user.read", d.Log)).
		Get("/user-groups", h.listUserGroups)
	r.With(middleware.RequirePermission("iam.user.write", d.Log)).
		Post("/user-groups", h.saveUserGroup)
	r.With(middleware.RequirePermission("iam.user.write", d.Log)).
		Post("/user-groups/{id}", h.saveUserGroup)
	r.With(middleware.RequirePermission("iam.user.write", d.Log)).
		Delete("/user-groups/{id}", h.deleteUserGroup)
	r.With(middleware.RequirePermission("iam.user.write", d.Log)).
		Post("/users/{id}/group", h.assignUserGroup)
}

func registerDeviceLimitRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 设备数限制 ---
	r.With(middleware.RequirePermission("iam.user.read", d.Log)).
		Get("/devices", h.listOnlineDevices)
	r.With(middleware.RequirePermission("iam.user.write", d.Log)).
		Post("/subscriptions/{id}/device-limit", h.setDeviceLimit)
	// 切换模式会影响所有人能否连上，与改支付渠道同级，要求近期重认证
	r.With(
		middleware.RequirePermission("iam.user.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/settings/device-limit", h.setDeviceMode)
}
