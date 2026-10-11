package nodefabric

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// 服务器签名请求的防重放，沿用节点链路那套 nonceGuard（nonce_guard.go）：进程内近期集
// 先「查并占」，主存储是 Valkey（SET NX PX），Valkey 不可用时回落 PG，签名时间戳落在
// 补查界内时 Valkey 认领成功后再在 PG 补查。键空间按服务器隔离（合约 §2.4 第 5 步）：
// 近期集的键带服务器种类字节、Valkey 的键带 server 前缀，PG 回落落在 server_request_nonces，
// 和节点 nonce 互不相撞。

// serverNonceKeyPrefix 是 Valkey 里服务器 nonce 键的前缀，与节点的 aegis:node-nonce: 分开。
const serverNonceKeyPrefix = "aegis:server-nonce:"

// errServerNonceReplayed 是 nonce 已被认领（重放）。网关对外与其它身份失败同一个 401。
var errServerNonceReplayed = fmt.Errorf("%w: request nonce was already claimed", ErrServerIdentityInvalid)

// ClaimServerRequestNonce 认领一个服务器签名请求的 nonce。重放回 ErrServerIdentityInvalid；
// 后端都不可用回 ErrServerAuthUnavailable。处理失败的请求不释放 nonce：重试必须换新的。
// 调用前必须已验过签名：fingerprint 是原像的 sha256。
func (s *Service) ClaimServerRequestNonce(ctx context.Context, tenantID, serverID string, nonce, fingerprint []byte,
	requestTS time.Time) error {
	if len(nonce) != 16 || len(fingerprint) != sha256.Size {
		return ErrServerIdentityInvalid
	}
	g := s.nonces
	if g == nil {
		return s.claimServerNonceInDatabase(ctx, tenantID, serverID, nonce, fingerprint, requestTS)
	}
	storeKey := serverNonceKeyPrefix + tenantID + ":" + serverID + ":" + base64.RawURLEncoding.EncodeToString(nonce)
	return g.claim(ctx, makeRecentKey(recentKindServer, tenantID, serverID, nonce), storeKey, requestTS,
		errServerNonceReplayed, func(ctx context.Context) error {
			return s.claimServerNonceInDatabase(ctx, tenantID, serverID, nonce, fingerprint, requestTS)
		})
}

// claimServerNonceInDatabase 在 PG 里认领 nonce。防重放只靠主键冲突：同一（租户, 服务器, nonce）
// 第二次插入 DO NOTHING、回重放。每条至少留到请求时间戳 +5 分钟、入库 +11 分钟之后，
// 与节点那张表同一口径（签名时间戳只接受 ±5 分钟）。
//
// 过期行在同一事务里顺手清掉，只清这一台服务器的（主键前缀，行数受保留期约束）：
// 只有回落时才写这张表，写的时候清，表不会无界增长，也不需要另起清理任务。
func (s *Service) claimServerNonceInDatabase(ctx context.Context, tenantID, serverID string, nonce, fingerprint []byte,
	requestTS time.Time) error {
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM server_request_nonces
			WHERE tenant_id=$1 AND server_id=$2::uuid AND expires_at < now()`, tenantID, serverID); err != nil {
			return err
		}
		var claimed int
		return tx.QueryRow(ctx, `
			INSERT INTO server_request_nonces
				(tenant_id, server_id, nonce, request_fingerprint, request_ts, expires_at)
			VALUES ($1,$2::uuid,$3,$4,$5::timestamptz,
				GREATEST($5::timestamptz + INTERVAL '5 minutes', now() + INTERVAL '11 minutes'))
			ON CONFLICT (tenant_id, server_id, nonce) DO NOTHING
			RETURNING 1`, tenantID, serverID, nonce, fingerprint, requestTS).Scan(&claimed)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return errServerNonceReplayed
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrServerAuthUnavailable, err)
	}
	return nil
}
