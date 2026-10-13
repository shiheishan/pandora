package service

// 保留端口与端口冲突文案的单测（不连库）。

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func nodeTestErr(t *testing.T, err error, code errs.Code) *errs.Error {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("期望 %s，得到 %v", code, err)
	}
	return e
}

func TestNodePortReservedAlways(t *testing.T) {
	p := defaultNodePortPolicy
	for _, port := range []int{22, 25, 53, 80} {
		_, err := p.checkReserved(port, nodePanelHostNo)
		e := nodeTestErr(t, err, errs.ValidationFailed)
		if e.Fields["server_port"] == "" {
			t.Fatalf("端口 %d 应标在 server_port：%v", port, e.Fields)
		}
	}
}

func TestNodePortPanelReserved(t *testing.T) {
	p := defaultNodePortPolicy
	for _, addrs := range [][]string{{"127.0.0.1"}, {"localhost"}, {"::1"}} {
		_, err := p.checkReserved(443, p.panelHost(addrs))
		nodeTestErr(t, err, errs.ValidationFailed)
	}
	// 普通公网地址：判断不了是不是面板机，只提示
	w, err := p.checkReserved(443, p.panelHost([]string{"203.0.113.10", "edge.example.test"}))
	if err != nil || w == "" {
		t.Fatalf("443 在普通地址上应只提示：%q %v", w, err)
	}
	// 配了面板地址、服务器的 IP 不在里面：判为不是面板机，不提示
	q := nodePortPolicy{PanelReserved: p.PanelReserved, PanelHosts: []string{"198.51.100.1"}}
	w, err = q.checkReserved(443, q.panelHost([]string{"203.0.113.10"}))
	if err != nil || w != "" {
		t.Fatalf("不是面板机时 443 不提示：%q %v", w, err)
	}
	// 命中面板地址（规整大小写与结尾的点）
	q.PanelHosts = []string{"Panel.Example.Test."}
	_, err = q.checkReserved(443, q.panelHost([]string{"panel.example.test"}))
	nodeTestErr(t, err, errs.ValidationFailed)
}

func TestNodePortOrdinary(t *testing.T) {
	p := defaultNodePortPolicy
	w, err := p.checkReserved(10000, nodePanelHostUnknown)
	if err != nil || w != "" {
		t.Fatalf("普通端口不应有任何提示：%q %v", w, err)
	}
}

func TestNodePortConflictMessage(t *testing.T) {
	e := nodeTestErr(t, nodePortConflict(8443, "udp", "香港-1"), errs.Conflict)
	if !strings.Contains(e.Message, "香港-1") || !strings.Contains(e.Message, "端口 ") {
		t.Fatalf("文案应写明占用者：%q", e.Message)
	}
	if e.Fields["server_port"] == "" || strings.Contains(e.Fields["server_port"], "换一个") {
		t.Fatalf("server_port 字段文案不对：%q", e.Fields["server_port"])
	}
	e = nodeTestErr(t, nodePortConflict(8443, "tcp", ""), errs.Conflict)
	if !strings.Contains(e.Message, "其他节点") {
		t.Fatalf("没有占用者名字时用通用文案：%q", e.Message)
	}
}

func TestNodeWriteErr(t *testing.T) {
	e := nodeTestErr(t, nodeWriteErr(repo.ErrListenClaimTaken, 443, "tcp"), errs.Conflict)
	if e.Fields["server_port"] == "" {
		t.Fatalf("端口冲突应标在 server_port：%v", e.Fields)
	}
	e = nodeTestErr(t, nodeWriteErr(repo.ErrNodeNameTaken, 443, "tcp"), errs.Conflict)
	if e.Fields["name"] == "" {
		t.Fatalf("重名应标在 name：%v", e.Fields)
	}
	nodeTestErr(t, nodeWriteErr(fmt.Errorf("%w: x", repo.ErrInventoryCheck), 0, ""), errs.Conflict)
	other := errors.New("boom")
	if got := nodeWriteErr(other, 0, ""); got != other {
		t.Fatalf("其它错误应原样返回：%v", got)
	}
	if nodeWriteErr(nil, 0, "") != nil {
		t.Fatal("nil 应原样返回")
	}
}
