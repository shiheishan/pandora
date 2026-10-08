---
paths:
  - "panel/frontend/tests/browser/**"
---

# 面板前端：购买路径的无头浏览器测试

- 分工：`tests/smoke/` 证明页面拿真数据不崩，`panel/tests/*.sh` 证明接口在真库上对，这里证明「用户在门户和后台的页面上点得通，看到的金额、到期日、按钮、错误提示都对」
  - 路径清单照 w8walk 第 2 节的 31 步改写，登记在 `paths.ts`（结果表按它的顺序出行，没跑到的也占一行）
  - 每步用 `fixtures.ts` 的 `step(page, id, body)`：换一个来源地址、跑完截一张 1280 宽的图，`body` 返回「页面上看到的」那句，写进产物里的 `steps.jsonl`；关键页面另用 `narrowShot` 截 375 宽
- 只在 CI 的 `panel-smoke.yml` 里跑，排在压测之后、最后一个：
  - 起栈之前 `make -C panel frontend-embed`，网关嵌的是真前端产物：门户在门户网关根上，后台在后台网关根上（冒烟栈没有 nginx 前缀）
  - 种子 `seed.ts` 是 globalSetup，只经后台真实接口造：五个套餐（绑冒烟种子的节点池）、12G 流量包、六种礼品卡模板、演示易支付渠道 `w9pay`（随机商户号与密钥，`allow_private_host` 只在非生产放行）
  - 它把其余 CNY 渠道暂停收新单，让站点最低付款额变成 ¥1.00（凑最低额、免零头、低于最低额被拦都要这个前提），所以必须排在所有会下单的步骤之后；再轮询门户报价直到 `min_payment` 为 100（各网关按进程缓存一分钟），不用固定等待
  - 结果表由 `table.ts` 出，写进 job summary；截图、失败的 trace、HTML 报告在产物 `browser-paths`
- 本机跑（要一套已起的冒烟栈，本机没有 Docker 时只做类型检查与 lint）：
  - `panel/deploy/run-smoke-stack.sh up panel <状态目录>`（起栈前先 `make -C panel frontend-embed`）→ `node panel/frontend/tests/smoke/seed.ts <状态目录>`
  - 在 `panel/frontend`：`npx playwright install --only-shell chromium`，再 `SMOKE_STATE=<状态目录> npx playwright test -c tests/browser/playwright.config.ts`；`node tests/browser/table.ts <状态目录>/browser` 出表
  - 种子写在状态目录的 `browser-world.json`（0600），文件在就整份复用，同一套栈重跑不撞套餐编码
  - 类型检查走 `npm run typecheck`（`tsconfig.node.json` 收 `tests/` 下除 smoke 外的文件），lint 走 `npm run lint`
- 选择器只用角色与可见文字（`getByRole` / `getByText` / `getByLabel`），不为测试给组件加 `data-testid`
  - 我的套餐的卡片是 `<article>`、套餐卡是 `<section>`，按标题 heading 过滤（`card` / `planCard`）；完成页按「发生了什么」下的列表读（`happened`）
  - 开关（ui/Switch）的透明 checkbox 被轨道盖住，点外面那层 label（`setSwitch`），和人点的是同一处
  - 后台写操作要重新认证时，`openAdmin` 挂的 `addLocatorHandler` 自动在弹框里填口令；登录自带 15 分钟窗口，通常不出现
- 每条路径一个现场注册的新用户，互不依赖，`fullyParallel` 4 个 worker；前提（赠送开一份、调余额、发卡号）经后台接口造，要验证的那一步在页面上点
- 来源地址：网关在冒烟栈上直接信 `X-Real-IP`（见 panel-e2e 规则）。浏览器按机器速度点，门户每 IP 每分钟 120 次、后台 240 次会被打满，所以每个用户、每一步各用一个 198.18.0.0/15 里的虚构地址（`api.ts` 的 `freshIp`），相邻两个落在不同的 /24
- 付款：付款页「在这台电脑上付款」（或「打开…付款页」）的地址就是收银台地址，读出金额与订单号，按易支付规则签名后打真实的 `/v1/webhooks/payments/w9pay`（与 `panel/tests/epay_e2e.sh` 同一口径）；页面自己查单走到完成页，测试不替它跳
- SQL 夹具（`api.ts` 的 `sql`）只用于接口造不出或要等时间流逝的数据，每处写明原因。现有四处：付款期限拨到过去（A0b、C5，只改 `orders.expires_at`、不动预留，释放任务不会中途关单）、本期已用 5G（C6b）、旧流量包的 migration 流水（C7，照 00138）、周期挪到过去（过期卡片，照 `expiry_e2e.sh`，状态等过期扫描自己翻）
- 被产品问题卡住的步骤：测试改成 `test.fixme`，原因登记进 `paths.ts` 的 `PRODUCT_ISSUES`（表里记「失败（产品问题）」），复现与期望写进任务报告，不在测试里绕过、不放宽断言
- 加一条路径：`paths.ts` 加一行 → 在对应的 `*.spec.ts` 里用同一个 id 写 `step` → 用到新的种子数据就加进 `seed.ts`（会改 `browser-world.json` 的形状，本机删掉旧文件重跑）
