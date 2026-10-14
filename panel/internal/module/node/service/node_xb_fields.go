package service

// 节点标签、流量上限与时段倍率（迁移 00045 补齐）的规整、校验与转换函数（不碰库）。

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/module/node/model"
)

const (
	// NodeTagsMax 是一个节点最多的标签数（与迁移 CHECK 一致）
	NodeTagsMax = 10
	// NodeTagMaxRunes 是单个标签的最大字数
	NodeTagMaxRunes = 16
)

// hasControlRune 检查字符串是否包含控制字符或 NUL 字符。
func hasControlRune(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || r == 0 {
			return true
		}
	}
	return false
}

// NormalizeNodeTags 规范化节点标签列表：逐个 trim、非空校验、无控制字符、≤16 字，不分大小写去重并保留首次出现原样，去重后 ≤10 个。
// 输入 nil 或空返回 []string{}（非 nil）。
func NormalizeNodeTags(in []string) ([]string, error) {
	if len(in) == 0 {
		return []string{}, nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for i, raw := range in {
		idx := i + 1
		tag := strings.TrimSpace(raw)
		if tag == "" {
			return nil, inventoryInvalid("tags", fmt.Sprintf("第 %d 个标签不能为空", idx))
		}
		if hasControlRune(tag) {
			return nil, inventoryInvalid("tags", fmt.Sprintf("第 %d 个标签含有控制字符", idx))
		}
		if utf8.RuneCountInString(tag) > NodeTagMaxRunes {
			return nil, inventoryInvalid("tags", fmt.Sprintf("第 %d 个标签最多 16 个字", idx))
		}
		lower := strings.ToLower(tag)
		if _, exists := seen[lower]; !exists {
			seen[lower] = struct{}{}
			out = append(out, tag)
		}
	}
	if len(out) > NodeTagsMax {
		return nil, inventoryInvalid("tags", "最多 10 个标签")
	}
	return out, nil
}

// NormalizeNodeTag 单个标签规范化（trim、非空、无控制字符、≤16 rune），给列表筛选用；不合法返回 "", false。
func NormalizeNodeTag(raw string) (string, bool) {
	tag := strings.TrimSpace(raw)
	if tag == "" {
		return "", false
	}
	if hasControlRune(tag) {
		return "", false
	}
	if utf8.RuneCountInString(tag) > NodeTagMaxRunes {
		return "", false
	}
	return tag, true
}

// encodeNodeTags 把标签切片编码为 JSON 数组文本，nil 或空返回 "[]"。
func encodeNodeTags(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// decodeNodeTags 解析 jsonb 文本为标签切片；空串、null、解析失败都返回 []string{}（非 nil）。
func decodeNodeTags(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" || text == "null" || text == "[]" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}

// nodeCheckTrafficLimit 校验流量上限：nil 合法，<= 0 报错。
func nodeCheckTrafficLimit(limit *int64) error {
	if limit == nil {
		return nil
	}
	if *limit <= 0 {
		return inventoryInvalid("traffic_limit_bytes", "必须是正整数（字节）；不限请留空")
	}
	return nil
}

// nodeCheckResetDay 校验流量月重置日：nil 合法，不在 1–NodeTrafficResetDayMax 报错。
func nodeCheckResetDay(day *int) error {
	if day == nil {
		return nil
	}
	if *day < 1 || *day > NodeTrafficResetDayMax {
		return inventoryInvalid("traffic_reset_day", "必须是 1 到 31 之间的整数；不自动重置请留空（短月取月末）")
	}
	return nil
}

// rateScheduleInputs 把 RateRange 切片转为 RateRangeInput 形式输出，nil 返回 []RateRangeInput{}（非 nil）。
func rateScheduleInputs(rs []RateRange) []RateRangeInput {
	if len(rs) == 0 {
		return []RateRangeInput{}
	}
	out := make([]RateRangeInput, len(rs))
	for i, r := range rs {
		out[i] = RateRangeInput{
			Start: formatHHMM(r.StartMin),
			End:   formatHHMM(r.EndMin),
			Rate:  r.Rate,
		}
	}
	return out
}

// decodeRateScheduleInputs 从 JSON 文本解码时段倍率并转为 RateRangeInput 切片；出错返回 []RateRangeInput{}。
func decodeRateScheduleInputs(text string) []RateRangeInput {
	rs, err := decodeRateSchedule(text)
	if err != nil || len(rs) == 0 {
		return []RateRangeInput{}
	}
	return rateScheduleInputs(rs)
}

// nodeTrafficFromModel 从 model.Node 提取流量上限与用量信息。
func nodeTrafficFromModel(n model.Node) NodeTrafficLimit {
	return NodeTrafficLimit{
		LimitBytes: n.TrafficLimitBytes,
		ResetDay:   n.TrafficResetDay,
		UsedBytes:  n.TrafficUsedBytes,
		ResetAt:    n.TrafficResetAt,
		ExceededAt: n.TrafficExceededAt,
	}
}

// nodeExceededAt 根据流量上限和已用字节计算超限时刻：达到或超过上限时若已有 prev 则保留，否则标为 now；未超限返回 nil。
func nodeExceededAt(limit *int64, used int64, prev *time.Time, now time.Time) *time.Time {
	if limit != nil && used >= *limit {
		if prev != nil {
			return prev
		}
		return &now
	}
	return nil
}
