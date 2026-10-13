package handler

import (
	"github.com/aegispanel/aegis/internal/module/plugin/service"
)

type eventView struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}

type hookView struct {
	ID          string   `json:"id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
	Events      []string `json:"events"`
	EndpointURL string   `json:"endpoint_url"`
	HasSecret   bool     `json:"has_secret"`
	TimeoutMS   int      `json:"timeout_ms"`
	MaxAttempts int      `json:"max_attempts"`
	QueuedCount int      `json:"queued_count"`
	FailedCount int      `json:"failed_count"`
	SentCount7d int      `json:"sent_count_7d"`
	LastSentAt  *string  `json:"last_sent_at,omitempty"`
}

type listResponse struct {
	Events []eventView `json:"events"`
	Hooks  []hookView  `json:"hooks"`
}

type saveRequest struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
	Events      []string `json:"events"`
	EndpointURL string   `json:"endpoint_url"`
	Secret      string   `json:"secret"`
	TimeoutMS   int      `json:"timeout_ms"`
	MaxAttempts int      `json:"max_attempts"`
}

type saveResponse struct {
	Saved      bool   `json:"saved"`
	Secret     string `json:"secret,omitempty"`
	SecretHint string `json:"secret_hint,omitempty"`
}

type deleteResponse struct {
	Deleted bool `json:"deleted"`
}

type deliveryView struct {
	Event        string `json:"event"`
	Status       string `json:"status"`
	Attempts     int    `json:"attempts"`
	ResponseCode int    `json:"response_code"`
	ErrorMessage string `json:"error_message"`
	CreatedAt    string `json:"created_at"`
	SentAt       string `json:"sent_at"`
	DurationMS   *int   `json:"duration_ms"`
}

type deliveriesResponse struct {
	Deliveries []deliveryView `json:"deliveries"`
}

type testResponse struct {
	DurationMS   int  `json:"duration_ms"`
	ResponseCode int  `json:"response_code"`
	Sent         bool `json:"sent"`
}

func hookFrom(h service.Hook) hookView {
	ev := h.Events
	if ev == nil {
		ev = []string{}
	}
	var last *string
	if h.LastSentAt != "" {
		last = &h.LastSentAt
	}
	return hookView{
		ID:          h.ID.String(),
		Code:        h.Code,
		Name:        h.Name,
		Description: h.Description,
		Enabled:     h.Enabled,
		Events:      ev,
		EndpointURL: h.EndpointURL,
		HasSecret:   h.HasSecret,
		TimeoutMS:   h.TimeoutMS,
		MaxAttempts: h.MaxAttempts,
		QueuedCount: h.QueuedCount,
		FailedCount: h.FailedCount,
		SentCount7d: h.SentCount7d,
		LastSentAt:  last,
	}
}

func deliveryFrom(d service.Delivery) deliveryView {
	return deliveryView{
		Event:        d.Event,
		Status:       d.Status,
		Attempts:     d.Attempts,
		ResponseCode: d.ResponseCode,
		ErrorMessage: d.ErrorMessage,
		CreatedAt:    d.CreatedAt,
		SentAt:       d.SentAt,
		DurationMS:   d.DurationMS,
	}
}

func eventFrom(e service.EventInfo) eventView {
	return eventView{Name: e.Name, Desc: e.Desc}
}
