package adminops

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestNormalizeHighlights(t *testing.T) {
	got, fields := normalizeHighlights([]string{"  高速专线 ", "不限设备", strings.Repeat("字", 40)})
	if len(fields) != 0 || !slices.Equal(got, []string{"高速专线", "不限设备", strings.Repeat("字", 40)}) {
		t.Fatalf("valid list: %v %v", got, fields)
	}
	if got, fields := normalizeHighlights(nil); got == nil || len(got) != 0 || len(fields) != 0 {
		t.Fatalf("nil must become an empty non-nil slice: %#v %v", got, fields)
	}
	_, fields = normalizeHighlights([]string{"a", "   ", strings.Repeat("字", 41), " a", "b", "c"})
	want := map[string]string{
		"highlights":   "最多 5 条卖点",
		"highlights.1": "卖点不能为空",
		"highlights.2": "每条卖点最多 40 个字",
		"highlights.3": "卖点不能重复",
	}
	if len(fields) != len(want) {
		t.Fatalf("fields=%v", fields)
	}
	for k, v := range want {
		if fields[k] != v {
			t.Fatalf("fields[%s]=%q want %q (all=%v)", k, fields[k], v, fields)
		}
	}
}

// 资料与卖点一次 422 标全，字段键与请求一致。
func TestPlanProfileAndHighlightsFailTogether(t *testing.T) {
	in := CreatePlanInput{Code: "BAD CODE", Name: "x", Highlights: []string{""}}
	err := prepareCreatePlanInput(&in)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || he.Fields["code"] == "" || he.Fields["highlights.0"] == "" {
		t.Fatalf("err=%v", err)
	}
	up := UpdatePlanInput{ExpectedRowVersion: 1, Code: "ok-code", Name: "x", Visibility: "public",
		Highlights: []string{" 推荐 "}, Recommended: true}
	if err := prepareUpdatePlanInput("8c000000-0000-7000-8000-000000000031", &up); err != nil {
		t.Fatalf("valid update rejected: %v", err)
	}
	if !slices.Equal(up.Highlights, []string{"推荐"}) {
		t.Fatalf("highlights not trimmed in place: %v", up.Highlights)
	}
}

// 向导「一次改完」：卖点与推荐缺省 = 不动，所以要分得出缺省与给了空列表。
func TestUpdatePlanCompleteHighlightsOmittedVersusEmpty(t *testing.T) {
	var omitted, empty UpdatePlanCompleteInput
	if err := json.Unmarshal([]byte(`{}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"highlights":[],"recommended":false}`), &empty); err != nil {
		t.Fatal(err)
	}
	if omitted.Highlights != nil || omitted.Recommended != nil {
		t.Fatalf("omitted decoded as set: %+v", omitted)
	}
	if empty.Highlights == nil || len(*empty.Highlights) != 0 || empty.Recommended == nil || *empty.Recommended {
		t.Fatalf("explicit empty decoded wrong: %+v", empty)
	}
}
