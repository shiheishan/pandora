// [INPUT]: 依赖 service.go 的 Service、canonicalJSON 与共用助手，依赖 platform 的 crypto（Ed25519 签名）、db、httpx
// [OUTPUT]: 对外提供 SignedConfig、EffectiveConfigReportInput、VerifyConfigSignature，Service 的 FetchConfig、ReportConfigApplied、ReportEffectiveConfigApplied
// [POS]: domain/nodefabric 的旧版配置签发与回报（AGT-006/007）：从 service.go 拆出。FetchConfig 按全局 → 池 → 节点分层合并后规范化签名，RLS 未命中回中性 404，退役节点拒绝下发；回报要求唯一匹配的已发布配置，零匹配与多匹配都拒绝；有效发布物的回报也在这里
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//------------------------------------------------------------------------------
// AGT-006/007 分层配置合并与签名
//------------------------------------------------------------------------------

type SignedConfig struct {
	ConfigContract       string          `json:"config_contract,omitempty"`
	TenantID             string          `json:"tenant_id,omitempty"`
	NodeID               string          `json:"node_id,omitempty"`
	ReleaseID            string          `json:"release_id,omitempty"`
	Generation           uint64          `json:"generation,omitempty"`
	ContentSHA256        string          `json:"content_sha256,omitempty"`
	SourceManifest       json.RawMessage `json:"source_manifest,omitempty"`
	SourceManifestSHA256 string          `json:"source_manifest_sha256,omitempty"`
	IssuedAt             time.Time       `json:"issued_at,omitempty"`
	Version              int             `json:"version"`
	Payload              json.RawMessage `json:"payload"`
	Hash                 string          `json:"hash"`
	Signature            string          `json:"signature"`
	KeyID                string          `json:"key_id"`
	ExpiresAt            time.Time       `json:"expires_at"`
	// Sources 说明每一层的来源，对应 AGT-006「后台可解释每个最终配置值的来源」
	Sources []string `json:"sources"`
}

// FetchConfig 返回该节点当前应当运行的配置，带签名与有效期。
//
// 合并顺序 global < pool < node，后者覆盖前者。合并结果本身不入库 ——
// 入库的是各层的独立版本，避免层级改动后历史合并结果与来源对不上。
func (s *Service) FetchConfig(ctx context.Context, tenantID, nodeID string) (*SignedConfig, error) {
	merged := map[string]any{}
	var sources []string
	var maxVer int
	seenLayers := map[string]struct{}{}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var poolID *string
		if err := tx.QueryRow(ctx,
			`SELECT pool_id FROM nodes WHERE tenant_id=$1 AND id=$2
			  AND status NOT IN ('destroyed','retired')
			  AND serving_status<>'retired' FOR SHARE`,
			tenantID, nodeID).Scan(&poolID); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT scope, coalesce(scope_ref::text,''), version, payload,
			       content_hash, signature, signature_expires_at
			  FROM node_configs
			 WHERE tenant_id = $1 AND status = 'published'
			   AND ( scope = 'global'
			      OR (scope = 'pool' AND scope_ref = $2::uuid)
			      OR (scope = 'node' AND scope_ref = $3::uuid) )
			 ORDER BY CASE scope WHEN 'global' THEN 0 WHEN 'pool' THEN 1 ELSE 2 END,
			          version`,
			tenantID, poolID, nodeID)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var scope, ref string
			var ver int
			var payload, storedHash, storedSig []byte
			var sigExp *time.Time
			if err := rows.Scan(&scope, &ref, &ver, &payload,
				&storedHash, &storedSig, &sigExp); err != nil {
				return err
			}
			if scope == "global" && ref != "" {
				return httpx.New(httpx.CodeUnavailable, "configuration temporarily unavailable").
					WithInternal(fmt.Errorf("global published config has a scope reference"))
			}
			layerIdentity := scope + "\x00" + ref
			if scope == "global" {
				layerIdentity = "global"
			}
			if _, duplicate := seenLayers[layerIdentity]; duplicate {
				return httpx.New(httpx.CodeUnavailable, "configuration temporarily unavailable").
					WithInternal(fmt.Errorf("multiple published configs for logical layer %s", scope))
			}
			seenLayers[layerIdentity] = struct{}{}

			// 逐层验签后才纳入合并。
			//
			// 下发给 Agent 的是合并结果的新签名，如果这里不校验各层，
			// 任何能改库的人都可以替换某一层的 payload —— 控制面会毫不知情地
			// 用自己的密钥为被污染的内容背书，Agent 侧的验签也就形同虚设。
			// 这一步让「改库」和「持有签名密钥」重新变成两件独立的事。
			canon, err := canonicalJSON(payload)
			if err != nil {
				return fmt.Errorf("配置层 %s@v%d: %w", scope, ver, err)
			}
			layerSum := sha256.Sum256(canon)
			var why string
			switch {
			case len(storedSig) == 0 || sigExp == nil:
				why = "缺少签名"
			case !sigExp.After(time.Now()):
				why = "stored signature expired"
			case !bytesEqual(layerSum[:], storedHash):
				why = "内容与存档哈希不符"
			case !crypto.Verify(s.signer.PublicKey(),
				append(layerSum[:], []byte(sigExp.UTC().Format(time.RFC3339))...), storedSig):
				why = "签名校验失败"
			}
			if why != "" {
				// 这不是系统故障，而是一条明确的安全信号：库里的配置被动过，
				// 或签名密钥已轮换但配置未重新发布。返回 503 而不是 500 ——
				// 500 会把运维引向「服务挂了」的方向，浪费排查时间。
				// 具体原因只进服务端日志，不回给 Agent。
				return httpx.New(httpx.CodeUnavailable, "配置暂不可用，请联系管理员").
					WithInternal(fmt.Errorf("配置层 %s@v%d %s，已拒绝下发", scope, ver, why))
			}

			var layer map[string]any
			if err := json.Unmarshal(payload, &layer); err != nil {
				return fmt.Errorf("配置层 %s 不是合法 JSON: %w", scope, err)
			}
			for k, v := range layer {
				merged[k] = v
			}
			sources = append(sources, fmt.Sprintf("%s@v%d", scope, ver))
			if ver > maxVer {
				maxVer = ver
			}
		}
		return rows.Err()
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFoundOrForbidden()
		}
		return nil, err
	}
	if len(sources) == 0 {
		return nil, httpx.New(httpx.CodeNotFound, "尚无已发布的配置")
	}

	body, err := json.Marshal(merged)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	sum := sha256.Sum256(body)
	exp := time.Now().Add(24 * time.Hour)

	// 签名覆盖内容哈希与到期时间：只改到期时间也会让签名失效，
	// 否则一份旧配置可以被无限延期重放（AGT-007）。
	signed := s.signer.Sign(append(sum[:], []byte(exp.UTC().Format(time.RFC3339))...))

	return &SignedConfig{
		Version:   maxVer,
		Payload:   body,
		Hash:      base64.StdEncoding.EncodeToString(sum[:]),
		Signature: base64.StdEncoding.EncodeToString(signed),
		KeyID:     s.signer.KeyID(),
		ExpiresAt: exp,
		Sources:   sources,
	}, nil
}

// VerifyConfigSignature 是 Agent 侧的验签逻辑，放在这里与签名逻辑贴在一起，
// 避免两边各写一份、改了一处忘了另一处。
func VerifyConfigSignature(pub ed25519.PublicKey, hashB64, sigB64 string, exp time.Time) bool {
	sum, err := base64.StdEncoding.DecodeString(hashB64)
	if err != nil {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	if time.Now().After(exp) {
		return false // 过期配置一律拒绝
	}
	return crypto.Verify(pub, append(sum, []byte(exp.UTC().Format(time.RFC3339))...), sig)
}

// ReportConfigApplied 记录配置应用结果（AGT-008 的过程留痕）。
func (s *Service) ReportConfigApplied(ctx context.Context, tenantID, nodeID string, version int, phase, detail string) error {
	if err := validateLegacyConfigReport(version, phase, detail); err != nil {
		return err
	}
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// Freeze the node's current pool while resolving its legacy scope set. This
		// follows the NODE-004 lock order (node before config evidence) and prevents
		// an admin pool move from changing attribution halfway through the report.
		var poolID *string
		if err := tx.QueryRow(ctx, `
			SELECT pool_id::text FROM nodes
			 WHERE tenant_id=$1 AND id=$2::uuid
			 FOR SHARE`, tenantID, nodeID).Scan(&poolID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}

		// Legacy reports only contain an integer version. First require that version
		// to identify exactly one historical layer in the whole tenant. Filtering by
		// the node's *current* pool before proving uniqueness can misattribute a late
		// pre-freeze pool-A report to a colliding pool-B row after the node moved.
		rows, err := tx.Query(ctx, `
			SELECT c.id, c.scope, coalesce(c.scope_ref::text,'')
			  FROM node_configs AS c
			 WHERE c.tenant_id = $1
			   AND c.version = $2
			   AND c.published_at IS NOT NULL
			   AND c.status IN ('published','superseded','rolled_back')
			 ORDER BY c.id
			 LIMIT 2`, tenantID, version)
		if err != nil {
			return err
		}
		defer rows.Close()
		type candidate struct{ id, scope, ref string }
		var candidates []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.scope, &c.ref); err != nil {
				return err
			}
			candidates = append(candidates, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(candidates) != 1 {
			return httpx.New(httpx.CodeConflict,
				"无法唯一确认该配置版本，请重新获取最新配置")
		}
		c := candidates[0]
		applicable := c.scope == "global" ||
			(c.scope == "pool" && poolID != nil && c.ref == *poolID) ||
			(c.scope == "node" && c.ref == nodeID)
		if !applicable {
			return httpx.New(httpx.CodeConflict,
				"无法唯一确认该配置版本，请重新获取最新配置")
		}
		cfgID := c.id
		_, err = tx.Exec(ctx, `
			INSERT INTO node_config_applications
				(tenant_id, node_id, config_id, phase, detail)
			VALUES ($1,$2,$3,$4,$5)`,
			tenantID, nodeID, cfgID, phase,
			map[string]any{"contract": "legacy_layer_attribution", "message": detail})
		return err
	})
}

type EffectiveConfigReportInput struct {
	ReportID    string
	ReleaseID   string
	Generation  uint64
	ContentHash string
	Phase       string
	Detail      string
}

// ReportEffectiveConfigApplied attributes an application phase to one exact,
// immutable node release. report_id makes a network retry idempotent without
// falling back to the ambiguous legacy integer version.
func (s *Service) ReportEffectiveConfigApplied(ctx context.Context, tenantID, nodeID string, in EffectiveConfigReportInput) error {
	reportID, err := uuid.Parse(in.ReportID)
	if err != nil || reportID == uuid.Nil || reportID.String() != in.ReportID {
		return httpx.Invalid(map[string]string{"report_id": "must be a non-zero canonical UUID"})
	}
	releaseID, err := uuid.Parse(in.ReleaseID)
	if err != nil || releaseID == uuid.Nil || releaseID.String() != in.ReleaseID {
		return httpx.Invalid(map[string]string{"release_id": "must be a non-zero canonical UUID"})
	}
	if in.Generation == 0 || in.Generation > math.MaxInt64 {
		return httpx.Invalid(map[string]string{"generation": "must be a positive PostgreSQL bigint"})
	}
	hash, err := base64.StdEncoding.DecodeString(in.ContentHash)
	if err != nil || len(hash) != sha256.Size || base64.StdEncoding.EncodeToString(hash) != in.ContentHash {
		return httpx.Invalid(map[string]string{"content_sha256": "must be canonical base64 SHA-256"})
	}
	if err := validateLegacyConfigReport(1, in.Phase, in.Detail); err != nil {
		return err
	}
	detail := map[string]any{"contract": EffectiveReleaseContract, "message": in.Detail}
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if in.Phase == "health_passed" {
			var switched bool
			err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM node_config_applications
					 WHERE tenant_id=$1 AND node_id=$2::uuid
					   AND effective_release_id=$3::uuid
					   AND effective_generation=$4
					   AND effective_content_hash=$5
					   AND phase='switched')`,
				tenantID, nodeID, in.ReleaseID, int64(in.Generation), hash).Scan(&switched)
			if err != nil {
				return err
			}
			if !switched {
				return httpx.New(httpx.CodeConflict, "health_passed requires prior switched evidence")
			}
		}
		var inserted int
		err := tx.QueryRow(ctx, `
			INSERT INTO node_config_applications
				(tenant_id,node_id,config_id,effective_release_id,effective_generation,
				 effective_content_hash,report_id,phase,detail)
			SELECT $1,$2,NULL,r.id,r.generation,r.content_hash,$6,$7,$8
			  FROM node_effective_config_releases r
			 WHERE r.tenant_id=$1 AND r.node_id=$2::uuid AND r.id=$3::uuid
			   AND r.generation=$4 AND r.content_hash=$5
			ON CONFLICT (tenant_id,node_id,report_id) WHERE report_id IS NOT NULL
			DO NOTHING RETURNING 1`, tenantID, nodeID, in.ReleaseID, int64(in.Generation), hash,
			in.ReportID, in.Phase, detail).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) {
			var existingRelease string
			var existingGeneration int64
			var existingHash []byte
			var existingPhase string
			var existingDetail map[string]any
			err = tx.QueryRow(ctx, `
				SELECT effective_release_id::text,effective_generation,effective_content_hash,phase,detail
				  FROM node_config_applications
				 WHERE tenant_id=$1 AND node_id=$2::uuid AND report_id=$3::uuid`,
				tenantID, nodeID, in.ReportID).Scan(&existingRelease, &existingGeneration, &existingHash, &existingPhase, &existingDetail)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.New(httpx.CodeConflict, "effective release does not match this node")
			}
			if err != nil {
				return err
			}
			contract, contractOK := existingDetail["contract"].(string)
			message, messageOK := existingDetail["message"].(string)
			if existingRelease != in.ReleaseID || existingGeneration != int64(in.Generation) ||
				!bytesEqual(existingHash, hash) || existingPhase != in.Phase || len(existingDetail) != 2 ||
				!contractOK || contract != EffectiveReleaseContract || !messageOK || message != in.Detail {
				return httpx.New(httpx.CodeConflict, "report_id was already used for different evidence")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if in.Phase == "health_passed" {
			cmd, err := tx.Exec(ctx, `
				UPDATE nodes SET applied_effective_release_id=$3::uuid,
				       applied_effective_generation=$4,applied_effective_hash=$5
				 WHERE tenant_id=$1 AND id=$2::uuid
				   AND desired_effective_release_id=$3::uuid
				   AND desired_effective_generation=$4`, tenantID, nodeID, in.ReleaseID, int64(in.Generation), hash)
			if err != nil {
				return err
			}
			if cmd.RowsAffected() != 1 {
				return httpx.New(httpx.CodeConflict, "effective release is no longer desired by this node")
			}
		}
		return nil
	})
}

func validateLegacyConfigReport(version int, phase, detail string) error {
	if version <= 0 || int64(version) > math.MaxInt32 {
		return httpx.Invalid(map[string]string{"version": "必须是有效的正整数版本"})
	}
	validPhase := map[string]bool{
		"downloaded": true, "verified": true,
		"precheck_passed": true, "precheck_failed": true,
		"switched": true, "health_passed": true, "health_failed": true,
		"rolled_back": true, "failed": true,
	}
	if !validPhase[phase] {
		return httpx.Invalid(map[string]string{"phase": "不支持的配置应用阶段"})
	}
	if len(detail) > 2048 {
		return httpx.Invalid(map[string]string{"detail": "最多 2048 字节"})
	}
	return nil
}
