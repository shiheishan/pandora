package service

import (
	"context"
	"encoding/base64"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// 溯源总览（安全与运维 › 溯源总览）的读服务：两路数据的口径与合并规则见 trace_types.go 文件头。

// errTraceTokenInvalid 是「显示」令牌解不开、过期或不是本人签发时的统一回应。
func errTraceTokenInvalid() error {
	return errs.Invalid(map[string]string{"token": "已失效，请刷新列表后再试"})
}

// errTraceSortInvalid 是排序取值不认识时的回应。
func errTraceSortInvalid() error {
	return errs.Invalid(map[string]string{"sort": "不支持的排序"})
}

// TraceIPs 返回时间窗内的概览与 IP 访问排行：两路各取头部 TraceScanIPs 个 IP，头部 TraceExtraIPs 个互相补齐另一路的数，
// 按明文合并后排序。列表只给打码 IP 与「显示」令牌，不写审计（明文经 TraceReveal 取，那里写）。
func (s *Service) TraceIPs(ctx context.Context, window string) (TraceIPReport, error) {
	p, err := principal(ctx)
	if err != nil {
		return TraceIPReport{}, err
	}
	win, since, err := s.traceWindow(window)
	if err != nil {
		return TraceIPReport{}, err
	}
	fetches, err := s.needTraceFetches()
	if err != nil {
		return TraceIPReport{}, err
	}
	sealer, err := s.keys.Sealer()
	if err != nil {
		return TraceIPReport{}, err
	}
	now := s.now()

	a, err := s.ops.TraceIPs(ctx, auditsvc.TraceIPQuery{Since: since, Limit: TraceScanIPs})
	if err != nil {
		return TraceIPReport{}, err
	}
	extra := traceExtraFromAudit(a.Rows)
	f, err := fetches.TraceFetchIPs(ctx, TraceFetchIPQuery{Since: since, Limit: TraceScanIPs, ExtraIPs: extra})
	if err != nil {
		return TraceIPReport{}, err
	}

	audit := a.Rows
	missing := traceMissingAuditIPs(audit, f.Rows)
	if len(missing) > 0 {
		a2, err := s.ops.TraceIPs(ctx, auditsvc.TraceIPQuery{Since: since, Limit: 0, ExtraIPs: missing})
		if err != nil {
			return TraceIPReport{}, err
		}
		audit = append(audit, a2.Rows...)
	}

	merged := mergeTraceIPs(audit, f.Rows)
	ov := traceOverview(a, f, merged, s.geo.Lookup)
	top := rankTraceIPs(merged)

	allIDs := make([]uuid.UUID, 0)
	for _, g := range top {
		for id := range g.ids {
			allIDs = append(allIDs, id)
		}
	}
	users, err := s.traceUsers(ctx, allIDs)
	if err != nil {
		return TraceIPReport{}, err
	}

	ips := make([]TraceIPRow, 0, len(top))
	for _, g := range top {
		loc := s.geo.Lookup(g.ip)
		accts, capped := traceAccountsOf(g)
		rowUsers := traceRowUsers(g.ids, users)
		ips = append(ips, TraceIPRow{
			Token:          traceToken(sealer, p.UserID, g.ip, now),
			IPMasked:       maskIP(g.ip),
			Geo:            loc.Display,
			NetworkKind:    string(loc.Kind),
			Hits:           g.events + g.fetches,
			Fetches:        g.fetches,
			Events:         g.events,
			Accounts:       accts,
			AccountsCapped: capped,
			UAKinds:        g.auditUA + g.fetchUA,
			Last:           g.last,
			Users:          rowUsers,
		})
	}

	return TraceIPReport{
		Window:   win,
		Since:    since,
		Overview: ov,
		IPs:      ips,
	}, nil
}

// traceExtraFromAudit 是审计头部 TraceExtraIPs 行里能解开的明文 IP，拿去拉取那一路补齐。
func traceExtraFromAudit(rows []auditsvc.TraceIP) []string {
	n := TraceExtraIPs
	if n > len(rows) {
		n = len(rows)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ip := strings.TrimSpace(rows[i].IP)
		if ip != "" {
			out = append(out, ip)
		}
	}
	return out
}

// traceMissingAuditIPs 是拉取头部 TraceExtraIPs 行里审计结果没有的 IP，再回审计补齐。
func traceMissingAuditIPs(audit []auditsvc.TraceIP, fetchRows []TraceFetchIP) []string {
	auditKeys := make(map[string]struct{})
	for _, row := range audit {
		key := normTraceIP(row.IP)
		if key != "" {
			auditKeys[key] = struct{}{}
		}
	}
	n := TraceExtraIPs
	if n > len(fetchRows) {
		n = len(fetchRows)
	}
	var missing []string
	for i := 0; i < n; i++ {
		ip := strings.TrimSpace(fetchRows[i].IP)
		if ip == "" {
			continue
		}
		key := normTraceIP(ip)
		if _, ok := auditKeys[key]; !ok {
			missing = append(missing, ip)
		}
	}
	return missing
}

// traceRowUsers 是一个 IP 关联的账号（查得到的），按邮箱升序。
func traceRowUsers(ids map[uuid.UUID]struct{}, users map[uuid.UUID]TraceUserRef) []TraceUserRef {
	out := make([]TraceUserRef, 0, len(ids))
	for id := range ids {
		if u, ok := users[id]; ok {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Email < out[j].Email
	})
	return out
}

// TraceSubscribers 返回订阅更新排行：按 sortKey 取拉取最多的 TraceRankLimit 个用户。
func (s *Service) TraceSubscribers(ctx context.Context, window, sortKey string) (TraceSubscriberReport, error) {
	_, err := principal(ctx)
	if err != nil {
		return TraceSubscriberReport{}, err
	}
	win, since, err := s.traceWindow(window)
	if err != nil {
		return TraceSubscriberReport{}, err
	}
	sortKey = strings.TrimSpace(sortKey)
	if sortKey == "" {
		sortKey = TraceSubSortFetches
	}
	switch sortKey {
	case TraceSubSortFetches, TraceSubSortIPs, TraceSubSortUAKinds, TraceSubSortFailures, TraceSubSortLast:
	default:
		return TraceSubscriberReport{}, errTraceSortInvalid()
	}
	fetches, err := s.needTraceFetches()
	if err != nil {
		return TraceSubscriberReport{}, err
	}
	rows, err := fetches.TraceFetchUsers(ctx, TraceFetchUserQuery{
		Since: since,
		Sort:  sortKey,
		Limit: TraceRankLimit,
	})
	if err != nil {
		return TraceSubscriberReport{}, err
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.UserID)
	}
	users, err := s.traceUsers(ctx, ids)
	if err != nil {
		return TraceSubscriberReport{}, err
	}
	out := make([]TraceSubscriberRow, 0, len(rows))
	for _, row := range rows {
		ref := users[row.UserID]
		fams := row.Families
		if fams == nil {
			fams = []string{}
		}
		out = append(out, TraceSubscriberRow{
			UserID:   row.UserID,
			Email:    ref.Email,
			Status:   ref.Status,
			Fetches:  row.Fetches,
			Failures: row.Failures,
			IPs:      row.DistinctIPs,
			UAKinds:  row.UAKinds,
			Families: fams,
			Regions:  traceRegionCount(row.IPs, s.geo.Lookup),
			Last:     row.Last,
		})
	}
	return TraceSubscriberReport{
		Window: win,
		Since:  since,
		Sort:   sortKey,
		Rows:   out,
	}, nil
}

// TraceAccounts 返回账号综合排行：两路各按 sortKey 对应的口径取头部，互相补齐另一路的数，合并后再排、截到 TraceRankLimit。
func (s *Service) TraceAccounts(ctx context.Context, window, sortKey string) (TraceAccountReport, error) {
	_, err := principal(ctx)
	if err != nil {
		return TraceAccountReport{}, err
	}
	win, since, err := s.traceWindow(window)
	if err != nil {
		return TraceAccountReport{}, err
	}
	sortKey = strings.TrimSpace(sortKey)
	if sortKey == "" {
		sortKey = TraceAccSortLinkedIPs
	}
	aSort, fSort, ok := traceAccountSortPair(sortKey)
	if !ok {
		return TraceAccountReport{}, errTraceSortInvalid()
	}
	fetches, err := s.needTraceFetches()
	if err != nil {
		return TraceAccountReport{}, err
	}

	auditList := []auditsvc.TraceUser{}
	if aSort != "" {
		aRows, err := s.ops.TraceUsers(ctx, auditsvc.TraceUserQuery{
			Since: since,
			Sort:  aSort,
			Limit: TraceRankLimit,
		})
		if err != nil {
			return TraceAccountReport{}, err
		}
		auditList = aRows
	}

	fLimit := 0
	fSortQuery := TraceSubSortFetches
	if fSort != "" {
		fLimit = TraceRankLimit
		fSortQuery = fSort
	}
	userIDs := make([]uuid.UUID, 0, len(auditList))
	for _, u := range auditList {
		userIDs = append(userIDs, u.UserID)
	}
	fRows, err := fetches.TraceFetchUsers(ctx, TraceFetchUserQuery{
		Since:   since,
		Sort:    fSortQuery,
		Limit:   fLimit,
		UserIDs: userIDs,
	})
	if err != nil {
		return TraceAccountReport{}, err
	}

	auditByID := make(map[uuid.UUID]auditsvc.TraceUser)
	for _, u := range auditList {
		auditByID[u.UserID] = u
	}
	fetchByID := make(map[uuid.UUID]TraceFetchUser)
	for _, u := range fRows {
		fetchByID[u.UserID] = u
	}
	missing := make([]uuid.UUID, 0)
	for id := range fetchByID {
		if _, ok := auditByID[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		aExtra, err := s.ops.TraceUsers(ctx, auditsvc.TraceUserQuery{
			Since:   since,
			Sort:    "ips",
			Limit:   0,
			UserIDs: missing,
		})
		if err != nil {
			return TraceAccountReport{}, err
		}
		for _, u := range aExtra {
			auditByID[u.UserID] = u
		}
	}

	rows := combineTraceAccounts(auditByID, fetchByID, s.geo.Lookup)
	sortTraceAccounts(rows, sortKey)
	if len(rows) > TraceRankLimit {
		rows = rows[:TraceRankLimit]
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.UserID)
	}
	users, err := s.traceUsers(ctx, ids)
	if err != nil {
		return TraceAccountReport{}, err
	}
	for i := range rows {
		ref := users[rows[i].UserID]
		rows[i].Email = ref.Email
		rows[i].Status = ref.Status
	}
	return TraceAccountReport{
		Window: win,
		Since:  since,
		Sort:   sortKey,
		Rows:   rows,
	}, nil
}

// traceAccountSortPair 是账号排行的排序在两路各按什么取头部（空串 = 这一路不取头部，只补齐）。
func traceAccountSortPair(sortKey string) (aSort, fSort string, ok bool) {
	switch sortKey {
	case TraceAccSortLinkedIPs:
		return "ips", "ips", true
	case TraceAccSortLogins:
		return "logins", "", true
	case TraceAccSortLoginIPs:
		return "login_ips", "", true
	case TraceAccSortFetchIPs:
		return "", "ips", true
	case TraceAccSortSharedIPs:
		return "shared_ips", "shared_ips", true
	case TraceAccSortUAKinds:
		return "ua_kinds", "ua_kinds", true
	case TraceAccSortDCHits:
		return "ips", "ips", true
	default:
		return "", "", false
	}
}

// TraceReveal 解开「显示」令牌，先写查看明文来源 IP 的审计，再返回明文与归属地。
func (s *Service) TraceReveal(ctx context.Context, token string) (TraceReveal, error) {
	p, err := principal(ctx)
	if err != nil {
		return TraceReveal{}, err
	}
	sealer, err := s.keys.Sealer()
	if err != nil {
		return TraceReveal{}, err
	}
	now := s.now()
	ip, ok := openTraceToken(sealer, p.UserID, token, now)
	if !ok {
		return TraceReveal{}, errTraceTokenInvalid()
	}
	if err := s.ops.RecordSourceIPView(ctx, auditsvc.ViewTrace, nil, map[string]any{"masked": maskIP(ip)}); err != nil {
		return TraceReveal{}, err
	}
	loc := s.geo.Lookup(ip)
	return TraceReveal{
		IP:          ip,
		Geo:         loc.Display,
		NetworkKind: string(loc.Kind),
	}, nil
}

// traceWindow 规整时间窗并算起点；空串按 24h。
func (s *Service) traceWindow(window string) (string, time.Time, error) {
	window = strings.TrimSpace(window)
	if window == "" {
		window = TraceWindow24h
	}
	now := s.now()
	var since time.Time
	switch window {
	case TraceWindow24h:
		since = now.Add(-24 * time.Hour)
	case TraceWindow7d:
		since = now.Add(-7 * 24 * time.Hour)
	case TraceWindow30d:
		since = now.Add(-30 * 24 * time.Hour)
	default:
		return "", time.Time{}, errs.Invalid(map[string]string{"window": "只支持 24h、7d 或 30d"})
	}
	return window, since, nil
}

func (s *Service) needTraceFetches() (TraceFetches, error) {
	if s.traceFetches == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.traceFetches, nil
}

// maskIP 给列表与审计用的打码形式：IPv4 留前两段（a.b.*.*），IPv6 留前两组（2001:db8:*）；空串回空串，解析不了回 *。
// 前端 users/RiskTab.tsx 的 maskIp 与它同口径。
func maskIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "*"
	}
	addr = addr.Unmap()
	if addr.Is4() {
		b := addr.As4()
		return strconv.Itoa(int(b[0])) + "." + strconv.Itoa(int(b[1])) + ".*.*"
	}
	parts := strings.Split(addr.String(), ":")
	if len(parts) >= 2 {
		return parts[0] + ":" + parts[1] + ":*"
	}
	return "*"
}

// traceRevealAAD 把令牌绑在签发它的管理员上：换个管理员解不开。
func traceRevealAAD(admin uuid.UUID) []byte {
	return []byte("identity.trace_reveal:" + admin.String())
}

// traceToken 签发「显示」令牌：封存「签发秒数|明文 IP」，base64url；明文为空或封存失败回空串。
func traceToken(sealer *crypto.Sealer, admin uuid.UUID, ip string, now time.Time) string {
	ip = strings.TrimSpace(ip)
	if ip == "" || sealer == nil {
		return ""
	}
	plain := strconv.FormatInt(now.Unix(), 10) + "|" + ip
	sealed, err := sealer.Seal([]byte(plain), traceRevealAAD(admin))
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(sealed)
}

// openTraceToken 解开令牌：签发超过 TraceRevealTTL、签发时刻比现在晚一分钟以上、不是本人签发的一律不认。
func openTraceToken(sealer *crypto.Sealer, admin uuid.UUID, token string, now time.Time) (string, bool) {
	if sealer == nil || len(token) == 0 || len(token) > 512 {
		return "", false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", false
	}
	plain, err := sealer.Open(sealed, traceRevealAAD(admin))
	if err != nil {
		return "", false
	}
	body := string(plain)
	pipe := strings.IndexByte(body, '|')
	if pipe < 0 {
		return "", false
	}
	tsStr := body[:pipe]
	ip := strings.TrimSpace(body[pipe+1:])
	if ip == "" {
		return "", false
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", false
	}
	issued := time.Unix(ts, 0)
	if now.After(issued.Add(TraceRevealTTL)) {
		return "", false
	}
	if issued.After(now.Add(time.Minute)) {
		return "", false
	}
	return ip, true
}

// traceUsers 一次取一批账号的邮箱与状态（去重，空列表不查库）。
func (s *Service) traceUsers(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]TraceUserRef, error) {
	out := make(map[uuid.UUID]TraceUserRef)
	if len(ids) == 0 {
		return out, nil
	}
	seen := make(map[uuid.UUID]struct{})
	uniq := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return out, nil
	}
	rows, err := s.repo.UsersByIDs(ctx, uniq)
	if err != nil {
		return nil, err
	}
	for _, u := range rows {
		out[u.ID] = TraceUserRef{ID: u.ID, Email: u.Email, Status: u.Status}
	}
	return out, nil
}
