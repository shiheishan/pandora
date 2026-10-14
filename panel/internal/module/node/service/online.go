package service

// 在线来源入账与节点列表的在线、流量读数。

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net/netip"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
)

// aliveRefresh 是在线记录 last_seen_at 的刷新粒度，必须远小于最短的设备识别窗口（5 分钟，settings 的 devices.window_minutes
// 最小档；「在线」的窗口见 devices.go 的 onlineWindow）：时间戳最多落后 2 分钟加一个上报间隔，仍在窗口内。
const aliveRefresh = 2 * time.Minute

// trafficListWindow 是节点列表里流量的时间窗。
const trafficListWindow = 24 * time.Hour

// deviceKey 是一个来源地址的设备键：IPv4 按地址，IPv6 按 /64（同一台设备的隐私地址会轮换）；先 Unmap、去掉 zone。
func deviceKey(a netip.Addr) string {
	a = a.Unmap().WithZone("")
	if a.Is4() {
		return a.String()
	}
	return netip.PrefixFrom(a, 64).Masked().String()
}

// IngestOnline 记一批在线来源：只收这台服务器上的节点、名单内（m）的正 uid、有效地址；其余丢弃。
// 只存设备键的 SHA-256（与旧库相同），不开事务：在线是遥测，每条语句各自提交，下一分钟就补上。
func (s *Service) IngestOnline(ctx context.Context, serverID uuid.UUID, entries []OnlineEntry, m RosterMembership) error {
	if len(entries) == 0 {
		return nil
	}
	if s.charger == nil {
		return errChargerMissing
	}
	var nodeIDs []uuid.UUID
	seenNode := map[uuid.UUID]bool{}
	for _, e := range entries {
		if !seenNode[e.NodeID] {
			seenNode[e.NodeID] = true
			nodeIDs = append(nodeIDs, e.NodeID)
		}
	}
	onServer, err := s.repo.ServerNodeIDs(ctx, serverID, nodeIDs)
	if err != nil {
		return err
	}
	kept := make([]OnlineEntry, 0, len(entries))
	var uids []int64
	seenUID := map[int64]bool{}
	for _, e := range entries {
		if !onServer[e.NodeID] || e.UID <= 0 || !m.Has(e.NodeID, e.UID) {
			continue
		}
		kept = append(kept, e)
		if !seenUID[e.UID] {
			seenUID[e.UID] = true
			uids = append(uids, e.UID)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	subs, err := s.charger.SubscriptionIDsByNodeUIDs(ctx, uids)
	if err != nil {
		return err
	}
	if s.keys == nil {
		return errors.New("node: 在线设备键的 HMAC 密钥未装配")
	}
	rows := aliveRows(kept, subs, func(b []byte) []byte { return s.keys.HMAC(crypto.InfoHMACNodeDevice, b) })
	return s.repo.UpsertAliveIPs(ctx, s.now().UTC().Truncate(time.Microsecond), aliveRefresh, rows)
}

// DeviceKeyHasher 是 *crypto.Keys 的 HMAC 面：在线设备键只落带密钥的摘要（InfoHMACNodeDevice）。
type DeviceKeyHasher interface {
	HMAC(info string, data []byte) []byte
}

// UseKeys 只在启动装配时调用。没装配时在线上报不落库并回错误。
func (s *Service) UseKeys(k DeviceKeyHasher) { s.keys = k }

// aliveRows 把条目摊平成 (节点, 订阅, 设备键哈希)，去重并按 (节点, 订阅, 哈希) 排序：
// 一条 INSERT … ON CONFLICT DO UPDATE 不能两次碰同一行，同一设备也可能报两遍。找不到订阅的 uid、无效地址跳过。
func aliveRows(entries []OnlineEntry, subs map[int64]uuid.UUID, mac func([]byte) []byte) []repo.AliveRow {
	seen := map[repo.AliveRow]bool{}
	var out []repo.AliveRow
	for _, e := range entries {
		subID, ok := subs[e.UID]
		if !ok {
			continue
		}
		for _, ip := range e.IPs {
			if !ip.IsValid() {
				continue
			}
			r := repo.AliveRow{NodeID: e.NodeID, SubscriptionID: subID, IPHash: hex.EncodeToString(mac([]byte(deviceKey(ip))))}
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if c := bytes.Compare(out[i].NodeID[:], out[j].NodeID[:]); c != 0 {
			return c < 0
		}
		if c := bytes.Compare(out[i].SubscriptionID[:], out[j].SubscriptionID[:]); c != 0 {
			return c < 0
		}
		return out[i].IPHash < out[j].IPHash
	})
	return out
}

// NodeLiveStat 是节点列表一行的在线与流量读数。
type NodeLiveStat struct {
	// OnlineUsers 是设备识别窗口（设置 devices.window_minutes）内在线的订阅数
	OnlineUsers int
	// OnlineIPs 是窗口内在线的 (订阅, 设备键) 数
	OnlineIPs int
	// TrafficBytes24h 是近 24 小时的实际字节（未乘倍率、含名单外，按整点桶）
	TrafficBytes24h int64
}

// NodeLiveStats 按节点 id 批量取在线与 24 小时流量（一条 SQL）；没有数据的节点不在结果里（调用方按零值处理）。
func (s *Service) NodeLiveStats(ctx context.Context, nodeIDs []uuid.UUID) (map[uuid.UUID]NodeLiveStat, error) {
	out := make(map[uuid.UUID]NodeLiveStat, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}
	now := s.now()
	rows, err := s.repo.NodeLiveStats(ctx, nodeIDs, now.Add(-s.onlineWindow(ctx)), now.Add(-trafficListWindow))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.NodeID] = NodeLiveStat{OnlineUsers: r.OnlineUsers, OnlineIPs: r.OnlineIPs, TrafficBytes24h: r.TrafficBytes24h}
	}
	return out, nil
}
