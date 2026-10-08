package node

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// beginServerEnrollmentReq 是接入 begin 的请求体（合约 §3.1），未知字段一律拒收。
type beginServerEnrollmentReq struct {
	Token        string          `json:"token"`
	RequestID    string          `json:"request_id"`
	PublicKey    string          `json:"public_key"`
	EncKEM       string          `json:"enc_kem"`
	EncPublicKey string          `json:"enc_public_key"`
	AgentVersion string          `json:"agent_version"`
	Features     []string        `json:"features"`
	Capabilities json.RawMessage `json:"capabilities"`
	Hostname     string          `json:"hostname"`
	CPUCores     int             `json:"cpu_cores"`
	MemoryMB     int             `json:"memory_mb"`
	DiskGB       int             `json:"disk_gb"`
}

func (h *handlers) beginServerEnrollment(w http.ResponseWriter, r *http.Request) {
	body, err := readEnrollmentBody(w, r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeBadRequest, "invalid enrollment request").WithInternal(err))
		return
	}
	var req beginServerEnrollmentReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	hash := sha256.Sum256(body)
	out, err := h.d.Node.BeginServerEnrollment(r.Context(), httpx.TenantIDFrom(r.Context()), nodefabric.ServerEnrollmentBeginInput{
		Token: req.Token, RequestID: req.RequestID, PublicKey: req.PublicKey, EncKEM: req.EncKEM,
		EncPublicKey: req.EncPublicKey, AgentVersion: req.AgentVersion, Features: req.Features,
		Capabilities: req.Capabilities, Hostname: req.Hostname, CPUCores: req.CPUCores,
		MemoryMB: req.MemoryMB, DiskGB: req.DiskGB, BeginRequestSHA256: hash[:],
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

func (h *handlers) serverEnrollmentStatus(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Node.ServerEnrollmentStatus(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "enrollmentID"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

// commitServerEnrollmentReq 沿用节点接入的证据字段（合约 §3.2）。
type commitServerEnrollmentReq struct {
	AgentVersion    string `json:"agent_version"`
	Architecture    string `json:"architecture"`
	BinarySHA256    string `json:"binary_sha256"`
	ConfigSHA256    string `json:"config_sha256"`
	UnitSHA256      string `json:"unit_sha256"`
	PreflightSHA256 string `json:"preflight_sha256"`
}

func (h *handlers) commitServerEnrollment(w http.ResponseWriter, r *http.Request) {
	body, err := readEnrollmentBody(w, r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	var req commitServerEnrollmentReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	hash := sha256.Sum256(body)
	out, err := h.d.Node.CommitServerEnrollment(r.Context(), httpx.TenantIDFrom(r.Context()), nodefabric.ServerEnrollmentCommitInput{
		EnrollmentID: chi.URLParam(r, "enrollmentID"), CommitRequestSHA256: hash[:],
		AgentVersion: req.AgentVersion, Architecture: req.Architecture, BinarySHA256: req.BinarySHA256,
		ConfigSHA256: req.ConfigSHA256, UnitSHA256: req.UnitSHA256, PreflightSHA256: req.PreflightSHA256,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.d.Log.Info("服务器已完成绑定", "server_id", out.ServerID, "serial", out.Serial,
		"request_id", httpx.RequestIDFrom(r.Context()))
	httpx.OK(w, out)
}

type abortServerEnrollmentReq struct {
	Reason string `json:"reason"`
}

func (h *handlers) abortServerEnrollment(w http.ResponseWriter, r *http.Request) {
	var req abortServerEnrollmentReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Node.AbortServerEnrollment(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "enrollmentID"), req.Reason)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}
