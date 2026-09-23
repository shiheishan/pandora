// [INPUT]: 依赖 admin/app、portal/app 两个目录（make frontend-embed 从 frontend/dist 同步来的 Vite 产物，未构建时只有占位 index.html）
// [OUTPUT]: 对外提供 AdminApp、PortalApp 两个 fs.FS，根即各自的 index.html
// [POS]: web 的 React 候选前端嵌入点，被 admin / public 两个 router 经 platform/webapp 挂到 /app/；与 embed.go 的旧单页并存，互不引用
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package web

import (
	"embed"
	"io/fs"
)

// all: 前缀不能省：Rolldown 会产出下划线开头的公共 chunk，默认规则会把它们漏掉，
// 页面就会在运行时才 404。点开头的 .vite/manifest.json 由 frontend-embed 排除。
//
// 仓库里只提交占位 index.html，保证没有 npm 的机器上 go build / go test 照常通过；
// 发布包由 deploy/build-release.sh 先跑 make frontend-embed，占位页进不了发布物。
//
//go:embed all:admin/app all:portal/app
var appFS embed.FS

var (
	AdminApp  = mustSub("admin/app")
	PortalApp = mustSub("portal/app")
)

func mustSub(dir string) fs.FS {
	sub, err := fs.Sub(appFS, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
