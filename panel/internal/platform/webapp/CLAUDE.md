# panel/internal/platform/webapp/
> L2 | 父级: /panel/internal/platform/CLAUDE.md

把一个 Vite 构建产物目录（fs.FS）挂在网关根上只读下发。它存在是因为面板前端要和它调用的 API 同一个二进制发布，而 http.FileServerFS 会列目录、把 index.html 重定向成 ./、按宿主 mime 表猜类型，三件事在静态二进制加 nginx 高熵前缀的部署里都会出错。
数据流：panel/web/app.go 的 AdminApp / PortalApp → Mount(r, fs) → admin 与 public 两个 router 各注册 GET/HEAD /、/assets/*。全局中间件照常生效：SecurityHeaders 先写 no-store，本包只在成功下发时覆盖 Cache-Control，404 保持 no-store。
设计取舍：只认入口 / 与 assets/ 两类路径，根下其余路径继续归 /v1、/healthz 与 public 的订阅通配 /{prefix}/{token}，chi 的静态段优先保证 /assets/x 不会落进订阅处理器；入口 CSP 的 script-src 与 style-src 都只有 'self'，前提是 Vite 产物没有内联脚本、前端不用 CSS-in-JS（React 的 style 属性走 CSSOM 不受约束），由 panel/web/app_test.go 对真实产物验证；CSP 的每类放行与 MIME 表一一对应（图片 img-src、字体 font-src 'self'、json/txt 走 connect-src），.wasm 因此不登记，编译它需要 script-src 'wasm-unsafe-eval'，要用时两处一起加；前端用 hash 路由，缺失资源一律 404，不回退到入口页。

成员清单
webapp.go: Routes 最小路由接口、Mount 注册、Handler 下发器；入口 index.html 启动时读一次并算 ETag，assets/ 走 public immutable；servable 用 fs.ValidPath 并拒绝任何点开头的段
webapp_test.go: fstest.MapFS 模拟产物，覆盖入口 CSP（禁 'unsafe-inline'）与 304、资源 MIME 与 immutable、HEAD、根下非产物/隐藏/穿越路径 404 且保持 no-store、入口缺失不 panic、Mount 只注册读方法

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
