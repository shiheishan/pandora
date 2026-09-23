// Package web 把前端资源编进二进制。
//
// 为什么用 embed 而不是让 nginx 直接托管静态目录：
//
//	· 版本一致性 —— 页面与它调用的 API 是同一个构建产物，
//	  不会出现「二进制回滚了但页面没回滚」的错配；
//	· 部署简单 —— 只需分发一个文件，没有静态目录同步问题；
//	· 首版页面很小（单文件、零外部依赖），内存代价可以忽略。
//
// 前端体量长大到需要独立构建流水线时，再换成 nginx 托管 + 版本化路径。
package web

import (
	_ "embed"
)

//go:embed portal/index.html
var PortalHTML []byte

//go:embed admin/index.html
var ConsoleHTML []byte
