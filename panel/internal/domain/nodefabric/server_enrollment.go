package nodefabric

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/bindingcontract"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 服务器接入 S1（合约 §3）：begin → status → commit / abort，照搬节点接入的证据链，
// 换成服务器身份。begin 不签名，由一次性绑定令牌证明身份；之后三步由 begin 里登记的候选
// 公钥按 aegis-server-request-v1 签名（网关验签，见 VerifyServerEnrollmentRequest）。
//
// 「同一面板同一租户只绑一台服务器」的判定在 pdnd 侧（合约 §12.3）：begin 对已绑定的服务器
// 照常受理，pdnd 拿到 server_id 自己判断是不是重复执行、再 abort。面板侧保证的是一台服务器
// 同时只有一个有效身份——别的机器拿同一台服务器 commit 回 409（唯一部分索引兜底）。

// serverEnrollmentTTL 是一次接入从 begin 到 commit 的最长时间，与节点接入一致。
const serverEnrollmentTTL = 15 * time.Minute

// panelServerFeatures 是面板在接入响应里声明的特性（合约 §15）。P1 只实现了 S1，还不声明
// server-binding-v1：那表示 S2–S7 都可用，由 P2 实现后再加。
var panelServerFeatures = []string{}

// maxServerCapabilitiesBytes 是能力表的上限。请求体本身限 1 MiB，能力表正常只有几 KiB。
const maxServerCapabilitiesBytes = 64 << 10

var featureNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,47}$`)

// ServerEnrollmentBeginInput 是 begin 的请求体（合约 §3.1）与网关算出的请求体摘要。
type ServerEnrollmentBeginInput struct {
	Token              string
	RequestID          string
	PublicKey          string
	EncKEM             string
	EncPublicKey       string
	AgentVersion       string
	Features           []string
	Capabilities       json.RawMessage
	Hostname           string
	CPUCores           int
	MemoryMB           int
	DiskGB             int
	BeginRequestSHA256 []byte
}

// ServerEnrollmentCommitInput 是 commit 的证据，字段沿用节点接入（合约 §3.2）。
type ServerEnrollmentCommitInput struct {
	EnrollmentID        string
	CommitRequestSHA256 []byte
	AgentVersion        string
	Architecture        string
	BinarySHA256        string
	ConfigSHA256        string
	UnitSHA256          string
	PreflightSHA256     string
}

// ServerEnrollmentOutput 是 begin / status / commit / abort 的响应（合约 §3.1）。
type ServerEnrollmentOutput struct {
	EnrollmentID    string    `json:"enrollment_id"`
	TenantID        string    `json:"tenant_id"`
	ServerID        string    `json:"server_id"`
	Serial          int       `json:"serial"`
	State           string    `json:"state"`
	ExpiresAt       time.Time `json:"expires_at"`
	ConfigKeyID     string    `json:"config_key_id"`
	ConfigPublicKey string    `json:"config_public_key"`
	Features        []string  `json:"features"`
}

// serverBindingTokenHash 是绑定令牌的存储哈希。哈希域与节点接入令牌（node-bootstrap-v2）不同，
// 两条接入互相查不到对方的令牌。
func serverBindingTokenHash(token string) []byte {
	return crypto.HashToken("aegis-server-binding-token-v1\x00" + token)
}

func decodeServerPublicKey(field, value string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 || base64.StdEncoding.EncodeToString(raw) != value {
		return nil, httpx.Invalid(map[string]string{field: "必须是 32 字节公钥的标准 base64"})
	}
	return raw, nil
}

// validateServerFeatures 要求特性名合规、严格升序、不重复（合约 §15）。
func validateServerFeatures(features []string) error {
	if len(features) > 64 {
		return httpx.Invalid(map[string]string{"features": "最多 64 项"})
	}
	for i, f := range features {
		if !featureNamePattern.MatchString(f) || (i > 0 && f <= features[i-1]) {
			return httpx.Invalid(map[string]string{"features": "特性名格式不正确，或没有严格升序"})
		}
	}
	return nil
}

// normalizeServerCapabilities 接受缺省、null 或 JSON 对象；对象压缩空白后入库。
func normalizeServerCapabilities(raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if len(trimmed) > maxServerCapabilitiesBytes || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, httpx.Invalid(map[string]string{"capabilities": "必须是不超过 64 KiB 的 JSON 对象"})
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return nil, httpx.Invalid(map[string]string{"capabilities": "必须是不超过 64 KiB 的 JSON 对象"})
	}
	return buf.Bytes(), nil
}

func canonicalAgentVersion(v string) error {
	parsed, err := bindingcontract.ParseAgentVersion(v)
	if err != nil || parsed.String() != v {
		return httpx.Invalid(map[string]string{"agent_version": "必须是 vX.Y.Z 规范版本"})
	}
	return nil
}

func (in *ServerEnrollmentBeginInput) validate() (pub, encPub, caps []byte, err error) {
	if _, err = canonicalUUIDField("request_id", in.RequestID); err != nil {
		return nil, nil, nil, err
	}
	if in.Token == "" || len(in.Token) > 256 {
		return nil, nil, nil, httpx.New(httpx.CodeUnauthorized, "绑定令牌无效或已过期")
	}
	if pub, err = decodeServerPublicKey("public_key", in.PublicKey); err != nil {
		return nil, nil, nil, err
	}
	if in.EncKEM != bindingcontract.EncKEM {
		return nil, nil, nil, httpx.Invalid(map[string]string{"enc_kem": "只支持 " + bindingcontract.EncKEM})
	}
	if encPub, err = decodeServerPublicKey("enc_public_key", in.EncPublicKey); err != nil {
		return nil, nil, nil, err
	}
	if err = canonicalAgentVersion(in.AgentVersion); err != nil {
		return nil, nil, nil, err
	}
	if err = validateServerFeatures(in.Features); err != nil {
		return nil, nil, nil, err
	}
	if caps, err = normalizeServerCapabilities(in.Capabilities); err != nil {
		return nil, nil, nil, err
	}
	in.Hostname = strings.TrimSpace(in.Hostname)
	if len(in.Hostname) > 253 || strings.ContainsRune(in.Hostname, '\x00') {
		return nil, nil, nil, httpx.Invalid(map[string]string{"hostname": "最多 253 个字符"})
	}
	if in.CPUCores < 0 || in.MemoryMB < 0 || in.DiskGB < 0 {
		return nil, nil, nil, httpx.Invalid(map[string]string{"host": "资源数不能为负"})
	}
	if len(in.BeginRequestSHA256) != sha256.Size {
		return nil, nil, nil, httpx.Invalid(map[string]string{"request": "缺少请求体摘要"})
	}
	return pub, encPub, caps, nil
}

// BeginServerEnrollment 用绑定令牌占一次使用，登记候选身份。它不建 server_identities：
// 候选公钥只能签本次接入的后续三步，commit 之后才是服务器身份。同一个 request_id 重试
// 得到同一个结果（证据不同则 409）。
func (s *Service) BeginServerEnrollment(ctx context.Context, tenantID string, in ServerEnrollmentBeginInput) (*ServerEnrollmentOutput, error) {
	pub, encPub, caps, err := in.validate()
	if err != nil {
		return nil, err
	}
	features := in.Features
	if features == nil {
		features = []string{}
	}
	fingerprint := sha256.Sum256(pub)
	var out *ServerEnrollmentOutput
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var tokenID, serverID string
		var usedCount, maxUses int16
		var consumedAt *time.Time
		var tokenExpiresAt time.Time
		var tokenUnexpired bool
		if err := tx.QueryRow(ctx, `
			SELECT id::text, server_id::text, used_count, max_uses, consumed_at, expires_at, expires_at > now()
			  FROM bootstrap_tokens
			 WHERE tenant_id=$1 AND token_hash=$2 AND kind='server'
			 FOR UPDATE`, tenantID, serverBindingTokenHash(in.Token)).
			Scan(&tokenID, &serverID, &usedCount, &maxUses, &consumedAt, &tokenExpiresAt, &tokenUnexpired); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.New(httpx.CodeUnauthorized, "绑定令牌无效或已过期")
			}
			return err
		}

		// 重试：同一 request_id 只能对应同一份证据
		var prior struct {
			id, tokenID        string
			begin, pub, encPub []byte
		}
		retryErr := tx.QueryRow(ctx, `
			SELECT id::text, bootstrap_token_id::text, begin_request_sha256, public_key, enc_public_key
			  FROM server_enrollments WHERE tenant_id=$1 AND request_id=$2::uuid`, tenantID, in.RequestID).
			Scan(&prior.id, &prior.tokenID, &prior.begin, &prior.pub, &prior.encPub)
		if retryErr == nil {
			if prior.tokenID != tokenID || !equalBytes(prior.begin, in.BeginRequestSHA256) ||
				!equalBytes(prior.pub, pub) || !equalBytes(prior.encPub, encPub) {
				return httpx.New(httpx.CodeConflict, "request_id 已用于另一份接入证据")
			}
			out, err = readServerEnrollmentTx(ctx, tx, tenantID, prior.id)
			return err
		}
		if !errors.Is(retryErr, pgx.ErrNoRows) {
			return retryErr
		}
		if consumedAt != nil || usedCount >= maxUses || !tokenUnexpired {
			return httpx.New(httpx.CodeUnauthorized, "绑定令牌无效或已过期")
		}
		var usable bool
		if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL AND status <> 'retired' FROM servers
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, serverID).Scan(&usable); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if !usable {
			return httpx.New(httpx.CodeConflict, "绑定令牌对应的服务器已删除或已退役")
		}
		if _, err := tx.Exec(ctx, `UPDATE server_enrollments SET state='expired', abort_reason='deadline elapsed'
			WHERE tenant_id=$1 AND server_id=$2::uuid AND state='pending' AND expires_at <= clock_timestamp()`,
			tenantID, serverID); err != nil {
			return err
		}
		var serial int
		if err := tx.QueryRow(ctx, `
			SELECT greatest(
			  coalesce((SELECT max(serial) FROM server_identities WHERE tenant_id=$1 AND server_id=$2::uuid), 0),
			  coalesce((SELECT max(candidate_serial) FROM server_enrollments WHERE tenant_id=$1 AND server_id=$2::uuid), 0)
			) + 1`, tenantID, serverID).Scan(&serial); err != nil {
			return err
		}
		var enrollmentID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO server_enrollments
			  (tenant_id, server_id, bootstrap_token_id, request_id, begin_request_sha256, candidate_serial,
			   public_key, fingerprint, enc_kem, enc_public_key, agent_version, features, capabilities,
			   hostname, cpu_cores, memory_mb, disk_gb, config_signing_key_id, config_signing_public_key, expires_at)
			VALUES ($1,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14,$15,$16,$17,$18,$19,
			        least($20::timestamptz, now() + make_interval(secs => $21)))
			RETURNING id::text`,
			tenantID, serverID, tokenID, in.RequestID, in.BeginRequestSHA256, serial,
			pub, fingerprint[:], in.EncKEM, encPub, in.AgentVersion, features, jsonbText(caps),
			nullStr(in.Hostname), nullInt(in.CPUCores), nullInt(in.MemoryMB), nullInt(in.DiskGB),
			s.signer.KeyID(), []byte(s.signer.PublicKey()), tokenExpiresAt, serverEnrollmentTTL.Seconds()).
			Scan(&enrollmentID); err != nil {
			if db.IsUniqueViolation(err) {
				return serverEnrollmentConflict(err)
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE bootstrap_tokens SET used_count=used_count+1,
			consumed_at=CASE WHEN used_count+1 >= max_uses THEN now() ELSE NULL END
			WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, tokenID); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "agent", Action: "server.enrollment.begin",
			ResourceType: "server_enrollment", ResourceID: &enrollmentID, APIDomain: "node", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"server_id": serverID, "serial": serial,
				"key_id": bindingcontract.KeyID(pub), "agent_version": in.AgentVersion}}); err != nil {
			return err
		}
		out, err = readServerEnrollmentTx(ctx, tx, tenantID, enrollmentID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// serverEnrollmentConflict 把接入相关的唯一冲突译成中文（原句只进日志）。
func serverEnrollmentConflict(err error) error {
	msg := "服务器接入冲突，请稍后重试"
	switch db.ConstraintName(err) {
	case "uq_server_enrollments_pending_server":
		msg = "这台服务器已有进行中的接入，请等它结束或先中止"
	case "server_enrollments_fingerprint_unique", "server_identities_fingerprint_unique":
		msg = "这把服务器公钥已经登记过，请重新生成密钥"
	case "uq_server_identities_active":
		msg = "这台服务器已绑定到另一台机器；如需换机，请先在面板上解除绑定"
	}
	return httpx.New(httpx.CodeConflict, msg).WithInternal(err)
}

// jsonbText 把 JSON 字节作为文本参数传给 ::jsonb（[]byte 会按 bytea 发送）；空即 NULL。
func jsonbText(raw []byte) *string {
	if len(raw) == 0 {
		return nil
	}
	text := string(raw)
	return &text
}

// readServerEnrollmentTx 读出接入的响应形状。
func readServerEnrollmentTx(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID string) (*ServerEnrollmentOutput, error) {
	out := ServerEnrollmentOutput{Features: panelServerFeatures}
	var configPub []byte
	if err := tx.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, server_id::text, candidate_serial, state, expires_at,
		       config_signing_key_id, config_signing_public_key
		  FROM server_enrollments WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, enrollmentID).
		Scan(&out.EnrollmentID, &out.TenantID, &out.ServerID, &out.Serial, &out.State, &out.ExpiresAt,
			&out.ConfigKeyID, &configPub); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFoundOrForbidden()
		}
		return nil, err
	}
	out.ExpiresAt = out.ExpiresAt.UTC()
	out.ConfigPublicKey = encodeKey(configPub)
	return &out, nil
}

// VerifyServerEnrollmentRequest 验接入 status / commit / abort 的签名（合约 §3.2）：
// 请求头里的 server_id 与 serial 必须就是这次接入的；pending / aborted / expired 用候选公钥验，
// 已 commit 的接入改按有效服务器身份验——身份被吊销后再来问状态一律 401。
func (s *Service) VerifyServerEnrollmentRequest(ctx context.Context, tenantID, enrollmentID, serverID string,
	serial int64, preimage, signature []byte) error {
	if _, err := canonicalUUIDField("enrollment_id", enrollmentID); err != nil {
		return ErrServerIdentityInvalid
	}
	var actualServer, state string
	var candidate int64
	var pub []byte
	err := s.pool.QueryRowScoped(ctx, db.Scope{TenantID: tenantID}, `
		SELECT server_id::text, candidate_serial, public_key, state
		  FROM server_enrollments WHERE tenant_id=$1 AND id=$2::uuid`,
		[]any{tenantID, enrollmentID}, &actualServer, &candidate, &pub, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrServerIdentityInvalid
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrServerAuthUnavailable, err)
	}
	if actualServer != serverID || candidate != serial || len(pub) != ed25519.PublicKeySize {
		return ErrServerIdentityInvalid
	}
	if state == "committed" {
		id, err := s.VerifyServerRequest(ctx, tenantID, serverID, serial, preimage, signature)
		if err != nil {
			return err
		}
		if !equalBytes(id.PublicKey, pub) {
			return ErrServerIdentityInvalid
		}
		return nil
	}
	if !crypto.Verify(ed25519.PublicKey(pub), preimage, signature) {
		return ErrServerSignatureMismatch
	}
	return nil
}

// ServerEnrollmentStatus 读接入状态；pending 且已过期的顺手标成 expired。
func (s *Service) ServerEnrollmentStatus(ctx context.Context, tenantID, enrollmentID string) (*ServerEnrollmentOutput, error) {
	if _, err := canonicalUUIDField("enrollment_id", enrollmentID); err != nil {
		return nil, err
	}
	var out *ServerEnrollmentOutput
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE server_enrollments SET state='expired', abort_reason='deadline elapsed'
			WHERE tenant_id=$1 AND id=$2::uuid AND state='pending' AND expires_at <= clock_timestamp()`,
			tenantID, enrollmentID); err != nil {
			return err
		}
		var err error
		out, err = readServerEnrollmentTx(ctx, tx, tenantID, enrollmentID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CommitServerEnrollment 把候选身份转成服务器身份。锁序：令牌 → 服务器 → 接入（与 begin 一致）。
// 同一份证据重放幂等；这台服务器已有别的有效身份时回 409，绝不顶替。
func (s *Service) CommitServerEnrollment(ctx context.Context, tenantID string, in ServerEnrollmentCommitInput) (*ServerEnrollmentOutput, error) {
	if _, err := canonicalUUIDField("enrollment_id", in.EnrollmentID); err != nil {
		return nil, err
	}
	if len(in.CommitRequestSHA256) != sha256.Size {
		return nil, httpx.Invalid(map[string]string{"request": "缺少请求体摘要"})
	}
	evidence := CommitEnrollmentInput{AgentVersion: in.AgentVersion, Architecture: in.Architecture,
		BinarySHA256: in.BinarySHA256, ConfigSHA256: in.ConfigSHA256, UnitSHA256: in.UnitSHA256,
		PreflightSHA256: in.PreflightSHA256}
	// 产物核对沿用节点接入：随包发布的摘要与版本（比合约 §13.6 的放宽口径更严，缺省 fail closed）
	if err := validateEnrollmentEvidence(evidence, s.release); err != nil {
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
	var out *ServerEnrollmentOutput
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var serverID, tokenID string
		if err := tx.QueryRow(ctx, `SELECT server_id::text, bootstrap_token_id::text FROM server_enrollments
			WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, in.EnrollmentID).Scan(&serverID, &tokenID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM bootstrap_tokens WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`,
			tenantID, tokenID); err != nil {
			return err
		}
		var usable bool
		if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL AND status <> 'retired' FROM servers
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, serverID).Scan(&usable); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var e struct {
			state, agentVersion, encKEM   string
			serial                        int
			pub, fp, encPub, caps, commit []byte
			features                      []string
			hostname                      *string
			cpu, mem, disk                *int
			expired                       bool
		}
		if err := tx.QueryRow(ctx, `
			SELECT state, candidate_serial, public_key, fingerprint, enc_kem, enc_public_key, agent_version,
			       features, capabilities, hostname, cpu_cores, memory_mb, disk_gb, commit_request_sha256,
			       expires_at <= clock_timestamp()
			  FROM server_enrollments WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, in.EnrollmentID).
			Scan(&e.state, &e.serial, &e.pub, &e.fp, &e.encKEM, &e.encPub, &e.agentVersion, &e.features, &e.caps,
				&e.hostname, &e.cpu, &e.mem, &e.disk, &e.commit, &e.expired); err != nil {
			return err
		}
		switch {
		case e.state == "committed":
			if !equalBytes(e.commit, in.CommitRequestSHA256) {
				return httpx.New(httpx.CodeConflict, "这次接入已用另一份证据提交过")
			}
			var err error
			out, err = readServerEnrollmentTx(ctx, tx, tenantID, in.EnrollmentID)
			return err
		case e.state != "pending":
			return httpx.New(httpx.CodeConflict, "接入已结束，不能再提交")
		case e.expired:
			return httpx.New(httpx.CodeConflict, "接入已超时，请重新执行绑定命令")
		case !usable:
			return httpx.New(httpx.CodeConflict, "服务器已删除或已退役")
		case in.AgentVersion != e.agentVersion:
			return httpx.Invalid(map[string]string{"agent_version": "与发起接入时上报的版本不一致"})
		}
		if e.features == nil {
			e.features = []string{}
		}
		var bound bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM server_identities
			WHERE tenant_id=$1 AND server_id=$2::uuid AND status='active')`, tenantID, serverID).Scan(&bound); err != nil {
			return err
		}
		if bound {
			return httpx.New(httpx.CodeConflict, "这台服务器已绑定到另一台机器；如需换机，请先在面板上解除绑定")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO server_identities
			  (tenant_id, server_id, serial, public_key, fingerprint, enc_kem, enc_public_key,
			   agent_version, features, capabilities, enrollment_id)
			VALUES ($1,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::uuid)`,
			tenantID, serverID, e.serial, e.pub, e.fp, e.encKEM, e.encPub, e.agentVersion, e.features,
			jsonbText(e.caps), in.EnrollmentID); err != nil {
			if db.IsUniqueViolation(err) {
				return serverEnrollmentConflict(err)
			}
			return err
		}
		// 只补机器上报的资产，名字与状态归管理员；地区不动（没有公网 IP 上报）
		if _, err := tx.Exec(ctx, `
			UPDATE servers SET hostname=coalesce($3,hostname), agent_version=$4,
			       cpu_cores=coalesce($5,cpu_cores), memory_mb=coalesce($6,memory_mb),
			       disk_gb=coalesce($7,disk_gb), row_version=row_version+1
			 WHERE tenant_id=$1 AND id=$2::uuid`,
			tenantID, serverID, e.hostname, e.agentVersion, e.cpu, e.mem, e.disk); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE server_enrollments SET state='committed', commit_request_sha256=$3,
			commit_evidence=$4::jsonb WHERE tenant_id=$1 AND id=$2::uuid`,
			tenantID, in.EnrollmentID, in.CommitRequestSHA256, evidenceJSON); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "agent", Action: "server.enrollment.commit",
			ResourceType: "server", ResourceID: &serverID, APIDomain: "node", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"enrollment_id": in.EnrollmentID, "serial": e.serial,
				"key_id": bindingcontract.KeyID(e.pub), "agent_version": e.agentVersion,
				"architecture": in.Architecture}}); err != nil {
			return err
		}
		var err error
		out, err = readServerEnrollmentTx(ctx, tx, tenantID, in.EnrollmentID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AbortServerEnrollment 中止一次进行中的接入。重复中止幂等；已提交或已过期的回 409。
// 令牌不退回：绑定令牌只能用一次，重来要在面板上重新生成命令。
func (s *Service) AbortServerEnrollment(ctx context.Context, tenantID, enrollmentID, reason string) (*ServerEnrollmentOutput, error) {
	if _, err := canonicalUUIDField("enrollment_id", enrollmentID); err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > 500 {
		return nil, httpx.Invalid(map[string]string{"reason": "最多 500 个字符"})
	}
	var out *ServerEnrollmentOutput
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM server_enrollments
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, enrollmentID).Scan(&state); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		switch state {
		case "pending":
			if _, err := tx.Exec(ctx, `UPDATE server_enrollments SET state='aborted', abort_reason=$3
				WHERE tenant_id=$1 AND id=$2::uuid AND state='pending'`, tenantID, enrollmentID, reason); err != nil {
				return err
			}
			if err := audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "agent", Action: "server.enrollment.abort",
				ResourceType: "server_enrollment", ResourceID: &enrollmentID, APIDomain: "node", Outcome: "success",
				RequestID: httpx.RequestIDFrom(ctx), AfterDigest: map[string]any{"reason": reason}}); err != nil {
				return err
			}
		case "aborted":
		default:
			return httpx.New(httpx.CodeConflict, "接入已结束，不能中止")
		}
		var err error
		out, err = readServerEnrollmentTx(ctx, tx, tenantID, enrollmentID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
