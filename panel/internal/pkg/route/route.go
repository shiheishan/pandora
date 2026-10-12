// Package route 约定业务模块怎么把路由挂到 public / admin 两个 gin 引擎上。
//
// 鉴权、重认证、幂等、开关这些路由级中间件由 auth、idempotency、settings 模块实现，
// app 装配时填进 Kit 传给每个模块；模块之间因此不需要 import 彼此的 handler 包。
package route

import "github.com/gin-gonic/gin"

// Kit 是模块注册路由时可用的路由级中间件。
type Kit struct {
	// RequireAuth 要求已登录（本域的会话）。后台不分权限：admin 上它就是「已登录的管理员」。
	RequireAuth gin.HandlerFunc
	// RecentReauth 要求 15 分钟内重认证过；只用于 admin，排在 Idempotency 之前。
	RecentReauth gin.HandlerFunc
	// Idempotency 按 Idempotency-Key 头认领；scope 是业务作用域名。
	Idempotency func(scope string) gin.HandlerFunc
	// Switch 要求功能开关打开。
	Switch func(code string) gin.HandlerFunc
}

// Module 由每个业务模块的 handler 包实现；不挂某个域就留空实现。
// public 与 admin 都是 /v1 分组。
type Module interface {
	RegisterPublic(v1 *gin.RouterGroup, k Kit)
	RegisterAdmin(v1 *gin.RouterGroup, k Kit)
}
