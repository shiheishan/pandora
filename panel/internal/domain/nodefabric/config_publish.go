// [INPUT]: 依赖 service.go 的 Service、canonicalJSON，依赖 effective_release_service.go 的 lockEffectiveReleaseNodes，依赖 platform 的 db、audit、httpx
// [OUTPUT]: 对外提供 PublishInput / PublishOutput、Service.PublishConfig；包内提供 lockLegacyConfigRelease、syncLegacyDesiredConfigVersion、validateLegacyPublishScope、nextLegacyConfigVersion
// [POS]: domain/nodefabric 的旧版配置发布：从 service.go 拆出。锁序为 node-config-release 发布锁 → 目标池 / 节点行 FOR SHARE → 受影响节点行 → 全租户版本分配 → 取代旧层 → 写新层；lockLegacyConfigRelease 与 syncLegacyDesiredConfigVersion 也被接入与后台建节点共用
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//------------------------------------------------------------------------------
// 配置发布
//------------------------------------------------------------------------------

type PublishInput struct {
	ActorID  string
	Scope    string
	ScopeRef string
	Payload  json.RawMessage
}

type PublishOutput struct {
	ConfigID string `json:"config_id"`
	Version  int    `json:"version"`
	Scope    string `json:"scope"`
	// AffectedNodes 让管理员在发布后立刻知道影响面
	AffectedNodes int `json:"affected_nodes"`
}

func lockLegacyConfigRelease(ctx context.Context, tx pgx.Tx, tenantID string) error {
	_, err := tx.Exec(ctx,
		`SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
		"node-config-release/"+tenantID)
	return err
}

// syncLegacyDesiredConfigVersion materializes the current applicable legacy
// maximum for a node created after an earlier global/pool publication. Callers
// must already hold lockLegacyConfigRelease so the max cannot change between
// the node insert and this projection update.
func syncLegacyDesiredConfigVersion(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE nodes AS n
		   SET desired_config_version = (
		       SELECT max(c.version)
		         FROM node_configs AS c
		        WHERE c.tenant_id=n.tenant_id AND c.status='published'
		          AND (c.scope='global'
		            OR (c.scope='pool' AND c.scope_ref=n.pool_id)
		            OR (c.scope='node' AND c.scope_ref=n.id)))
		 WHERE n.tenant_id=$1 AND n.id=$2::uuid
		   AND n.status NOT IN ('destroyed','retired')
		   AND n.serving_status<>'retired'`, tenantID, nodeID)
	return err
}

func validateLegacyPublishScope(scope, ref string) error {
	scope, ref = strings.TrimSpace(scope), strings.TrimSpace(ref)
	switch scope {
	case "global":
		if ref != "" {
			return httpx.Invalid(map[string]string{"scope_ref": "global 层不能指定对象"})
		}
	case "pool", "node":
		parsed, err := uuid.Parse(ref)
		if err != nil || parsed.String() != ref {
			return httpx.Invalid(map[string]string{"scope_ref": "必须是规范的小写 UUID"})
		}
	default:
		return httpx.Invalid(map[string]string{"scope": "只支持 global/pool/node"})
	}
	return nil
}

func nextLegacyConfigVersion(current int64, anomalous bool) (int, error) {
	if anomalous || current < 0 || current >= math.MaxInt32 {
		return 0, httpx.New(httpx.CodeConflict,
			"节点配置版本状态异常，请完成配置发布身份升级")
	}
	return int(current + 1), nil
}

// PublishConfig 发布一层配置。
//
// NODE-004 的最终身份是 00048 合同中的 immutable release UUID。迁移前的
// 整数只是兼容 token；这里在租户发布锁内全局递增，保证通过本服务产生的新
// global/pool/node layer 不再碰撞或倒退。它不能替代 release identity，也不能
// 修复既有歧义历史，因此上报路径仍必须拒绝零匹配和多匹配。
func (s *Service) PublishConfig(ctx context.Context, tenantID string, in PublishInput) (*PublishOutput, error) {
	in.Scope = strings.TrimSpace(in.Scope)
	in.ScopeRef = strings.TrimSpace(in.ScopeRef)
	if err := validateLegacyPublishScope(in.Scope, in.ScopeRef); err != nil {
		return nil, err
	}
	canon, err := canonicalJSON(in.Payload)
	if err != nil {
		return nil, httpx.Invalid(map[string]string{"payload": err.Error()})
	}
	sum := sha256.Sum256(canon)
	exp := time.Now().Add(24 * time.Hour)
	sig := s.signer.Sign(append(sum[:], []byte(exp.UTC().Format(time.RFC3339))...))

	var out PublishOutput
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		// Use the same lock domain reserved by the effective-release contract so a
		// later dual-write server cannot race this legacy writer during rollout.
		if err := lockLegacyConfigRelease(ctx, tx, tenantID); err != nil {
			return err
		}

		var ref *string
		if in.ScopeRef != "" {
			ref = &in.ScopeRef
		}
		if in.Scope != "global" {
			var targetID string
			var query string
			if in.Scope == "pool" {
				query = `SELECT id::text FROM node_pools
					WHERE tenant_id=$1 AND id=$2::uuid AND status<>'disabled'
					FOR SHARE`
			} else {
				query = `SELECT id::text FROM nodes
					WHERE tenant_id=$1 AND id=$2::uuid
					  AND status NOT IN ('destroyed','retired')
					  AND serving_status<>'retired'
					FOR SHARE`
			}
			if err := tx.QueryRow(ctx, query, tenantID, in.ScopeRef).Scan(&targetID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.NotFoundOrForbidden()
				}
				return err
			}
		}
		// Freeze the exact affected node set before mutating the published layer.
		// FetchEffectiveConfig locks the same node row first, so this ordering makes
		// every fetch observe either the complete old generation or the complete new
		// generation. UUID ordering also prevents global/pool writers from acquiring
		// overlapping node locks in an arbitrary order.
		if err := lockEffectiveReleaseNodes(ctx, tx, tenantID, in.Scope, in.ScopeRef); err != nil {
			return err
		}

		// applied_config_version comes from an authenticated but still untrusted
		// legacy agent, so it must never be allowed to poison this allocator. The
		// published config history is the only cooperative writer watermark.
		var current int64
		var anomalous bool
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(max(version)::bigint, 0),
			       coalesce(bool_or(version <= 0), false)
			  FROM node_configs
			 WHERE tenant_id=$1`, tenantID).Scan(&current, &anomalous); err != nil {
			return err
		}
		ver, err := nextLegacyConfigVersion(current, anomalous)
		if err != nil {
			return err
		}

		// 同层旧版本置为 superseded：FetchConfig 只取 published，
		// 不这样做会一次取出同层多个版本，合并结果取决于行序，不可复现。
		if _, err := tx.Exec(ctx, `
			UPDATE node_configs SET status='superseded'
			 WHERE tenant_id=$1 AND scope=$2 AND scope_ref IS NOT DISTINCT FROM $3::uuid
			   AND status='published'`, tenantID, in.Scope, ref); err != nil {
			return err
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO node_configs
				(tenant_id, scope, scope_ref, version, payload, content_hash,
				 signature, signing_key_id, signed_at, signature_expires_at,
				 status, rollout_percent, published_at, created_by)
			VALUES ($1,$2,$3::uuid,$4,$5,$6,$7,$8,now(),$9,'published',100,now(),$10)
			RETURNING id`,
			tenantID, in.Scope, ref, ver, in.Payload, sum[:],
			sig, s.signer.KeyID(), exp, in.ActorID).Scan(&out.ConfigID); err != nil {
			return err
		}

		// 把受影响节点的期望版本推上去，Agent 下次心跳就会发现有新配置
		var ct int64
		switch in.Scope {
		case "global":
			tag, err := tx.Exec(ctx, `
				UPDATE nodes SET desired_config_version = $2,
				       config_source_generation=config_source_generation+1
				 WHERE tenant_id=$1 AND status NOT IN ('destroyed','retired')
				   AND serving_status<>'retired'`, tenantID, ver)
			if err != nil {
				return err
			}
			ct = tag.RowsAffected()
		case "pool":
			tag, err := tx.Exec(ctx, `
				UPDATE nodes SET desired_config_version = $3,
				       config_source_generation=config_source_generation+1
				 WHERE tenant_id=$1 AND pool_id=$2::uuid
				   AND status NOT IN ('destroyed','retired')
				   AND serving_status<>'retired'`, tenantID, in.ScopeRef, ver)
			if err != nil {
				return err
			}
			ct = tag.RowsAffected()
		case "node":
			tag, err := tx.Exec(ctx, `
				UPDATE nodes SET desired_config_version = $3,
				       config_source_generation=config_source_generation+1
				 WHERE tenant_id=$1 AND id=$2::uuid
				   AND status NOT IN ('destroyed','retired')
				   AND serving_status<>'retired'`, tenantID, in.ScopeRef, ver)
			if err != nil {
				return err
			}
			ct = tag.RowsAffected()
		}
		out.Version, out.Scope, out.AffectedNodes = ver, in.Scope, int(ct)

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.config.publish", ResourceType: "node_config",
			ResourceID: &out.ConfigID, APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{
				"scope": in.Scope, "version": ver, "affected_nodes": ct,
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
