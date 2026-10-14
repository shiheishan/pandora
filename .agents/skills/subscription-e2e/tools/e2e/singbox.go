// sing-box 客户端：按面板渲染给用户的 sing-box 订阅配置出站，连接 NativeCore 节点端。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	sjson "github.com/sagernet/sing/common/json"
)

func runSingbox(id, typ string, port int, kcfg map[string]any, kcfgErr string, env *caseEnv) string {
	sbRaw, err := os.ReadFile(filepath.Join(*outDir, id+".singbox"))
	if err != nil {
		return "NO-RENDER"
	}
	var sb map[string]any
	must(json.Unmarshal(sbRaw, &sb))
	var proxyOut map[string]any
	var extraOuts []any
	outs, _ := sb["outbounds"].([]any)
	for _, o := range outs {
		m := o.(map[string]any)
		switch m["type"] {
		case "selector", "urltest", "direct", "block", "dns":
			continue
		}
		if m["tag"] == id {
			proxyOut = m
		} else {
			// 成对出站（如 shadowtls 外层）一起带上
			extraOuts = append(extraOuts, m)
		}
	}
	if proxyOut == nil {
		return "SKIPPED-IN-SINGBOX (订阅里没有这个节点)"
	}

	srvPort, fail := startNode(id, typ, port, kcfg, kcfgErr, env)
	if fail != "" {
		return fail
	}

	// ---- 客户端（sing-box，出站原样取自订阅，只改端口与信任） ----
	proxyOut["server_port"] = srvPort
	if t, ok := proxyOut["tls"].(map[string]any); ok && t["enabled"] == true {
		if _, reality := t["reality"]; !reality {
			t["certificate"] = []string{env.certPEM}
		}
	}
	for _, e := range extraOuts {
		m := e.(map[string]any)
		if m["server_port"] != nil {
			m["server_port"] = srvPort
		}
		if t, ok := m["tls"].(map[string]any); ok && m["type"] == "shadowtls" {
			t["certificate"] = []string{decoyCertPEM}
		}
	}
	tagName := proxyOut["tag"].(string)
	mixedPort := freePort()
	client := map[string]any{
		"log": map[string]any{"level": *logLevel},
		"dns": map[string]any{"servers": []any{map[string]any{"type": "hosts", "tag": "hosts",
			"predefined": map[string]any{"node.example.com": "127.0.0.1"}}}},
		"inbounds":  []any{map[string]any{"type": "mixed", "tag": "in", "listen": "127.0.0.1", "listen_port": mixedPort}},
		"outbounds": append(append([]any{proxyOut}, extraOuts...), map[string]any{"type": "direct", "tag": "direct"}),
		"route":     map[string]any{"final": tagName, "default_domain_resolver": "hosts"},
	}
	cb, _ := json.Marshal(client)
	// 留一份客户端配置，排查时可以直接拿去手跑
	_ = os.WriteFile(filepath.Join(env.tmp, id+".client.json"), cb, 0o644)
	sctx := include.Context(context.Background())
	opts, err := sjson.UnmarshalExtendedContext[option.Options](sctx, cb)
	if err != nil {
		return "CLIENT-PARSE-FAIL " + err.Error()
	}
	b, err := box.New(box.Options{Context: sctx, Options: opts})
	if err != nil {
		return "CLIENT-NEW-FAIL " + err.Error()
	}
	cleanup = append(cleanup, func() { b.Close() })
	if err := b.Start(); err != nil {
		return "CLIENT-START-FAIL " + err.Error()
	}

	return fetchViaSOCKS(fmt.Sprintf("127.0.0.1:%d", mixedPort), env.target)
}
