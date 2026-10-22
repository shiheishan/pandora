package handler

import (
	"strconv"
	"time"

	"github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// bulkFilterRequest 是批量预览的筛选条件（JSON 顶层字段）。
type bulkFilterRequest struct {
	Status            string `json:"status" binding:"omitempty,oneof=active suspended banned"`
	GroupID           string `json:"group_id" binding:"max=64"`
	Query             string `json:"query" binding:"max=200"`
	HasActiveSub      *bool  `json:"has_active_sub"`
	PlanID            string `json:"plan_id" binding:"max=64"`
	ExpiresWithinDays int    `json:"expires_within_days"`
	SubState          string `json:"sub_state"`
}

func (r bulkFilterRequest) toFilter() (service.BulkFilter, error) {
	fields := map[string]string{}
	var groupID, planID *uuid.UUID
	if r.GroupID != "" {
		gid, err := uuid.Parse(r.GroupID)
		if err != nil {
			fields["group_id"] = "分组标识格式不正确"
		} else {
			groupID = &gid
		}
	}
	if r.PlanID != "" {
		pid, err := uuid.Parse(r.PlanID)
		if err != nil {
			fields["plan_id"] = "套餐标识格式不正确"
		} else {
			planID = &pid
		}
	}
	if len(fields) > 0 {
		return service.BulkFilter{}, errs.Invalid(fields)
	}
	return service.BulkFilter{
		Status:            r.Status,
		GroupID:           groupID,
		Query:             r.Query,
		HasActiveSub:      r.HasActiveSub,
		PlanID:            planID,
		ExpiresWithinDays: r.ExpiresWithinDays,
		SubState:          r.SubState,
	}, nil
}

// bulkExportQuery 是批量导出 CSV 的查询参数（form 均为 string）。
type bulkExportQuery struct {
	Status            string `form:"status"`
	GroupID           string `form:"group_id" binding:"max=64"`
	Query             string `form:"query" binding:"max=200"`
	HasActiveSub      string `form:"has_active_sub"`
	PlanID            string `form:"plan_id" binding:"max=64"`
	ExpiresWithinDays string `form:"expires_within_days"`
	SubState          string `form:"sub_state"`
	Limit             string `form:"limit"`
}

func (q bulkExportQuery) toFilter() (service.BulkFilter, int, error) {
	fields := map[string]string{}
	var groupID, planID *uuid.UUID
	if q.GroupID != "" {
		gid, err := uuid.Parse(q.GroupID)
		if err != nil {
			fields["group_id"] = "分组标识格式不正确"
		} else {
			groupID = &gid
		}
	}
	if q.PlanID != "" {
		pid, err := uuid.Parse(q.PlanID)
		if err != nil {
			fields["plan_id"] = "套餐标识格式不正确"
		} else {
			planID = &pid
		}
	}
	if len(fields) > 0 {
		return service.BulkFilter{}, 0, errs.Invalid(fields)
	}

	var hasSub *bool
	switch q.HasActiveSub {
	case "true":
		b := true
		hasSub = &b
	case "false":
		b := false
		hasSub = &b
	}

	expires := 0
	if q.ExpiresWithinDays != "" {
		n, err := strconv.Atoi(q.ExpiresWithinDays)
		if err != nil {
			n = -1
		}
		expires = n
	}

	limit := 0
	if q.Limit != "" {
		n, err := strconv.Atoi(q.Limit)
		if err == nil {
			limit = n
		}
	}

	return service.BulkFilter{
		Status:            q.Status,
		GroupID:           groupID,
		Query:             q.Query,
		HasActiveSub:      hasSub,
		PlanID:            planID,
		ExpiresWithinDays: expires,
		SubState:          q.SubState,
	}, limit, nil
}

// generateRequest 是提交批量生成任务的请求体。
type generateRequest struct {
	Count       int    `json:"count"`
	EmailPrefix string `json:"email_prefix" binding:"max=64"`
	EmailDomain string `json:"email_domain" binding:"max=128"`
	GroupID     string `json:"group_id" binding:"max=64"`
	Reason      string `json:"reason" binding:"max=2000"`
}

func (r generateRequest) toInput() (service.GenerateInput, error) {
	var groupID *uuid.UUID
	if r.GroupID != "" {
		gid, err := uuid.Parse(r.GroupID)
		if err != nil {
			return service.GenerateInput{}, errs.Invalid(map[string]string{"group_id": "分组标识格式不正确"})
		}
		groupID = &gid
	}
	return service.GenerateInput{
		Count:       r.Count,
		EmailPrefix: r.EmailPrefix,
		EmailDomain: r.EmailDomain,
		GroupID:     groupID,
		Reason:      r.Reason,
	}, nil
}

// accessLogQuery 是访问明细列表的查询参数（form 均为 string）。
type accessLogQuery struct {
	Category string `form:"category" binding:"max=200"`
	Outcome  string `form:"outcome" binding:"max=200"`
	IP       string `form:"ip" binding:"max=200"`
	User     string `form:"user" binding:"max=200"`
}

func (q accessLogQuery) toQuery(limit, offset int) service.AccessLogQuery {
	return service.AccessLogQuery{
		Category: q.Category,
		Outcome:  q.Outcome,
		IP:       q.IP,
		User:     q.User,
		Limit:    limit,
		Offset:   offset,
	}
}

// reviewClusterRequest 是标记聚类为正常的请求体（可为空 JSON）。
type reviewClusterRequest struct {
	Note string `json:"note" binding:"max=2000"`
}

// disableClusterRequest 是批量停用聚类内账号的请求体。
type disableClusterRequest struct {
	UserIDs []string `json:"user_ids" binding:"max=500"`
	Reason  string   `json:"reason" binding:"max=2000"`
}

// parseUserIDs 把请求里的用户 id 字符串逐个解析为 UUID。
func parseUserIDs(raw []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, errs.Invalid(map[string]string{"user_ids": "包含无效的账号 ID"})
		}
		out = append(out, id)
	}
	return out, nil
}

// bulkPreviewResponse 是批量预览的响应。
type bulkPreviewResponse struct {
	Total      int64           `json:"total"`
	Samples    []string        `json:"samples"`
	SampleRows []bulkSampleRow `json:"sample_rows"`
}

// bulkSampleRow 是预览样本的一行（含订阅摘要）。
type bulkSampleRow struct {
	Email            string     `json:"email"`
	PlanName         *string    `json:"plan_name"`
	CurrentPeriodEnd *time.Time `json:"current_period_end"`
}

// generationJobResponse 是单个批量生成任务的进度。
type generationJobResponse struct {
	ID              string     `json:"id"`
	ActorID         string     `json:"actor_id"`
	Status          string     `json:"status"`
	Total           int        `json:"total"`
	Completed       int        `json:"completed"`
	Failed          int        `json:"failed"`
	EmailPrefix     string     `json:"email_prefix"`
	EmailDomain     string     `json:"email_domain"`
	GroupID         *string    `json:"group_id"`
	Reason          string     `json:"reason"`
	Error           *string    `json:"error"`
	ResultAvailable bool       `json:"result_available"`
	ResultExpiresAt *time.Time `json:"result_expires_at"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}

// generationJobsResponse 是批量生成任务列表。
type generationJobsResponse struct {
	Jobs []generationJobResponse `json:"jobs"`
}

func previewFrom(p service.BulkPreview) bulkPreviewResponse {
	samples := make([]string, 0, len(p.Samples))
	rows := make([]bulkSampleRow, 0, len(p.Samples))
	for _, sm := range p.Samples {
		samples = append(samples, sm.Email)
		rows = append(rows, bulkSampleRow{
			Email:            sm.Email,
			PlanName:         sm.PlanName,
			CurrentPeriodEnd: sm.PeriodEnd,
		})
	}
	return bulkPreviewResponse{Total: p.Total, Samples: samples, SampleRows: rows}
}

func generationJobFrom(j service.GenerationJob) generationJobResponse {
	var groupID *string
	if j.GroupID != nil {
		s := j.GroupID.String()
		groupID = &s
	}
	return generationJobResponse{
		ID:              j.ID.String(),
		ActorID:         j.ActorID.String(),
		Status:          j.Status,
		Total:           j.Total,
		Completed:       j.Completed,
		Failed:          j.Failed,
		EmailPrefix:     j.EmailPrefix,
		EmailDomain:     j.EmailDomain,
		GroupID:         groupID,
		Reason:          j.Reason,
		Error:           j.Error,
		ResultAvailable: j.ResultAvailable,
		ResultExpiresAt: j.ResultExpiresAt,
		CreatedAt:       j.CreatedAt,
		StartedAt:       j.StartedAt,
		FinishedAt:      j.FinishedAt,
	}
}

func generationJobsFrom(js []service.GenerationJob) generationJobsResponse {
	jobs := make([]generationJobResponse, len(js))
	for i, j := range js {
		jobs[i] = generationJobFrom(j)
	}
	return generationJobsResponse{Jobs: jobs}
}

// userProfileResponse 是后台用户风控画像。
type userProfileResponse struct {
	Events         []profileEventResponse `json:"events"`
	IPs            []profileIPResponse    `json:"ips"`
	Related        []profileUserResponse  `json:"related"`
	Fetches        []profileFetchResponse `json:"fetches"`
	FetchSources7d int                    `json:"fetch_sources_7d"`
	RegisteredIP   string                 `json:"registered_ip"`
	Risk           string                 `json:"risk"`
}

type profileEventResponse struct {
	Action  string    `json:"action"`
	Outcome string    `json:"outcome"`
	IP      string    `json:"ip"`
	UA      string    `json:"ua"`
	Domain  string    `json:"domain"`
	At      time.Time `json:"at"`
}

type profileIPResponse struct {
	IP          string    `json:"ip"`
	Count       int       `json:"count"`
	First       time.Time `json:"first"`
	Last        time.Time `json:"last"`
	Accounts    int       `json:"accounts"`
	Geo         string    `json:"geo"`
	NetworkKind string    `json:"network_kind"`
	Risk        string    `json:"risk"`
}

type profileUserResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type profileFetchResponse struct {
	IP     string    `json:"ip"`
	UA     string    `json:"ua"`
	Family string    `json:"family"`
	Result string    `json:"result"`
	Format string    `json:"format"`
	At     time.Time `json:"at"`
}

// accessLogResponse 是访问明细列表。
type accessLogResponse struct {
	Items []accessLogItem `json:"items"`
}

// accessLogItem 是访问明细的一行。
type accessLogItem struct {
	Category        string    `json:"category"`
	Action          string    `json:"action,omitempty"`
	UserID          string    `json:"user_id,omitempty"`
	UserEmail       string    `json:"user_email,omitempty"`
	TargetUserID    string    `json:"target_user_id,omitempty"`
	TargetUserEmail string    `json:"target_user_email,omitempty"`
	IP              string    `json:"ip,omitempty"`
	Geo             string    `json:"geo,omitempty"`
	NetworkKind     string    `json:"network_kind,omitempty"`
	UserAgent       string    `json:"user_agent,omitempty"`
	Outcome         string    `json:"outcome,omitempty"`
	OccurredAt      time.Time `json:"occurred_at"`
}

// ipClustersResponse 是同源 IP 聚类列表。
type ipClustersResponse struct {
	Clusters []ipClusterResponse `json:"clusters"`
}

// ipClusterResponse 是一个同源 IP 聚类。
type ipClusterResponse struct {
	IP          string                 `json:"ip"`
	Geo         string                 `json:"geo"`
	NetworkKind string                 `json:"network_kind"`
	Risk        string                 `json:"risk"`
	Key         string                 `json:"key"`
	Accounts    int                    `json:"accounts"`
	Events      int                    `json:"events"`
	First       time.Time              `json:"first"`
	Last        time.Time              `json:"last"`
	Emails      []string               `json:"emails"`
	Users       []clusterUserResponse  `json:"users"`
	Review      *clusterReviewResponse `json:"review"`
}

// clusterUserResponse 是聚类里的一个账号。
type clusterUserResponse struct {
	ID         string  `json:"id"`
	Email      string  `json:"email"`
	Status     string  `json:"status"`
	ActivePlan *string `json:"active_plan"`
}

// clusterReviewResponse 是聚类当前的处置结论。
type clusterReviewResponse struct {
	Decision  string     `json:"decision"`
	DecidedAt time.Time  `json:"decided_at"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// reviewClusterResponse 是标记聚类为正常后的响应。
type reviewClusterResponse struct {
	Key       string `json:"key"`
	Decision  string `json:"decision"`
	ExpiresAt string `json:"expires_at"`
}

// disableClusterResponse 是批量停用聚类账号的结果。
type disableClusterResponse struct {
	Disabled int                   `json:"disabled"`
	Skipped  []disableSkipResponse `json:"skipped"`
}

type disableSkipResponse struct {
	UserID string `json:"user_id"`
	Reason string `json:"reason"`
}

func profileFrom(p service.UserProfile) userProfileResponse {
	events := make([]profileEventResponse, 0, len(p.Events))
	for _, e := range p.Events {
		events = append(events, profileEventResponse{
			Action: e.Action, Outcome: e.Outcome, IP: e.IP, UA: e.UserAgent,
			Domain: e.Domain, At: e.At,
		})
	}
	ips := make([]profileIPResponse, 0, len(p.IPs))
	for _, ip := range p.IPs {
		ips = append(ips, profileIPResponse{
			IP:          ip.IP,
			Count:       ip.Count,
			First:       ip.First,
			Last:        ip.Last,
			Accounts:    ip.Accounts,
			Geo:         ip.Geo,
			NetworkKind: ip.NetworkKind,
			Risk:        ip.Risk,
		})
	}
	related := make([]profileUserResponse, 0, len(p.Related))
	for _, u := range p.Related {
		related = append(related, profileUserResponse{ID: u.ID.String(), Email: u.Email})
	}
	fetches := make([]profileFetchResponse, 0, len(p.Fetches))
	for _, f := range p.Fetches {
		fetches = append(fetches, profileFetchResponse{
			IP: f.IP, UA: f.UserAgent, Family: f.Family, Result: f.Result, Format: f.Format, At: f.At,
		})
	}
	return userProfileResponse{
		Events: events, IPs: ips, Related: related, Fetches: fetches,
		FetchSources7d: p.FetchSources7d, RegisteredIP: p.RegisteredIP, Risk: p.Risk,
	}
}

func accessLogFrom(items []service.AccessItem) accessLogResponse {
	out := make([]accessLogItem, 0, len(items))
	for _, it := range items {
		out = append(out, accessItemFrom(it))
	}
	return accessLogResponse{Items: out}
}

func accessItemFrom(it service.AccessItem) accessLogItem {
	row := accessLogItem{
		Category:    it.Category,
		Action:      it.Action,
		UserEmail:   it.UserEmail,
		IP:          it.IP,
		Geo:         it.Geo,
		NetworkKind: it.NetworkKind,
		UserAgent:   it.UserAgent,
		Outcome:     it.Outcome,
		OccurredAt:  it.At,
	}
	if it.UserID != nil {
		row.UserID = it.UserID.String()
	}
	if it.TargetUserID != nil {
		row.TargetUserID = it.TargetUserID.String()
		row.TargetUserEmail = it.TargetUserEmail
	}
	return row
}

func clustersFrom(cs []service.IPCluster) ipClustersResponse {
	clusters := make([]ipClusterResponse, 0, len(cs))
	for _, c := range cs {
		clusters = append(clusters, clusterFrom(c))
	}
	return ipClustersResponse{Clusters: clusters}
}

func clusterFrom(c service.IPCluster) ipClusterResponse {
	users := make([]clusterUserResponse, 0, len(c.Users))
	for _, u := range c.Users {
		users = append(users, clusterUserResponse{
			ID: u.ID.String(), Email: u.Email, Status: u.Status, ActivePlan: u.ActivePlan,
		})
	}
	emails := c.Emails
	if emails == nil {
		emails = []string{}
	}
	var review *clusterReviewResponse
	if c.Review != nil {
		review = &clusterReviewResponse{
			Decision: c.Review.Decision, DecidedAt: c.Review.DecidedAt, ExpiresAt: c.Review.ExpiresAt,
		}
	}
	return ipClusterResponse{
		IP: c.IP, Geo: c.Geo, NetworkKind: c.NetworkKind, Risk: c.Risk, Key: c.Key,
		Accounts: c.Accounts, Events: c.Events, First: c.First, Last: c.Last,
		Emails: emails, Users: users, Review: review,
	}
}

func reviewFrom(key string, expires time.Time) reviewClusterResponse {
	return reviewClusterResponse{
		Key: key, Decision: "normal", ExpiresAt: expires.UTC().Format(time.RFC3339),
	}
}

func disableFrom(r service.DisableClusterResult) disableClusterResponse {
	skipped := make([]disableSkipResponse, 0, len(r.Skipped))
	for _, s := range r.Skipped {
		skipped = append(skipped, disableSkipResponse{UserID: s.UserID.String(), Reason: s.Reason})
	}
	return disableClusterResponse{Disabled: r.Disabled, Skipped: skipped}
}
