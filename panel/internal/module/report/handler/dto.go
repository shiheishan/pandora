package handler

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/aegispanel/aegis/internal/module/report/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

const trafficTimeLayout = "2006-01-02T15:04:05.000000Z"

// --- 请求与查询 ---

type revenueTimeseriesQuery struct {
	Days int `form:"days"`
}

type daysQuery struct {
	Days int `form:"days"`
}

type trafficQuery struct {
	Range      string `form:"range"`
	Limit      int    `form:"limit"`
	SnapshotAt string `form:"snapshot_at"`
}

type createAdjustmentRequest struct {
	Amount      int64  `json:"amount"`
	Reason      string `json:"reason" binding:"required"`
	EffectiveOn string `json:"effective_on"`
}

type reverseAdjustmentRequest struct {
	Reason string `json:"reason" binding:"required"`
}

// --- 响应 ---

type overviewResponse struct {
	Users               userCountsResponse         `json:"users"`
	Subscriptions       subscriptionCountsResponse `json:"subscriptions"`
	Revenue             revenueRowResponse         `json:"revenue"`
	Orders              orderCountsResponse        `json:"orders"`
	LedgerDriftAccounts int64                      `json:"ledger_drift_accounts"`
	Nodes               nodeCountsResponse         `json:"nodes"`
}

type userCountsResponse struct {
	Total     int64 `json:"total"`
	Active    int64 `json:"active"`
	Today     int64 `json:"today"`
	Last7Days int64 `json:"last_7_days"`
}

type subscriptionCountsResponse struct {
	Active        int64 `json:"active"`
	Expiring7Days int64 `json:"expiring_7_days"`
	Expired       int64 `json:"expired"`
	New7Days      int64 `json:"new_7_days"`
}

type orderCountsResponse struct {
	PaidToday   int64 `json:"paid_today"`
	Pending     int64 `json:"pending"`
	FailedToday int64 `json:"failed_today"`
}

type nodeCountsResponse struct {
	Total  int64 `json:"total"`
	Online int64 `json:"online"`
}

type revenueRowResponse struct {
	Today            int64 `json:"today"`
	Last7Days        int64 `json:"last_7_days"`
	Last30Days       int64 `json:"last_30_days"`
	Total            int64 `json:"total"`
	ActualToday      int64 `json:"actual_today"`
	Actual7Days      int64 `json:"actual_7_days"`
	Actual30Days     int64 `json:"actual_30_days"`
	ActualTotal      int64 `json:"actual_total"`
	AdjustmentToday  int64 `json:"adjustment_today"`
	Adjustment7Days  int64 `json:"adjustment_7_days"`
	Adjustment30Days int64 `json:"adjustment_30_days"`
	AdjustmentTotal  int64 `json:"adjustment_total"`
	Yesterday        int64 `json:"yesterday"`
	ActualYesterday  int64 `json:"actual_yesterday"`
}

type revenueSeriesResponse struct {
	Days          int                    `json:"days"`
	Points        []revenuePointResponse `json:"points"`
	PreviousTotal int64                  `json:"previous_total"`
}

type revenuePointResponse struct {
	Date         string `json:"date"`
	ActualCredit int64  `json:"actual_credit"`
	ActualDebit  int64  `json:"actual_debit"`
	Adjustment   int64  `json:"adjustment"`
	DisplayedNet int64  `json:"displayed_net"`
}

type adjustmentResponse struct {
	ID             string    `json:"id"`
	Amount         int64     `json:"amount"`
	Reason         string    `json:"reason"`
	EffectiveOn    string    `json:"effective_on"`
	ReversalOf     *string   `json:"reversal_of,omitempty"`
	CreatedBy      string    `json:"created_by"`
	CreatedByEmail *string   `json:"created_by_email"`
	CreatedAt      time.Time `json:"created_at"`
	Reversed       bool      `json:"reversed"`
}

type adjustmentsListResponse struct {
	Adjustments []adjustmentResponse `json:"adjustments"`
}

type backlogResponse struct {
	AsOf                   time.Time                 `json:"as_of"`
	BacklogState           string                    `json:"backlog_state"`
	ProcessorState         string                    `json:"processor_state"`
	ScannerIntervalSeconds int64                     `json:"scanner_interval_seconds"`
	Counts                 backlogCountsResponse     `json:"counts"`
	OldestReadyAt          *time.Time                `json:"oldest_ready_at"`
	MaxReadyLagSeconds     int64                     `json:"max_ready_lag_seconds"`
	LastSentAt             *time.Time                `json:"last_sent_at"`
	Assessment             backlogAssessmentResponse `json:"assessment"`
}

type backlogCountsResponse struct {
	Ready               int64 `json:"ready"`
	ReadyRetry          int64 `json:"ready_retry"`
	Scheduled           int64 `json:"scheduled"`
	ScheduledRetry      int64 `json:"scheduled_retry"`
	SendingUnobservable int64 `json:"sending_unobservable"`
	FailedTotal         int64 `json:"failed_total"`
	SuppressedTotal     int64 `json:"suppressed_total"`
	BouncedTotal        int64 `json:"bounced_total"`
}

type backlogAssessmentResponse struct {
	ThresholdSeconds int64  `json:"threshold_seconds"`
	Reason           string `json:"reason"`
}

type tasksResponse struct {
	AsOf  time.Time `json:"as_of"`
	Items []any     `json:"items"`
}

type taskTicketsOpen struct {
	Kind              string `json:"kind"`
	Count             int64  `json:"count"`
	HighPriority      int64  `json:"high_priority"`
	OldestWaitSeconds *int64 `json:"oldest_wait_seconds"`
}

type taskWithdrawalsPending struct {
	Kind   string `json:"kind"`
	Count  int64  `json:"count"`
	Amount int64  `json:"amount"`
}

type taskNodesOffline struct {
	Kind                  string           `json:"kind"`
	Count                 int64            `json:"count"`
	Sample                []taskNodeSample `json:"sample"`
	LongestOfflineSeconds *int64           `json:"longest_offline_seconds"`
}

type taskNodeSample struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type taskOrdersPendingStale struct {
	Kind             string `json:"kind"`
	Count            int64  `json:"count"`
	ThresholdSeconds int64  `json:"threshold_seconds"`
}

type taskNotificationsBacklog struct {
	Kind         string `json:"kind"`
	Queued       int64  `json:"queued"`
	FailedTotal  int64  `json:"failed_total"`
	BacklogState string `json:"backlog_state"`
}

type taskLedgerDrift struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

type systemStatusResponse struct {
	Backup     json.RawMessage     `json:"backup"`
	Database   json.RawMessage     `json:"database"`
	State      string              `json:"state"`
	Components []componentResponse `json:"components"`
}

type componentResponse struct {
	Key       string         `json:"key"`
	State     string         `json:"state"`
	LatencyMS *float64       `json:"latency_ms,omitempty"`
	Metrics   map[string]any `json:"metrics"`
	Message   string         `json:"message,omitempty"`
}

type activityTimeseriesResponse struct {
	Points []activityPointResponse `json:"points"`
}

type activityPointResponse struct {
	Day         string `json:"day"`
	Registered  int64  `json:"registered"`
	Logins      int64  `json:"logins"`
	Orders      int64  `json:"orders"`
	UniqueIPs   int64  `json:"unique_ips"`
	ActiveUsers int64  `json:"active_users"`
}

type nodeTrafficResponse struct {
	Range      string                    `json:"range"`
	SnapshotAt string                    `json:"snapshot_at"`
	From       string                    `json:"from"`
	To         string                    `json:"to"`
	Basis      string                    `json:"basis"`
	Totals     trafficTotalsResponse     `json:"totals"`
	Quality    trafficQualityResponse    `json:"quality"`
	Items      []nodeTrafficItemResponse `json:"items"`
	Ranking    nodeRankingResponse       `json:"ranking"`
}

type userTrafficResponse struct {
	Range      string                    `json:"range"`
	SnapshotAt string                    `json:"snapshot_at"`
	From       string                    `json:"from"`
	To         string                    `json:"to"`
	Basis      string                    `json:"basis"`
	Totals     trafficTotalsResponse     `json:"totals"`
	Quality    trafficQualityResponse    `json:"quality"`
	Items      []userTrafficItemResponse `json:"items"`
	Ranking    userRankingResponse       `json:"ranking"`
}

type trafficTotalsResponse struct {
	ReportedBytes     string `json:"reported_bytes"`
	AttributedBytes   string `json:"attributed_bytes"`
	UnattributedBytes string `json:"unattributed_bytes"`
}

type trafficQualityResponse struct {
	InvalidReportCount int64 `json:"invalid_report_count"`
	InvalidEntryCount  int64 `json:"invalid_entry_count"`
}

type nodeTrafficItemResponse struct {
	NodeID        string  `json:"node_id"`
	Name          string  `json:"name"`
	DisplayName   *string `json:"display_name"`
	UploadBytes   string  `json:"upload_bytes"`
	DownloadBytes string  `json:"download_bytes"`
	TotalBytes    string  `json:"total_bytes"`
	LastReportAt  string  `json:"last_report_at"`
}

type userTrafficItemResponse struct {
	UserID            string `json:"user_id"`
	EmailMasked       string `json:"email_masked"`
	SubscriptionCount int64  `json:"subscription_count"`
	UploadBytes       string `json:"upload_bytes"`
	DownloadBytes     string `json:"download_bytes"`
	TotalBytes        string `json:"total_bytes"`
	LastReportAt      string `json:"last_report_at"`
}

type nodeRankingResponse struct {
	ReturnedBytes  string `json:"returned_bytes"`
	OtherNodeBytes string `json:"other_node_bytes"`
}

type userRankingResponse struct {
	ReturnedBytes  string `json:"returned_bytes"`
	OtherUserBytes string `json:"other_user_bytes"`
}

func decBytes(v int64) string { return strconv.FormatInt(v, 10) }

func trafficTime(t time.Time) string {
	return t.UTC().Format(trafficTimeLayout)
}

func cents(d decimal.Decimal) (int64, error) {
	return money.ToCents(d)
}

func overviewFrom(o service.Overview) (overviewResponse, error) {
	rev, err := revenueRowFrom(o.Revenue)
	if err != nil {
		return overviewResponse{}, err
	}
	return overviewResponse{
		Users: userCountsResponse{
			Total: o.Users.Total, Active: o.Users.Active, Today: o.Users.Today, Last7Days: o.Users.Last7Days,
		},
		Subscriptions: subscriptionCountsResponse{
			Active: o.Subscriptions.Active, Expiring7Days: o.Subscriptions.Expiring7Days, Expired: o.Subscriptions.Expired, New7Days: o.Subscriptions.New7Days,
		},
		Revenue: rev,
		Orders: orderCountsResponse{
			PaidToday: o.Orders.PaidToday, Pending: o.Orders.Pending, FailedToday: o.Orders.FailedToday,
		},
		LedgerDriftAccounts: o.LedgerDriftAccounts,
		Nodes:               nodeCountsResponse{Total: o.Nodes.Total, Online: o.Nodes.Online},
	}, nil
}

func revenueRowFrom(r service.RevenueRow) (revenueRowResponse, error) {
	vals := []decimal.Decimal{
		r.Today, r.Last7Days, r.Last30Days, r.Total,
		r.ActualToday, r.Actual7Days, r.Actual30Days, r.ActualTotal,
		r.AdjustmentToday, r.Adjustment7Days, r.Adjustment30Days, r.AdjustmentTotal,
		r.Yesterday, r.ActualYesterday,
	}
	out := make([]int64, len(vals))
	for i, v := range vals {
		n, err := cents(v)
		if err != nil {
			return revenueRowResponse{}, err
		}
		out[i] = n
	}
	return revenueRowResponse{
		Today: out[0], Last7Days: out[1], Last30Days: out[2], Total: out[3],
		ActualToday: out[4], Actual7Days: out[5], Actual30Days: out[6], ActualTotal: out[7],
		AdjustmentToday: out[8], Adjustment7Days: out[9], Adjustment30Days: out[10], AdjustmentTotal: out[11],
		Yesterday: out[12], ActualYesterday: out[13],
	}, nil
}

func revenueSeriesFrom(s service.RevenueSeries) (revenueSeriesResponse, error) {
	pts := make([]revenuePointResponse, 0, len(s.Points))
	for _, p := range s.Points {
		ac, err := cents(p.ActualCredit)
		if err != nil {
			return revenueSeriesResponse{}, err
		}
		ad, err := cents(p.ActualDebit)
		if err != nil {
			return revenueSeriesResponse{}, err
		}
		adj, err := cents(p.Adjustment)
		if err != nil {
			return revenueSeriesResponse{}, err
		}
		dn, err := cents(p.DisplayedNet)
		if err != nil {
			return revenueSeriesResponse{}, err
		}
		pts = append(pts, revenuePointResponse{
			Date: p.Date, ActualCredit: ac, ActualDebit: ad, Adjustment: adj, DisplayedNet: dn,
		})
	}
	prev, err := cents(s.PreviousTotal)
	if err != nil {
		return revenueSeriesResponse{}, err
	}
	return revenueSeriesResponse{
		Days: s.Days, Points: pts, PreviousTotal: prev,
	}, nil
}

func adjustmentFrom(a service.Adjustment) (adjustmentResponse, error) {
	amt, err := cents(a.Amount)
	if err != nil {
		return adjustmentResponse{}, err
	}
	var rev *string
	if a.ReversalOf != nil {
		s := a.ReversalOf.String()
		rev = &s
	}
	return adjustmentResponse{
		ID: a.ID.String(), Amount: amt, Reason: a.Reason,
		EffectiveOn: a.EffectiveOn, ReversalOf: rev, CreatedBy: a.CreatedBy.String(),
		CreatedByEmail: a.CreatedByEmail, CreatedAt: a.CreatedAt, Reversed: a.Reversed,
	}, nil
}

func adjustmentsListFrom(rows []service.Adjustment) (adjustmentsListResponse, error) {
	out := make([]adjustmentResponse, 0, len(rows))
	for _, r := range rows {
		row, err := adjustmentFrom(r)
		if err != nil {
			return adjustmentsListResponse{}, err
		}
		out = append(out, row)
	}
	if out == nil {
		out = []adjustmentResponse{}
	}
	return adjustmentsListResponse{Adjustments: out}, nil
}

func backlogFrom(b service.NotificationBacklog) backlogResponse {
	return backlogResponse{
		AsOf: b.AsOf, BacklogState: b.State, ProcessorState: "unobservable",
		ScannerIntervalSeconds: int64(service.ScannerInterval / time.Second),
		Counts: backlogCountsResponse{
			Ready: b.Ready, ReadyRetry: b.ReadyRetry, Scheduled: b.Scheduled, ScheduledRetry: b.ScheduledRetry,
			SendingUnobservable: 0, FailedTotal: b.Failed, SuppressedTotal: b.Suppressed, BouncedTotal: 0,
		},
		OldestReadyAt: b.OldestReadyAt, MaxReadyLagSeconds: b.MaxReadyLag, LastSentAt: b.LastSentAt,
		Assessment: backlogAssessmentResponse{
			ThresholdSeconds: int64(service.BacklogThreshold / time.Second),
			Reason:           b.Reason,
		},
	}
}

func tasksFrom(t service.Tasks) (tasksResponse, error) {
	withdrawAmount, err := cents(t.WithdrawalsPending.Amount)
	if err != nil {
		return tasksResponse{}, err
	}
	sample := make([]taskNodeSample, len(t.NodesOffline.Sample))
	for i, n := range t.NodesOffline.Sample {
		sample[i] = taskNodeSample{ID: n.ID.String(), Name: n.Name}
	}
	items := []any{
		taskTicketsOpen{
			Kind: "tickets_open", Count: t.TicketsOpen.Count, HighPriority: t.TicketsOpen.HighPriority,
			OldestWaitSeconds: t.TicketsOpen.OldestWaitSeconds,
		},
		taskWithdrawalsPending{Kind: "withdrawals_pending", Count: t.WithdrawalsPending.Count, Amount: withdrawAmount},
		taskNodesOffline{
			Kind: "nodes_offline", Count: t.NodesOffline.Count, Sample: sample,
			LongestOfflineSeconds: t.NodesOffline.LongestOfflineSeconds,
		},
		taskOrdersPendingStale{
			Kind: "orders_pending_stale", Count: t.OrdersPendingStale.Count,
			ThresholdSeconds: t.OrdersPendingStale.ThresholdSeconds,
		},
		taskNotificationsBacklog{
			Kind: "notifications_backlog", Queued: t.NotificationsBacklog.Queued,
			FailedTotal: t.NotificationsBacklog.FailedTotal, BacklogState: t.NotificationsBacklog.BacklogState,
		},
		taskLedgerDrift{Kind: "ledger_drift", Count: t.LedgerDrift},
	}
	return tasksResponse{AsOf: t.AsOf, Items: items}, nil
}

func nodeTrafficFrom(t service.NodeTraffic) nodeTrafficResponse {
	items := make([]nodeTrafficItemResponse, 0, len(t.Items))
	for _, it := range t.Items {
		items = append(items, nodeTrafficItemResponse{
			NodeID: it.NodeID.String(), Name: it.Name, DisplayName: it.DisplayName,
			UploadBytes: decBytes(it.Upload), DownloadBytes: decBytes(it.Download),
			TotalBytes: decBytes(it.Total), LastReportAt: trafficTime(it.LastAt),
		})
	}
	return nodeTrafficResponse{
		Range: t.Window.Range, SnapshotAt: trafficTime(t.Window.SnapshotAt),
		From: trafficTime(t.Window.From), To: trafficTime(t.Window.To),
		Basis: "strict_raw_report_entries",
		Totals: trafficTotalsResponse{
			ReportedBytes: decBytes(t.Totals.Reported), AttributedBytes: decBytes(t.Totals.Attributed),
			UnattributedBytes: decBytes(t.Totals.Unattributed),
		},
		Quality: trafficQualityResponse{
			InvalidReportCount: t.Quality.InvalidReports, InvalidEntryCount: t.Quality.InvalidEntries,
		},
		Items:   items,
		Ranking: nodeRankingResponse{ReturnedBytes: decBytes(t.Returned), OtherNodeBytes: decBytes(t.Other)},
	}
}

func userTrafficFrom(t service.UserTraffic) userTrafficResponse {
	items := make([]userTrafficItemResponse, 0, len(t.Items))
	for _, it := range t.Items {
		items = append(items, userTrafficItemResponse{
			UserID: it.UserID.String(), EmailMasked: it.EmailMasked, SubscriptionCount: it.SubscriptionCount,
			UploadBytes: decBytes(it.Upload), DownloadBytes: decBytes(it.Download), TotalBytes: decBytes(it.Total),
			LastReportAt: trafficTime(it.LastAt),
		})
	}
	return userTrafficResponse{
		Range: t.Window.Range, SnapshotAt: trafficTime(t.Window.SnapshotAt),
		From: trafficTime(t.Window.From), To: trafficTime(t.Window.To),
		Basis: "strict_raw_report_entries",
		Totals: trafficTotalsResponse{
			ReportedBytes: decBytes(t.Totals.Reported), AttributedBytes: decBytes(t.Totals.Attributed),
			UnattributedBytes: decBytes(t.Totals.Unattributed),
		},
		Quality: trafficQualityResponse{
			InvalidReportCount: t.Quality.InvalidReports, InvalidEntryCount: t.Quality.InvalidEntries,
		},
		Items:   items,
		Ranking: userRankingResponse{ReturnedBytes: decBytes(t.Returned), OtherUserBytes: decBytes(t.Other)},
	}
}

func activityTimeseriesFrom(pts []service.ActivityPoint) activityTimeseriesResponse {
	out := make([]activityPointResponse, 0, len(pts))
	for _, p := range pts {
		out = append(out, activityPointResponse{
			Day: p.Day, Registered: p.Registered, Logins: p.Logins, Orders: p.Orders,
			UniqueIPs: p.UniqueIPs, ActiveUsers: p.ActiveUsers,
		})
	}
	return activityTimeseriesResponse{Points: out}
}

// backup 段按可读性条件放键，用 RawMessage 拼出前端要的 JSON 形状。
func marshalBackup(b service.BackupStatus) json.RawMessage {
	if !b.Readable {
		m := map[string]any{"dir": b.Dir, "readable": false, "message": b.Message}
		raw, _ := json.Marshal(m)
		return raw
	}
	m := map[string]any{
		"dir": b.Dir, "readable": true,
		"count": b.Count, "total_bytes": b.TotalBytes, "stale": b.Stale,
		"offsite_configured": b.OffsiteConfigured,
	}
	if b.Latest != nil {
		m["latest"] = map[string]any{
			"name": b.Latest.Name, "size": b.Latest.Size, "created_at": b.Latest.ModTime,
			"has_checksum": b.Latest.HasChecksum, "has_seal": b.Latest.HasSeal,
		}
		if b.LatestAgeHours != nil {
			m["latest_age_hours"] = *b.LatestAgeHours
		}
	}
	if b.MissingChecksum != nil {
		m["missing_checksum"] = *b.MissingChecksum
	}
	if b.MissingSeal != nil {
		m["missing_seal"] = *b.MissingSeal
	}
	if len(b.Recent) > 0 {
		rec := make([]map[string]any, 0, len(b.Recent))
		for _, f := range b.Recent {
			rec = append(rec, map[string]any{
				"name": f.Name, "size": f.Size, "created_at": f.ModTime,
				"has_checksum": f.HasChecksum, "has_seal": f.HasSeal,
			})
		}
		m["recent"] = rec
	}
	raw, _ := json.Marshal(m)
	return raw
}

func marshalDatabase(db *service.DatabaseStats, errMsg string) json.RawMessage {
	if db == nil {
		raw, _ := json.Marshal(map[string]string{"error": errMsg})
		return raw
	}
	raw, _ := json.Marshal(map[string]int64{
		"size_bytes": db.SizeBytes, "connections": db.Connections, "max_connections": db.MaxConnections,
	})
	return raw
}

func systemStatusFrom(s service.SystemStatus) systemStatusResponse {
	comps := make([]componentResponse, 0, len(s.Components))
	for _, c := range s.Components {
		metrics := c.Metrics
		if metrics == nil {
			metrics = map[string]any{}
		}
		comps = append(comps, componentResponse{
			Key: c.Key, State: c.State, LatencyMS: c.LatencyMS, Metrics: metrics, Message: c.Message,
		})
	}
	errMsg := s.DatabaseError
	if errMsg == "" {
		errMsg = "读取数据库状态失败"
	}
	return systemStatusResponse{
		Backup:     marshalBackup(s.Backup),
		Database:   marshalDatabase(s.Database, errMsg),
		State:      s.State,
		Components: comps,
	}
}
