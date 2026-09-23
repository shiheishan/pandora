# panel/web/
> L2 | 父级: /panel/CLAUDE.md

网关下发的全部前端都从这里经 go:embed 编进 aegis-admin 与 aegis-public 二进制，页面与它调用的 API 永远是同一个构建产物。两代前端并存：
生产入口仍是 / 下的两份手写单页（零外部依赖，内联 CSS 与 JS，网关按内容 SHA-256 做 ETag）；React 候选前端（panel/frontend 的 Vite 产物）经 platform/webapp 挂在 /app/，旧控制台顶栏有一个“试用新版”入口。切换条件是 frontend/tests/legacy-parity.ts 迁移清单归零，之后 / 改下发 React、旧页挪到 /legacy/ 保留一个版本。
仓库里 admin/app、portal/app 只提交占位 index.html，保证没有 npm 的机器上 go build / go test 照常通过；make frontend-embed 用真实产物覆盖它们，deploy/build-release.sh 在 Go 构建前强制执行并拒绝占位页进入发布物。所有 Playwright 验收（docs/pw_smoke_r*.cjs、deploy/test-*-playwright.*）都按两份手写单页的 data-page 与元素 id 编写，切换时这些证据要用 React 选择器重建。

成员清单
embed.go: go:embed 两个单文件为 ConsoleHTML / PortalHTML 字节，由 api/admin 与 api/public 的 handlers 在 GET / 下发；文件形状，与 app.go 互不引用
app.go: go:embed all:admin/app all:portal/app，经 fs.Sub 导出 AdminApp / PortalApp；all: 前缀保住 Rolldown 下划线开头的 chunk
admin/index.html: 管理控制台，23 个 data-page 视图；内联 api() 封装相对 ADMIN_BASE 发请求，兼容 nginx 高熵前缀；按钮按 data-perm 门控；顶栏 lnkNewConsole 的 href 由脚本改写为 ADMIN_BASE + /app/（带不带尾斜杠打开都落在管理前缀下）
portal/index.html: 用户门户，9 个 data-page 视图；注册、通知中心、Telegram 绑定、快捷登录、充值、工单撤回等 React 尚未覆盖的操作在这里，清单见 frontend/tests/legacy-parity.ts
admin/app/、portal/app/: React 候选前端的嵌入目录；仓库只有带 pandora-placeholder 标记的占位 index.html，其余文件由 make frontend-embed 生成并被 .gitignore 排除
embed_test.go: 对两份手写单页源文本的字面契约：data-page 与权限标记、api 路径、ARIA、CSS 断点、禁止子串、粘连关键字扫描；守的是页面文本，不可移植到打包产物
app_test.go: 嵌入完整性契约：入口存在且 pandora-app 域标记正确；嵌入真实产物时再查无内联脚本（配合 webapp 的 script-src 'self'）、入口引用的 ./ 资源全部在包内、.vite 元数据未嵌入

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
