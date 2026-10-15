package render

// SIP008（https://shadowsocks.org/doc/sip008.html），只含 Shadowsocks 节点；id 由地址、端口、名字确定性生成（同一节点每次拉取不变）。

import (
	"encoding/json"
	"strconv"

	"github.com/google/uuid"
)

type sip008Server struct {
	ID         string `json:"id"`
	Remarks    string `json:"remarks"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Password   string `json:"password"`
	Method     string `json:"method"`
}

type sip008Doc struct {
	Version        int            `json:"version"`
	Servers        []sip008Server `json:"servers"`
	BytesUsed      *int64         `json:"bytes_used,omitempty"`
	BytesRemaining *int64         `json:"bytes_remaining,omitempty"`
}

// nodeToSIP008 返回一条 SIP008 的 server 条目；不能渲染时返回 nil 和跳过原因。
func nodeToSIP008(n Node, proxyUUID string) (*sip008Server, string) {
	if n.Type == "shadowtls" {
		return nil, "SIP008 没有 ShadowTLS 的写法"
	}
	if n.Type != "shadowsocks" {
		return nil, "SIP008 只有 Shadowsocks"
	}

	pw, reason := ssPassword(n.Config, proxyUUID)
	if reason != "" {
		return nil, reason
	}

	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(n.Host+":"+strconv.Itoa(n.Port)+"#"+n.Name)).String()
	s := &sip008Server{
		ID:         id,
		Remarks:    n.Name,
		Server:     n.Host,
		ServerPort: n.Port,
		Password:   pw,
		Method:     ssMethod(n.Config),
	}
	return s, ""
}

func renderSIP008(rc renderCtx) ([]byte, string, int) {
	infoNodes, realNodes := rc.splitInfo()
	var realServers []sip008Server
	for _, n := range realNodes {
		if s, _ := nodeToSIP008(n, rc.uuid); s != nil {
			realServers = append(realServers, *s)
		}
	}

	servers := make([]sip008Server, 0)
	if len(realServers) > 0 {
		for _, n := range infoNodes {
			if s, _ := nodeToSIP008(n, rc.uuid); s != nil {
				servers = append(servers, *s)
			}
		}
		servers = append(servers, realServers...)
	}

	doc := sip008Doc{
		Version: 1,
		Servers: servers,
	}

	if rc.status.Total > 0 {
		used := rc.status.Upload + rc.status.Download
		rem := rc.status.Total - used
		if rem < 0 {
			rem = 0
		}
		doc.BytesUsed = &used
		doc.BytesRemaining = &rem
	}

	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return []byte("{}"), "application/json; charset=utf-8", 0
	}
	return b, "application/json; charset=utf-8", len(realServers)
}
