package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/node/service"
)

type bootstrapTokenRequest struct {
	TTLMinutes int `json:"ttl_minutes"` // 0 或省略 = 缺省 20 分钟
}

type nodeBootstrapTokenRequest struct {
	NodeName   string `json:"node_name"` // 前端会发，忽略
	ServerID   string `json:"server_id"` // 节点所在服务器；空 = 节点还没放到服务器上
	TTLMinutes int    `json:"ttl_minutes"`
}

type bootstrapTokenResponse struct {
	Token          string    `json:"token"`
	ExpiresAt      time.Time `json:"expires_at"`
	InstallCommand string    `json:"install_command"`
}

type nodeIdentityResponse struct {
	Identity               *identityResponse         `json:"identity"` // null = 从未接入
	ServerToken            serverTokenStatusResponse `json:"server_token"`
	BootstrapTokensPending int64                     `json:"bootstrap_tokens_pending"`
}

type identityResponse struct {
	Serial            int64      `json:"serial"` // 这台服务器第几次签发
	Status            string     `json:"status"`
	SPIFFEID          string     `json:"spiffe_id"`
	FingerprintSHA256 string     `json:"fingerprint_sha256"`
	IssuedAt          time.Time  `json:"issued_at"`
	ExpiresAt         time.Time  `json:"expires_at"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
	RevokedReason     *string    `json:"revoked_reason,omitempty"`
}

// serverTokenStatusResponse：第三方节点端的服务端令牌已不支持，present 恒为 false（前端字段照给）。
type serverTokenStatusResponse struct {
	Present bool `json:"present"`
}

type identityOKResponse struct {
	OK bool `json:"ok"`
}

func bootstrapTokenResponseFrom(r service.BootstrapTokenIssued) bootstrapTokenResponse {
	return bootstrapTokenResponse{
		Token:          r.Token,
		ExpiresAt:      r.ExpiresAt.UTC(),
		InstallCommand: r.InstallCommand,
	}
}

func nodeIdentityResponseFrom(v service.NodeIdentityView) nodeIdentityResponse {
	out := nodeIdentityResponse{
		ServerToken:            serverTokenStatusResponse{Present: false},
		BootstrapTokensPending: v.PendingTokens,
	}
	if v.Identity != nil {
		id := v.Identity
		revokedAt := id.RevokedAt
		if revokedAt != nil {
			t := revokedAt.UTC()
			revokedAt = &t
		}
		out.Identity = &identityResponse{
			Serial:            int64(id.Seq),
			Status:            id.Status,
			SPIFFEID:          id.SPIFFEID,
			FingerprintSHA256: id.FingerprintSHA256,
			IssuedAt:          id.IssuedAt.UTC(),
			ExpiresAt:         id.ExpiresAt.UTC(),
			RevokedAt:         revokedAt,
			RevokedReason:     id.RevokedReason,
		}
	}
	return out
}
