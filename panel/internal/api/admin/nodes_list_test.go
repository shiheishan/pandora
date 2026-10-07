package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 列表行只带列表要显示、要搜索的字段；编辑表单的字段只在 ?id= 单取时出现。
func TestNodeListRowLeavesFormFieldsToDetail(t *testing.T) {
	formOnly := []string{"protocol_config", "kernel", "traffic_rate", "protocol_schema_version",
		"config_validated_at", "traffic_bytes"}
	row := nodefabric.AdminNodeListRow{ID: "n-1", Name: "edge", GrantedPlans: []string{}}
	row.Protocol = json.RawMessage(`{"flow":"xtls-rprx-vision"}`)
	row.Kernel = "auto"
	list, err := json.Marshal(nodeListItem{AdminNodeListRow: row})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := json.Marshal(nodeDetailItem{nodeListItem: nodeListItem{AdminNodeListRow: row}, AdminNodeDetail: row.AdminNodeDetail})
	if err != nil {
		t.Fatal(err)
	}
	var listKeys, detailKeys map[string]json.RawMessage
	if err := json.Unmarshal(list, &listKeys); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(detail, &detailKeys); err != nil {
		t.Fatal(err)
	}
	for _, k := range formOnly {
		if _, ok := listKeys[k]; ok {
			t.Errorf("list row carries form-only field %q", k)
		}
		if _, ok := detailKeys[k]; !ok {
			t.Errorf("detail row lacks form field %q", k)
		}
	}
	// 单取的行是列表行的超集：前端两个 schema 共用列表那一份
	for k := range listKeys {
		if _, ok := detailKeys[k]; !ok {
			t.Errorf("detail row lacks list field %q", k)
		}
	}
	if string(detailKeys["protocol_config"]) != `{"flow":"xtls-rprx-vision"}` {
		t.Errorf("detail protocol_config=%s", detailKeys["protocol_config"])
	}
}

// 不认得的状态、过长的搜索词、不是 UUID 的 id 在碰库之前就回 422。
func TestNodeListRejectsBadFilters(t *testing.T) {
	h := &handlers{d: Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Node: nodefabric.NewService(nil, nil)}}
	for query, field := range map[string]string{
		"?state=gone":                            "state",
		"?id=not-a-uuid":                         "id",
		"?q=" + strings.Repeat("长", 121):         "q",
		"?state=online&id=0000-not-uuid&limit=5": "id",
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/nodes"+query, nil)
		req = req.WithContext(httpx.WithTenantID(req.Context(), "00000000-0000-4000-8000-000000000001"))
		w := httptest.NewRecorder()
		h.nodeList(w, req)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: status=%d body=%s, want 422 on %s", query, w.Code, w.Body.String(), field)
		}
	}
}
