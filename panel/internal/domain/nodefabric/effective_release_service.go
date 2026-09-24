package nodefabric

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

var effectiveReservedKeys = map[string]struct{}{
	"protocol": {}, "server_port": {}, "host": {}, "server_name": {},
	"kernel": {}, "kernel_type": {}, "base_config": {}, "outbounds": {},
	"routes": {}, "custom_outbounds": {}, "custom_route_rules": {},
}

type effectiveLayerManifest struct {
	ID      string `json:"id"`
	Scope   string `json:"scope"`
	Ref     string `json:"scope_ref,omitempty"`
	Version int    `json:"version"`
	Hash    string `json:"content_sha256"`
}

// FetchEffectiveConfig materializes one immutable, node-specific runtime
// release. Re-fetching an unchanged generation returns the same release ID;
// producing different bytes for an existing generation fails closed.
func (s *Service) FetchEffectiveConfig(ctx context.Context, tenantID, nodeID string) (*SignedConfig, error) {
	var out SignedConfig
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var n ServingNode
		var protocol []byte
		var generation int64
		if err := tx.QueryRow(ctx, `
			SELECT n.id::text,n.name,coalesce(n.node_type,''),coalesce(n.server_host,''),
			       coalesce(n.server_port,0),n.traffic_rate,n.protocol_config,n.pool_id,
			       n.status,coalesce(n.kernel,'auto'),coalesce(s.status,''),n.serving_status,
			       n.config_source_generation
			  FROM nodes n
			  LEFT JOIN servers s ON s.tenant_id=n.tenant_id AND s.id=n.server_id AND s.deleted_at IS NULL
			 WHERE n.tenant_id=$1 AND n.id=$2::uuid
			   AND n.status NOT IN ('destroyed','retired') AND n.serving_status<>'retired'
			   AND n.node_type IS NOT NULL AND n.server_port BETWEEN 1 AND 65535
			 FOR UPDATE OF n`, tenantID, nodeID).Scan(&n.ID, &n.Name, &n.NodeType, &n.ServerHost,
			&n.ServerPort, &n.TrafficRate, &protocol, &n.PoolID, &n.Status, &n.Kernel,
			&n.ServerStatus, &n.ServingStatus, &generation); err != nil {
			return err
		}
		n.Protocol = protocol
		n.NodeType = CanonicalNodeType(n.NodeType)

		// A signer change is itself a release-source change. Advance the node's
		// generation once while holding the same node lock used by all other
		// materialization writers, so immutable releases are never re-signed in
		// place and the new key cannot trigger same-generation drift.
		var generationKeyID string
		keyErr := tx.QueryRow(ctx, `SELECT key_id FROM node_effective_config_releases
			 WHERE tenant_id=$1 AND node_id=$2::uuid AND generation=$3`, tenantID, nodeID, generation).Scan(&generationKeyID)
		if keyErr == nil && generationKeyID != s.signer.KeyID() {
			if err := tx.QueryRow(ctx, `UPDATE nodes SET config_source_generation=config_source_generation+1
				 WHERE tenant_id=$1 AND id=$2::uuid RETURNING config_source_generation`, tenantID, nodeID).Scan(&generation); err != nil {
				return err
			}
		} else if keyErr != nil && !errors.Is(keyErr, pgx.ErrNoRows) {
			return keyErr
		}

		// An immutable release has already passed source-layer verification. A
		// later expiry of the publishing envelope must not strand a node that is
		// re-fetching the same generation after a restart or a lost response.
		// Source writers advance config_source_generation under this same node
		// lock, so only an exact current-generation/current-key release may use
		// this path; a new generation is always materialized and verified below.
		var existingID, existingKeyID string
		var existingPayload, existingManifest, existingContent, existingManifestHash []byte
		existingErr := tx.QueryRow(ctx, `
			SELECT id::text,key_id,payload,content_hash,source_manifest,source_manifest_hash
			  FROM node_effective_config_releases
			 WHERE tenant_id=$1 AND node_id=$2::uuid AND generation=$3`,
			tenantID, nodeID, generation).Scan(&existingID, &existingKeyID, &existingPayload,
			&existingContent, &existingManifest, &existingManifestHash)
		if existingErr == nil && existingKeyID == s.signer.KeyID() {
			payload, err := canonicalJSON(existingPayload)
			if err != nil || !hashMatches(payload, existingContent) {
				return httpx.New(httpx.CodeConflict, "stored effective configuration is invalid").WithInternal(err)
			}
			manifest, err := canonicalJSON(existingManifest)
			if err != nil || !hashMatches(manifest, existingManifestHash) {
				return httpx.New(httpx.CodeConflict, "stored effective manifest is invalid").WithInternal(err)
			}
			if _, err := tx.Exec(ctx, `UPDATE nodes SET desired_effective_release_id=$3::uuid,
				desired_effective_generation=$4 WHERE tenant_id=$1 AND id=$2::uuid`,
				tenantID, nodeID, existingID, generation); err != nil {
				return err
			}
			out, err = s.signEffectiveRelease(tenantID, nodeID, existingID, generation, existingKeyID,
				payload, existingContent, manifest, existingManifestHash)
			return err
		}
		if existingErr != nil && !errors.Is(existingErr, pgx.ErrNoRows) {
			return existingErr
		}

		outs, routes, err := loadEffectiveRoutingTx(ctx, tx, tenantID, nodeID)
		if err != nil {
			return err
		}
		n.Outbounds, n.Routes = outs, routes
		baseBody, _, err := s.BuildNodeConfig(&n)
		if err != nil {
			return httpx.New(httpx.CodeUnavailable, "configuration temporarily unavailable").WithInternal(err)
		}
		var merged map[string]any
		if err := json.Unmarshal(baseBody, &merged); err != nil {
			return err
		}

		layers, err := s.applyEffectiveLayersTx(ctx, tx, tenantID, nodeID, n.PoolID, merged)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		contentSum := sha256.Sum256(payload)
		baseSum := sha256.Sum256(baseBody)
		routingBytes, _ := json.Marshal(map[string]any{"outbounds": outs, "routes": routes})
		routingSum := sha256.Sum256(routingBytes)
		protocolSum := sha256.Sum256(protocol)
		manifestBytes, err := json.Marshal(map[string]any{
			"node": map[string]any{"id": nodeID, "generation": generation,
				"protocol_sha256": base64.StdEncoding.EncodeToString(protocolSum[:]),
				"base_sha256":     base64.StdEncoding.EncodeToString(baseSum[:])},
			"routing_sha256": base64.StdEncoding.EncodeToString(routingSum[:]),
			"layers":         layers,
		})
		if err != nil {
			return err
		}
		// manifest 里的 layers 是结构体切片，json.Marshal 按字段声明顺序输出；
		// 而它存进 jsonb 之后再读回来，会先被 canonicalJSON 拍成字典序。
		// 两份字节因此永远不相等，自检必定判成「内容变了但 generation 没动」，
		// 结果是节点永远拿不到有效配置（实测每次请求都在这里 409 并回滚，
		// 所以 node_effective_config_releases 一直是空表）。
		// 这里先规范化一次，让存库、算哈希、回读比对三者用同一份字节。
		manifestBytes, err = canonicalJSON(manifestBytes)
		if err != nil {
			return err
		}
		manifestSum := sha256.Sum256(manifestBytes)

		releaseID := uuid.New().String()
		var storedID, storedKeyID string
		var storedPayload, storedManifest []byte
		var storedContent, storedManifestHash []byte
		inserted := false
		err = tx.QueryRow(ctx, `
			INSERT INTO node_effective_config_releases
			 (id,tenant_id,node_id,generation,payload,content_hash,source_manifest,source_manifest_hash,key_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (tenant_id,node_id,generation) DO NOTHING
			RETURNING id::text,key_id,payload,content_hash,source_manifest,source_manifest_hash`,
			releaseID, tenantID, nodeID, generation, payload, contentSum[:], manifestBytes, manifestSum[:], s.signer.KeyID()).
			Scan(&storedID, &storedKeyID, &storedPayload, &storedContent, &storedManifest, &storedManifestHash)
		if err == nil {
			inserted = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !inserted {
			if err := tx.QueryRow(ctx, `
				SELECT id::text,key_id,payload,content_hash,source_manifest,source_manifest_hash
				  FROM node_effective_config_releases
				 WHERE tenant_id=$1 AND node_id=$2::uuid AND generation=$3`, tenantID, nodeID, generation).
				Scan(&storedID, &storedKeyID, &storedPayload, &storedContent, &storedManifest, &storedManifestHash); err != nil {
				return err
			}
		}
		storedPayloadCanonical, err := canonicalJSON(storedPayload)
		if err != nil {
			return httpx.New(httpx.CodeConflict, "stored effective configuration is not canonical JSON").WithInternal(err)
		}
		storedManifestCanonical, err := canonicalJSON(storedManifest)
		if err != nil {
			return httpx.New(httpx.CodeConflict, "stored effective manifest is not canonical JSON").WithInternal(err)
		}
		if !bytesEqual(storedContent, contentSum[:]) || !bytesEqual(storedManifestHash, manifestSum[:]) ||
			!bytesEqual(storedPayloadCanonical, payload) || !bytesEqual(storedManifestCanonical, manifestBytes) ||
			storedKeyID != s.signer.KeyID() {
			return httpx.New(httpx.CodeConflict, "effective configuration changed without a generation bump")
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET desired_effective_release_id=$3::uuid,
			desired_effective_generation=$4 WHERE tenant_id=$1 AND id=$2::uuid`,
			tenantID, nodeID, storedID, generation); err != nil {
			return err
		}

		// Permit a bounded one-minute negative node clock skew while keeping the
		// complete signed delivery window capped at ten minutes.
		out, err = s.signEffectiveRelease(tenantID, nodeID, storedID, generation, storedKeyID,
			storedPayloadCanonical, storedContent, storedManifestCanonical, storedManifestHash)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func hashMatches(canonical, stored []byte) bool {
	sum := sha256.Sum256(canonical)
	return bytesEqual(sum[:], stored)
}

func (s *Service) signEffectiveRelease(tenantID, nodeID, releaseID string, generation int64, keyID string,
	payload, contentHash, manifest, manifestHash []byte) (SignedConfig, error) {
	issued := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	expires := issued.Add(EffectiveReleaseMaxDeliveryWindow)
	fields := EffectiveReleaseSignatureFields{TenantID: tenantID, NodeID: nodeID, ReleaseID: releaseID,
		Generation: uint64(generation), ContentHash: base64.StdEncoding.EncodeToString(contentHash),
		SourceManifestHash: base64.StdEncoding.EncodeToString(manifestHash), KeyID: keyID,
		IssuedAt: issued, ExpiresAt: expires}
	signature, err := SignEffectiveRelease(s.signer, fields)
	if err != nil {
		return SignedConfig{}, err
	}
	return SignedConfig{ConfigContract: EffectiveReleaseContract, TenantID: tenantID, NodeID: nodeID,
		ReleaseID: releaseID, Generation: uint64(generation), Payload: payload,
		ContentSHA256: fields.ContentHash, Hash: fields.ContentHash, SourceManifest: manifest,
		SourceManifestSHA256: fields.SourceManifestHash, KeyID: keyID, IssuedAt: issued,
		ExpiresAt: expires, Signature: signature}, nil
}

func loadEffectiveRoutingTx(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) ([]NodeOutbound, []NodeRoute, error) {
	var outs []NodeOutbound
	rows, err := tx.Query(ctx, `SELECT tag,type,settings,(node_id IS NOT NULL) FROM node_outbounds
		WHERE tenant_id=$1 AND (node_id IS NULL OR node_id=$2::uuid) ORDER BY (node_id IS NOT NULL),sort_order,tag`, tenantID, nodeID)
	if err != nil {
		return nil, nil, err
	}
	idx := map[string]int{}
	for rows.Next() {
		var o NodeOutbound
		var scoped bool
		if err := rows.Scan(&o.Tag, &o.Type, &o.Settings, &scoped); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if at, ok := idx[o.Tag]; ok {
			outs[at] = o
		} else {
			idx[o.Tag] = len(outs)
			outs = append(outs, o)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	var routes []NodeRoute
	// 生效规则 = 节点私有规则在前、全局规则在后（节点规则覆盖全局）；与 LoadRouting 同一口径
	rrows, err := tx.Query(ctx, `SELECT matcher,outbound_tag FROM node_routes
		WHERE tenant_id=$1 AND (node_id=$2::uuid OR node_id IS NULL) AND enabled
		ORDER BY (node_id IS NULL),priority,created_at`, tenantID, nodeID)
	if err != nil {
		return nil, nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var r NodeRoute
		if err := rrows.Scan(&r.Matcher, &r.OutboundTag); err != nil {
			return nil, nil, err
		}
		routes = append(routes, r)
	}
	return outs, routes, rrows.Err()
}

func lockEffectiveReleaseNodes(ctx context.Context, tx pgx.Tx, tenantID, scope, scopeRef string) error {
	query := `SELECT id FROM nodes WHERE tenant_id=$1
		AND status NOT IN ('destroyed','retired') AND serving_status<>'retired'`
	args := []any{tenantID}
	switch scope {
	case "global":
	case "pool":
		query += ` AND pool_id=$2::uuid`
		args = append(args, scopeRef)
	case "node":
		query += ` AND id=$2::uuid`
		args = append(args, scopeRef)
	default:
		return fmt.Errorf("unsupported effective release scope %q", scope)
	}
	query += ` ORDER BY id FOR UPDATE`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Service) applyEffectiveLayersTx(ctx context.Context, tx pgx.Tx, tenantID, nodeID string, poolID *string, merged map[string]any) ([]effectiveLayerManifest, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,scope,coalesce(scope_ref::text,''),version,payload,content_hash,signature,signature_expires_at
		FROM node_configs WHERE tenant_id=$1 AND status='published' AND (scope='global' OR (scope='pool' AND scope_ref=$2::uuid) OR (scope='node' AND scope_ref=$3::uuid))
		ORDER BY CASE scope WHEN 'global' THEN 0 WHEN 'pool' THEN 1 ELSE 2 END,version`, tenantID, poolID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var manifests []effectiveLayerManifest
	seenLayers := make(map[string]struct{})
	for rows.Next() {
		var id, scope, ref string
		var version int
		var payload, hash, sig []byte
		var exp *time.Time
		if err := rows.Scan(&id, &scope, &ref, &version, &payload, &hash, &sig, &exp); err != nil {
			return nil, err
		}
		layerIdentity := scope + "\x00" + ref
		if scope == "global" {
			if ref != "" {
				return nil, httpx.New(httpx.CodeUnavailable, "configuration temporarily unavailable").
					WithInternal(fmt.Errorf("global published config has a scope reference"))
			}
			layerIdentity = "global"
		}
		if _, duplicate := seenLayers[layerIdentity]; duplicate {
			return nil, httpx.New(httpx.CodeUnavailable, "configuration temporarily unavailable").
				WithInternal(fmt.Errorf("multiple published configs for logical layer %s", scope))
		}
		seenLayers[layerIdentity] = struct{}{}
		canon, err := canonicalJSON(payload)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(canon)
		if exp == nil || !exp.After(time.Now()) || !bytesEqual(sum[:], hash) ||
			!crypto.Verify(s.signer.PublicKey(), append(sum[:], []byte(exp.UTC().Format(time.RFC3339))...), sig) {
			return nil, httpx.New(httpx.CodeUnavailable, "configuration temporarily unavailable").WithInternal(fmt.Errorf("published layer %s failed verification", id))
		}
		var layer map[string]any
		if err := json.Unmarshal(canon, &layer); err != nil {
			return nil, err
		}
		for key, value := range layer {
			if _, reserved := effectiveReservedKeys[key]; reserved {
				return nil, httpx.New(httpx.CodeConflict, "published layer attempts to override a reserved runtime field")
			}
			merged[key] = value
		}
		manifests = append(manifests, effectiveLayerManifest{ID: id, Scope: scope, Ref: ref,
			Version: version, Hash: base64.StdEncoding.EncodeToString(hash)})
	}
	return manifests, rows.Err()
}
