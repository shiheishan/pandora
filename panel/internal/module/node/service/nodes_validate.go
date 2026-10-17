package service

// 节点输入校验的纯函数。

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

const (
	nodeNameMax        = 64
	nodeDisplayMax     = 64
	nodeHostMax        = 255
	nodeReasonMax      = 500
	nodeNulMessage     = "不能包含空字符"
	nodeRowVersionHint = "必须提供正整数版本号"
)

// nodeRateMax 是流量倍率的上限（numeric(6,2)）。
var nodeRateMax = decimal.RequireFromString("9999.99")

// nodeValidateName 规整并校验节点名称：1–64 个字符、不含 NUL。
func nodeValidateName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if strings.ContainsRune(name, 0) {
		return "", inventoryInvalid("name", nodeNulMessage)
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > nodeNameMax {
		return "", inventoryInvalid("name", "名称必须为 1 到 64 个字符")
	}
	return name, nil
}

// nodeValidateDisplay 规整并校验展示名：空返回 nil。
func nodeValidateDisplay(raw string) (*string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil, nil
	}
	if strings.ContainsRune(v, 0) {
		return nil, inventoryInvalid("display_name", nodeNulMessage)
	}
	if utf8.RuneCountInString(v) > nodeDisplayMax {
		return nil, inventoryInvalid("display_name", "最多 64 个字符")
	}
	return &v, nil
}

// nodeNormalizeCountry 把国家代码规整成两位大写字母；空返回 nil。
func nodeNormalizeCountry(raw string) (*string, error) {
	code := strings.ToUpper(strings.TrimSpace(raw))
	if code == "" {
		return nil, nil
	}
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return nil, inventoryInvalid("country_code", "必须是两位字母国家代码")
	}
	return &code, nil
}

// nodeValidHost 报告 host 是不是 IP 字面量或 ASCII 主机名。
func nodeValidHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

// nodeCheckRate 校验流量倍率：0 ≤ r ≤ 9999.99，最多两位小数。0 表示不计量（迁移 00018）。
func nodeCheckRate(r decimal.Decimal) error {
	if r.IsNegative() || r.GreaterThan(nodeRateMax) || !r.Equal(r.Round(2)) {
		return inventoryInvalid("traffic_rate", "必须是 0 到 9999.99 之间、最多两位小数的数")
	}
	return nil
}

// nodeValidateProtocol 校验一次完整的协议写入（地址、端口、类型、配置体），返回 schema 版本。raw 为空当作 {}。
func nodeValidateProtocol(nodeType, host string, port int, raw json.RawMessage) (int, error) {
	host = strings.TrimSpace(host)
	switch {
	case host == "":
		return 0, inventoryInvalid("server_host", "必填")
	case len(host) > nodeHostMax || !nodeValidHost(host):
		return 0, inventoryInvalid("server_host", "必须是有效的 IP 或 ASCII 主机名")
	}
	if port < 1 || port > 65535 {
		return 0, inventoryInvalid("server_port", "端口必须在 1 到 65535 之间")
	}
	if protocol.CanonicalType(nodeType) == "" {
		return 0, inventoryInvalid("node_type", "必填：请选择协议类型")
	}
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	version, fields := protocol.Validate(nodeType, port, raw)
	if len(fields) > 0 {
		return 0, errs.Invalid(fields)
	}
	if version <= 0 {
		return 0, inventoryInvalid("node_type", "不支持的协议类型")
	}
	return version, nil
}

// nodeSameJSON 报告两段 JSON 语义上是否相同（数字按原文比较，键序无关）。
func nodeSameJSON(a, b []byte) bool {
	decode := func(raw []byte) (any, bool) {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			return nil, false
		}
		return v, true
	}
	va, okA := decode(a)
	vb, okB := decode(b)
	return okA && okB && reflect.DeepEqual(va, vb)
}

// nodeProtocolReady 报告节点的协议是否配齐（能上线的前提，与 nodes_active_shape 同口径）。
func nodeProtocolReady(n model.Node) bool {
	return n.NodeType != nil && n.ServerHost != nil && n.ServerPort != nil && n.ProtocolSchemaVersion > 0
}

// nodeStr 取可空文本，nil 为空串。
func nodeStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// nodeInt 取可空整数，nil 为 0。
func nodeInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// nodeValidateReason 规整并校验操作原因（迁移、退役、删除、批量改状态）：最多 500 字、不含 NUL。
func nodeValidateReason(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if strings.ContainsRune(v, 0) {
		return "", inventoryInvalid("reason", nodeNulMessage)
	}
	if utf8.RuneCountInString(v) > nodeReasonMax {
		return "", inventoryInvalid("reason", "最多 500 个字符")
	}
	return v, nil
}

// nodeLockPool 以 FOR SHARE 锁住要划入的池：nil 通过；不存在或已停用回 422 pool_id。
func (s *Service) nodeLockPool(ctx context.Context, poolID *uuid.UUID) error {
	if poolID == nil {
		return nil
	}
	status, found, err := s.repo.LockPoolForNode(ctx, *poolID)
	if err != nil {
		return err
	}
	if !found || status == "disabled" {
		return inventoryInvalid("pool_id", "节点池不存在或已停用")
	}
	return nil
}

// nodeSameUUID 报告两个可空 id 是否相同。
func nodeSameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// nodeOpt 把空串转成 nil。
func nodeOpt(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
