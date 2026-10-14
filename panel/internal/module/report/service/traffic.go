package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// ownerChunk 是按订阅取主人时每批的订阅数。
const ownerChunk = 5000

// trafficRanges 是排行支持的区间。
var trafficRanges = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// trafficLimits 是排行支持的条数。
var trafficLimits = map[int]struct{}{
	5:  {},
	10: {},
	20: {},
}

// NodeTraffic 给窗口内按实际字节排序的节点排行。
func (s *Service) NodeTraffic(ctx context.Context, q TrafficQuery) (NodeTraffic, error) {
	key := fmt.Sprintf("report:v1:traffic:nodes:%s:%d:%s", q.Range, q.Limit, q.SnapshotAt)
	return cached(ctx, s, key, func(ctx context.Context) (NodeTraffic, error) {
		return s.loadNodeTraffic(ctx, q)
	})
}

func (s *Service) loadNodeTraffic(ctx context.Context, q TrafficQuery) (NodeTraffic, error) {
	w, limit, err := s.trafficWindow(q)
	if err != nil {
		return NodeTraffic{}, err
	}

	nodes, err := s.deps.Nodes.TrafficByNode(ctx, w.From, w.To)
	if err != nil {
		return NodeTraffic{}, err
	}
	subs, err := s.deps.Nodes.TrafficBySubscription(ctx, w.From, w.To)
	if err != nil {
		return NodeTraffic{}, err
	}
	qual, err := s.deps.Nodes.TrafficQuality(ctx, w.From, w.To)
	if err != nil {
		return NodeTraffic{}, err
	}

	var reported, attributed int64
	for _, n := range nodes {
		reported += n.Upload + n.Download
	}
	for _, sub := range subs {
		attributed += sub.Upload + sub.Download
	}
	unattributed := reported - attributed
	if unattributed < 0 {
		unattributed = 0
	}

	items := make([]NodeTrafficItem, len(nodes))
	for i, n := range nodes {
		items[i] = NodeTrafficItem{
			NodeID:      n.NodeID,
			Name:        n.Name,
			DisplayName: n.DisplayName,
			Upload:      n.Upload,
			Download:    n.Download,
			Total:       n.Upload + n.Download,
			LastAt:      n.LastAt,
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Total != items[j].Total {
			return items[i].Total > items[j].Total
		}
		return items[i].NodeID.String() < items[j].NodeID.String()
	})
	if len(items) > limit {
		items = items[:limit]
	}
	if items == nil {
		items = []NodeTrafficItem{}
	}

	var returned int64
	for _, it := range items {
		returned += it.Total
	}
	other := reported - returned
	if other < 0 {
		other = 0
	}

	return NodeTraffic{
		Window: w,
		Items:  items,
		Totals: TrafficTotals{
			Reported:     reported,
			Attributed:   attributed,
			Unattributed: unattributed,
		},
		Returned: returned,
		Other:    other,
		Quality: TrafficQuality{
			InvalidReports: qual.InvalidReports,
			InvalidEntries: qual.InvalidEntries,
		},
	}, nil
}

// UserTraffic 给窗口内按入账字节排序的用户排行（只给脱敏邮箱）。
func (s *Service) UserTraffic(ctx context.Context, q TrafficQuery) (UserTraffic, error) {
	key := fmt.Sprintf("report:v1:traffic:users:%s:%d:%s", q.Range, q.Limit, q.SnapshotAt)
	return cached(ctx, s, key, func(ctx context.Context) (UserTraffic, error) {
		return s.loadUserTraffic(ctx, q)
	})
}

func (s *Service) loadUserTraffic(ctx context.Context, q TrafficQuery) (UserTraffic, error) {
	w, limit, err := s.trafficWindow(q)
	if err != nil {
		return UserTraffic{}, err
	}

	nodes, err := s.deps.Nodes.TrafficByNode(ctx, w.From, w.To)
	if err != nil {
		return UserTraffic{}, err
	}
	subs, err := s.deps.Nodes.TrafficBySubscription(ctx, w.From, w.To)
	if err != nil {
		return UserTraffic{}, err
	}
	qual, err := s.deps.Nodes.TrafficQuality(ctx, w.From, w.To)
	if err != nil {
		return UserTraffic{}, err
	}

	var reported, attributed int64
	for _, n := range nodes {
		reported += n.Upload + n.Download
	}
	for _, sub := range subs {
		attributed += sub.Upload + sub.Download
	}
	unattributed := reported - attributed
	if unattributed < 0 {
		unattributed = 0
	}

	subIDs := make([]uuid.UUID, len(subs))
	for i, sub := range subs {
		subIDs[i] = sub.SubscriptionID
	}
	ownerMap := map[uuid.UUID]uuid.UUID{}
	for i := 0; i < len(subIDs); i += ownerChunk {
		end := i + ownerChunk
		if end > len(subIDs) {
			end = len(subIDs)
		}
		m, err := s.deps.Subscriptions.OwnersBySubscription(ctx, subIDs[i:end])
		if err != nil {
			return UserTraffic{}, err
		}
		for k, v := range m {
			ownerMap[k] = v
		}
	}

	type agg struct {
		userID            uuid.UUID
		upload, download  int64
		subscriptionCount int64
		lastAt            time.Time
	}
	byUser := map[uuid.UUID]*agg{}
	for _, sub := range subs {
		uid, ok := ownerMap[sub.SubscriptionID]
		if !ok {
			continue
		}
		a := byUser[uid]
		if a == nil {
			a = &agg{userID: uid, lastAt: sub.LastAt}
			byUser[uid] = a
		}
		a.upload += sub.Upload
		a.download += sub.Download
		a.subscriptionCount++
		if sub.LastAt.After(a.lastAt) {
			a.lastAt = sub.LastAt
		}
	}

	items := make([]UserTrafficItem, 0, len(byUser))
	for _, a := range byUser {
		items = append(items, UserTrafficItem{
			UserID:            a.userID,
			Upload:            a.upload,
			Download:          a.download,
			Total:             a.upload + a.download,
			SubscriptionCount: a.subscriptionCount,
			LastAt:            a.lastAt,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Total != items[j].Total {
			return items[i].Total > items[j].Total
		}
		return items[i].UserID.String() < items[j].UserID.String()
	})
	if len(items) > limit {
		items = items[:limit]
	}
	if items == nil {
		items = []UserTrafficItem{}
	}

	userIDs := make([]uuid.UUID, len(items))
	for i, it := range items {
		userIDs[i] = it.UserID
	}
	emails := map[uuid.UUID]string{}
	if len(userIDs) > 0 {
		emails, err = s.deps.Users.Emails(ctx, userIDs)
		if err != nil {
			return UserTraffic{}, err
		}
	}
	for i := range items {
		if e, ok := emails[items[i].UserID]; ok {
			items[i].EmailMasked = maskEmail(e)
		} else {
			items[i].EmailMasked = "***"
		}
	}

	var returned int64
	for _, it := range items {
		returned += it.Total
	}
	other := attributed - returned
	if other < 0 {
		other = 0
	}

	return UserTraffic{
		Window: w,
		Items:  items,
		Totals: TrafficTotals{
			Reported:     reported,
			Attributed:   attributed,
			Unattributed: unattributed,
		},
		Returned: returned,
		Other:    other,
		Quality: TrafficQuality{
			InvalidReports: qual.InvalidReports,
			InvalidEntries: qual.InvalidEntries,
		},
	}, nil
}

// trafficWindow 校验排行查询并算出窗口：小时桶起点落在 [From, To) 的整桶，From 是 SnapshotAt − 区间后向上取整到整点。
func (s *Service) trafficWindow(q TrafficQuery) (TrafficWindow, int, error) {
	fields := map[string]string{}

	rng := q.Range
	if rng == "" {
		rng = "7d"
	}
	dur, ok := trafficRanges[rng]
	if !ok {
		fields["range"] = "只支持 24h、7d、30d"
	}

	limit := q.Limit
	if limit == 0 {
		limit = 10
	}
	if _, ok := trafficLimits[limit]; !ok {
		fields["limit"] = "只支持 5、10、20"
	}

	now := s.now().UTC()
	var snap time.Time
	if q.SnapshotAt == "" {
		snap = now
	} else {
		parsed, err := time.Parse(time.RFC3339Nano, q.SnapshotAt)
		if err != nil {
			fields["snapshot_at"] = "时间格式应为 RFC3339"
		} else {
			snap = parsed.UTC().Truncate(time.Microsecond)
			if snap.After(now) {
				fields["snapshot_at"] = "不能晚于现在"
			}
			if snap.Before(now.Add(-31 * 24 * time.Hour)) {
				fields["snapshot_at"] = "不能早于 31 天前"
			}
		}
	}

	if len(fields) > 0 {
		return TrafficWindow{}, 0, errs.Invalid(fields)
	}

	from := snap.Add(-dur)
	t := from.Truncate(time.Hour)
	if !t.Equal(from) {
		from = t.Add(time.Hour)
	}
	to := snap

	return TrafficWindow{
		Range:      rng,
		SnapshotAt: snap,
		From:       from,
		To:         to,
	}, limit, nil
}

// maskEmail 只留本地部分的第一个字符与小写域名，如 a***@example.test；格式不对给 ***。
func maskEmail(e string) string {
	at := strings.LastIndex(e, "@")
	if at <= 0 || at == len(e)-1 {
		return "***"
	}
	r, _ := utf8.DecodeRuneInString(e[:at])
	return string(r) + "***@" + strings.ToLower(e[at+1:])
}
