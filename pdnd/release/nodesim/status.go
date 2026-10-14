package main

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/aegispanel/nodeagent/panel"
)

// RosterApply 是一次名单版本变化：变成哪个版本、什么时候、经全量还是增量。
type RosterApply struct {
	Version   string    `json:"version"`
	AppliedAt time.Time `json:"applied_at"`
	Via       string    `json:"via"` // "full" 或 "delta"
}

// rosterHistoryMax 是每个节点保留的最近几次名单变化。
const rosterHistoryMax = 8

// NodeStatus 是一个节点在模拟器里的当前状态。
type NodeStatus struct {
	NodeType      string `json:"node_type"`
	ConfigSHA256  string `json:"config_sha256"`
	RosterVersion string `json:"roster_version"`
	// RosterAppliedAt 是名单版本最近一次变化（变成 RosterVersion）的时间；同版本重发不更新。
	RosterAppliedAt time.Time `json:"roster_applied_at,omitzero"`
	FullsReceived   int       `json:"fulls_received"`
	DeltasReceived  int       `json:"deltas_received"`
	// RosterHistory 是最近 rosterHistoryMax 次名单版本变化，旧的在前。
	RosterHistory []RosterApply `json:"roster_history,omitempty"`
	UserCount     int           `json:"user_count"`
	// Users 是名单里各用户的 uuid（订阅的 proxy_uuid），升序；脚本据此判断某个用户在不在名单里。
	// 启动参数 --status-users=false 时为 null（节点多、名单大时省掉写盘）。
	Users []string `json:"users"`
}

// ServerStatus 是一台模拟服务器的当前状态与计数。
type ServerStatus struct {
	Name       string `json:"name"`
	ServerID   string `json:"server_id"`
	Enrolled   bool   `json:"enrolled"`
	Connected  bool   `json:"connected"`
	Tombstoned bool   `json:"tombstoned"`
	// Epoch 是收到 Welcome 的次数（每建一条新流加一）。
	Epoch     int    `json:"epoch"`
	SessionID string `json:"session_id"`
	// FirstConnectedAt 是第一次收到 Welcome 的时间；ConnectedAt 是最近一次收到 Welcome 的时间（每条新流更新）。
	FirstConnectedAt time.Time `json:"first_connected_at,omitzero"`
	ConnectedAt      time.Time `json:"connected_at,omitzero"`
	// DisconnectedAt 是最近一次已建立的流断开的时间。
	DisconnectedAt time.Time `json:"disconnected_at,omitzero"`
	ReleaseID      string    `json:"release_id"`
	// Errors 是出错次数：接入失败、已建立的流断开、Tombstone、配置摘要不符或不是 JSON 对象。
	Errors    int    `json:"errors"`
	LastError string `json:"last_error"`

	HeartbeatsSent    int   `json:"heartbeats_sent"`
	TrafficSent       int   `json:"traffic_reports_sent"`
	TrafficAcked      int   `json:"traffic_reports_acked"`
	TrafficBytes      int64 `json:"traffic_bytes_acked"`
	OnlineSent        int   `json:"online_reports_sent"`
	ConfigAcks        int   `json:"config_acks_sent"`
	UsersAcks         int   `json:"users_acks_sent"`
	VersionMismatches int   `json:"version_mismatches"`

	// Nodes 按节点 id。
	Nodes map[string]*NodeStatus `json:"nodes"`
}

// Snapshot 是写进状态文件的整体。
type Snapshot struct {
	UpdatedAt time.Time `json:"updated_at"`
	// Errors 是各服务器 Errors 之和。
	Errors int `json:"errors"`
	// Servers 按服务器名字。
	Servers map[string]*ServerStatus `json:"servers"`
}

// Board 汇总各模拟服务器的状态，并发安全。
type Board struct {
	mu      sync.Mutex
	path    string
	now     func() time.Time
	servers map[string]*ServerStatus
}

// NewBoard 建一个汇总；path 为空时 Write 什么都不做。now 为 nil 用 time.Now。
func NewBoard(path string, now func() time.Time) *Board {
	if now == nil {
		now = time.Now
	}
	return &Board{
		path:    path,
		now:     now,
		servers: make(map[string]*ServerStatus),
	}
}

// Update 在锁内对名为 name 的服务器状态调 fn；不存在先建（Name 填好、Nodes 为非 nil 的空 map）。
func (b *Board) Update(name string, fn func(s *ServerStatus)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.servers[name]
	if s == nil {
		s = &ServerStatus{
			Name:  name,
			Nodes: make(map[string]*NodeStatus),
		}
		b.servers[name] = s
	}
	fn(s)
}

// RecordError 给 name 的 Errors 加一并把 LastError 设成 err.Error()（err 为 nil 时只加一，LastError 不变）。
func (b *Board) RecordError(name string, err error) {
	b.Update(name, func(s *ServerStatus) {
		s.Errors++
		if err != nil {
			s.LastError = err.Error()
		}
	})
}

func copyNodeStatus(n *NodeStatus) *NodeStatus {
	if n == nil {
		return nil
	}
	cp := *n
	if n.Users != nil {
		cp.Users = append([]string(nil), n.Users...)
	}
	if n.RosterHistory != nil {
		cp.RosterHistory = append([]RosterApply(nil), n.RosterHistory...)
	}
	return &cp
}

func copyServerStatus(s *ServerStatus) *ServerStatus {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Nodes != nil {
		cp.Nodes = make(map[string]*NodeStatus, len(s.Nodes))
		for id, n := range s.Nodes {
			cp.Nodes[id] = copyNodeStatus(n)
		}
	}
	return &cp
}

// Snapshot 返回深拷贝（ServerStatus、Nodes、Users 切片都复制），UpdatedAt 取 now()，Errors 是各服务器之和。
func (b *Board) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	snap := Snapshot{
		UpdatedAt: b.now(),
		Servers:   make(map[string]*ServerStatus, len(b.servers)),
	}
	for name, s := range b.servers {
		snap.Servers[name] = copyServerStatus(s)
		snap.Errors += s.Errors
	}
	return snap
}

// Write 把 Snapshot 用 json.MarshalIndent(…, "", "  ") 加换行，经 panel.WriteFileAtomic(path, body, 0o644) 原子写盘
// （先写同目录临时文件再 rename，读者不会看到半截）。path 为空返回 nil。
func (b *Board) Write() error {
	if b.path == "" {
		return nil
	}
	snap := b.Snapshot()
	body, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return panel.WriteFileAtomic(b.path, body, 0o644)
}
