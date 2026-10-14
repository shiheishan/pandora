// yamlcheck：Clash（mihomo / Premium）订阅的静态检查。本机没有 mihomo，只做 YAML 结构解析，
// 再按 mihomo 文档核对几类必备字段——它们缺了 mihomo 不报错，只是那个节点连不上。
//
// 用法：yamlcheck a.clash b.clash-premium ...
// 输出：每个文件一行「文件名  YAML-OK (proxies=N)」，有静态问题时每条问题一行「YAML-WARN 说明」，
// 解析失败为「YAML-FAIL 原因」。
package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func main() {
	for _, p := range os.Args[1:] {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Printf("%-36s READ-FAIL %v\n", p, err)
			continue
		}
		var doc struct {
			Proxies []map[string]any `yaml:"proxies"`
			Groups  []map[string]any `yaml:"proxy-groups"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			fmt.Printf("%-36s YAML-FAIL %v\n", p, err)
			continue
		}
		var issues []string
		for _, g := range doc.Groups {
			ps, _ := g["proxies"].([]any)
			if len(ps) == 0 && g["use"] == nil {
				// mihomo 对空组整份拒载：`use` or `proxies` missing
				issues = append(issues, fmt.Sprintf("group %v 的 proxies 为空（mihomo 整份拒载）", g["name"]))
			}
		}
		for _, px := range doc.Proxies {
			if px["server"] == nil || px["port"] == nil {
				issues = append(issues, fmt.Sprintf("%v: 缺 server 或 port", px["name"]))
			}
			network, _ := px["network"].(string)
			switch network {
			case "ws":
				if px["ws-opts"] == nil {
					issues = append(issues, fmt.Sprintf("%v: network=ws 却没有 ws-opts(path/headers.Host)", px["name"]))
				}
			case "grpc":
				if px["grpc-opts"] == nil {
					issues = append(issues, fmt.Sprintf("%v: network=grpc 却没有 grpc-opts.grpc-service-name", px["name"]))
				}
			case "httpupgrade":
				issues = append(issues, fmt.Sprintf("%v: mihomo 没有 network=httpupgrade（应为 ws + ws-opts.v2ray-http-upgrade: true）", px["name"]))
			case "xhttp":
				if px["xhttp-opts"] == nil {
					issues = append(issues, fmt.Sprintf("%v: network=xhttp 却没有 xhttp-opts", px["name"]))
				}
			}
			if px["type"] == "vless" && px["tls"] == true && px["reality-opts"] == nil && px["servername"] == nil {
				issues = append(issues, fmt.Sprintf("%v: vless tls=true 但无 reality-opts / servername（REALITY 节点被渲染成普通 TLS）", px["name"]))
			}
			if ro, ok := px["reality-opts"].(map[string]any); ok && ro["public-key"] == nil {
				issues = append(issues, fmt.Sprintf("%v: reality-opts 缺 public-key", px["name"]))
			}
		}
		if len(issues) == 0 {
			fmt.Printf("%-36s YAML-OK (proxies=%d)\n", p, len(doc.Proxies))
			continue
		}
		for _, i := range issues {
			fmt.Printf("%-36s YAML-WARN %s\n", p, i)
		}
	}
}
