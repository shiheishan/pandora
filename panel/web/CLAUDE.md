# panel/web/
> L2 | 父级: /panel/CLAUDE.md

面板前端从这里经 go:embed 编进 aegis-admin 与 aegis-public 二进制，页面与它调用的 API 永远是同一个构建产物。两个网关经 platform/webapp 把它挂在网关根：/ 是入口，/assets/* 是带 hash 的产物，其余根路径（/v1、/healthz、订阅通配）归各自的路由。
2026-09-23 起旧的两套前端（手写单页与 React 候选）已整体删除，前端按设计稿在 panel/frontend 重写；重写完成前两个入口下发的都是占位页。
仓库里 admin/、portal/ 只提交带 pandora-placeholder 标记的占位 index.html，保证没有 npm 的机器上 go build / go test 照常通过；make frontend-embed 用 frontend/dist/{admin,portal} 的真实产物覆盖这两个目录，deploy/build-release.sh 在 Go 构建前强制执行并拒绝占位页进入发布物。

成员清单
app.go: go:embed all:admin all:portal，经 fs.Sub 导出 AdminApp / PortalApp；all: 前缀保住 Rolldown 下划线开头的 chunk
admin/、portal/: 两个域的嵌入目录；仓库只有占位 index.html，其余文件由 make frontend-embed 生成并被根 .gitignore 排除
app_test.go: 嵌入完整性契约：入口存在且 pandora-app 域标记正确；嵌入真实产物时再查无内联脚本与内联样式（配合 webapp 的 script-src/style-src 'self'）、入口引用的 ./ 资源全部在 assets/ 下且在包内、.vite 元数据未嵌入

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
