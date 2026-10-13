package service

// 服务器输入校验与读模型转换的单测（不连库）。

import (
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestServerValidateInput(t *testing.T) {
	in := ServerInput{Name: "  sg-1 ", PublicIPv4: " 203.0.113.1 ", PublicIPv6: "2001:db8::1", PrivateIPv4: "10.0.0.1"}
	if err := serverValidateInput(&in); err != nil {
		t.Fatal(err)
	}
	if in.Name != "sg-1" || in.PublicIPv4 != "203.0.113.1" || in.CapacityNodes != 32 {
		t.Fatalf("规整不对：%+v", in)
	}

	cases := []struct {
		name  string
		in    ServerInput
		field string
	}{
		{"名字空", ServerInput{Name: " "}, "name"},
		{"名字超长", ServerInput{Name: strings.Repeat("字", 65)}, "name"},
		{"容量负", ServerInput{Name: "a", CapacityNodes: -1}, "capacity_nodes"},
		{"v4 给了 v6", ServerInput{Name: "a", PublicIPv4: "2001:db8::1"}, "public_ipv4"},
		{"v4 映射写法", ServerInput{Name: "a", PublicIPv4: "::ffff:203.0.113.1"}, "public_ipv4"},
		{"v6 给了 v4", ServerInput{Name: "a", PublicIPv6: "203.0.113.1"}, "public_ipv6"},
		{"内网不是 IP", ServerInput{Name: "a", PrivateIPv4: "host"}, "private_ipv4"},
		{"地区超长", ServerInput{Name: "a", Region: strings.Repeat("a", 65)}, "region"},
		{"备注超长", ServerInput{Name: "a", Notes: strings.Repeat("a", 2001)}, "notes"},
		{"主机名含 NUL", ServerInput{Name: "a", Hostname: "h\x00"}, "hostname"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := c.in
			e := nodeTestErr(t, serverValidateInput(&in), errs.ValidationFailed)
			if e.Fields[c.field] == "" {
				t.Fatalf("应标在 %s：%v", c.field, e.Fields)
			}
		})
	}
}

func TestServerValidatePatch(t *testing.T) {
	empty := " "
	e := nodeTestErr(t, serverValidatePatch(&ServerPatch{RowVersion: 0, Name: &empty}), errs.ValidationFailed)
	if e.Fields["row_version"] == "" || e.Fields["name"] == "" {
		t.Fatalf("应同时标 row_version 与 name：%v", e.Fields)
	}
	zero := 0
	e = nodeTestErr(t, serverValidatePatch(&ServerPatch{RowVersion: 1, CapacityNodes: &zero}), errs.ValidationFailed)
	if e.Fields["capacity_nodes"] == "" {
		t.Fatalf("容量 0 应拒：%v", e.Fields)
	}
	// 文本给空串表示清空，合法
	blank := ""
	if err := serverValidatePatch(&ServerPatch{RowVersion: 1, Region: &blank, PublicIPv4: &blank}); err != nil {
		t.Fatal(err)
	}
}

func TestServerValidateQuery(t *testing.T) {
	if err := serverValidateQuery(&ServerFilter{Status: "ready", Query: "sg"}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []ServerFilter{{Status: "unhealthy"}, {Query: strings.Repeat("字", 121)}} {
		_ = nodeTestErr(t, serverValidateQuery(&f), errs.ValidationFailed)
	}
}

func TestServerFromModel(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-30 * time.Second)
	old := now.Add(-5 * time.Minute)
	counts := repo.ServerNodeCount{Total: 5, Active: 3, Live: 4}

	s := serverFromModel(model.Server{Status: ServerReady, LastHeartbeatAt: &fresh}, counts, now)
	if !s.HeartbeatOnline || s.NodeCount != 5 || s.ActiveNodeCount != 3 || s.ServingNodeCount != 3 || s.NeverSeenNodeCount != 0 {
		t.Fatalf("ready 且心跳新鲜：%+v", s)
	}
	if s = serverFromModel(model.Server{Status: ServerReady, LastHeartbeatAt: &old}, counts, now); s.HeartbeatOnline {
		t.Fatal("心跳超过 90 秒不算在线")
	}
	if s = serverFromModel(model.Server{Status: ServerDraining, LastHeartbeatAt: &fresh}, counts, now); s.HeartbeatOnline {
		t.Fatal("不是 ready 不算在线")
	}
	s = serverFromModel(model.Server{Status: ServerDraft}, counts, now)
	if s.HeartbeatOnline || s.ServingNodeCount != 0 || s.NeverSeenNodeCount != 3 {
		t.Fatalf("从没心跳：%+v", s)
	}
}

func TestServerOptText(t *testing.T) {
	if serverOptText("  ") != nil {
		t.Fatal("空白应为 nil")
	}
	if v := serverOptText(" a "); v == nil || *v != "a" {
		t.Fatalf("%v", v)
	}
}
