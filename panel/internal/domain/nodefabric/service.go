// [INPUT]: 依赖 platform 的 crypto/db/httpx/audit/realtime/geoip
// [OUTPUT]: 对外提供 Service、NewService，bootstrap 令牌与接入、身份、签名请求、心跳、配置签发与回报、指标
// [POS]: domain/nodefabric 的主服务：Agent 接入与配置下发；uniproxy.go、node_admin.go、enrollment.go 等同包文件扩展它
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package nodefabric 实现节点接入与配置下发（PRD 第 8–9 章）。
//
// 认证方案的取舍：AGT-001 要求 Agent 主动建立 mTLS 长连接。首版改用
// 「Agent 主动轮询 + 每请求 Ed25519 签名」，理由是它同样满足那条要求的实质 ——
// 节点不开放任何监听端口、连接一律由 Agent 发起 —— 却不需要先立起一套证书
// 签发与吊销基础设施。node_identities 表已按证书模型建好（serial 单调、可吊销），
// 将来换成 mTLS 时身份模型不用动，只换传输层。
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
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/geoip"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

type Service struct {
	pool           *db.Pool
	signer         *crypto.Signer // 给节点配置签名（AGT-007）
	previousSigner *crypto.Signer
	// stream 是节点事件流的注册表，可为 nil（没启用推送时）。
	stream       *StreamHub
	realtime     *realtime.Hub
	watchMu      sync.Mutex
	nodeWatchers map[string]context.CancelFunc
	// geoIP 把节点上报的公网 IP 翻成地区（省/州），Agent 首次接入时
	// 自动填 servers.region，管理员不必手抄。可以为 nil：数据文件
	// 缺失时地区留空，接入本身照常。
	geoIP *geoip.Resolver
}

func NewService(pool *db.Pool, signer *crypto.Signer) *Service {
	return &Service{pool: pool, signer: signer, nodeWatchers: make(map[string]context.CancelFunc)}
}

// SetGeoIP 注入 IP 归属地库（可空，缺库时自动识别地区静默降级为空）。
func (s *Service) SetGeoIP(r *geoip.Resolver) {
	s.geoIP = r
}

// regionForIP 用 ip2region 把公网 IP 翻成地区文本（如「日本 东京都 东京 亚马逊」）。
// 只取国家+省份+城市三段，运营商那截对线路名没意义。空 IP 或没配库返回空串。
func (s *Service) regionForIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" || s.geoIP == nil {
		return ""
	}
	loc := s.geoIP.Lookup(ip)
	segs := make([]string, 0, 3)
	for _, v := range []string{loc.Country, loc.Region, loc.City} {
		if v != "" {
			segs = append(segs, v)
		}
	}
	return strings.Join(segs, " ")
}

// SetPreviousConfigSigner enables a bounded old-to-new trust transition. The
// previous key only certifies the current public key and never signs configs.
func (s *Service) SetPreviousConfigSigner(previous *crypto.Signer) error {
	if previous != nil && previous.KeyID() == s.signer.KeyID() {
		return errors.New("previous config signer must differ from current signer")
	}
	s.previousSigner = previous
	return nil
}

//------------------------------------------------------------------------------
// NODE-008 一次性 Bootstrap Token
//------------------------------------------------------------------------------

type IssueTokenInput struct {
	ActorID  string
	NodeName string
	PoolID   string
	// ServerID 把这张令牌绑到一台已经建好的服务器上。Agent 拿它接入时
	// 会挂到那台，而不是另建一条——「先在面板建服务器，再让机器接进来」
	// 这条路要能走通，靠的就是这个字段。留空则维持原来的行为。
	ServerID string
	// PanelURL 是给机器回连用的面板地址，由调用方从请求推导。
	PanelURL   string
	TTLMinutes int
}

type IssueTokenOutput struct {
	Token     string    `json:"token"` // 明文只在此处出现一次
	ExpiresAt time.Time `json:"expires_at"`
	// InstallCommand 是直接贴到机器上执行的接入命令。
	//
	// 由服务端拼而不是前端：内核换代（面板自带的节点代理已换成 pdnd 的两阶段接入）时命令形态会变，
	// 前端硬编码的话，页面、文档、部署脚本要各改一遍，漏掉哪个就会有人
	// 照着过时的命令装了个装不上的东西。放在这里，改一处配置就够了。
	InstallCommand string `json:"install_command"`
}

// 接入命令模板。占位符在生成时替换，不参与 shell 解析。
//
// {{PANEL}} 面板地址 / {{TOKEN}} 引导令牌 / {{NAME}} 节点名
const (
	settingInstallTemplate = "node.install_command_template"
	defaultInstallTemplate = "umask 077; printf 'Bootstrap token: ' >&2; IFS= read -r PANDORA_BOOTSTRAP_TOKEN </dev/tty; " +
		"PANDORA_TOKEN_FILE=$(mktemp); trap 'PANDORA_RC=$?; rm -f \"$PANDORA_TOKEN_FILE\"; exit \"$PANDORA_RC\"' EXIT; " +
		"printf '%s' \"$PANDORA_BOOTSTRAP_TOKEN\" >\"$PANDORA_TOKEN_FILE\"; " +
		"unset PANDORA_BOOTSTRAP_TOKEN; curl -fsSL {{PANEL}}/pdnd/install.sh | sh -s -- " +
		"--token-file \"$PANDORA_TOKEN_FILE\" --name {{NAME}}; PANDORA_RC=$?; rm -f \"$PANDORA_TOKEN_FILE\"; exit $PANDORA_RC"
)

// shellQuote 用单引号包裹并转义内部单引号。
//
// 令牌是随机 base64，节点名是管理员起的，两者都可能含有 shell 会解释的
// 字符。不引起来的话，一个带空格的服务器名就会让命令悄悄跑偏。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// safePanelURL 匹配「不需要 shell 引用」的面板地址。
//
// 字符集限死在 scheme://host[:port] 用得到的那些上，没有空格、引号、
// 反引号、$ 这类会被 shell 解释的东西。
var safePanelURL = regexp.MustCompile(`^https?://[A-Za-z0-9._~:\[\]-]+$`)

func renderInstallCommand(tpl, panelURL, token, nodeName string) string {
	if strings.TrimSpace(tpl) == "" {
		tpl = defaultInstallTemplate
	}
	// 面板地址通常会被拼进更长的 URL（{{PANEL}}/pdnd/install.sh）。
	// 无脑加引号的话会渲染成 'https://x'/pdnd/install.sh —— shell 解析
	// 得对，但复制出来像是坏的，会有人手动去掉引号顺手改错别的地方。
	panel := panelURL
	if !safePanelURL.MatchString(panel) {
		panel = shellQuote(panel)
	}
	r := strings.NewReplacer(
		"{{PANEL}}", panel,
		// 令牌是随机 base64，节点名是管理员起的，两者都可能带上 shell
		// 会解释的字符，始终引起来。
		"{{TOKEN}}", shellQuote(token),
		"{{NAME}}", shellQuote(nodeName),
	)
	return r.Replace(tpl)
}

// RenderLegacyInstallCommand keeps the compatibility UniProxy token out of
// argv and shell history. The operator pastes it into stdin, where the
// generated script immediately moves it into a 0600 temporary file and passes
// only that file path to the installer.
func RenderLegacyInstallCommand(panelURL, nodeID, nodeType string) string {
	panel := panelURL
	if !safePanelURL.MatchString(panel) {
		panel = shellQuote(panel)
	}
	return "umask 077; printf 'UniProxy runtime token: ' >&2; IFS= read -r PANDORA_RUNTIME_TOKEN </dev/tty; " +
		"PANDORA_TOKEN_FILE=$(mktemp); trap 'PANDORA_RC=$?; rm -f \"$PANDORA_TOKEN_FILE\"; exit \"$PANDORA_RC\"' EXIT; " +
		"printf '%s' \"$PANDORA_RUNTIME_TOKEN\" >\"$PANDORA_TOKEN_FILE\"; " +
		"unset PANDORA_RUNTIME_TOKEN; curl -fsSL " + panel + "/pdnd/install.sh | sh -s -- " +
		"--node-id " + shellQuote(nodeID) + " --node-type " + shellQuote(nodeType) +
		" --token-file \"$PANDORA_TOKEN_FILE\"; PANDORA_RC=$?; " +
		"rm -f \"$PANDORA_TOKEN_FILE\"; exit $PANDORA_RC"
}

func normalizeBootstrapNodeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsRune(name, '\x00') || len([]rune(name)) > 120 {
		return "", httpx.Invalid(map[string]string{"node_name": "必填且最多 120 个字符"})
	}
	return name, nil
}

// bootstrapTokenHash binds the bearer secret to its canonical target-name slot
// without adding a schema dependency in the pre-00048 compatibility window.
// Existing targets are additionally pinned through bootstrap_tokens.node_id.
// A token presented with any other node_name is indistinguishable from an
// invalid token and therefore cannot rotate another Node's active identity.
func bootstrapTokenHash(token, nodeName string) []byte {
	return crypto.HashToken("node-bootstrap-v2\x00" + nodeName + "\x00" + token)
}

func (s *Service) IssueBootstrapToken(ctx context.Context, tenantID string, in IssueTokenInput) (*IssueTokenOutput, error) {
	nodeName, err := normalizeBootstrapNodeName(in.NodeName)
	if err != nil {
		return nil, err
	}
	in.NodeName = nodeName
	in.PoolID = strings.TrimSpace(in.PoolID)
	if err := validateAdminUUID("pool_id", in.PoolID, false); err != nil {
		return nil, err
	}
	in.ServerID = strings.TrimSpace(in.ServerID)
	if err := validateAdminUUID("server_id", in.ServerID, false); err != nil {
		return nil, err
	}
	if in.TTLMinutes <= 0 || in.TTLMinutes > 30 {
		in.TTLMinutes = 20 // NODE-008 要求 10–30 分钟
	}
	tok, err := crypto.NewToken(32)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	exp := time.Now().Add(time.Duration(in.TTLMinutes) * time.Minute)
	installTpl := defaultInstallTemplate

	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		// 顺带把接入命令模板读出来。没配就用默认——读不到不是错误，
		// 大多数部署根本不需要改这个模板。
		var stored string
		if err := tx.QueryRow(ctx,
			`SELECT value #>> '{}' FROM system_settings WHERE tenant_id=$1 AND key=$2`,
			tenantID, settingInstallTemplate).Scan(&stored); err == nil &&
			strings.TrimSpace(stored) != "" {
			installTpl = stored
		}
		effectivePoolID := in.PoolID
		var poolID *string
		var targetNodeID *string
		var existingNodeID, nodeStatus, servingStatus, actualPoolID string
		var silencedByDelete bool
		lookupErr := tx.QueryRow(ctx, `SELECT id::text,status,serving_status,coalesce(pool_id::text,''),
			silenced_by_server_delete FROM nodes
			WHERE tenant_id=$1 AND name=$2 FOR SHARE`, tenantID, in.NodeName).
			Scan(&existingNodeID, &nodeStatus, &servingStatus, &actualPoolID, &silencedByDelete)
		if lookupErr == nil {
			// 只有「服务器删除级联静默」（silenced_by_server_delete=true）
			// 才允许同名重装覆盖；手工退役（serving_status='retired' 但标记
			// 为 false）仍是退役终态，拒绝签发，避免绕过退役意图。
			if nodeStatus == "retired" || nodeStatus == "destroyed" || servingStatus == "retired" {
				if !silencedByDelete {
					return httpx.New(httpx.CodeConflict, "已退役或销毁节点不能签发引导令牌")
				}
			}
			if in.PoolID != "" && in.PoolID != actualPoolID {
				return httpx.New(httpx.CodeConflict, "引导令牌资源池与目标节点不匹配")
			}
			effectivePoolID = actualPoolID
			targetNodeID = &existingNodeID
		} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return lookupErr
		}
		if err := validatePool(ctx, tx, tenantID, effectivePoolID); err != nil {
			return err
		}
		if effectivePoolID != "" {
			poolID = &effectivePoolID
		}
		// 绑定的服务器必须真实存在、未删除、且未退役。校验放在签发这一侧：
		// 令牌发出去之后再发现服务器状态不对，机器那边只会得到一个
		// 语焉不详的接入失败。
		var serverID *string
		if in.ServerID != "" {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT true FROM servers
				 WHERE tenant_id=$1 AND id=$2::uuid AND deleted_at IS NULL AND status <> 'retired'`,
				tenantID, in.ServerID).Scan(&exists); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.New(httpx.CodeNotFound, "服务器不存在、已删除或已退役")
				}
				return err
			}
			serverID = &in.ServerID
		}
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO bootstrap_tokens
			  (tenant_id, token_hash, pool_id, node_id, server_id, expires_at, created_by)
			VALUES ($1,$2,$3,$4,$5::uuid,$6,$7) RETURNING id`,
			tenantID, bootstrapTokenHash(tok, in.NodeName), poolID, targetNodeID,
			serverID, exp, in.ActorID).Scan(&id); err != nil {
			return err
		}
		auditNodeID := ""
		if targetNodeID != nil {
			auditNodeID = *targetNodeID
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.bootstrap_token.issue", ResourceType: "bootstrap_token",
			ResourceID: &id, APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"ttl_minutes": in.TTLMinutes, "node_name": in.NodeName,
				"pool_id": effectivePoolID, "node_id": auditNodeID},
		})
	})
	if err != nil {
		return nil, err
	}
	return &IssueTokenOutput{
		Token:          tok,
		ExpiresAt:      exp,
		InstallCommand: renderInstallCommand(installTpl, in.PanelURL, tok, in.NodeName),
	}, nil
}

type BootstrapInput struct {
	Token     string `json:"token"`
	NodeName  string `json:"node_name"`
	PublicKey string `json:"public_key"` // base64，Agent 本地生成，私钥永不离开节点
	AgentVer  string `json:"agent_version"`
	Hostname  string `json:"hostname"`
	CPUCores  int    `json:"cpu_cores"`
	MemoryMB  int    `json:"memory_mb"`
	DiskGB    int    `json:"disk_gb"`
	PublicIP  string `json:"public_ipv4"`
}

type BootstrapOutput struct {
	NodeID       string `json:"node_id"`
	Serial       int    `json:"serial"`
	SpiffeID     string `json:"spiffe_id"`
	RuntimeToken string `json:"runtime_token"`
	// ConfigPublicKey 让 Agent 能独立验证下发配置的签名（AGT-007）
	ConfigPublicKey string `json:"config_public_key"`
	ConfigKeyID     string `json:"config_key_id"`
}

// Bootstrap 用一次性令牌换取节点身份。
//
// 令牌消费与身份签发在同一事务里完成：中途失败则令牌不算用掉，
// 否则一次网络抖动就会让这台机器永远无法接入。
func (s *Service) Bootstrap(ctx context.Context, tenantID string, in BootstrapInput) (*BootstrapOutput, error) {
	runtimeToken, err := crypto.NewToken(24)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.PublicKey))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, httpx.Invalid(map[string]string{"public_key": "必须是 base64 编码的 Ed25519 公钥"})
	}
	nodeName, err := normalizeBootstrapNodeName(in.NodeName)
	if err != nil {
		return nil, err
	}
	in.NodeName = nodeName

	var out BootstrapOutput
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// Node creation must share the publication lock domain. If publication wins
		// first, the new/re-enrolled node is materialized to the published maximum
		// below; if bootstrap wins first, publication sees and updates the node.
		if err := lockLegacyConfigRelease(ctx, tx, tenantID); err != nil {
			return err
		}
		var (
			btID, boundNodeID string
			boundServerID     string
			poolID            *string
			maxU, usedU       int16
		)
		err := tx.QueryRow(ctx, `
			SELECT id, pool_id, coalesce(node_id::text,''), coalesce(server_id::text,''),
			       max_uses, used_count FROM bootstrap_tokens
			 WHERE tenant_id = $1 AND token_hash = $2
			   AND expires_at > now() AND consumed_at IS NULL
			 FOR UPDATE`,
			tenantID, bootstrapTokenHash(in.Token, in.NodeName)).
			Scan(&btID, &poolID, &boundNodeID, &boundServerID, &maxU, &usedU)
		if errors.Is(err, pgx.ErrNoRows) {
			// 过期、已用、不存在返回同一种错误：不给探测令牌状态的机会
			return httpx.New(httpx.CodeUnauthorized, "引导令牌无效或已过期")
		}
		if err != nil {
			return err
		}

		// 节点名在租户内唯一；同名视为重装，复用原节点并轮换身份
		var nodeID string
		var prevSerial int
		var nodeStatus, servingStatus, actualPoolID string
		var silencedByDelete bool
		err = tx.QueryRow(ctx, `
			SELECT id,status,serving_status,coalesce(pool_id::text,''),silenced_by_server_delete
			  FROM nodes
			 WHERE tenant_id = $1 AND name = $2
			 FOR UPDATE`, tenantID, in.NodeName).Scan(&nodeID, &nodeStatus, &servingStatus, &actualPoolID, &silencedByDelete)

		if errors.Is(err, pgx.ErrNoRows) {
			if boundNodeID != "" {
				return httpx.New(httpx.CodeConflict, "引导令牌绑定的节点已不存在或已改名")
			}
			if poolID != nil {
				if err := validatePool(ctx, tx, tenantID, *poolID); err != nil {
					return err
				}
			}
			if err := tx.QueryRow(ctx, `
				INSERT INTO nodes (tenant_id, name, pool_id, status, agent_version,
				                   hostname, cpu_cores, memory_mb, disk_gb, public_ipv4)
				VALUES ($1,$2,$3,'bootstrapping',$4,$5,$6,$7,$8,$9::inet)
				RETURNING id`,
				tenantID, in.NodeName, poolID, in.AgentVer, nullStr(in.Hostname),
				nullInt(in.CPUCores), nullInt(in.MemoryMB), nullInt(in.DiskGB),
				nullStr(in.PublicIP)).Scan(&nodeID); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if boundNodeID == "" || boundNodeID != nodeID {
				return httpx.New(httpx.CodeConflict, "引导令牌与目标节点不匹配")
			}
			// 服务器删除会级联把名下节点 serving_status 置 retired（探针静默），
			// 并以 silenced_by_server_delete=true 标记。同名节点重新接入视为
			// 「覆盖重装」：允许从静默态复活，而不是报「已退役或销毁不能重新
			// 引导」。手工退役（serving_status='retired' 但标记为 false）仍拒绝。
			if servingStatus == "retired" {
				if !silencedByDelete {
					return httpx.New(httpx.CodeConflict, "已退役或销毁节点不能重新引导")
				}
				// 只把 serving_status 从 retired 拨回 active（AGT-004 身份
				// 查询恢复放行），并清除静默标记；lifecycle status 保持原值——
				// active 改 bootstrapping 会被 NODE-010 状态机拒绝。
				if _, err := tx.Exec(ctx, `
					UPDATE nodes SET serving_status='active', silenced_by_server_delete=false,
					       entered_status_at=now(), row_version=row_version+1
					 WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, nodeID); err != nil {
					return err
				}
			} else if nodeStatus == "retired" || nodeStatus == "destroyed" {
				return httpx.New(httpx.CodeConflict, "已退役或销毁节点不能重新引导")
			}
			// The legacy one-phase bootstrap must not race a pending two-phase
			// enrollment. Both paths lock the node row, so this check makes the
			// protocol choice deterministic before legacy bootstrap can revoke the
			// active identity, claim the next serial, or replace the runtime token.
			var pendingEnrollment bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM node_enrollments
					 WHERE tenant_id=$1 AND node_id=$2::uuid AND state='pending'
				)`, tenantID, nodeID).Scan(&pendingEnrollment); err != nil {
				return err
			}
			if pendingEnrollment {
				return httpx.New(httpx.CodeConflict, "node has a pending enrollment; complete or abort it before legacy bootstrap")
			}
			tokenPoolID := ""
			if poolID != nil {
				tokenPoolID = *poolID
			}
			if tokenPoolID != actualPoolID {
				return httpx.New(httpx.CodeConflict, "引导令牌资源池与目标节点不匹配")
			}
			if err := validatePool(ctx, tx, tenantID, actualPoolID); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `
				SELECT coalesce(max(serial), 0)
				  FROM node_identities
				 WHERE tenant_id=$1 AND node_id=$2`, tenantID, nodeID).Scan(&prevSerial); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE node_identities SET status='revoked', revoked_at=now(),
				       revoked_reason='节点重新引导'
				 WHERE node_id=$1 AND status='active'`, nodeID); err != nil {
				return err
			}
			// 只更新资产，不动 status：身份轮换与生命周期是两件事。
			// 强行把 active 节点设回 bootstrapping 会被状态机拒绝（那本就不是合法转换），
			// 而重装一台在跑的机器并不意味着它要退回待验证阶段 ——
			// 是否需要重新灰度由管理员决定。
			if _, err := tx.Exec(ctx, `
				UPDATE nodes SET agent_version=$2, hostname=$3, cpu_cores=$4,
				       memory_mb=$5, disk_gb=$6, public_ipv4=$7::inet
				 WHERE id=$1`,
				nodeID, in.AgentVer, nullStr(in.Hostname), nullInt(in.CPUCores),
				nullInt(in.MemoryMB), nullInt(in.DiskGB), nullStr(in.PublicIP)); err != nil {
				return err
			}
		}

		// 这台 Node 归属哪台 Server。
		//
		// 令牌绑了服务器就用那台——管理员先在面板上建好记录、再把机器接
		// 进来，两者是同一条。没绑就沿用兼容期的老规矩：拿 nodeID 当
		// server id 现建一条。
		serverID := nodeID
		region := s.regionForIP(in.PublicIP)
		if boundServerID != "" {
			serverID = boundServerID
			// 只补 Agent 上报的资产，不动 name / status：名字是管理员
			// 在面板上起的，状态由生命周期管，机器接入不该把它们改掉。
			// region 只在为空时用 ip2region 自动补：管理员手填过的地区不能
			// 被机器换 IP 时冲掉。
			if _, err := tx.Exec(ctx, `
				UPDATE servers SET hostname=coalesce($3,hostname),
				       public_ipv4=coalesce($4::inet,public_ipv4),
				       agent_version=$5,
				       cpu_cores=coalesce($6,cpu_cores),
				       memory_mb=coalesce($7,memory_mb),
				       disk_gb=coalesce($8,disk_gb),
				       region=coalesce(nullif(region,''),$9),
				       row_version=row_version+1
				 WHERE tenant_id=$1 AND id=$2`,
				tenantID, serverID, nullStr(in.Hostname), nullStr(in.PublicIP),
				in.AgentVer, nullInt(in.CPUCores), nullInt(in.MemoryMB),
				nullInt(in.DiskGB), region); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `
			INSERT INTO servers
			  (id, tenant_id, name, status, hostname, public_ipv4, agent_version,
			   cpu_cores, memory_mb, disk_gb, region, control_node_id)
			VALUES ($1,$2,$3,'draft',$4,$5::inet,$6,$7,$8,$9,$10,NULL)
			ON CONFLICT (id) DO UPDATE SET
			  hostname=EXCLUDED.hostname,
			  public_ipv4=EXCLUDED.public_ipv4,
			  agent_version=EXCLUDED.agent_version,
			  cpu_cores=EXCLUDED.cpu_cores,
			  memory_mb=EXCLUDED.memory_mb,
			  disk_gb=EXCLUDED.disk_gb,
			  -- 必须写表名：DO UPDATE 里 EXCLUDED 也有 region，裸写会报
			  -- 42702 ambiguous，整条 bootstrap 失败（enrollment.go 同一句早已写对）
			  region=coalesce(nullif(servers.region,''),EXCLUDED.region)`,
			nodeID, tenantID, in.NodeName, nullStr(in.Hostname), nullStr(in.PublicIP),
			in.AgentVer, nullInt(in.CPUCores), nullInt(in.MemoryMB), nullInt(in.DiskGB), region); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE nodes SET server_id=$3::uuid, row_version=row_version+1
			 WHERE tenant_id=$1 AND id=$2 AND server_id IS NULL`,
			tenantID, nodeID, serverID); err != nil {
			return err
		}
		// Bind the control Node only after the Node belongs to the Server. This
		// ordering satisfies the membership FK without relying solely on its
		// deferred check and keeps legacy Agent node_id compatibility intact.
		//
		// 绑定服务器的场景下只在还没有控制节点时认领：一台服务器可以承载
		// 多个节点，第二个接进来的不该把控制权抢走。
		if _, err := tx.Exec(ctx, `
			UPDATE servers SET control_node_id=$3, row_version=row_version+1
			 WHERE tenant_id=$1 AND id=$2::uuid AND control_node_id IS DISTINCT FROM $3
			   AND ($4 OR control_node_id IS NULL)`,
			tenantID, serverID, nodeID, boundServerID == ""); err != nil {
			return err
		}
		if err := syncLegacyDesiredConfigVersion(ctx, tx, tenantID, nodeID); err != nil {
			return err
		}

		serial := prevSerial + 1
		spiffe := fmt.Sprintf("spiffe://aegis/tenant/%s/node/%s", tenantID, nodeID)
		fp := sha256.Sum256(pub)

		if _, err := tx.Exec(ctx, `
			INSERT INTO node_identities
				(tenant_id, node_id, serial, public_key, spiffe_id, fingerprint, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6, now() + interval '90 days')`,
			tenantID, nodeID, serial, pub, spiffe, fp[:]); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE bootstrap_tokens
			   SET used_count = used_count + 1, node_id = $2,
			       consumed_at = CASE WHEN used_count + 1 >= max_uses THEN now() ELSE NULL END
			 WHERE id = $1`, btID, nodeID); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE nodes SET server_token_hash=$3,
			       server_token_issued_at=now(), server_token_issued_by=NULL
			 WHERE tenant_id=$1 AND id=$2`, tenantID, nodeID, crypto.HashToken(runtimeToken)); err != nil {
			return err
		}

		out = BootstrapOutput{NodeID: nodeID, Serial: serial, SpiffeID: spiffe,
			RuntimeToken: runtimeToken}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "node", Action: "node.bootstrap", ResourceType: "node",
			ResourceID: &nodeID, APIDomain: "node", Outcome: "success",
			RequestID:   httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"serial": serial, "name": in.NodeName},
		})
	})
	if err != nil {
		return nil, err
	}
	out.ConfigPublicKey = base64.StdEncoding.EncodeToString(s.signer.PublicKey())
	out.ConfigKeyID = s.signer.KeyID()
	return &out, nil
}

//------------------------------------------------------------------------------
// 身份校验（NODE-014：吊销后旧身份立即失效）
//------------------------------------------------------------------------------

type Identity struct {
	NodeID    string
	Serial    int
	PublicKey ed25519.PublicKey
	Status    string
}

// LookupIdentity 取节点当前有效身份。只返回 active 的那一份 ——
// 吊销、过期、被更高 serial 取代的身份一律查不到，旧 Agent 立刻失去访问。
func (s *Service) LookupIdentity(ctx context.Context, tenantID, nodeID string) (*Identity, error) {
	var id Identity
	var pub []byte
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT i.node_id, i.serial, i.public_key, i.status
			  FROM node_identities i
			  JOIN nodes n ON n.tenant_id=i.tenant_id AND n.id=i.node_id
			 WHERE i.tenant_id=$1 AND i.node_id=$2 AND i.status='active' AND i.expires_at > now()
			   AND n.status NOT IN ('destroyed','retired') AND n.serving_status<>'retired'`,
			tenantID, nodeID).Scan(&id.NodeID, &id.Serial, &pub, &id.Status)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.New(httpx.CodeUnauthorized, "节点身份无效")
	}
	if err != nil {
		return nil, err
	}
	id.PublicKey = pub
	return &id, nil
}

// CanonicalPayload 构造待签名串。Agent 与服务端必须用完全一致的规则，
// 任何一方多一个分隔符都会导致全量验签失败，所以这个函数是唯一来源。
func CanonicalPayload(method, path, nodeID, ts string, bodyHash []byte) []byte {
	return []byte(method + "\n" + path + "\n" + nodeID + "\n" + ts + "\n" +
		base64.StdEncoding.EncodeToString(bodyHash))
}

const nodeRequestSignatureDomainV2 = "PANDORA-NODE-REQUEST-V2"

// SignedRequestAcceptanceWindow is shared by timestamp validation and nonce
// retention. A nonce must remain claimed until its signature can no longer be
// accepted, including requests whose clocks are ahead of the server.
const SignedRequestAcceptanceWindow = 5 * time.Minute

// DecodeNodeRequestNonce accepts only the canonical raw URL-base64 encoding
// of a 128-bit nonce.
func DecodeNodeRequestNonce(encoded string) ([]byte, error) {
	if len(encoded) != 22 {
		return nil, fmt.Errorf("node request nonce must be 22 characters")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 16 || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return nil, fmt.Errorf("invalid node request nonce")
	}
	return raw, nil
}

// CanonicalPayloadV2 binds the nonce and separates this signature protocol
// from both legacy V1 and unrelated Ed25519 uses.
func CanonicalPayloadV2(method, path, nodeID, ts, nonce string, bodyHash []byte) []byte {
	return []byte(nodeRequestSignatureDomainV2 + "\n" + method + "\n" + path + "\n" + nodeID + "\n" + ts + "\n" + nonce + "\n" +
		base64.StdEncoding.EncodeToString(bodyHash))
}

// ClaimSignedRequest atomically consumes a signed request nonce. Handler
// failures deliberately do not release it; a retry must use a fresh nonce.
func (s *Service) ClaimSignedRequest(ctx context.Context, tenantID, nodeID string, nonce, fingerprint []byte, requestTS time.Time) error {
	if len(nonce) != 16 || len(fingerprint) != sha256.Size {
		return httpx.New(httpx.CodeUnauthorized, "节点身份校验失败")
	}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var claimed int
		err := tx.QueryRow(ctx, `
			INSERT INTO node_request_nonces
				(tenant_id, node_id, nonce, request_fingerprint, request_ts, expires_at)
			VALUES ($1,$2,$3,$4,$5::timestamptz,
				GREATEST($5::timestamptz + INTERVAL '5 minutes', now() + INTERVAL '11 minutes'))
			ON CONFLICT (tenant_id, node_id, nonce) DO NOTHING
			RETURNING 1`, tenantID, nodeID, nonce, fingerprint, requestTS).Scan(&claimed)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeUnauthorized, "节点身份校验失败")
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			WITH expired AS (
				SELECT tenant_id, node_id, nonce
				  FROM node_request_nonces
				 WHERE tenant_id = $1 AND expires_at < now()
				 ORDER BY expires_at
				 LIMIT 32
			)
			DELETE FROM node_request_nonces n
			 USING expired e
			 WHERE n.tenant_id=e.tenant_id AND n.node_id=e.node_id AND n.nonce=e.nonce`, tenantID)
		return err
	})
	if err == nil {
		return nil
	}
	var apiErr *httpx.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return httpx.New(httpx.CodeUnavailable, "节点认证服务暂不可用").WithInternal(err)
}

//------------------------------------------------------------------------------
// AGT-004 心跳
//------------------------------------------------------------------------------

// Metrics 是探针上报的一组瞬时值。全部为整数：
// 百分比放大 10000 倍、负载放大 100 倍，避免浮点在存储与聚合时引入误差。
type Metrics struct {
	CPUBasisPoints int   `json:"cpu_bp"`
	MemUsedMB      int   `json:"mem_used_mb"`
	MemTotalMB     int   `json:"mem_total_mb"`
	DiskUsedGB     int   `json:"disk_used_gb"`
	DiskTotalGB    int   `json:"disk_total_gb"`
	Load1CBP       int   `json:"load1_cbp"`
	Load5CBP       int   `json:"load5_cbp"`
	Load15CBP      int   `json:"load15_cbp"`
	NetRxBytes     int64 `json:"net_rx_bytes"`
	NetTxBytes     int64 `json:"net_tx_bytes"`
	TCPConns       int   `json:"tcp_conns"`
	UptimeSec      int64 `json:"uptime_sec"`
}

type HeartbeatInput struct {
	AgentVersion         string   `json:"agent_version"`
	RuntimeVersion       string   `json:"runtime_version"`
	ConfigSigningKeyID   string   `json:"config_signing_key_id"`
	ConfigVersion        int      `json:"applied_config_version"`
	ConfigHash           string   `json:"applied_config_hash"`
	AppliedReleaseID     string   `json:"applied_effective_release_id"`
	AppliedGeneration    uint64   `json:"applied_effective_generation"`
	AppliedContentSHA256 string   `json:"applied_effective_content_sha256"`
	CPUCores             int      `json:"cpu_cores"`
	MemoryMB             int      `json:"memory_mb"`
	DiskGB               int      `json:"disk_gb"`
	LoadPercent          int      `json:"load_percent"`
	RuntimeStatus        string   `json:"runtime_status"`
	Metrics              *Metrics `json:"metrics"`
	MetricsPartial       bool     `json:"metrics_partial"`
}

type HeartbeatOutput struct {
	NodeStatus string `json:"node_status"`
	// DesiredConfigVersion 与 Agent 上报的版本不同即表示有新配置待应用
	DesiredConfigVersion int    `json:"desired_config_version"`
	DesiredReleaseID     string `json:"desired_effective_release_id,omitempty"`
	DesiredGeneration    uint64 `json:"desired_effective_generation,omitempty"`
	IntervalSeconds      int    `json:"interval_seconds"`
}

func (s *Service) Heartbeat(ctx context.Context, tenantID, nodeID string, in HeartbeatInput) (*HeartbeatOutput, error) {
	if in.ConfigSigningKeyID != "" {
		if _, err := canonicalEffectiveReleaseKeyID(in.ConfigSigningKeyID); err != nil {
			return nil, httpx.New(httpx.CodeBadRequest, "invalid config signing key id").WithInternal(err)
		}
	}
	var out HeartbeatOutput
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var hash []byte
		if in.ConfigHash != "" {
			hash, _ = base64.StdEncoding.DecodeString(in.ConfigHash)
		}
		// 健康分先给一个可解释的粗粒度值：能上报即 90，运行时异常降到 40。
		// POOL-004 的多维评分留到调度实装时再细化。
		score := 90
		if in.RuntimeStatus != "" && in.RuntimeStatus != "running" {
			score = 40
		}
		// 探针点独立于节点当前状态存放：节点行只保留「最新一眼」，
		// 时序表保留曲线。两者混在一张表会让节点表被高频写入拖慢。
		if in.Metrics != nil {
			m := in.Metrics
			if _, err := tx.Exec(ctx, `
				INSERT INTO node_metrics
					(tenant_id, node_id, cpu_bp, mem_used_mb, mem_total_mb,
					 disk_used_gb, disk_total_gb, load1_cbp, load5_cbp, load15_cbp,
					 net_rx_bytes, net_tx_bytes, tcp_conns, uptime_sec)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
				ON CONFLICT (node_id, recorded_at) DO NOTHING`,
				tenantID, nodeID, m.CPUBasisPoints, m.MemUsedMB, m.MemTotalMB,
				m.DiskUsedGB, m.DiskTotalGB, m.Load1CBP, m.Load5CBP, m.Load15CBP,
				m.NetRxBytes, m.NetTxBytes, m.TCPConns, m.UptimeSec); err != nil {
				return err
			}
		}

		var desiredReleaseID *string
		var desiredGeneration *int64
		if err := tx.QueryRow(ctx, `
			UPDATE nodes
			   SET last_heartbeat_at = now(), agent_version = $3,
			       runtime_version = coalesce(nullif($4,''), runtime_version),
			       config_signing_key_id = coalesce(nullif($11,''), config_signing_key_id),
			       applied_config_version = nullif($5,0),
			       applied_config_hash = $6,
			       cpu_cores = coalesce(nullif($7,0), cpu_cores),
			       memory_mb = coalesce(nullif($8,0), memory_mb),
			       disk_gb   = coalesce(nullif($9,0), disk_gb),
			       health_score = $10
			 WHERE tenant_id = $1 AND id = $2
			RETURNING status, coalesce(desired_config_version, 0),
			          desired_effective_release_id::text, desired_effective_generation`,
			tenantID, nodeID, in.AgentVersion, in.RuntimeVersion,
			in.ConfigVersion, hash, in.CPUCores, in.MemoryMB, in.DiskGB, score, in.ConfigSigningKeyID,
		).Scan(&out.NodeStatus, &out.DesiredConfigVersion, &desiredReleaseID, &desiredGeneration); err != nil {
			return err
		}
		if desiredReleaseID != nil {
			out.DesiredReleaseID = *desiredReleaseID
		}
		if desiredGeneration != nil && *desiredGeneration > 0 {
			out.DesiredGeneration = uint64(*desiredGeneration)
		}
		_, err := tx.Exec(ctx, `
			UPDATE servers SET last_heartbeat_at=now(), agent_version=$3,
			       cpu_cores=coalesce(nullif($4,0),cpu_cores),
			       memory_mb=coalesce(nullif($5,0),memory_mb),
			       disk_gb=coalesce(nullif($6,0),disk_gb)
			 WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`,
			tenantID, nodeID, in.AgentVersion, in.CPUCores, in.MemoryMB, in.DiskGB)
		return err
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.New(httpx.CodeNotFound, "节点不存在")
		}
		return nil, err
	}
	out.IntervalSeconds = 30
	return &out, nil
}

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

//------------------------------------------------------------------------------

// canonicalJSON 把任意 JSON 字节规范化为确定的字节序列。
//
// 为什么必须有这一步：payload 列是 jsonb，PostgreSQL 会按自己的规则
// 重排键、去掉空格后存储，读回来的字节与写进去的几乎必然不同。
// 若发布时对原始字节签名、下发时对读回的字节验签，签名永远对不上。
// Go 的 json.Marshal 对 map 按键名排序输出，因此两侧各自规范化后必定一致。
func canonicalJSON(raw []byte) ([]byte, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, fmt.Errorf("配置不是无重复字段的 JSON 对象: %w", err)
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("配置不是 JSON 对象: %w", err)
	}
	if m == nil {
		return nil, errors.New("配置必须是 JSON 对象")
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, fmt.Errorf("配置包含多余 JSON 数据: %w", err)
	}
	return json.Marshal(m)
}

// bytesEqual 用于比较哈希。长度不同直接判否，避免越界。
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
func nullInt(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}

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

//------------------------------------------------------------------------------
// 探针查询
//------------------------------------------------------------------------------

type MetricPoint struct {
	At         time.Time `json:"at"`
	CPUPercent float64   `json:"cpu_percent"`
	MemPercent float64   `json:"mem_percent"`
	Load1      float64   `json:"load1"`
	// 速率由相邻两点的累计值差分得出，单位 字节/秒
	RxSpeed  int64 `json:"rx_speed"`
	TxSpeed  int64 `json:"tx_speed"`
	TCPConns int   `json:"tcp_conns"`
}

type NodeMetrics struct {
	Points []MetricPoint `json:"points"`
	// Latest 是最近一个点的原始值，前端用它显示当前数值
	Latest *struct {
		CPUPercent  float64 `json:"cpu_percent"`
		MemUsedMB   int     `json:"mem_used_mb"`
		MemTotalMB  int     `json:"mem_total_mb"`
		DiskUsedGB  int     `json:"disk_used_gb"`
		DiskTotalGB int     `json:"disk_total_gb"`
		Load1       float64 `json:"load1"`
		Load5       float64 `json:"load5"`
		Load15      float64 `json:"load15"`
		TCPConns    int     `json:"tcp_conns"`
		UptimeSec   int64   `json:"uptime_sec"`
		RxTotal     int64   `json:"rx_total"`
		TxTotal     int64   `json:"tx_total"`
	} `json:"latest"`
}

// FetchMetrics 返回该节点最近 minutes 分钟的探针曲线。
//
// 网络速率在这里差分而不是让 Agent 上报：Agent 重启会让累计计数器归零，
// 若由它算速率就会产生一个巨大的虚假尖峰；在服务端差分则表现为一个负值，
// 可以识别并跳过。
func (s *Service) FetchMetrics(ctx context.Context, tenantID, nodeID string, minutes int) (*NodeMetrics, error) {
	if minutes <= 0 || minutes > 1440 {
		minutes = 60
	}
	out := &NodeMetrics{Points: []MetricPoint{}}

	type raw struct {
		at              time.Time
		cpu, memU, memT *int
		l1, l5, l15     *int
		rx, tx          *int64
		conns           *int
		diskU, diskT    *int
		uptime          *int64
	}
	var rows []raw

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rs, err := tx.Query(ctx, `
			SELECT recorded_at, cpu_bp, mem_used_mb, mem_total_mb,
			       load1_cbp, load5_cbp, load15_cbp,
			       net_rx_bytes, net_tx_bytes, tcp_conns,
			       disk_used_gb, disk_total_gb, uptime_sec
			  FROM node_metrics
			 WHERE tenant_id = $1 AND node_id = $2
			   AND recorded_at > now() - make_interval(mins => $3)
			 ORDER BY recorded_at`,
			tenantID, nodeID, minutes)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r raw
			if err := rs.Scan(&r.at, &r.cpu, &r.memU, &r.memT,
				&r.l1, &r.l5, &r.l15, &r.rx, &r.tx, &r.conns,
				&r.diskU, &r.diskT, &r.uptime); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		return rs.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return out, nil
	}

	iv := func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	}
	i64 := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}

	for i, r := range rows {
		pt := MetricPoint{
			At:         r.at,
			CPUPercent: float64(iv(r.cpu)) / 100,
			Load1:      float64(iv(r.l1)) / 100,
			TCPConns:   iv(r.conns),
		}
		if t := iv(r.memT); t > 0 {
			pt.MemPercent = float64(iv(r.memU)) / float64(t) * 100
		}
		if i > 0 {
			prev := rows[i-1]
			dt := r.at.Sub(prev.at).Seconds()
			// 间隔过长说明中间有断点，差分出来的「速率」没有意义
			if dt > 0 && dt < 300 {
				if d := i64(r.rx) - i64(prev.rx); d >= 0 {
					pt.RxSpeed = int64(float64(d) / dt)
				}
				if d := i64(r.tx) - i64(prev.tx); d >= 0 {
					pt.TxSpeed = int64(float64(d) / dt)
				}
			}
		}
		out.Points = append(out.Points, pt)
	}

	last := rows[len(rows)-1]
	out.Latest = &struct {
		CPUPercent  float64 `json:"cpu_percent"`
		MemUsedMB   int     `json:"mem_used_mb"`
		MemTotalMB  int     `json:"mem_total_mb"`
		DiskUsedGB  int     `json:"disk_used_gb"`
		DiskTotalGB int     `json:"disk_total_gb"`
		Load1       float64 `json:"load1"`
		Load5       float64 `json:"load5"`
		Load15      float64 `json:"load15"`
		TCPConns    int     `json:"tcp_conns"`
		UptimeSec   int64   `json:"uptime_sec"`
		RxTotal     int64   `json:"rx_total"`
		TxTotal     int64   `json:"tx_total"`
	}{
		CPUPercent: float64(iv(last.cpu)) / 100,
		MemUsedMB:  iv(last.memU), MemTotalMB: iv(last.memT),
		DiskUsedGB: iv(last.diskU), DiskTotalGB: iv(last.diskT),
		Load1:    float64(iv(last.l1)) / 100,
		Load5:    float64(iv(last.l5)) / 100,
		Load15:   float64(iv(last.l15)) / 100,
		TCPConns: iv(last.conns), UptimeSec: i64(last.uptime),
		RxTotal: i64(last.rx), TxTotal: i64(last.tx),
	}
	return out, nil
}

// PurgeMetrics 清理超出保留期的探针点。幂等。
func (s *Service) PurgeMetrics(ctx context.Context, tenantID string, keepHours int) (int64, error) {
	if keepHours <= 0 {
		keepHours = 48
	}
	var n int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT app.purge_node_metrics($1)`, keepHours).Scan(&n)
	})
	return n, err
}
