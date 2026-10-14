package handler

import (
	"encoding/json"
	"time"

	"github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
)

// --- 请求 DTO ---

type createPlanRequest struct {
	Code                 string     `json:"code"`
	Name                 string     `json:"name"`
	Description          *string    `json:"description"`
	Visibility           string     `json:"visibility"`
	VisibleGroupIDs      []string   `json:"visible_group_ids"`
	VisibleFrom          *time.Time `json:"visible_from"`
	VisibleUntil         *time.Time `json:"visible_until"`
	AllowNewPurchase     *bool      `json:"allow_new_purchase"`
	AllowRenewal         *bool      `json:"allow_renewal"`
	AllowUpgrade         *bool      `json:"allow_upgrade"`
	PurchaseLimitPerUser *int       `json:"purchase_limit_per_user"`
	StockTotal           *int       `json:"stock_total"`
	SortOrder            int        `json:"sort_order"`
	Highlights           []string   `json:"highlights"`
	Recommended          bool       `json:"recommended"`
}

type updatePlanRequest struct {
	ExpectedRowVersion   int64      `json:"expected_row_version"`
	Code                 string     `json:"code"`
	Name                 string     `json:"name"`
	Description          *string    `json:"description"`
	Visibility           string     `json:"visibility"`
	VisibleGroupIDs      []string   `json:"visible_group_ids"`
	VisibleFrom          *time.Time `json:"visible_from"`
	VisibleUntil         *time.Time `json:"visible_until"`
	AllowNewPurchase     bool       `json:"allow_new_purchase"`
	AllowRenewal         bool       `json:"allow_renewal"`
	AllowUpgrade         bool       `json:"allow_upgrade"`
	PurchaseLimitPerUser *int       `json:"purchase_limit_per_user"`
	StockTotal           *int       `json:"stock_total"`
	SortOrder            int        `json:"sort_order"`
	Highlights           []string   `json:"highlights"`
	Recommended          bool       `json:"recommended"`
}

type entitlementRequest struct {
	Code  string          `json:"code"`
	Value json.RawMessage `json:"value"`
}

type quotaRequest struct {
	Metric string `json:"metric"`
	Limit  *int64 `json:"limit"`
	Unit   string `json:"unit"`
	Period string `json:"period"`
}

type updateVersionRequest struct {
	ExpectedRowVersion   int64                `json:"expected_row_version"`
	QuotaResetStrategy   string               `json:"quota_reset_strategy"`
	QuotaResetDay        *int                 `json:"quota_reset_day"`
	GracePeriodHours     int                  `json:"grace_period_hours"`
	GraceKeepsService    bool                 `json:"grace_keeps_service"`
	RenewalExtendsPeriod bool                 `json:"renewal_extends_period"`
	RenewalResetsQuota   bool                 `json:"renewal_resets_quota"`
	RenewalKeepsAddons   bool                 `json:"renewal_keeps_addons"`
	MaxDevices           *int                 `json:"max_devices"`
	MaxConcurrent        *int                 `json:"max_concurrent"`
	DeviceReleaseHours   int                  `json:"device_release_hours"`
	OveragePolicy        string               `json:"overage_policy"`
	ThrottleKbps         *int                 `json:"throttle_kbps"`
	Notes                *string              `json:"notes"`
	Entitlements         []entitlementRequest `json:"entitlements"`
	Quotas               []quotaRequest       `json:"quotas"`
	PoolIDs              []string             `json:"pool_ids"`
}

type createPriceRequest struct {
	Currency        string     `json:"currency"`
	UnitAmount      int64      `json:"unit_amount"`
	BillingInterval string     `json:"billing_interval"`
	IntervalCount   int16      `json:"interval_count"`
	TrialDays       int16      `json:"trial_days"`
	UserGroupID     *string    `json:"user_group_id"`
	ValidFrom       *time.Time `json:"valid_from"`
	ValidUntil      *time.Time `json:"valid_until"`
}

type expectedRowVersionRequest struct {
	ExpectedRowVersion int64 `json:"expected_row_version"`
}

type publishVersionRequest struct {
	ExpectedPlanRowVersion    int64 `json:"expected_plan_row_version"`
	ExpectedVersionRowVersion int64 `json:"expected_version_row_version"`
}

type wizardPriceRequest struct {
	BillingInterval string `json:"billing_interval"`
	IntervalCount   int16  `json:"interval_count"`
	UnitAmount      int64  `json:"unit_amount"`
	Currency        string `json:"currency"`
	TrialDays       int16  `json:"trial_days"`
}

type createPlanCompleteRequest struct {
	Code                 string   `json:"code"`
	Name                 string   `json:"name"`
	Description          *string  `json:"description"`
	Visibility           string   `json:"visibility"`
	SortOrder            int      `json:"sort_order"`
	AllowNewPurchase     *bool    `json:"allow_new_purchase"`
	AllowRenewal         *bool    `json:"allow_renewal"`
	AllowUpgrade         *bool    `json:"allow_upgrade"`
	VisibleGroupIDs      []string `json:"visible_group_ids"`
	PurchaseLimitPerUser *int     `json:"purchase_limit_per_user"`
	StockTotal           *int     `json:"stock_total"`
	Highlights           []string `json:"highlights"`
	Recommended          bool     `json:"recommended"`
	TrafficGB            *int64   `json:"traffic_gb"`
	MaxDevices           *int     `json:"max_devices"`
	ThrottleKbps         *int     `json:"throttle_kbps"`
	QuotaResetStrategy   string   `json:"quota_reset_strategy"`
	QuotaResetDay        *int16   `json:"quota_reset_day"`
	// PoolIDs 是新建草稿版本要绑定的节点池 id 列表。
	PoolIDs []string             `json:"pool_ids"`
	Prices  []wizardPriceRequest `json:"prices"`
	Publish bool                 `json:"publish"`
}

type updatePlanCompleteRequest struct {
	ExpectedRowVersion   int64                 `json:"expected_row_version"`
	Code                 string                `json:"code"`
	Name                 string                `json:"name"`
	Description          *string               `json:"description"`
	Visibility           string                `json:"visibility"`
	SortOrder            int                   `json:"sort_order"`
	AllowNewPurchase     *bool                 `json:"allow_new_purchase"`
	AllowRenewal         *bool                 `json:"allow_renewal"`
	AllowUpgrade         *bool                 `json:"allow_upgrade"`
	VisibleGroupIDs      []string              `json:"visible_group_ids"`
	PurchaseLimitPerUser *int                  `json:"purchase_limit_per_user"`
	StockTotal           *int                  `json:"stock_total"`
	Highlights           *[]string             `json:"highlights"`
	Recommended          *bool                 `json:"recommended"`
	TrafficGB            *int64                `json:"traffic_gb"`
	MaxDevices           optionalInt           `json:"max_devices"`
	ThrottleKbps         optionalInt           `json:"throttle_kbps"`
	Prices               *[]wizardPriceRequest `json:"prices"`
	// PoolIDs：null 表示线路不动；数组表示换成这些池（有变化时会滚出新版本并发布）。
	PoolIDs *[]string `json:"pool_ids"`
}

// optionalInt 区分字段缺省与显式 null。
type optionalInt struct {
	Set   bool
	Value *int
}

func (o *optionalInt) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	var v int
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

func optionalIntToService(o optionalInt) service.OptionalInt {
	return service.OptionalInt{Set: o.Set, Value: o.Value}
}

// --- 响应 DTO ---

type planRowResponse struct {
	ID               string             `json:"id"`
	RowVersion       int64              `json:"row_version"`
	Code             string             `json:"code"`
	Name             string             `json:"name"`
	Description      *string            `json:"description"`
	Status           string             `json:"status"`
	Visibility       string             `json:"visibility"`
	SortOrder        int                `json:"sort_order"`
	CurrentVersionID *string            `json:"current_version_id"`
	DraftVersionID   *string            `json:"draft_version_id"`
	Version          *int               `json:"version"`
	MaxDevices       *int               `json:"max_devices"`
	TrafficLimit     *int64             `json:"traffic_limit"`
	Highlights       []string           `json:"highlights"`
	Recommended      bool               `json:"recommended"`
	Prices           []priceRowResponse `json:"prices"`
	ActiveSubs       int                `json:"active_subscriptions"`
	NodeCount        int                `json:"node_count"`
}

type priceRowResponse struct {
	ID          string     `json:"id"`
	Currency    string     `json:"currency"`
	UnitAmount  int64      `json:"unit_amount"`
	Interval    string     `json:"billing_interval"`
	Count       int        `json:"interval_count"`
	TrialDays   int        `json:"trial_days"`
	Status      string     `json:"status"`
	UserGroupID *string    `json:"user_group_id"`
	ValidFrom   *time.Time `json:"valid_from"`
	ValidUntil  *time.Time `json:"valid_until"`
	RowVersion  int64      `json:"row_version"`
}

type versionRowResponse struct {
	ID                   string               `json:"id"`
	Version              int                  `json:"version"`
	Status               string               `json:"status"`
	FrozenAt             *time.Time           `json:"frozen_at"`
	RowVersion           int64                `json:"row_version"`
	QuotaResetStrategy   string               `json:"quota_reset_strategy"`
	QuotaResetDay        *int                 `json:"quota_reset_day"`
	GracePeriodHours     int                  `json:"grace_period_hours"`
	GraceKeepsService    bool                 `json:"grace_keeps_service"`
	RenewalExtendsPeriod bool                 `json:"renewal_extends_period"`
	RenewalResetsQuota   bool                 `json:"renewal_resets_quota"`
	RenewalKeepsAddons   bool                 `json:"renewal_keeps_addons"`
	MaxDevices           *int                 `json:"max_devices"`
	MaxConcurrent        *int                 `json:"max_concurrent"`
	DeviceReleaseHours   int                  `json:"device_release_hours"`
	OveragePolicy        string               `json:"overage_policy"`
	ThrottleKbps         *int                 `json:"throttle_kbps"`
	Notes                *string              `json:"notes"`
	Entitlements         []entitlementRequest `json:"entitlements"`
	Quotas               []quotaRequest       `json:"quotas"`
	PoolIDs              []string             `json:"pool_ids"`
	CreatedByEmail       *string              `json:"created_by_email"`
	CreatedAt            time.Time            `json:"created_at"`
}

type catalogPlanDetailResponse struct {
	ID                   string               `json:"id"`
	CurrentVersionID     *string              `json:"current_version_id"`
	RowVersion           int64                `json:"row_version"`
	Code                 string               `json:"code"`
	Name                 string               `json:"name"`
	Description          *string              `json:"description"`
	Status               string               `json:"status"`
	Visibility           string               `json:"visibility"`
	VisibleGroupIDs      []string             `json:"visible_group_ids"`
	VisibleFrom          *time.Time           `json:"visible_from"`
	VisibleUntil         *time.Time           `json:"visible_until"`
	AllowNewPurchase     bool                 `json:"allow_new_purchase"`
	AllowRenewal         bool                 `json:"allow_renewal"`
	AllowUpgrade         bool                 `json:"allow_upgrade"`
	PurchaseLimitPerUser *int                 `json:"purchase_limit_per_user"`
	StockTotal           *int                 `json:"stock_total"`
	StockReserved        int                  `json:"stock_reserved"`
	SortOrder            int                  `json:"sort_order"`
	Highlights           []string             `json:"highlights"`
	Recommended          bool                 `json:"recommended"`
	Versions             []versionRowResponse `json:"versions"`
	Prices               []priceRowResponse   `json:"prices"`
}

type listPlansResponse struct {
	Plans []planRowResponse `json:"plans"`
}

type getPlanResponse struct {
	Plan catalogPlanDetailResponse `json:"plan"`
}

type createPlanResponse struct {
	Plan catalogPlanDetailResponse `json:"plan"`
}

type createPlanCompleteResponse struct {
	Plan      catalogPlanDetailResponse `json:"plan"`
	VersionID string                    `json:"version_id"`
	PriceIDs  []string                  `json:"price_ids"`
	Published bool                      `json:"published"`
}

type updatePlanCompleteResponse struct {
	Plan    catalogPlanDetailResponse `json:"plan"`
	Changed []string                  `json:"changed"`
}

type updatePlanResponse struct {
	OK         bool  `json:"ok"`
	RowVersion int64 `json:"row_version"`
}

type okRowVersionResponse struct {
	OK         bool  `json:"ok"`
	RowVersion int64 `json:"row_version"`
}

type createVersionResponse struct {
	Version versionRowResponse `json:"version"`
}

type publishVersionResponse struct {
	OK                bool  `json:"ok"`
	PlanRowVersion    int64 `json:"plan_row_version"`
	VersionRowVersion int64 `json:"version_row_version"`
}

type createPriceResponse struct {
	Price priceRowResponse `json:"price"`
}

// --- 解析与转换 ---

func defaultTrueBool(v *bool) bool {
	return v == nil || *v
}

func parseVisibleGroupIDs(ids []string) ([]uuid.UUID, error) {
	seen := make(map[string]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, errs.Invalid(map[string]string{"visible_group_ids": "必须是无重复的 UUID 列表"})
		}
		uid, err := uuid.Parse(id)
		if err != nil {
			return nil, errs.Invalid(map[string]string{"visible_group_ids": "必须是无重复的 UUID 列表"})
		}
		seen[id] = true
		out = append(out, uid)
	}
	return out, nil
}

func parseUserGroupID(s *string) (*uuid.UUID, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	uid, err := uuid.Parse(*s)
	if err != nil {
		return nil, errs.Invalid(map[string]string{"user_group_id": "用户组不存在"})
	}
	return &uid, nil
}

// parseWizardPoolIDs 把向导里的池 id 解析成去重后的 UUID 列表（保持首次出现的顺序）。
func parseWizardPoolIDs(ids []string) ([]uuid.UUID, error) {
	if ids == nil {
		return []uuid.UUID{}, nil
	}
	seen := make(map[string]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		uid, err := uuid.Parse(id)
		if err != nil {
			return nil, errs.Invalid(map[string]string{"pool_ids": "必须是节点池 UUID 列表"})
		}
		seen[id] = true
		out = append(out, uid)
	}
	if len(out) > 500 {
		return nil, errs.Invalid(map[string]string{"pool_ids": "一次最多绑定 500 个节点池"})
	}
	return out, nil
}

func planInputFromCreate(req createPlanRequest) (service.PlanInput, error) {
	groups, err := parseVisibleGroupIDs(req.VisibleGroupIDs)
	if err != nil {
		return service.PlanInput{}, err
	}
	return service.PlanInput{
		Code:                 req.Code,
		Name:                 req.Name,
		Description:          req.Description,
		Visibility:           req.Visibility,
		VisibleGroupIDs:      groups,
		VisibleFrom:          req.VisibleFrom,
		VisibleUntil:         req.VisibleUntil,
		AllowNewPurchase:     defaultTrueBool(req.AllowNewPurchase),
		AllowRenewal:         defaultTrueBool(req.AllowRenewal),
		AllowUpgrade:         defaultTrueBool(req.AllowUpgrade),
		PurchaseLimitPerUser: req.PurchaseLimitPerUser,
		StockTotal:           req.StockTotal,
		SortOrder:            req.SortOrder,
		Highlights:           nonNilStrings(req.Highlights),
		Recommended:          req.Recommended,
	}, nil
}

func planInputFromUpdate(req updatePlanRequest) (service.PlanInput, error) {
	groups, err := parseVisibleGroupIDs(req.VisibleGroupIDs)
	if err != nil {
		return service.PlanInput{}, err
	}
	return service.PlanInput{
		Code:                 req.Code,
		Name:                 req.Name,
		Description:          req.Description,
		Visibility:           req.Visibility,
		VisibleGroupIDs:      groups,
		VisibleFrom:          req.VisibleFrom,
		VisibleUntil:         req.VisibleUntil,
		AllowNewPurchase:     req.AllowNewPurchase,
		AllowRenewal:         req.AllowRenewal,
		AllowUpgrade:         req.AllowUpgrade,
		PurchaseLimitPerUser: req.PurchaseLimitPerUser,
		StockTotal:           req.StockTotal,
		SortOrder:            req.SortOrder,
		Highlights:           nonNilStrings(req.Highlights),
		Recommended:          req.Recommended,
	}, nil
}

func versionInputFromRequest(req updateVersionRequest) service.VersionInput {
	ents := make([]service.Entitlement, len(req.Entitlements))
	for i, e := range req.Entitlements {
		ents[i] = service.Entitlement{Code: e.Code, Value: e.Value}
	}
	quotas := make([]service.Quota, len(req.Quotas))
	for i, q := range req.Quotas {
		quotas[i] = service.Quota{Metric: q.Metric, Limit: q.Limit, Unit: q.Unit, Period: q.Period}
	}
	return service.VersionInput{
		QuotaResetStrategy:   req.QuotaResetStrategy,
		QuotaResetDay:        req.QuotaResetDay,
		GracePeriodHours:     req.GracePeriodHours,
		GraceKeepsService:    req.GraceKeepsService,
		RenewalExtendsPeriod: req.RenewalExtendsPeriod,
		RenewalResetsQuota:   req.RenewalResetsQuota,
		RenewalKeepsAddons:   req.RenewalKeepsAddons,
		MaxDevices:           req.MaxDevices,
		MaxConcurrent:        req.MaxConcurrent,
		DeviceReleaseHours:   req.DeviceReleaseHours,
		OveragePolicy:        req.OveragePolicy,
		ThrottleKbps:         req.ThrottleKbps,
		Notes:                req.Notes,
		Entitlements:         ents,
		Quotas:               quotas,
	}
}

func priceInputFromRequest(req createPriceRequest) (service.PriceInput, error) {
	gid, err := parseUserGroupID(req.UserGroupID)
	if err != nil {
		return service.PriceInput{}, err
	}
	return service.PriceInput{
		Currency:        req.Currency,
		UnitAmount:      money.FromCents(req.UnitAmount),
		BillingInterval: req.BillingInterval,
		IntervalCount:   int(req.IntervalCount),
		TrialDays:       int(req.TrialDays),
		UserGroupID:     gid,
		ValidFrom:       req.ValidFrom,
		ValidUntil:      req.ValidUntil,
	}, nil
}

func wizardPricesFromRequest(in []wizardPriceRequest) []service.WizardPrice {
	out := make([]service.WizardPrice, len(in))
	for i, p := range in {
		out[i] = service.WizardPrice{
			BillingInterval: p.BillingInterval,
			IntervalCount:   int(p.IntervalCount),
			UnitAmount:      money.FromCents(p.UnitAmount),
			Currency:        p.Currency,
			TrialDays:       int(p.TrialDays),
		}
	}
	return out
}

func wizardCreateFromRequest(req createPlanCompleteRequest) (service.WizardCreate, error) {
	groups, err := parseVisibleGroupIDs(req.VisibleGroupIDs)
	if err != nil {
		return service.WizardCreate{}, err
	}
	plan := service.PlanInput{
		Code:                 req.Code,
		Name:                 req.Name,
		Description:          req.Description,
		Visibility:           req.Visibility,
		VisibleGroupIDs:      groups,
		AllowNewPurchase:     defaultTrueBool(req.AllowNewPurchase),
		AllowRenewal:         defaultTrueBool(req.AllowRenewal),
		AllowUpgrade:         defaultTrueBool(req.AllowUpgrade),
		PurchaseLimitPerUser: req.PurchaseLimitPerUser,
		StockTotal:           req.StockTotal,
		SortOrder:            req.SortOrder,
		Highlights:           nonNilStrings(req.Highlights),
		Recommended:          req.Recommended,
	}
	var resetDay *int
	if req.QuotaResetDay != nil {
		d := int(*req.QuotaResetDay)
		resetDay = &d
	}
	poolIDs, err := parseWizardPoolIDs(req.PoolIDs)
	if err != nil {
		return service.WizardCreate{}, err
	}
	return service.WizardCreate{
		Plan:               plan,
		TrafficGB:          req.TrafficGB,
		MaxDevices:         req.MaxDevices,
		ThrottleKbps:       req.ThrottleKbps,
		QuotaResetStrategy: req.QuotaResetStrategy,
		QuotaResetDay:      resetDay,
		PoolIDs:            poolIDs,
		Prices:             wizardPricesFromRequest(req.Prices),
		Publish:            req.Publish,
	}, nil
}

func wizardUpdateFromRequest(req updatePlanCompleteRequest) (service.WizardUpdate, error) {
	groups, err := parseVisibleGroupIDs(req.VisibleGroupIDs)
	if err != nil {
		return service.WizardUpdate{}, err
	}
	out := service.WizardUpdate{
		ExpectedRowVersion:   req.ExpectedRowVersion,
		Code:                 req.Code,
		Name:                 req.Name,
		Description:          req.Description,
		Visibility:           req.Visibility,
		VisibleGroupIDs:      groups,
		SortOrder:            req.SortOrder,
		AllowNewPurchase:     req.AllowNewPurchase,
		AllowRenewal:         req.AllowRenewal,
		AllowUpgrade:         req.AllowUpgrade,
		PurchaseLimitPerUser: req.PurchaseLimitPerUser,
		StockTotal:           req.StockTotal,
		Highlights:           req.Highlights,
		Recommended:          req.Recommended,
		TrafficGB:            req.TrafficGB,
		MaxDevices:           optionalIntToService(req.MaxDevices),
		ThrottleKbps:         optionalIntToService(req.ThrottleKbps),
	}
	if req.Prices != nil {
		wp := wizardPricesFromRequest(*req.Prices)
		out.Prices = &wp
	}
	if req.PoolIDs != nil {
		ids, err := parseWizardPoolIDs(*req.PoolIDs)
		if err != nil {
			return service.WizardUpdate{}, err
		}
		out.PoolIDs = &ids
	}
	return out, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func uuidToString(id uuid.UUID) string { return id.String() }

func uuidPtrToString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func uuidsToStrings(ids []uuid.UUID) []string {
	if len(ids) == 0 {
		return []string{}
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

func priceRowFrom(p service.Price) priceRowResponse {
	return priceRowResponse{
		ID:          uuidToString(p.ID),
		Currency:    string(p.Currency),
		UnitAmount:  money.MustCents(p.UnitAmount),
		Interval:    p.BillingInterval,
		Count:       p.IntervalCount,
		TrialDays:   p.TrialDays,
		Status:      p.Status,
		UserGroupID: uuidPtrToString(p.UserGroupID),
		ValidFrom:   p.ValidFrom,
		ValidUntil:  p.ValidUntil,
		RowVersion:  p.RowVersion,
	}
}

func priceRowsFrom(prices []service.Price) []priceRowResponse {
	out := make([]priceRowResponse, len(prices))
	for i, p := range prices {
		out[i] = priceRowFrom(p)
	}
	return out
}

func versionRowFrom(v service.Version) versionRowResponse {
	ents := make([]entitlementRequest, len(v.Entitlements))
	for i, e := range v.Entitlements {
		ents[i] = entitlementRequest{Code: e.Code, Value: e.Value}
	}
	quotas := make([]quotaRequest, len(v.Quotas))
	for i, q := range v.Quotas {
		quotas[i] = quotaRequest{Metric: q.Metric, Limit: q.Limit, Unit: q.Unit, Period: q.Period}
	}
	return versionRowResponse{
		ID:                   uuidToString(v.ID),
		Version:              v.Version,
		Status:               v.Status,
		FrozenAt:             v.FrozenAt,
		RowVersion:           v.RowVersion,
		QuotaResetStrategy:   v.QuotaResetStrategy,
		QuotaResetDay:        v.QuotaResetDay,
		GracePeriodHours:     v.GracePeriodHours,
		GraceKeepsService:    v.GraceKeepsService,
		RenewalExtendsPeriod: v.RenewalExtendsPeriod,
		RenewalResetsQuota:   v.RenewalResetsQuota,
		RenewalKeepsAddons:   v.RenewalKeepsAddons,
		MaxDevices:           v.MaxDevices,
		MaxConcurrent:        v.MaxConcurrent,
		DeviceReleaseHours:   v.DeviceReleaseHours,
		OveragePolicy:        service.OveragePolicySuspend,
		ThrottleKbps:         v.ThrottleKbps,
		Notes:                v.Notes,
		Entitlements:         ents,
		Quotas:               quotas,
		PoolIDs:              uuidsToStrings(v.PoolIDs),
		CreatedByEmail:       v.CreatedByEmail,
		CreatedAt:            v.CreatedAt,
	}
}

func versionRowsFrom(versions []service.Version) []versionRowResponse {
	out := make([]versionRowResponse, len(versions))
	for i, v := range versions {
		out[i] = versionRowFrom(v)
	}
	return out
}

func catalogPlanDetailFrom(d service.PlanDetail) catalogPlanDetailResponse {
	p := d.Plan
	return catalogPlanDetailResponse{
		ID:                   uuidToString(p.ID),
		CurrentVersionID:     uuidPtrToString(p.CurrentVersionID),
		RowVersion:           p.RowVersion,
		Code:                 p.Code,
		Name:                 p.Name,
		Description:          p.Description,
		Status:               p.Status,
		Visibility:           p.Visibility,
		VisibleGroupIDs:      uuidsToStrings(p.VisibleGroupIDs),
		VisibleFrom:          p.VisibleFrom,
		VisibleUntil:         p.VisibleUntil,
		AllowNewPurchase:     p.AllowNewPurchase,
		AllowRenewal:         p.AllowRenewal,
		AllowUpgrade:         p.AllowUpgrade,
		PurchaseLimitPerUser: p.PurchaseLimitPerUser,
		StockTotal:           p.StockTotal,
		StockReserved:        p.StockReserved,
		SortOrder:            p.SortOrder,
		Highlights:           nonNilStrings(p.Highlights),
		Recommended:          p.Recommended,
		Versions:             versionRowsFrom(d.Versions),
		Prices:               priceRowsFrom(d.Prices),
	}
}

func planRowFromSummary(s service.PlanSummary, activeSubs int64, nodeCount int64) planRowResponse {
	p := s.Plan
	return planRowResponse{
		ID:               uuidToString(p.ID),
		RowVersion:       p.RowVersion,
		Code:             p.Code,
		Name:             p.Name,
		Description:      p.Description,
		Status:           p.Status,
		Visibility:       p.Visibility,
		SortOrder:        p.SortOrder,
		CurrentVersionID: uuidPtrToString(p.CurrentVersionID),
		DraftVersionID:   uuidPtrToString(s.DraftVersionID),
		Version:          s.Version,
		MaxDevices:       s.MaxDevices,
		TrafficLimit:     s.TrafficLimit,
		Highlights:       nonNilStrings(p.Highlights),
		Recommended:      p.Recommended,
		Prices:           priceRowsFrom(s.Prices),
		ActiveSubs:       int(activeSubs),
		NodeCount:        int(nodeCount),
	}
}

func collectCurrentVersionIDs(rows []service.PlanSummary) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		if r.CurrentVersionID != nil {
			out = append(out, *r.CurrentVersionID)
		}
	}
	return out
}

func collectPlanIDs(rows []service.PlanSummary) []uuid.UUID {
	out := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func wizardCreatedResponse(w service.WizardCreated) createPlanCompleteResponse {
	priceIDs := make([]string, len(w.PriceIDs))
	for i, id := range w.PriceIDs {
		priceIDs[i] = uuidToString(id)
	}
	return createPlanCompleteResponse{
		Plan:      catalogPlanDetailFrom(w.Plan),
		VersionID: uuidToString(w.VersionID),
		PriceIDs:  priceIDs,
		Published: w.Published,
	}
}

func wizardUpdatedResponse(w service.WizardUpdated) updatePlanCompleteResponse {
	changed := w.Changed
	if changed == nil {
		changed = []string{}
	}
	return updatePlanCompleteResponse{
		Plan:    catalogPlanDetailFrom(w.Plan),
		Changed: changed,
	}
}
