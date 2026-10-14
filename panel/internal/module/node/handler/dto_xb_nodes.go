package handler

// Xboard 补齐：标签、流量上限、时段倍率、中转、ECH、批量修改的请求/响应 DTO 与转换。

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/node/service"
)

// nodeOptInt64 区分「没给」「给了 null」「给了数字」：UnmarshalJSON 被调用即 Set。
type nodeOptInt64 struct {
	Set   bool
	Value *int64
}

// UnmarshalJSON 实现 json.Unmarshaler。
func (o *nodeOptInt64) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Value = nil
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	o.Value = &n
	return nil
}

// nodeOptInt 区分「没给」「给了 null」「给了数字」：UnmarshalJSON 被调用即 Set。
type nodeOptInt struct {
	Set   bool
	Value *int
}

// UnmarshalJSON 实现 json.Unmarshaler。
func (o *nodeOptInt) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Value = nil
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	o.Value = &n
	return nil
}

// nodeRateRangeRequest 是时段倍率的一段（JSON rate 为数字）。
type nodeRateRangeRequest struct {
	Start string  `json:"start"`
	End   string  `json:"end"`
	Rate  float64 `json:"rate"`
}

// nodeRateRangeResponse 是时段倍率的一段响应。
type nodeRateRangeResponse struct {
	Start string  `json:"start"`
	End   string  `json:"end"`
	Rate  float64 `json:"rate"`
}

// nodeTrafficLimitResponse 是流量对象 traffic。
type nodeTrafficLimitResponse struct {
	LimitBytes *int64     `json:"limit_bytes"`
	ResetDay   *int       `json:"reset_day"`
	UsedBytes  int64      `json:"used_bytes"`
	ResetAt    time.Time  `json:"reset_at"`
	ExceededAt *time.Time `json:"exceeded_at"`
}

// nodeRelayResponse 是中转条目。
type nodeRelayResponse struct {
	ID           uuid.UUID `json:"id"`
	ParentNodeID uuid.UUID `json:"parent_node_id"`
	Name         string    `json:"name"`
	ServerHost   string    `json:"server_host"`
	ServerPort   int       `json:"server_port"`
	SNI          *string   `json:"sni"`
	HostHeader   *string   `json:"host_header"`
	Enabled      bool      `json:"enabled"`
	SortOrder    int       `json:"sort_order"`
	RowVersion   int64     `json:"row_version"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// nodeRelayCreateRequest 是新建中转。
type nodeRelayCreateRequest struct {
	Name       string  `json:"name"`
	ServerHost string  `json:"server_host"`
	ServerPort int     `json:"server_port"`
	SNI        *string `json:"sni"`
	HostHeader *string `json:"host_header"`
	Enabled    *bool   `json:"enabled"`
	SortOrder  int     `json:"sort_order"`
}

// nodeRelayPatchRequest 是修改中转。
type nodeRelayPatchRequest struct {
	RowVersion int64         `json:"row_version"`
	Name       *string       `json:"name"`
	ServerHost *string       `json:"server_host"`
	ServerPort *int          `json:"server_port"`
	SNI        nodeOptString `json:"sni"`
	HostHeader nodeOptString `json:"host_header"`
	Enabled    *bool         `json:"enabled"`
	SortOrder  *int          `json:"sort_order"`
}

// nodeRelayDeleteRequest 是删除中转。
type nodeRelayDeleteRequest struct {
	RowVersion int64 `json:"row_version"`
}

// nodeRelaysListResponse 是列出中转。
type nodeRelaysListResponse struct {
	Relays []nodeRelayResponse `json:"relays"`
}

// nodeTrafficResetRequest 是手动重置流量。
type nodeTrafficResetRequest struct {
	Reason string `json:"reason"`
}

// nodeOKResponse 是 {"ok":true}。
type nodeOKResponse struct {
	OK bool `json:"ok"`
}

// nodeBatchEditRequest 是批量修改节点。
type nodeBatchEditRequest struct {
	Items  []nodeVersionRefRequest `json:"items"`
	Set    nodeBatchFieldsRequest  `json:"set"`
	Reason string                  `json:"reason"`
}

// nodeBatchFieldsRequest 是批量修改可改的字段。
type nodeBatchFieldsRequest struct {
	PoolID              nodeOptString           `json:"pool_id"`
	TrafficRate         *float64                `json:"traffic_rate"`
	Tags                *[]string               `json:"tags"`
	ServingStatus       string                  `json:"serving_status"`
	TrafficLimitBytes   nodeOptInt64            `json:"traffic_limit_bytes"`
	TrafficResetDay     nodeOptInt              `json:"traffic_reset_day"`
	TrafficRateSchedule *[]nodeRateRangeRequest `json:"traffic_rate_schedule"`
}

// nodeECHResponse 是 ECH 状态。
type nodeECHResponse struct {
	Supported         bool       `json:"supported"`
	UnsupportedReason string     `json:"unsupported_reason"`
	Enabled           bool       `json:"enabled"`
	PublicName        string     `json:"public_name"`
	ConfigList        *string    `json:"config_list"`
	ConfigPEM         string     `json:"config_pem"`
	CreatedAt         *time.Time `json:"created_at"`
	RetiredKeys       int        `json:"retired_keys"`
}

// nodeECHGenerateRequest 是生成 ECH。
type nodeECHGenerateRequest struct {
	PublicName string `json:"public_name"`
}

func nodeTrafficLimitFrom(t service.NodeTrafficLimit) nodeTrafficLimitResponse {
	return nodeTrafficLimitResponse{
		LimitBytes: t.LimitBytes,
		ResetDay:   t.ResetDay,
		UsedBytes:  t.UsedBytes,
		ResetAt:    t.ResetAt,
		ExceededAt: t.ExceededAt,
	}
}

func nodeRateRangesFrom(in []service.RateRangeInput) []nodeRateRangeResponse {
	if len(in) == 0 {
		return []nodeRateRangeResponse{}
	}
	out := make([]nodeRateRangeResponse, len(in))
	for i, r := range in {
		out[i] = nodeRateRangeResponse{Start: r.Start, End: r.End, Rate: r.Rate.InexactFloat64()}
	}
	return out
}

func rateRangesToService(in []nodeRateRangeRequest) []service.RateRangeInput {
	if len(in) == 0 {
		return nil
	}
	out := make([]service.RateRangeInput, len(in))
	for i, r := range in {
		out[i] = service.RateRangeInput{
			Start: r.Start,
			End:   r.End,
			Rate:  decimal.NewFromFloat(r.Rate),
		}
	}
	return out
}

func relayFrom(r service.NodeRelay) nodeRelayResponse {
	return nodeRelayResponse{
		ID:           r.ID,
		ParentNodeID: r.ParentNodeID,
		Name:         r.Name,
		ServerHost:   r.ServerHost,
		ServerPort:   r.ServerPort,
		SNI:          r.SNI,
		HostHeader:   r.HostHeader,
		Enabled:      r.Enabled,
		SortOrder:    r.SortOrder,
		RowVersion:   r.RowVersion,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	}
}

func echFrom(e service.NodeECH) nodeECHResponse {
	out := nodeECHResponse{
		Supported:         e.Supported,
		UnsupportedReason: e.UnsupportedReason,
		Enabled:           e.Enabled,
		PublicName:        e.PublicName,
		ConfigPEM:         e.ConfigPEM,
		CreatedAt:         e.CreatedAt,
		RetiredKeys:       e.RetiredKeys,
	}
	if e.Enabled && len(e.ConfigList) > 0 {
		s := base64.StdEncoding.EncodeToString(e.ConfigList)
		out.ConfigList = &s
	}
	return out
}

func relaysFrom(list []service.NodeRelay) nodeRelaysListResponse {
	out := make([]nodeRelayResponse, len(list))
	for i, r := range list {
		out[i] = relayFrom(r)
	}
	return nodeRelaysListResponse{Relays: out}
}

func nodeBatchFieldsToService(set nodeBatchFieldsRequest) service.NodeBatchFields {
	// pool_id 要解析 UUID、可能报错，由 handler 另行填（照 patchNode）
	out := service.NodeBatchFields{ServingStatus: set.ServingStatus}
	if set.TrafficRate != nil {
		d := decimal.NewFromFloat(*set.TrafficRate)
		out.TrafficRate = &d
	}
	out.Tags = set.Tags
	if set.TrafficLimitBytes.Set {
		out.TrafficLimitBytes = service.OptionalInt64{Set: true, Value: set.TrafficLimitBytes.Value}
	}
	if set.TrafficResetDay.Set {
		out.TrafficResetDay = service.OptionalInt{Set: true, Value: set.TrafficResetDay.Value}
	}
	if set.TrafficRateSchedule != nil {
		sched := rateRangesToService(*set.TrafficRateSchedule)
		out.TrafficRateSchedule = &sched
	}
	return out
}
