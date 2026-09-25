// [INPUT]: 依赖同包 protocol_schema.go 的 sensitiveProtocolKey 与 rejectDuplicateJSONKeys，依赖 encoding/json
// [OUTPUT]: 对外提供 PreserveRedactedProtocolSecrets：PATCH 时把请求里缺席的敏感键按原路径从库里补回
// [POS]: domain/nodefabric 的协议密钥保全，是 RedactProtocolConfig 的逆运算；被 node_admin.go 的 PatchAdminNode 调用
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"bytes"
	"encoding/json"
	"strings"
)

// PreserveRedactedProtocolSecrets 修的是 R78：读接口按名字抹掉敏感键，前端拿
// 抹过的配置改一个普通字段再整体 PATCH 回来，库里的密钥就被清空了。
//
// 规则与 RedactProtocolConfig 严格对称——它删哪条路径，这里就只补哪条路径：
//   - 请求里缺席的敏感键，从库里同一路径补回；
//   - 请求里给了的（哪怕是空串或 null）一律以请求为准，显式清空仍然可行；
//   - 普通键不补：缺席就是删除，与整体替换的原语义一致；
//   - 数组只在两边长度相同时按下标对齐往里走，长度变了无法判断谁是谁，不补。
//
// 没有任何东西要补时原样返回请求字节，不做重编码。请求本身带重复键时也
// 原样返回，交给后面的协议校验拒掉——先解码成 map 会让后一个值悄悄盖掉
// 前一个，重复键就再也查不出来了。
func PreserveRedactedProtocolSecrets(stored, incoming json.RawMessage) (json.RawMessage, error) {
	if len(stored) == 0 || len(incoming) == 0 || rejectDuplicateJSONKeys(incoming) != nil {
		return incoming, nil
	}
	storedValue, err := decodeJSONNumber(stored)
	if err != nil {
		// 库里的旧配置读不出来就没有可补的东西，不能因此挡住管理员修配置。
		return incoming, nil
	}
	incomingValue, err := decodeJSONNumber(incoming)
	if err != nil {
		return incoming, nil
	}
	if !restoreRedactedKeys(storedValue, incomingValue) {
		return incoming, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(incomingValue); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// decodeJSONNumber 保留数字原文：float64 往返会把大整数改掉。
func decodeJSONNumber(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func restoreRedactedKeys(stored, incoming any) bool {
	changed := false
	switch s := stored.(type) {
	case map[string]any:
		in, ok := incoming.(map[string]any)
		if !ok {
			return false
		}
		for key, storedChild := range s {
			incomingChild, present := in[key]
			if _, sensitive := sensitiveProtocolKey[strings.ToLower(key)]; sensitive {
				if !present {
					in[key] = storedChild
					changed = true
				}
				continue
			}
			if present && restoreRedactedKeys(storedChild, incomingChild) {
				changed = true
			}
		}
	case []any:
		in, ok := incoming.([]any)
		if !ok || len(in) != len(s) {
			return false
		}
		for i := range s {
			if restoreRedactedKeys(s[i], in[i]) {
				changed = true
			}
		}
	}
	return changed
}
