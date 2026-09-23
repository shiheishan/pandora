package nodefabric

import "encoding/json"

// 面板推给节点端的事件。
//
// 轮询能把「配置有没有变」压到一次 304，但压不掉延迟：改完配置最多要等
// 一个周期（15 秒）才生效。长连接把这段等待去掉，顺便让用户列表可以只
// 传变化的那部分——REST 做不到增量，因为服务端不知道对端手上是哪一版，
// 而一条长连接是有状态的，服务端清楚自己往这条连接推过什么。
//
// 事件名沿用上游 Xboard-Node 的那一套。不是为了兼容它的节点端——协议
// 细节本来就不同——而是这些名字已经把语义说清楚了，另起一套只会在两边
// 对照排查时多一层翻译。
//
// # 传输用 SSE，不是 WebSocket
//
// 上游用的是 WebSocket。我们这条链路只需要面板单向推给节点：节点的上行
// （流量、在线用户、运行状态）本来就走 REST，没有一条要从这个通道回来。
// 为了单向需求引入 WebSocket，换来的是一个新依赖、一次协议升级握手、
// 一套 nginx 配置，以及双向连接自带的那些状态。
//
// SSE 是标准库就能写的普通 HTTP 流，面板里已经有两处在跑（管理端和用户端
// 的实时刷新），nginx 那边的缓冲和超时也早就调好了。它还自带断线重连
// 语义，客户端不用自己实现退避。
//
// 代价是节点端没法从这个通道往回说话。真需要的时候再上 WebSocket——
// 到那时事件定义这一层不用动。
const (
	// EventSyncConfig 节点配置变了，附完整配置。
	EventSyncConfig = "sync.config"
	// EventSyncUsers 全量用户列表。连接建立后的第一条，以及增量对不上
	// 时的兜底。
	EventSyncUsers = "sync.users"
	// EventSyncUserDelta 用户增量。
	EventSyncUserDelta = "sync.user.delta"
	// EventPing / EventPong 应用层心跳。
	//
	// 不能只靠 TCP keepalive：中间的 nginx、云厂商的 LB 都会在空闲若干
	// 分钟后悄悄断掉连接，而两端的 socket 要到下次写才发现。应用层心跳
	// 既维持住中间设备的会话表，也让断连能被及时察觉。
	EventPing = "ping"
	EventPong = "pong"
)

// StreamMessage 是所有事件共用的信封。
type StreamMessage struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
	// Timestamp 是面板发出时的毫秒时间戳，给节点端记日志和算延迟用。
	// 不参与任何判断——两端时钟不同步是常态，拿它做逻辑迟早出问题。
	Timestamp int64 `json:"timestamp,omitempty"`
}

// SyncUsersPayload 是全量用户事件的载荷。
type SyncUsersPayload struct {
	Users []ProxyUser `json:"users"`
	// Version 是这份列表的指纹，和 REST 那边的 ETag 同源。节点端存下来，
	// 断线重连时报给面板，面板据此判断能不能只发增量。
	Version string `json:"version"`
}

// SyncUserDeltaPayload 是用户增量事件的载荷。
type SyncUserDeltaPayload struct {
	Delta UserDelta `json:"delta"`
	// FromVersion 是这条增量基于哪一版算出来的。节点端手上的版本对不上
	// 就丢弃它并请求全量——错位地打补丁比不打更糟，会留下一批本该删掉
	// 的用户还在放行。
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
}

// SyncConfigPayload 是配置变更事件的载荷。
type SyncConfigPayload struct {
	Config json.RawMessage `json:"config"`
	// ETag 与 REST /config 返回的是同一个值，节点端可以直接拿去更新自己
	// 记的版本，避免重连后又拉一次同样的配置。
	ETag string `json:"etag"`
}
