// [INPUT]: 依赖 node_enrollments / node_identities / bootstrap_tokens / nodes，依赖 platform 的 audit/crypto/db/httpx
// [OUTPUT]: 对外提供接入签名规范串（CanonicalEnrollment*）、LookupEnrollmentCredential、两段式接入 BeginEnrollment / GetEnrollment / CommitEnrollment / AbortEnrollment
// [POS]: domain/nodefabric 的节点两段式接入：候选身份与运行令牌在提交时一次落定，服务端令牌签发记录随之重置为「接入所得、无签发人」
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const EnrollmentSignatureDomainV1 = "PANDORA-NODE-ENROLLMENT-V1"
const EnrollmentBeginSignatureDomainV1 = "PANDORA-NODE-ENROLL-BEGIN-V1"

func CanonicalEnrollmentBeginV1(path, requestID string, bodyHash []byte) []byte {
	return []byte(EnrollmentBeginSignatureDomainV1 + "\nPOST\n" + path + "\n" + requestID + "\n" +
		base64.StdEncoding.EncodeToString(bodyHash))
}

func CanonicalEnrollmentRequestV1(method, path, enrollmentID, nodeID string, serial int, ts, nonce string, bodyHash []byte) []byte {
	return []byte(EnrollmentSignatureDomainV1 + "\n" + method + "\n" + path + "\n" + enrollmentID + "\n" +
		nodeID + "\n" + fmt.Sprintf("%d", serial) + "\n" + ts + "\n" + nonce + "\n" +
		base64.StdEncoding.EncodeToString(bodyHash))
}

type BeginEnrollmentInput struct {
	Token              string
	NodeName           string
	RequestID          string
	PublicKey          string
	RuntimeTokenSHA256 string
	BeginRequestSHA256 []byte
	AgentVersion       string
	Hostname           string
	CPUCores           int
	MemoryMB           int
	DiskGB             int
	PublicIPv4         string
}

type EnrollmentOutput struct {
	EnrollmentID    string    `json:"enrollment_id"`
	NodeID          string    `json:"node_id"`
	Serial          int       `json:"serial"`
	State           string    `json:"state"`
	ExpiresAt       time.Time `json:"expires_at"`
	ConfigKeyID     string    `json:"config_key_id"`
	ConfigPublicKey string    `json:"config_public_key"`
}

type CommitEnrollmentInput struct {
	EnrollmentID        string
	CommitRequestSHA256 []byte
	AgentVersion        string
	Architecture        string
	BinarySHA256        string
	ConfigSHA256        string
	UnitSHA256          string
	PreflightSHA256     string
}

func validateEnrollmentEvidence(in CommitEnrollmentInput) error {
	fields := map[string]string{
		"binary_sha256": in.BinarySHA256, "config_sha256": in.ConfigSHA256,
		"unit_sha256": in.UnitSHA256, "preflight_sha256": in.PreflightSHA256,
	}
	errs := map[string]string{}
	if strings.TrimSpace(in.AgentVersion) == "" || len(in.AgentVersion) > 128 {
		errs["agent_version"] = "is required and must be at most 128 characters"
	}
	if in.Architecture != "amd64" && in.Architecture != "arm64" {
		errs["architecture"] = "must be amd64 or arm64"
	}
	for name, value := range fields {
		raw, err := hex.DecodeString(value)
		if err != nil || len(raw) != sha256.Size || value != strings.ToLower(value) {
			errs[name] = "must be a canonical lowercase SHA-256 hex digest"
		}
	}
	digestEnv := "PANDORA_NATIVE_ARTIFACT_" + strings.ToUpper(in.Architecture) + "_SHA256"
	expectedDigest := strings.ToLower(strings.TrimSpace(os.Getenv(digestEnv)))
	if expectedDigest == "" && strings.EqualFold(strings.TrimSpace(os.Getenv("AEGIS_ENV")), "production") {
		errs["artifact"] = digestEnv + " must be configured in production"
	} else if expectedDigest != "" && expectedDigest != strings.ToLower(in.BinarySHA256) {
		errs["binary_sha256"] = "does not match the published architecture artifact"
	}
	expectedVersion := strings.TrimSpace(os.Getenv("PANDORA_NATIVE_RELEASE_VERSION"))
	if expectedVersion == "" && strings.EqualFold(strings.TrimSpace(os.Getenv("AEGIS_ENV")), "production") {
		errs["agent_version"] = "PANDORA_NATIVE_RELEASE_VERSION must be configured in production"
	} else if expectedVersion != "" && in.AgentVersion != expectedVersion {
		errs["agent_version"] = "does not match the published release version"
	}
	if len(errs) != 0 {
		return httpx.Invalid(errs)
	}
	return nil
}

type EnrollmentCredential struct {
	EnrollmentID string
	NodeID       string
	Serial       int
	PublicKey    ed25519.PublicKey
	State        string
	ExpiresAt    time.Time
}

func (s *Service) LookupEnrollmentCredential(ctx context.Context, tenantID, enrollmentID string) (*EnrollmentCredential, error) {
	if _, err := canonicalUUIDField("enrollment_id", enrollmentID); err != nil {
		return nil, err
	}
	var out EnrollmentCredential
	var pub []byte
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text,node_id::text,candidate_serial,public_key,state,expires_at
			FROM node_enrollments WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, enrollmentID).
			Scan(&out.EnrollmentID, &out.NodeID, &out.Serial, &pub, &out.State, &out.ExpiresAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.New(httpx.CodeUnauthorized, "enrollment identity is invalid")
	}
	if err != nil {
		return nil, err
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, httpx.New(httpx.CodeUnauthorized, "enrollment identity is invalid")
	}
	out.PublicKey = ed25519.PublicKey(pub)
	return &out, nil
}

func decodeCanonicalSHA256(field, value string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(raw) != sha256.Size || base64.StdEncoding.EncodeToString(raw) != value {
		return nil, httpx.Invalid(map[string]string{field: "must be canonical base64 SHA-256"})
	}
	return raw, nil
}

func canonicalUUIDField(field, value string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || id == uuid.Nil || id.String() != value {
		return "", httpx.Invalid(map[string]string{field: "must be a non-zero canonical UUID"})
	}
	return value, nil
}

// BeginEnrollment reserves a bootstrap-token use and persists an unusable
// credential candidate. It intentionally does not create node_identities and
// does not install nodes.server_token_hash.
func (s *Service) BeginEnrollment(ctx context.Context, tenantID string, in BeginEnrollmentInput) (*EnrollmentOutput, error) {
	requestID, err := canonicalUUIDField("request_id", in.RequestID)
	if err != nil {
		return nil, err
	}
	nodeName, err := normalizeBootstrapNodeName(in.NodeName)
	if err != nil {
		return nil, err
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.PublicKey))
	if err != nil || len(pub) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(pub) != in.PublicKey {
		return nil, httpx.Invalid(map[string]string{"public_key": "must be canonical base64 Ed25519 public key"})
	}
	runtimeHash, err := decodeCanonicalSHA256("runtime_token_sha256", in.RuntimeTokenSHA256)
	if err != nil {
		return nil, err
	}
	if len(in.BeginRequestSHA256) != sha256.Size {
		return nil, httpx.Invalid(map[string]string{"request": "missing verified request digest"})
	}
	fingerprint := sha256.Sum256(pub)

	var out EnrollmentOutput
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := lockLegacyConfigRelease(ctx, tx, tenantID); err != nil {
			return err
		}
		var tokenID, boundNodeID, boundServerID string
		var poolID *string
		var maxUses, usedCount int16
		var expiresAt time.Time
		var consumedAt *time.Time
		var tokenUnexpired bool
		if err := tx.QueryRow(ctx, `
			SELECT id::text,pool_id::text,coalesce(node_id::text,''),coalesce(server_id::text,''),max_uses,used_count,expires_at,consumed_at,
			       expires_at > now()
			  FROM bootstrap_tokens
			 WHERE tenant_id=$1 AND token_hash=$2
			 FOR UPDATE`, tenantID, bootstrapTokenHash(in.Token, nodeName)).
			Scan(&tokenID, &poolID, &boundNodeID, &boundServerID, &maxUses, &usedCount, &expiresAt, &consumedAt, &tokenUnexpired); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.New(httpx.CodeUnauthorized, "bootstrap token is invalid or expired")
			}
			return err
		}

		var existing EnrollmentOutput
		var existingBegin, existingPub, existingRuntime []byte
		var existingToken, existingConfigKeyID string
		var existingConfigPublicKey []byte
		retryErr := tx.QueryRow(ctx, `
			SELECT e.id::text,e.node_id::text,e.candidate_serial,e.state,e.expires_at,
			       e.begin_request_sha256,e.public_key,e.runtime_token_hash,e.bootstrap_token_id::text,
			       e.config_signing_key_id,e.config_signing_public_key
			  FROM node_enrollments e
			 WHERE e.tenant_id=$1 AND e.request_id=$2::uuid`, tenantID, requestID).
			Scan(&existing.EnrollmentID, &existing.NodeID, &existing.Serial, &existing.State, &existing.ExpiresAt,
				&existingBegin, &existingPub, &existingRuntime, &existingToken,
				&existingConfigKeyID, &existingConfigPublicKey)
		if retryErr == nil {
			if existingToken != tokenID || !equalBytes(existingBegin, in.BeginRequestSHA256) ||
				!equalBytes(existingPub, pub) || !equalBytes(existingRuntime, runtimeHash) {
				return httpx.New(httpx.CodeConflict, "request_id was already used for different enrollment evidence")
			}
			out = existing
			out.ConfigKeyID = existingConfigKeyID
			out.ConfigPublicKey = base64.StdEncoding.EncodeToString(existingConfigPublicKey)
			return nil
		}
		if !errors.Is(retryErr, pgx.ErrNoRows) {
			return retryErr
		}
		if consumedAt != nil || usedCount >= maxUses || !tokenUnexpired {
			return httpx.New(httpx.CodeUnauthorized, "bootstrap token is invalid or expired")
		}
		if boundServerID != "" {
			var serverUsable bool
			if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL AND status <> 'retired' FROM servers
				WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, boundServerID).Scan(&serverUsable); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.New(httpx.CodeConflict, "bootstrap token server is unavailable")
				}
				return err
			}
			if !serverUsable {
				return httpx.New(httpx.CodeConflict, "bootstrap token server is unavailable")
			}
		}

		var nodeID, nodeStatus, servingStatus, actualPoolID, actualServerID string
		lookupErr := tx.QueryRow(ctx, `
			SELECT id::text,status,serving_status,coalesce(pool_id::text,''),coalesce(server_id::text,'')
			  FROM nodes WHERE tenant_id=$1 AND name=$2 FOR UPDATE`, tenantID, nodeName).
			Scan(&nodeID, &nodeStatus, &servingStatus, &actualPoolID, &actualServerID)
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			if boundNodeID != "" {
				return httpx.New(httpx.CodeConflict, "bootstrap token target no longer exists")
			}
			if poolID != nil {
				if err := validatePool(ctx, tx, tenantID, *poolID); err != nil {
					return err
				}
			}
			if err := tx.QueryRow(ctx, `
				INSERT INTO nodes (tenant_id,name,pool_id,status,agent_version,hostname,cpu_cores,memory_mb,disk_gb,public_ipv4)
				VALUES ($1,$2,$3,'bootstrapping',$4,$5,$6,$7,$8,$9::inet) RETURNING id::text`,
				tenantID, nodeName, poolID, in.AgentVersion, nullStr(in.Hostname), nullInt(in.CPUCores),
				nullInt(in.MemoryMB), nullInt(in.DiskGB), nullStr(in.PublicIPv4)).Scan(&nodeID); err != nil {
				return err
			}
		} else if lookupErr != nil {
			return lookupErr
		} else {
			if boundNodeID == "" || boundNodeID != nodeID {
				return httpx.New(httpx.CodeConflict, "bootstrap token does not match target node")
			}
			if nodeStatus == "retired" || nodeStatus == "destroyed" || servingStatus == "retired" {
				return httpx.New(httpx.CodeConflict, "retired node cannot be enrolled")
			}
			var active bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM node_identities WHERE tenant_id=$1 AND node_id=$2::uuid AND status='active')`, tenantID, nodeID).Scan(&active); err != nil {
				return err
			}
			if active {
				return httpx.New(httpx.CodeConflict, "node already has an active identity; explicit rotation is required")
			}
			if poolID != nil && *poolID != actualPoolID {
				return httpx.New(httpx.CodeConflict, "bootstrap token pool does not match target node")
			}
			if boundServerID != "" && actualServerID != "" && boundServerID != actualServerID {
				return httpx.New(httpx.CodeConflict, "bootstrap token server does not match target node")
			}
			if boundServerID == "" && actualServerID != "" {
				var serverUsable bool
				if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL AND status <> 'retired' FROM servers
					WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, actualServerID).Scan(&serverUsable); err != nil || !serverUsable {
					return httpx.New(httpx.CodeConflict, "target node server is unavailable")
				}
			}
			if nodeStatus != "bootstrapping" {
				cmd, err := tx.Exec(ctx, `UPDATE nodes SET status='bootstrapping',row_version=row_version+1
					WHERE tenant_id=$1 AND id=$2::uuid AND status IN ('draft','provisioning','bootstrap_failed')`, tenantID, nodeID)
				if err != nil {
					return err
				}
				if cmd.RowsAffected() != 1 {
					return httpx.New(httpx.CodeConflict, "node is not in an enrollable lifecycle state")
				}
			}
			if err := validatePool(ctx, tx, tenantID, actualPoolID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE node_enrollments SET state='expired',abort_reason='deadline elapsed'
			WHERE tenant_id=$1 AND node_id=$2::uuid AND state='pending' AND expires_at<=clock_timestamp()`,
			tenantID, nodeID); err != nil {
			return err
		}

		var serial int
		if err := tx.QueryRow(ctx, `
			SELECT greatest(
			  coalesce((SELECT max(serial) FROM node_identities WHERE tenant_id=$1 AND node_id=$2::uuid),0),
			  coalesce((SELECT max(candidate_serial) FROM node_enrollments WHERE tenant_id=$1 AND node_id=$2::uuid),0)
			) + 1`, tenantID, nodeID).Scan(&serial); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO node_enrollments
			  (tenant_id,node_id,bootstrap_token_id,use_ordinal,request_id,begin_request_sha256,
			   candidate_serial,public_key,fingerprint,runtime_token_hash,
			   config_signing_key_id,config_signing_public_key,expires_at)
			VALUES ($1,$2::uuid,$3::uuid,$4,$5::uuid,$6,$7,$8,$9,$10,$11,$12,
			        least($13::timestamptz,now()+interval '15 minutes'))
			RETURNING id::text,state,expires_at`, tenantID, nodeID, tokenID, usedCount+1, requestID,
			in.BeginRequestSHA256, serial, pub, fingerprint[:], runtimeHash,
			s.signer.KeyID(), s.signer.PublicKey(), expiresAt).
			Scan(&out.EnrollmentID, &out.State, &out.ExpiresAt); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return httpx.New(httpx.CodeConflict, "node already has a pending enrollment or reserved credential")
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE bootstrap_tokens SET used_count=used_count+1,node_id=$2::uuid,
			consumed_at=CASE WHEN used_count+1>=max_uses THEN now() ELSE NULL END WHERE tenant_id=$1 AND id=$3::uuid`,
			tenantID, nodeID, tokenID); err != nil {
			return err
		}
		out.NodeID, out.Serial = nodeID, serial
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "node", Action: "node.enrollment.begin",
			ResourceType: "node_enrollment", ResourceID: &out.EnrollmentID, APIDomain: "node", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx), AfterDigest: map[string]any{"node_id": nodeID, "serial": serial}})
	})
	if err != nil {
		return nil, err
	}
	if out.ConfigKeyID == "" {
		out.ConfigKeyID = s.signer.KeyID()
		out.ConfigPublicKey = base64.StdEncoding.EncodeToString(s.signer.PublicKey())
	}
	return &out, nil
}

func (s *Service) GetEnrollment(ctx context.Context, tenantID, enrollmentID string) (*EnrollmentOutput, error) {
	if _, err := canonicalUUIDField("enrollment_id", enrollmentID); err != nil {
		return nil, err
	}
	var out EnrollmentOutput
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var nodeID, tokenID string
		if err := tx.QueryRow(ctx, `SELECT node_id::text,bootstrap_token_id::text FROM node_enrollments
			WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, enrollmentID).Scan(&nodeID, &tokenID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM bootstrap_tokens WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, tokenID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM nodes WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, nodeID); err != nil {
			return err
		}
		var expired bool
		if err := tx.QueryRow(ctx, `SELECT id::text,node_id::text,candidate_serial,state,expires_at,
			expires_at<=clock_timestamp() FROM node_enrollments WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, enrollmentID).
			Scan(&out.EnrollmentID, &out.NodeID, &out.Serial, &out.State, &out.ExpiresAt, &expired); err != nil {
			return err
		}
		if out.State == "pending" && expired {
			if _, err := tx.Exec(ctx, `UPDATE node_enrollments SET state='expired',abort_reason='deadline elapsed'
				WHERE tenant_id=$1 AND id=$2::uuid AND state='pending'`, tenantID, enrollmentID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE nodes SET status='bootstrap_failed',row_version=row_version+1
				WHERE tenant_id=$1 AND id=$2::uuid AND status='bootstrapping'
				AND NOT EXISTS (SELECT 1 FROM node_enrollments e WHERE e.tenant_id=$1 AND e.node_id=$2::uuid
				  AND e.state='pending' AND e.id<>$3::uuid)`, tenantID, out.NodeID, enrollmentID); err != nil {
				return err
			}
			out.State = "expired"
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Service) CommitEnrollment(ctx context.Context, tenantID string, in CommitEnrollmentInput) (*EnrollmentOutput, error) {
	if _, err := canonicalUUIDField("enrollment_id", in.EnrollmentID); err != nil {
		return nil, err
	}
	if len(in.CommitRequestSHA256) != sha256.Size {
		return nil, httpx.Invalid(map[string]string{"request": "missing verified request digest"})
	}
	if err := validateEnrollmentEvidence(in); err != nil {
		return nil, err
	}
	evidenceJSON, err := json.Marshal(map[string]string{
		"agent_version": in.AgentVersion, "architecture": in.Architecture,
		"binary_sha256": in.BinarySHA256, "config_sha256": in.ConfigSHA256,
		"unit_sha256": in.UnitSHA256, "preflight_sha256": in.PreflightSHA256,
	})
	if err != nil {
		return nil, err
	}
	var out EnrollmentOutput
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var nodeID, tokenID, state, spiffe, boundServerID, nodeStatus, actualServerID, nodePublicIP string
		var serial int
		var pub, fp, runtimeHash, priorCommit []byte
		var expiry time.Time
		if err := tx.QueryRow(ctx, `SELECT node_id::text,bootstrap_token_id::text FROM node_enrollments
			WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, in.EnrollmentID).Scan(&nodeID, &tokenID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(server_id::text,'') FROM bootstrap_tokens
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, tokenID).Scan(&boundServerID); err != nil {
			return err
		}
		if boundServerID != "" {
			var serverUsable bool
			if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL AND status <> 'retired' FROM servers
				WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, boundServerID).Scan(&serverUsable); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.New(httpx.CodeConflict, "bootstrap token server is unavailable")
				}
				return err
			}
			if !serverUsable {
				return httpx.New(httpx.CodeConflict, "bootstrap token server is unavailable")
			}
		}
		if err := tx.QueryRow(ctx, `SELECT status,coalesce(server_id::text,''),coalesce(public_ipv4::text,'')
				FROM nodes WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, nodeID).
			Scan(&nodeStatus, &actualServerID, &nodePublicIP); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT state,candidate_serial,public_key,fingerprint,runtime_token_hash,
			commit_request_sha256,expires_at FROM node_enrollments WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, in.EnrollmentID).
			Scan(&state, &serial, &pub, &fp, &runtimeHash, &priorCommit, &expiry); err != nil {
			return err
		}
		if state == "committed" {
			if !equalBytes(priorCommit, in.CommitRequestSHA256) {
				return httpx.New(httpx.CodeConflict, "commit evidence differs")
			}
		} else {
			if state != "pending" {
				return httpx.New(httpx.CodeConflict, "enrollment is not committable")
			}
			if nodeStatus != "bootstrapping" {
				return httpx.New(httpx.CodeConflict, "node lifecycle changed before enrollment commit")
			}
			if boundServerID != "" && actualServerID != "" && boundServerID != actualServerID {
				return httpx.New(httpx.CodeConflict, "bootstrap token server does not match target node")
			}
			if boundServerID == "" && actualServerID != "" {
				var serverUsable bool
				if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL AND status <> 'retired' FROM servers
					WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, actualServerID).Scan(&serverUsable); err != nil || !serverUsable {
					return httpx.New(httpx.CodeConflict, "target node server is unavailable")
				}
			}
			spiffe = fmt.Sprintf("spiffe://aegis/tenant/%s/node/%s", tenantID, nodeID)
			if _, err := tx.Exec(ctx, `INSERT INTO node_identities
				(tenant_id,node_id,serial,public_key,spiffe_id,fingerprint,expires_at)
				VALUES ($1,$2::uuid,$3,$4,$5,$6,now()+interval '90 days')`, tenantID, nodeID, serial, pub, spiffe, fp); err != nil {
				return err
			}
			// 签发记录随令牌一起换：接入时发的令牌没有签发人（由安装令牌换得）
			if _, err := tx.Exec(ctx, `UPDATE nodes SET server_token_hash=$3,
				server_token_issued_at=now(), server_token_issued_by=NULL
				WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, nodeID, runtimeHash); err != nil {
				return err
			}
			cmd, err := tx.Exec(ctx, `UPDATE nodes SET status='attesting',row_version=row_version+1
				WHERE tenant_id=$1 AND id=$2::uuid AND status='bootstrapping'`, tenantID, nodeID)
			if err != nil {
				return err
			}
			if cmd.RowsAffected() != 1 {
				return httpx.New(httpx.CodeConflict, "node lifecycle changed before enrollment commit")
			}
			serverID := boundServerID
			if serverID == "" {
				serverID = actualServerID
			}
			if serverID == "" {
				serverID = nodeID
				if _, err := tx.Exec(ctx, `
					INSERT INTO servers (id,tenant_id,name,status,hostname,public_ipv4,agent_version,
					 cpu_cores,memory_mb,disk_gb,region,control_node_id)
					SELECT n.id,n.tenant_id,n.name,'draft',n.hostname,n.public_ipv4,n.agent_version,
					       n.cpu_cores,n.memory_mb,n.disk_gb,$3,NULL
					  FROM nodes n WHERE n.tenant_id=$1 AND n.id=$2::uuid
					ON CONFLICT (id) DO UPDATE SET hostname=excluded.hostname,public_ipv4=excluded.public_ipv4,
					 agent_version=excluded.agent_version,cpu_cores=excluded.cpu_cores,
					 memory_mb=excluded.memory_mb,disk_gb=excluded.disk_gb,
					 region=coalesce(nullif(servers.region,''),excluded.region)`,
					tenantID, nodeID, s.regionForIP(nodePublicIP)); err != nil {
					return err
				}
			}
			cmd, err = tx.Exec(ctx, `UPDATE nodes SET server_id=$3::uuid,row_version=row_version+1
				WHERE tenant_id=$1 AND id=$2::uuid AND server_id IS NULL`, tenantID, nodeID, serverID)
			if err != nil {
				return err
			}
			if cmd.RowsAffected() == 0 && actualServerID != serverID {
				return httpx.New(httpx.CodeConflict, "node is already bound to another server")
			}
			cmd, err = tx.Exec(ctx, `UPDATE servers SET control_node_id=$3::uuid,row_version=row_version+1
				WHERE tenant_id=$1 AND id=$2::uuid AND control_node_id IS NULL`, tenantID, serverID, nodeID)
			if err != nil {
				return err
			}
			// A server may host multiple nodes. Existing control ownership is kept;
			// only an unclaimed server elects the newly enrolled node.
			if _, err := tx.Exec(ctx, `UPDATE node_enrollments SET state='committed',commit_request_sha256=$3,
				commit_evidence=$4::jsonb WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, in.EnrollmentID, in.CommitRequestSHA256, evidenceJSON); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `SELECT id::text,node_id::text,candidate_serial,state,expires_at FROM node_enrollments
			WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, in.EnrollmentID).
			Scan(&out.EnrollmentID, &out.NodeID, &out.Serial, &out.State, &out.ExpiresAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Service) AbortEnrollment(ctx context.Context, tenantID, enrollmentID, reason string) (*EnrollmentOutput, error) {
	if _, err := canonicalUUIDField("enrollment_id", enrollmentID); err != nil {
		return nil, err
	}
	if len(reason) > 500 {
		return nil, httpx.Invalid(map[string]string{"reason": "must be at most 500 characters"})
	}
	var out EnrollmentOutput
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var nodeID, tokenID, state string
		if err := tx.QueryRow(ctx, `SELECT node_id::text,bootstrap_token_id::text,state FROM node_enrollments
			WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, enrollmentID).Scan(&nodeID, &tokenID, &state); err != nil {
			return err
		}
		if state == "pending" {
			if _, err := tx.Exec(ctx, `SELECT 1 FROM bootstrap_tokens WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, tokenID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT 1 FROM nodes WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, nodeID); err != nil {
				return err
			}
			cmd, err := tx.Exec(ctx, `UPDATE node_enrollments SET state='aborted',abort_reason=$3 WHERE tenant_id=$1 AND id=$2::uuid AND state='pending'`, tenantID, enrollmentID, strings.TrimSpace(reason))
			if err != nil {
				return err
			}
			if cmd.RowsAffected() != 1 {
				return httpx.New(httpx.CodeConflict, "enrollment state changed concurrently")
			}
			// Only fail the bootstrap lifecycle when no newer pending attempt owns
			// the node. This CAS prevents a late abort from clobbering a retry.
			if _, err := tx.Exec(ctx, `UPDATE nodes SET status='bootstrap_failed',row_version=row_version+1
				WHERE tenant_id=$1 AND id=$2::uuid AND status='bootstrapping'
				  AND NOT EXISTS (SELECT 1 FROM node_enrollments e WHERE e.tenant_id=$1
				    AND e.node_id=$2::uuid AND e.state='pending' AND e.id<>$3::uuid)`, tenantID, nodeID, enrollmentID); err != nil {
				return err
			}
		} else if state != "aborted" {
			return httpx.New(httpx.CodeConflict, "terminal enrollment cannot be aborted")
		}
		return tx.QueryRow(ctx, `SELECT id::text,node_id::text,candidate_serial,state,expires_at FROM node_enrollments
			WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, enrollmentID).Scan(&out.EnrollmentID, &out.NodeID, &out.Serial, &out.State, &out.ExpiresAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func equalBytes(a, b []byte) bool { return len(a) == len(b) && crypto.ConstantTimeEqual(a, b) }
