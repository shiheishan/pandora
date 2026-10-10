package kernel

import (
	"fmt"
	"sync"
)

// quicUDPSessionsPerUser 是单个用户在一个 Hysteria2 / TUIC 入站上同时存在的 UDP
// 会话上限，也是 Juicity 入站上同时存在的 UDP 路由（每个目标一个上游 socket）上限。
//
// 每个 UDP 会话占一个上游 socket（fd、内核收发缓冲，见 hy2UDPSocketBuffer）和三个
// goroutine，会话数又由客户端决定、空闲 5 分钟才回收。不设上限时，一个已认证用户
// 能把节点的 fd（LimitNOFILE）或全局 udp_mem 耗尽，所有用户一起连不上或丢包。
//
// 取值按正常用法留足余量：tun 模式下每个应用 UDP 流（含经代理的 DNS 查询）各开
// 一个会话，按 5 分钟空闲回收，日常几十到几百个；1024 只挡滥用。超出的新会话被拒、
// 走 OnConnError 的 limit 分类，已有会话不受影响（与设备数上限同语义：拒新不踢旧）。
const quicUDPSessionsPerUser = 1024

// udpSessionQuota 按用户数在途的 UDP 会话。零值可用；limit 为 0 时用缺省上限。
type udpSessionQuota struct {
	mu     sync.Mutex
	limit  int
	byUser map[int64]int
}

// acquire 为用户占一个会话名额，到上限返回 false（什么也不登记）。
func (q *udpSessionQuota) acquire(userID int64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	limit := q.limit
	if limit <= 0 {
		limit = quicUDPSessionsPerUser
	}
	if q.byUser[userID] >= limit {
		return false
	}
	if q.byUser == nil {
		q.byUser = make(map[int64]int)
	}
	q.byUser[userID]++
	return true
}

// release 归还一次成功 acquire 的名额。
func (q *udpSessionQuota) release(userID int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	n, ok := q.byUser[userID]
	if !ok {
		return
	}
	if n > 1 {
		q.byUser[userID] = n - 1
		return
	}
	delete(q.byUser, userID)
}

// udpSessionLimitError 是 UDP 会话数到上限的拒绝原因，归 limit 分类。
func udpSessionLimitError(protocol string) error {
	return markConnError(connErrLimit, fmt.Errorf("%s UDP session limit", protocol))
}
