// sbcheck：离线版 sing-box check。对每个配置文件做解析加 box.New（校验出站类型、detour、
// TLS 与传输选项），带 -start 时再 Start 一次（能抓到 detour 指向不存在的出站这类启动期错误）。
//
// 用法：sbcheck [-start] a.singbox b.singbox ...
// 输出：每个文件一行「文件名  OK / PARSE-FAIL / NEW-FAIL / START-FAIL 原因」。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
)

func main() {
	start := flag.Bool("start", false, "box.New 之后再 Start 一次")
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
		if *start {
			if err := b.Start(); err != nil {
				fmt.Printf("%-40s START-FAIL %v\n", path, err)
				_ = b.Close()
				continue
			}
		}
		_ = b.Close()
		fmt.Printf("%-40s OK\n", path)
	}
}
