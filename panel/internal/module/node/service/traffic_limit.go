package service

// 节点时段倍率与流量周期的纯函数（不碰库）。
// 时段为站点时区下一天的半开区间 [StartMin, EndMin)，不跨零点；扣量按入账时刻取 effectiveRate。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	// NodeRateScheduleMax 是一个节点时段倍率的最大段数（与迁移 CHECK 一致）。
	NodeRateScheduleMax = 12
	// NodeTrafficResetDayMax 是流量每月重置日的上限。
	NodeTrafficResetDayMax = 31
)

// RateRange 是一段时段倍率：站点时区一天里的 [StartMin, EndMin) 分钟，0 <= StartMin < EndMin <= 1440（1440 即 24:00）。
type RateRange struct {
	StartMin int
	EndMin   int
	Rate     decimal.Decimal
}

// RateRangeInput 是后台给的一段；JSON 形状 {"start":"08:00","end":"12:00","rate":1.5}。
type RateRangeInput struct {
	Start string          `json:"start"`
	End   string          `json:"end"`
	Rate  decimal.Decimal `json:"rate"`
}

type rateScheduleWire struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Rate  string `json:"rate"`
}

// ParseRateSchedule 校验并解析时段倍率表，按 StartMin 升序返回；空输入返回 nil, nil。
func ParseRateSchedule(in []RateRangeInput) ([]RateRange, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > NodeRateScheduleMax {
		return nil, inventoryInvalid("traffic_rate_schedule", fmt.Sprintf("最多 %d 段", NodeRateScheduleMax))
	}

	out := make([]RateRange, 0, len(in))
	for i, seg := range in {
		idx := i + 1
		startMin, err := parseScheduleStart(seg.Start)
		if err != nil {
			return nil, inventoryInvalid("traffic_rate_schedule", fmt.Sprintf("第 %d 段：时间要写成 HH:MM（如 08:00），结束可写 24:00", idx))
		}
		endMin, err := parseScheduleEnd(seg.End)
		if err != nil {
			return nil, inventoryInvalid("traffic_rate_schedule", fmt.Sprintf("第 %d 段：时间要写成 HH:MM（如 08:00），结束可写 24:00", idx))
		}
		if startMin >= endMin {
			return nil, inventoryInvalid("traffic_rate_schedule", fmt.Sprintf("第 %d 段：开始要早于结束；跨零点请拆成两段（如 22:00–24:00 与 00:00–06:00）", idx))
		}
		if err := nodeCheckRate(seg.Rate); err != nil {
			return nil, inventoryInvalid("traffic_rate_schedule", fmt.Sprintf("第 %d 段：倍率要在 0 到 9999.99 之间，最多两位小数", idx))
		}
		out = append(out, RateRange{StartMin: startMin, EndMin: endMin, Rate: seg.Rate})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].StartMin < out[j].StartMin })
	for i := 1; i < len(out); i++ {
		if out[i].StartMin < out[i-1].EndMin {
			return nil, inventoryInvalid("traffic_rate_schedule", fmt.Sprintf("第 %d 段与第 %d 段时间重叠", i+1, i))
		}
	}
	return out, nil
}

// encodeRateSchedule 把时段表编成 JSON 数组文本，rate 为字符串；nil 或空返回 "[]"。
func encodeRateSchedule(rs []RateRange) string {
	if len(rs) == 0 {
		return "[]"
	}
	wire := make([]rateScheduleWire, len(rs))
	for i, r := range rs {
		wire[i] = rateScheduleWire{
			Start: formatHHMM(r.StartMin),
			End:   formatHHMM(r.EndMin),
			Rate:  r.Rate.String(),
		}
	}
	b, err := json.Marshal(wire)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// decodeRateSchedule 读库里的 JSON 文本；空串、"[]"、"null" 返回 nil, nil。
func decodeRateSchedule(text string) ([]RateRange, error) {
	text = strings.TrimSpace(text)
	if text == "" || text == "[]" || text == "null" {
		return nil, nil
	}
	var in []RateRangeInput
	if err := json.Unmarshal([]byte(text), &in); err != nil {
		return nil, err
	}
	return ParseRateSchedule(in)
}

// formatHHMM 把分钟数转成 HH:MM；1440 为 "24:00"。
func formatHHMM(min int) string {
	if min == 1440 {
		return "24:00"
	}
	h := min / 60
	m := min % 60
	return fmt.Sprintf("%02d:%02d", h, m)
}

// effectiveRate 按入账时刻 at 在站点时区 loc 下的分钟数取命中段的倍率；未命中返回 base。loc 为 nil 时按 UTC。
func effectiveRate(base decimal.Decimal, sched []RateRange, at time.Time, loc *time.Location) decimal.Decimal {
	if loc == nil {
		loc = time.UTC
	}
	local := at.In(loc)
	m := local.Hour()*60 + local.Minute()
	for _, r := range sched {
		if r.StartMin <= m && m < r.EndMin {
			return r.Rate
		}
	}
	return base
}

// trafficPeriodStart 返回本流量周期起点：站点时区下当月「重置日」0 点（日 = min(resetDay, 当月天数））；
// 若该时刻晚于 now 则取上一月同样规则。resetDay 不在 1–31 时按 31 处理。
func trafficPeriodStart(resetDay int, now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	if resetDay < 1 || resetDay > NodeTrafficResetDayMax {
		resetDay = NodeTrafficResetDayMax
	}
	nowLocal := now.In(loc)
	y, mo, _ := nowLocal.Date()
	day := minInt(resetDay, daysInMonth(y, mo))
	start := time.Date(y, mo, day, 0, 0, 0, 0, loc)
	if start.After(nowLocal) {
		y, mo = prevMonth(y, mo)
		day = minInt(resetDay, daysInMonth(y, mo))
		start = time.Date(y, mo, day, 0, 0, 0, 0, loc)
	}
	return start
}

func parseScheduleStart(s string) (int, error) {
	min, ok := parseHHMMStrict(s)
	if !ok {
		return 0, fmt.Errorf("invalid")
	}
	if min < 0 || min > 23*60+59 {
		return 0, fmt.Errorf("invalid")
	}
	return min, nil
}

func parseScheduleEnd(s string) (int, error) {
	if s == "24:00" {
		return 1440, nil
	}
	min, ok := parseHHMMStrict(s)
	if !ok {
		return 0, fmt.Errorf("invalid")
	}
	if min < 1 || min > 23*60+59 {
		return 0, fmt.Errorf("invalid")
	}
	return min, nil
}

func parseHHMMStrict(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func prevMonth(year int, month time.Month) (int, time.Month) {
	if month == time.January {
		return year - 1, time.December
	}
	return year, month - 1
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
