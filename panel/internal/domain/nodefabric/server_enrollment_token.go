package nodefabric

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/bindingcontract"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 后台给服务器生成绑定令牌与绑定命令（合约 §3.1、§5、§11）。

// serverBindingTokenTTL 是绑定令牌的有效期（合约 §3.1：单次、1 小时）。
const serverBindingTokenTTL = time.Hour

// IssueServerBindingTokenInput 是生成绑定命令的输入。
type IssueServerBindingTokenInput struct {
	ActorID  string
	ServerID string
	// PanelURL 是机器回连的面板地址，由调用方经 platform/config 推导。
	PanelURL string
	// TLSPin 是节点网关证书 SPKI 的钉住值（sha256//…，合约 §11.1）。网关自签证书属于 P4，
	// 在那之前为空：命令不带 -k 与 --pin，按系统 CA 校验（或回环 http 用于开发）。
	TLSPin string
}

// ServerBindingTokenOutput 是生成绑定命令的结果。令牌明文只在这里出现一次。
type ServerBindingTokenOutput struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	ServerID  string    `json:"server_id"`
	PanelURL  string    `json:"panel_url"`
	// PanelKey 是面板配置签名公钥指纹（--panel-key），pdnd 在接入时核对。
	PanelKey string `json:"panel_key"`
	// TLSPin 为空表示网关钉住还没有启用（P4）。
	TLSPin string `json:"tls_pin"`
	// Command 是贴到机器上执行的一行命令；令牌不在命令里，执行时从终端读入。
	Command string `json:"command"`
}

// IssueServerBindingToken 给一台服务器签发绑定令牌（kind=server，单次，1 小时）并生成绑定命令。
// 已有有效身份的服务器拒绝签发（先解除绑定再换机），免得命令被贴到第二台机器上。
// 同一台服务器只保留最新的一张未用令牌：新签发时作废旧的。
func (s *Service) IssueServerBindingToken(ctx context.Context, tenantID string, in IssueServerBindingTokenInput) (*ServerBindingTokenOutput, error) {
	if err := validateAdminUUID("server_id", in.ServerID, true); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	if in.TLSPin != "" {
		if _, err := bindingcontract.ParseTLSPin(in.TLSPin); err != nil {
			return nil, httpx.Internal(err)
		}
	}
	panelKey, err := s.panelKeyPin()
	if err != nil {
		return nil, httpx.Internal(err)
	}
	token, err := crypto.NewToken(32)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	expiresAt := time.Now().Add(serverBindingTokenTTL).UTC().Truncate(time.Microsecond)
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT true FROM servers
			WHERE tenant_id=$1 AND id=$2::uuid AND deleted_at IS NULL AND status <> 'retired' FOR UPDATE`,
			tenantID, in.ServerID).Scan(&exists); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.New(httpx.CodeNotFound, "服务器不存在、已删除或已退役")
			}
			return err
		}
		var bound bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM server_identities
			WHERE tenant_id=$1 AND server_id=$2::uuid AND status='active')`, tenantID, in.ServerID).Scan(&bound); err != nil {
			return err
		}
		if bound {
			return httpx.New(httpx.CodeConflict, "这台服务器已绑定；如需换机，请先解除绑定")
		}
		voided, err := tx.Exec(ctx, `UPDATE bootstrap_tokens SET consumed_at=now()
			WHERE tenant_id=$1 AND server_id=$2::uuid AND kind='server' AND consumed_at IS NULL`,
			tenantID, in.ServerID)
		if err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO bootstrap_tokens (tenant_id, token_hash, server_id, kind, max_uses, expires_at, created_by)
			VALUES ($1,$2,$3::uuid,'server',1,$4,$5) RETURNING id::text`,
			tenantID, serverBindingTokenHash(token), in.ServerID, expiresAt, nullStr(in.ActorID)).Scan(&id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: nullStr(in.ActorID), Action: "server.binding_token.issue",
			ResourceType: "bootstrap_token", ResourceID: &id, APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"server_id": in.ServerID, "ttl_minutes": int(serverBindingTokenTTL / time.Minute),
				"voided_tokens": voided.RowsAffected(), "tls_pinned": in.TLSPin != ""},
		})
	})
	if err != nil {
		return nil, err
	}
	return &ServerBindingTokenOutput{
		Token: token, ExpiresAt: expiresAt, ServerID: in.ServerID, PanelURL: in.PanelURL,
		PanelKey: panelKey, TLSPin: in.TLSPin,
		Command: RenderServerBindingCommand(in.PanelURL, in.TLSPin, panelKey),
	}, nil
}

// RenderServerBindingCommand 生成服务器绑定命令（合约 §5、§11.1）。
//
//   - 令牌不进命令行与 shell 历史：执行时关回显从终端读入，写进 0600 临时文件，只把路径交给
//     pdnd（--token-file），退出与中断时都删掉（与节点接入命令同一套开头）。
//   - 机器上已经有 pandora-native 就只执行 bind，不跑面板下发的 root 脚本：追加第二个面板时，
//     那个面板拿不到在机器上以 root 执行代码的机会（设计稿 §5）。
//   - 带钉住值时 curl 用 -k --pinnedpubkey：curl 带钉住即使加 -k 也会校验公钥；没有钉住值时
//     不加 -k，按系统 CA 校验。--panel-key 总是带上，pdnd 用它核对接入响应里的配置公钥。
//
// pandora-native bind 与安装脚本的 --pin / --panel-key 参数在 P4 / P5 落地；本阶段命令先按
// 合约把字段排好。
func RenderServerBindingCommand(panelURL, tlsPin, panelKey string) string {
	panel := panelURL
	if !safePanelURL.MatchString(panel) {
		panel = shellQuote(panel)
	}
	pinArgs := ""
	curl := "curl -fsSL "
	if tlsPin != "" {
		pinArgs = " --pin " + shellQuote(tlsPin)
		curl = "curl -fsS -k --pinnedpubkey " + shellQuote(tlsPin) + " "
	}
	bindArgs := "--panel " + panel + pinArgs + " --panel-key " + shellQuote(panelKey) +
		" --token-file \"$PANDORA_TOKEN_FILE\""
	return installTokenPrologue +
		"printf 'Binding token: ' >&2; " + installEchoOff +
		"IFS= read -r PANDORA_BINDING_TOKEN </dev/tty; " + installEchoOn +
		"PANDORA_TOKEN_FILE=$(mktemp); " +
		"printf '%s' \"$PANDORA_BINDING_TOKEN\" >\"$PANDORA_TOKEN_FILE\"; unset PANDORA_BINDING_TOKEN; " +
		"if command -v pandora-native >/dev/null 2>&1; then pandora-native bind " + bindArgs + "; " +
		"else " + curl + panel + "/pdnd/install.sh | sh -s -- " + bindArgs + "; fi; " +
		"PANDORA_RC=$?; rm -f \"$PANDORA_TOKEN_FILE\"; exit $PANDORA_RC"
}
