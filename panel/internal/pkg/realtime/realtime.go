// Package realtime 提供服务端推送，让页面不刷新也能跟上数据变化。
//
// 选 SSE 而不是 WebSocket：这里的数据流是单向的（服务端 → 浏览器），
// 客户端要提交什么走现成的 REST 就行，用不上双向通道。而 SSE 是纯 HTTP，
// nginx 只需关掉 buffering，不必配 Upgrade；浏览器原生 EventSource
// 自带断线重连；认证直接复用现有的 JWT 中间件，不用为握手另写一套。
//
// 每条事件只说明「某个东西变了」，客户端收到后自己去拉最新数据。
package realtime

import (
	"github.com/google/uuid"
)

// Event 是一条推送。Payload 只放定位信息（哪个工单、哪个订阅），
// 不放业务数据本身。
type Event struct {
	Topic   string         `json:"topic"`
	Payload map[string]any `json:"payload,omitempty"`
}

// 前端认得的变更主题。
const (
	TopicOrders        = "orders.changed"
	TopicSubscriptions = "subscriptions.changed"
	TopicTickets       = "tickets.changed"
	TopicTicketUpdated = "ticket.updated"
	TopicPlans         = "plans.changed"
	TopicNodes         = "nodes.changed"
	TopicAnnouncements = "announcements.changed"
	TopicSwitches      = "switches.changed"
)

// topicSessionRevoked 只在 Hub 与 SSE 循环之间用，不发给浏览器。
const topicSessionRevoked = "session.revoked"

// 变更操作，与前端 EventsCapsule 的 op 字段一致。
const (
	OpInsert = "INSERT"
	OpUpdate = "UPDATE"
	OpDelete = "DELETE"
)

// ChannelPublic 是门户公共频道。
func ChannelPublic() string { return "rt:public" }

// ChannelAdmin 是管理端频道：租户内每一条变更都会额外抄送一份。
func ChannelAdmin() string { return "rt:admin" }

// ChannelUser 是某个用户的私有频道。
func ChannelUser(id uuid.UUID) string { return "rt:user:" + id.String() }

// ChannelSession 是会话吊销信号频道；前缀必须与 auth 吊销 Lua 里的 rt:sess: 一致。
func ChannelSession(sid string) string { return "rt:sess:" + sid }
