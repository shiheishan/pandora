// sbcheck：离线版 sing-box check。对每个配置文件做解析加 box.New（校验出站类型、detour、
// TLS 与传输选项，也校验 TUN 入站与规则集的写法），带 -start 时再 Start 一次（能抓到 detour 指向
// 不存在的出站这类启动期错误）。
//
// Start 用的是「离线副本」：订阅附带的模板（w5retain）有 TUN 入站、远程规则集与 cache_file，
// 原样 Start 要 root 权限建 TUN、要联网下载规则集、会在当前目录写缓存。离线副本去掉 TUN 入站
// 与 cache_file，把远程规则集换成同 tag 的内联规则集（规则引用照样要能解析），其余原样；
// 远程规则集的 download_detour 另行核对必须指向存在的出站。原样配置的 box.New 仍然先跑一遍。
//
// 用法：sbcheck [-start] a.singbox b.singbox ...
// 输出：每个文件一行「文件名  OK / PARSE-FAIL / NEW-FAIL / START-FAIL 原因」。
package main

import (
	"context"
	stdjson "encoding/json"
	"flag"
	"fmt"
	"os"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
)

func main() {
	start := flag.Bool("start", false, "box.New 之后再 Start 一次（用离线副本）")
	flag.Parse()
	for _, path := range flag.Args() {
		content, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("%-40s READ-FAIL %v\n", path, err)
			continue
		}
		ctx := include.Context(context.Background())
		opts, err := json.UnmarshalExtendedContext[option.Options](ctx, content)
		if err != nil {
			fmt.Printf("%-40s PARSE-FAIL %v\n", path, err)
			continue
		}
		b, err := box.New(box.Options{Context: ctx, Options: opts})
		if err != nil {
			fmt.Printf("%-40s NEW-FAIL %v\n", path, err)
			continue
		}
		_ = b.Close()
		if *start {
			offline, err := offlineCopy(content)
			if err != nil {
				fmt.Printf("%-40s START-FAIL %v\n", path, err)
				continue
			}
			sctx := include.Context(context.Background())
			sopts, err := json.UnmarshalExtendedContext[option.Options](sctx, offline)
			if err != nil {
				fmt.Printf("%-40s PARSE-FAIL (offline copy) %v\n", path, err)
				continue
			}
			sb, err := box.New(box.Options{Context: sctx, Options: sopts})
			if err != nil {
				fmt.Printf("%-40s NEW-FAIL (offline copy) %v\n", path, err)
				continue
			}
			if err := sb.Start(); err != nil {
				fmt.Printf("%-40s START-FAIL %v\n", path, err)
				_ = sb.Close()
				continue
			}
			_ = sb.Close()
		}
		fmt.Printf("%-40s OK\n", path)
	}
}

// offlineCopy 去掉 TUN 入站与 cache_file，把远程规则集换成同 tag 的内联规则集；远程规则集的
// download_detour 必须指向存在的出站。
func offlineCopy(content []byte) ([]byte, error) {
	var cfg map[string]any
	if err := stdjson.Unmarshal(content, &cfg); err != nil {
		return nil, err
	}
	tags := map[string]bool{}
	if outs, ok := cfg["outbounds"].([]any); ok {
		for _, o := range outs {
			if m, ok := o.(map[string]any); ok {
				if tag, ok := m["tag"].(string); ok {
					tags[tag] = true
				}
			}
		}
	}
	if ins, ok := cfg["inbounds"].([]any); ok {
		kept := []any{}
		for _, in := range ins {
			if m, ok := in.(map[string]any); ok && m["type"] == "tun" {
				continue
			}
			kept = append(kept, in)
		}
		cfg["inbounds"] = kept
	}
	if exp, ok := cfg["experimental"].(map[string]any); ok {
		delete(exp, "cache_file")
	}
	if route, ok := cfg["route"].(map[string]any); ok {
		if sets, ok := route["rule_set"].([]any); ok {
			for i, s := range sets {
				m, ok := s.(map[string]any)
				if !ok || m["type"] != "remote" {
					continue
				}
				if d, ok := m["download_detour"].(string); ok && !tags[d] {
					return nil, fmt.Errorf("rule-set %v: download_detour %q is not an outbound", m["tag"], d)
				}
				sets[i] = map[string]any{
					"type": "inline", "tag": m["tag"],
					"rules": []any{map[string]any{"domain": []any{"offline-check.invalid"}}},
				}
			}
		}
	}
	return stdjson.Marshal(cfg)
}
