package service

// 节点标签、流量上限与时段倍率纯函数的单测（不连库）。

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestNormalizeNodeTags(t *testing.T) {
	got, err := NormalizeNodeTags(nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("nil → 空切片非 nil：%v %v", got, err)
	}
	got, err = NormalizeNodeTags([]string{})
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("空 → 空切片非 nil：%v %v", got, err)
	}
	got, err = NormalizeNodeTags([]string{" 香港 ", "IPLC"})
	if err != nil || len(got) != 2 || got[0] != "香港" || got[1] != "IPLC" {
		t.Fatalf("去空白：%v %v", got, err)
	}
	got, err = NormalizeNodeTags([]string{"HK", "hk", "Hk"})
	if err != nil || len(got) != 1 || got[0] != "HK" {
		t.Fatalf("大小写去重保留首次：%v %v", got, err)
	}
	_, err = NormalizeNodeTags([]string{"  "})
	e := nodeTestErr(t, err, errs.ValidationFailed)
	if e.Fields["tags"] == "" {
		t.Fatal("空串应标在 tags")
	}
	_, err = NormalizeNodeTags([]string{"a\tb"})
	e = nodeTestErr(t, err, errs.ValidationFailed)
	if e.Fields["tags"] == "" {
		t.Fatal("控制字符应标在 tags")
	}
	_, err = NormalizeNodeTags([]string{strings.Repeat("字", 17)})
	e = nodeTestErr(t, err, errs.ValidationFailed)
	if e.Fields["tags"] == "" {
		t.Fatal("17 字应标在 tags")
	}
	var eleven []string
	for i := 0; i < 11; i++ {
		eleven = append(eleven, "t"+string(rune('a'+i)))
	}
	_, err = NormalizeNodeTags(eleven)
	e = nodeTestErr(t, err, errs.ValidationFailed)
	if e.Fields["tags"] == "" {
		t.Fatal("11 个标签应标在 tags")
	}
	var ten []string
	for i := 0; i < 10; i++ {
		ten = append(ten, "t"+string(rune('a'+i)))
	}
	got, err = NormalizeNodeTags(ten)
	if err != nil || len(got) != 10 {
		t.Fatalf("10 个合法：%v %v", got, err)
	}
}

func TestDecodeNodeTags(t *testing.T) {
	for _, text := range []string{"", "null", "{bad"} {
		got := decodeNodeTags(text)
		if got == nil || len(got) != 0 {
			t.Fatalf("decodeNodeTags(%q) = %v", text, got)
		}
	}
	got := decodeNodeTags(`["香港","IPLC"]`)
	if len(got) != 2 || got[0] != "香港" || got[1] != "IPLC" {
		t.Fatalf("正常 JSON：%v", got)
	}
}

func TestNodeCheckTrafficLimit(t *testing.T) {
	if err := nodeCheckTrafficLimit(nil); err != nil {
		t.Fatalf("nil 应合法：%v", err)
	}
	for _, v := range []int64{0, -1} {
		e := nodeTestErr(t, nodeCheckTrafficLimit(&v), errs.ValidationFailed)
		if e.Fields["traffic_limit_bytes"] == "" {
			t.Fatalf("%d 应标在 traffic_limit_bytes", v)
		}
	}
	one := int64(1)
	if err := nodeCheckTrafficLimit(&one); err != nil {
		t.Fatalf("1 应合法：%v", err)
	}
}

func TestNodeCheckResetDay(t *testing.T) {
	if err := nodeCheckResetDay(nil); err != nil {
		t.Fatalf("nil 应合法：%v", err)
	}
	for _, v := range []int{0, 32} {
		e := nodeTestErr(t, nodeCheckResetDay(&v), errs.ValidationFailed)
		if e.Fields["traffic_reset_day"] == "" {
			t.Fatalf("%d 应标在 traffic_reset_day", v)
		}
	}
	for _, v := range []int{1, 31} {
		if err := nodeCheckResetDay(&v); err != nil {
			t.Fatalf("%d 应合法：%v", v, err)
		}
	}
}

func TestNodeExceededAt(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if nodeExceededAt(nil, 100, nil, now) != nil {
		t.Fatal("limit nil 应返回 nil")
	}
	limit := int64(100)
	if nodeExceededAt(&limit, 99, nil, now) != nil {
		t.Fatal("used < limit 应返回 nil")
	}
	got := nodeExceededAt(&limit, 100, nil, now)
	if got == nil || !got.Equal(now) {
		t.Fatalf("used==limit 且 prev nil 应返回 now：%v", got)
	}
	prev := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	got = nodeExceededAt(&limit, 100, &prev, now)
	if got == nil || !got.Equal(prev) {
		t.Fatalf("prev 非 nil 应原样返回：%v", got)
	}
}

func TestDecodeRateScheduleInputs(t *testing.T) {
	got := decodeRateScheduleInputs("[]")
	if got == nil || len(got) != 0 {
		t.Fatalf("[] → 空：%v", got)
	}
	text := `[{"start":"08:00","end":"24:00","rate":"1.5"}]`
	got = decodeRateScheduleInputs(text)
	if len(got) != 1 {
		t.Fatalf("一段：%v", got)
	}
	if got[0].Start != "08:00" || got[0].End != "24:00" {
		t.Fatalf("时间：%q %q", got[0].Start, got[0].End)
	}
	if !got[0].Rate.Equal(decimal.RequireFromString("1.5")) {
		t.Fatalf("倍率：%s", got[0].Rate)
	}
}
