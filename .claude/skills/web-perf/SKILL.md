---
name: web-perf
description: pandora 面板网页打开性能的测量：静态分析（产物大小、首屏 JS/CSS、路由分包、依赖构成）、本机假后端造 N 个节点的后台节点页基准（首屏、重拉、长任务、滚动、常驻动画）、线上实测（curl 验 gzip 与 HTTP/2 和缓存头，Lighthouse 冷热缓存 × 桌面移动 × 空闲压测）。改了前端分包或依赖、nginx 模板与下发链路、实时事件与重拉节奏，或要出网页性能报告、验收性能类前端改动时使用。
---

# 网页打开性能

目标：每个结论都有数字，而且能在同一台机器、同一份产物上复现。

测量全部在会话 scratchpad 里的前端拷贝上做：
- 不在仓库的 `panel/frontend` 里装东西、打补丁，也不改它的 `package.json`；
- 测量工具（puppeteer-core 驱动本机 Chrome、Lighthouse、source-map-explorer）按本 skill 的 `package.json` 和 `package-lock.json` 锁定版本，装在 scratchpad。

## 步骤

所有子命令都接受 `--work <scratchpad>/webperf`，不给时用 `$TMPDIR/pandora-web-perf`。

1. **准备**：`bash .claude/skills/web-perf/scripts/run.sh prep --work <W>`
   - 在 `W/fe` 拷一份前端并打性能补丁，测量工具装到 `W/tools`；lock 文件没变时跳过 `npm ci`。要测 worktree 就加 `--repo <worktree>`。
2. **静态分析**：`run.sh static --work <W>`
   - 构建两个入口，出首屏大小（原始 / gzip / brotli）、各路由分包大小、按 npm 包的依赖构成，结果在 `W/results/static/`；每个脚本做什么、注意点见 `scripts/` 里各文件头。
3. **本机 N 节点基准**：`run.sh bench --work <W> [--nodes 1000] [--scenarios …] [--throttle "1 4 6"] [--observe 20]`
   - 每个场景起一个 `vite preview`（admin 入口加假后端），用 `nodes-bench.mjs` 跑「场景 × CPU 降速」，出一张表（`W/results/bench/summary.md`）。
   - 默认三个场景：
     - `quiet:none:0`：心跳不推事件；
     - `refetch2s:none:2000`：每 2 秒推一条，强制每 2 秒重拉；
     - `storm:old:0`：每次心跳都推，1000 个节点约 33 条/秒，即 nodes 通知触发器跳过纯心跳（w3live 的迁移 00110）之前的行为。
   - 场景串的第二段是 `PERF_TRIGGER`：`old` 模拟迁移 00110 之前（每次心跳都推 `nodes.changed`，前端按 2 秒节流整表重拉 1.26 MB），`new` 模拟 00110 之后（只在在线状态翻转时才推，如 `flip:new:0`）。测前端重拉节奏的改动，两种都跑。
4. **常驻动画**：`run.sh idle --work <W>`，同一页面动画开、关各静置 10 秒，比主线程 Task 毫秒数。
5. **线上实测**（测试机，不在面板机本机上跑，压测期间先和总协调确认）：
   1. 先跑 `run.sh online https://<站点> --admin <后台前缀>`，用 curl 核对：
      - 协议是 2；
      - JS、CSS 带 `Content-Encoding`；
      - `/assets/*` 是 `public, max-age=31536000, immutable`；
      - index.html 是 `no-cache` 加 ETag，带 If-None-Match 再请求回 304；
      - 每个首屏资源的原始字节、传输字节、TTFB。
   2. 再跑 Lighthouse：`TOKEN=<令牌> run.sh lighthouse "https://<站点>/#/overview" --app portal --runs 5`。
      - 后台用 `--app admin`，URL 写 `https://<站点>/<前缀>/#/dash`。
      - 默认跑 cold/warm × desktop/mobile，每格 5 次取中位数，原始数据是 `W/results/lighthouse/*.jsonl`。
      - 登录页不需要 TOKEN。
   3. 「空闲」和「压测中」各跑一轮。压测按 prod-retest skill 起，两轮的结果分开存。
   4. 节点页实测：`ADMIN_TOKEN=<令牌> EXPECT_ROWS=<实际节点数> node W/tools/nodes-bench.mjs https://<站点>/<前缀>/ 1 20`。
6. **记录**：交付报告里按下一节列数字，并写明机器、网络、Chrome 版本、是否无头。域名、IP、后台前缀一律写成占位符（`panel.example.com`、`<后台前缀>`）。

## 记录哪些指标

- **静态**：每个入口首屏 JS、CSS 的原始 / gzip / br；后台看板、门户概览、门户订阅页三个路由的增量；全部 JS+CSS 合计；最大的 10 个 chunk；第三方依赖各多大。
- **1000 节点基准**：
  - 加载：首屏（990 行进 DOM 的时刻）、加载期长任务数和最长那个、列表响应字节；
  - 静置窗口：nodes.changed 条数、重拉次数、每次重拉的脚本毫秒、长任务，以及 Task / Script / Style / Layout；
  - 交互：滚动 fps、p95、卡顿帧，全选和搜索到下一帧。
- **线上**：
  - Lighthouse 的 TTFB、FCP、LCP、TBT、CLS、Speed Index、性能分；
  - 请求数、传输 KB、协议；
  - 最慢的几个接口，重点看 `v1/me` 和后台看板的 6 个接口。
  - curl 表里每个首屏资源的编码、缓存头与耗时。
  - 服务端同时采 nginx 访问日志里 `/assets/` 的 request_time 和 upstream_time，空闲与压测中对比。

基线数字（静态体积、1000 节点基准、常驻动画）在 `ops-local/web-perf/baseline-from-skill.md`，不在 skill 里；出新报告时追加到那个文件。

## 环境变量

脚本是什么看文件头，这里只列看不出来的：`PERF_NODES`、`PERF_NODES_EVENT_MS`（假后端补丁）；`ADMIN_TOKEN`、`EXPECT_ROWS`（nodes-bench 指向测试机）；`KILL_CSS`（idle-cpu 只关某一个动画）；`TOKEN`、`TOKEN_KEY`（lh.mjs 的登录令牌）；`CHROME`（Chrome 默认 `/Applications/Google Chrome.app`，换路径时设）；`MOCK_EMAIL`、`MOCK_PASSWORD`（假后端账号默认是 `dev/mock-api.ts` 的 `MOCK_ACCOUNTS`）。`patch-mock.py` 锚点对不上时报错退出。

## 坑

- **无头 Chrome 的数字偏大**（没有 GPU 合成）：只做同一台机器、同一次会话、机器空闲时的开关或改前改后对照，每格至少跑两遍；绝对值以有 GPU 的真浏览器复核为准。
- **常驻动画**：改任何常驻动画，都用 `run.sh idle` 验。
- **上线后必验**：`run.sh online` 看 JS、CSS 有没有 `Content-Encoding`（不能只读 nginx 配置：已装机器要重跑 install.sh 重新渲染才生效）；改 router 后用 curl 连打 `/assets/` 确认不 429（静态资源不应经过限流）。HTTP/1.1 下每个标签页的 SSE 长期占一条连接，开 h2 后 `limit_conn` 上限是 64。
- **看源站**：用回环 `127.0.0.1:9080` 或 `--resolve`（前面有 Cloudflare 时 curl 和 Lighthouse 量的是边缘）。
- **SSE 让网络永远不空闲**（同 flow-walk）：puppeteer、Lighthouse 都不能等 `networkidle`，要等 `load` 再加固定延时。只差 `#路由` 的导航是同文档跳转，Lighthouse 量不到绘制，报 NO_FCP，所以 `lh.mjs` 的热缓存先跳到 `about:blank` 再回来。
- **冷缓存会清掉 localStorage**，令牌是每个新文档开始时由 `evaluateOnNewDocument` 重新写入的（键名见 flow-walk「坑」）。令牌是登录后的会话令牌（不在 1Password 里）：由用户本人在浏览器登录后取出（`localStorage` 里，键名同上）交给你，经环境变量传入，不落盘、不写进结果文件。
- **nodes-bench 依赖页面文案和列表形态**：
  - 依赖 aria-label「选择 <名>」「全选当前列表」「搜索节点」，改了文案要同步改脚本。
  - 列表改成服务端翻页或搜索后，`/v1/nodes?` 的参数和每页行数都会变，`EXPECT_ROWS` 要跟着改。搜索那一项只量到发请求前的那一帧。
- **假后端补丁靠锚点**：`patch-mock.py` 锚在 `dev/mock/admin/nodes.ts` 的 `store[0]!.routing = …` 和 `dev/mock-api.ts` 的 SSE 保活定时器、`mockApi` 插件块上。假后端改过之后锚点对不上，脚本会报错退出，照报错位置更新脚本，不要手改拷贝凑合。
- **sizes 用的是 gzip-9**，nginx 实际是 5 级，线上传输会略大。「首屏」只算 index.html 引用的文件和静态 import；后台路由分包原先要等 `v1/me` 回来才开始下，现在有 `admin/prefetch.ts` 并行预取，看串行往返要用 Lighthouse 的网络瀑布确认。
- 拷贝里的依赖坏了，删掉 `W/fe/node_modules` 再 prep（lock 文件没变时 run.sh 会跳过 `npm ci`）。
