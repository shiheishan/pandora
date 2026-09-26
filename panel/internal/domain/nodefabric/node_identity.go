// [INPUT]: 依赖 node_identities / nodes.server_token_issued_* / bootstrap_tokens 三处凭据事实，依赖 platform/db 与 httpx
// [OUTPUT]: 对外提供 NodeCredentialState 及其三个子结构、Service.NodeCredentials
// [POS]: nodefabric 的节点凭据只读视图，服务后台节点抽屉「身份与令牌」；签发与吊销仍在 enrollment.go / uniproxy.go / bootstrap.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// NodeCredentialState 回答「这个节点现在凭什么接入」：mTLS 身份、UniProxy
// 服务端令牌、还有多少张没用掉的安装令牌。三样都只给元数据——令牌只存哈希，
// 原文在签发那一刻之后谁也看不到，这里也不假装能给。
type NodeCredentialState struct {
	Identity               *NodeIdentityInfo `json:"identity"`
	ServerToken            ServerTokenInfo   `json:"server_token"`
	BootstrapTokensPending int               `json:"bootstrap_tokens_pending"`
}

type NodeIdentityInfo struct {
	Serial            int        `json:"serial"`
	Status            string     `json:"status"`
	SpiffeID          string     `json:"spiffe_id"`
	FingerprintSHA256 string     `json:"fingerprint_sha256"`
	IssuedAt          time.Time  `json:"issued_at"`
	ExpiresAt         time.Time  `json:"expires_at"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
	RevokedReason     *string    `json:"revoked_reason,omitempty"`
}

// ServerTokenInfo 的签发人为空有两种来历：令牌是节点接入时由安装令牌换得的，
// 或签发发生在 00082 之前。issued_at 为空只可能是后者。
type ServerTokenInfo struct {
	Present      bool       `json:"present"`
	IssuedAt     *time.Time `json:"issued_at,omitempty"`
	IssuedBy     *string    `json:"issued_by,omitempty"`
	IssuedByName *string    `json:"issued_by_name,omitempty"`
}

// NodeCredentials 读取节点凭据状态。已销毁的节点同样可读：事后追查
// 「它被销毁前用的是哪张证书」正是这个视图的用途之一。
func (s *Service) NodeCredentials(ctx context.Context, tenantID, nodeID string) (*NodeCredentialState, error) {
	if _, err := uuid.Parse(nodeID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	var out NodeCredentialState
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT n.server_token_hash IS NOT NULL, n.server_token_issued_at,
			       n.server_token_issued_by::text,
			       coalesce(nullif(u.display_name, ''), u.email::text)
			  FROM nodes n
			  LEFT JOIN users u ON u.id = n.server_token_issued_by
			 WHERE n.tenant_id = $1 AND n.id = $2::uuid`, tenantID, nodeID).Scan(
			&out.ServerToken.Present, &out.ServerToken.IssuedAt,
			&out.ServerToken.IssuedBy, &out.ServerToken.IssuedByName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}

		// 有效身份优先；没有就给最近一份（被吊销或过期的），让管理员看见
		// 「它曾经有过、现在为什么没了」，而不是一个无从解释的空白
		var id NodeIdentityInfo
		var fp []byte
		err := tx.QueryRow(ctx, `
			SELECT serial, status, spiffe_id, fingerprint, issued_at, expires_at,
			       revoked_at, revoked_reason
			  FROM node_identities
			 WHERE tenant_id = $1 AND node_id = $2::uuid
			 ORDER BY (status = 'active') DESC, serial DESC
			 LIMIT 1`, tenantID, nodeID).Scan(&id.Serial, &id.Status, &id.SpiffeID, &fp,
			&id.IssuedAt, &id.ExpiresAt, &id.RevokedAt, &id.RevokedReason)
		switch {
		case err == nil:
			id.FingerprintSHA256 = hex.EncodeToString(fp)
			out.Identity = &id
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}

		return tx.QueryRow(ctx, `
			SELECT count(*)::int FROM bootstrap_tokens
			 WHERE tenant_id = $1 AND node_id = $2::uuid
			   AND consumed_at IS NULL AND used_count < max_uses AND expires_at > now()`,
			tenantID, nodeID).Scan(&out.BootstrapTokensPending)
	})
	if err != nil {
		if httpErr := new(httpx.Error); errors.As(err, &httpErr) {
			return nil, err
		}
		return nil, httpx.Internal(err)
	}
	return &out, nil
}
