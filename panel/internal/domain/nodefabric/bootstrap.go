// [INPUT]: 依赖 service.go 的 Service 与 regionForIP（按公网 IP 取地区），依赖 platform 的 crypto（令牌与身份签发）、db、audit、httpx
// [OUTPUT]: 对外提供 IssueTokenInput / IssueTokenOutput、BootstrapInput / BootstrapOutput、RenderLegacyInstallCommand，Service 的 IssueBootstrapToken、Bootstrap
// [POS]: domain/nodefabric 的一次性 bootstrap 令牌（NODE-008）与旧版接入：从 service.go 拆出。令牌只存绑定节点名的哈希，接入命令模板占位符经 shellQuote 转义；旧版 Bootstrap 持 node-config-release 锁，令牌消费与身份签发同事务，退役 / 销毁节点只有服务器删除级联静默的才许同名重装；新接入走 enrollment.go 的两阶段
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

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
