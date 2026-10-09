---
name: ci-triage
description: pandora 的 CI 变红后怎么查：一条命令把某个提交的检查机结论、GitHub 各失败 run 的失败测试、首条报错、SQL 报错、前端、e2e 与浏览器购买路径（Playwright）的失败压成一页，并按已知偶发项、整合问题、前提没满足、已登记的产品问题、真缺陷分流。wait-status.sh / wait-github.sh 退出非 0、子 agent 报告 CI 红、合并后主线变红时使用。
---

# CI 变红怎么查

目标：几分钟内回答三件事——哪些测试红了、首条报错是什么、是偶发还是要改代码。各 job 管什么见 verify skill；本 skill 只管「红了之后」。

## 一条命令

```bash
bash .claude/skills/ci-triage/scripts/triage.sh <sha 或分支> [输出目录]
```

- 只读。完整失败日志按 run id 存进输出目录（`<id>.log` 原文、`<id>.txt` 去掉前缀与颜色码），默认 `$TMPDIR/ci-triage-<sha>`；会话里放 scratchpad。
- 每个失败 run 打印：失败步骤、Go `--- FAIL` 与它上方最近一条 `_test.go:行号:` 报错、SQLSTATE 汇总、vitest 失败与接口状态码、e2e 脚本的首个失败段、浏览器购买路径的失败步骤（下一节）、命中的已知偶发项。
- 要查某个测试的上下文：`grep -n -B30 -- '--- FAIL: <测试名>' <输出目录>/<id>.txt`。

## 分流

0. **先核分支**：`wait-github.sh` 退出 1 时，同一个 sha 推到两个分支可能把别的分支的 run 算进来（w9quic 的误判）。先 `gh run view <id> --json headBranch -q .headBranch` 核一下红的 run 是不是当前分支的；`triage.sh` 传分支名（或 sha 加第三个参数）时只取该分支的 run，只给 sha 时每行带 `[headBranch]`，跨了多个分支会提示。
1. **已知偶发**，不改代码，重跑：
   - `TestIdempotencyMiddlewarePG18` 锁超时 → `gh run rerun <id> --failed`；
   - Actions 因付款失败没启动 → `gh run rerun <id>`；
   - 同一提交有时先绿、再出一组新运行 → 以最新一组为准；
   - 检查机红、GitHub 同一 job 绿（例：10-07 fda8a31 的 linux-race，GitHub 与本机 `-race -count=3` 都过）→ 先当偶发，`MEMOH_FRESH=1 wait-status.sh <sha>` 重跑一次；第二次还红再查，查时先怀疑检查机的容器环境（`/dev/fd`、apt 包随重启丢失），不要直接改代码。已知原因：检查机的循环忽略 SIGHUP，bash 测试里装不上 HUP trap（入口时已忽略的信号 bash 不能 trap 或复位；10-09 w12native 397b8bc 查清，本机 `trap '' HUP` 可复现）；测试要先用 python3 或 perl 把 SIGHUP 复位成缺省再起被测脚本，做法见该提交。检查机日志在云电脑 `/data/memoh-ci/logs/<sha>/<job>.log`，本机读不到。
   - **观察中**（还没查清，不算已知偶发）：先重跑一次，第二次还红再当真缺陷查；条目和已有数据在 `.claude/TASKS.md`，别复制进来。
     - pdnd `TestDelUsersEndsQUICUDPSessions`（检查机 linux-race）：见「节点遗留」；
     - smoke 浏览器 A4 续费日期：见「w9https 后续」；
     - `portal-buy.spec.ts` 套餐卡标题断言：见「w9quic 后续」⑥；
     - pdnd `TestLongConnectionTrafficIsReportedEachPeriod`（检查机）：见「节点遗留」；
     - pdnd shadowtls `TestServiceV1HandshakeClearsDeadline`（检查机）：见「节点遗留」；
     - 检查机的 panel-frontend 单元测试：见「节点遗留」（行里搜 `d4ca344`）；
     - certs 域的 pebble 端口占用（`bind: address already in use`，GitHub 的 panel-pg18）：见「节点遗留」（行里搜 `pebble`）。
2. **一处报错连带一片**：先看「SQL 报错」和 e2e 首个失败。同一条 SQLSTATE 出现在几个测试里，通常是一条共享查询坏了，修一处全好（10-07：ListLinks 漏逗号 → 门户链接 500、两个 PG18 用例、expiry/risk e2e、两个前端冒烟一起红）。
3. **整合问题**（几路并行合并后才出现）：
   - 计数类断言：内置模板数、后台循环数（`workers.Add` 与两份契约测试）、`run-pg18-gates.sh` 的 DOMAINS、`run-smoke-e2e.sh` 的 SCRIPTS；
   - 同一个函数被两路各自 `CREATE OR REPLACE`（如 `app.seed_tenant_defaults`），见根 CLAUDE.md「环境与工具坑」与 new-migration skill；
   - 一路改了签名或口径，另一路的新测试还按旧的写（返回值个数、状态名、枚举个数）；
   - 夹具 id 撞号（同一 PG18 域库共用），见 `.claude/rules/platform-pg18.md`。
4. **审计、限频类 e2e**：smoke 里各脚本从同一来源 IP 打同一个栈，按 IP 窗口去重的逻辑会被前一个脚本的记录吞掉。先确认是产品语义（要不要豁免）还是脚本假设，再改。
5. **浏览器购买路径**：见下一节。
6. **真缺陷**：在本机复现（PG18 用例本机没有 Docker 会跳过，跳过不等于通过）。修完按 verify skill 重跑改到的包（本机 go 命令的 `GOTOOLCHAIN` 也见 verify），推送后再等两个脚本。

## 浏览器购买路径（panel-smoke 的 Playwright 步骤）红了

做法与约定在 `.claude/rules/frontend-browser-e2e.md`（只在改 `tests/browser/**` 时自动加载，查 CI 时手动读）。`triage.sh` 已经做了前三步：

1. **先看结果表**：脚本从同一 run 的完整日志里取「Browser path table」那步的输出（job summary 里也是这张表），按最后一列分三类：
   - 「失败」：这次红的原因，逐条列出步骤 id、页面上看到的和截图名；
   - 「失败（产品问题）」：`paths.ts` 的 `PRODUCT_ISSUES` 里已登记、测试是 `test.fixme`，不跑，不是这次红的原因；
   - 「未执行」：同一个 test 里前一步先失败，或在步骤之外就报错了（登录、种子），看失败那步和报错。
2. **再看报错**：Playwright 的失败标题（`N) tests/browser/<文件>:<行> › <test> › <步骤>`）和 `Error:` 行。
3. **认出前提没满足**（不是页面回归，先查栈与种子）：
   - 「门户报价的最低付款额 90 秒内没变成 100」：种子没能把站点最低付款额压到 ¥1.00（其他 CNY 渠道没暂停、或网关渠道缓存没过期）；凑最低额、免零头、低于最低额这几步都会跟着错；
   - 「返回 429」「rate_limited」：来源 IP 没换开、限流打满（每步要用 `freshIp`）；
   - 「套餐 … 没发布出去」「SQL 夹具「…」失败」：种子或夹具本身坏了；
   - `POST /v1/auth/login 返回 500` 一类接口 5xx：后端问题，按 requestId 去同一提交的 PG18 run 或网关日志找堆栈。
4. **要看截图和 trace 时**才下产物（十几到几十 MB，本机经代理很慢）：`gh run download <id> -n browser-paths -D <目录>`，看 `shots/<步骤>-fail.png` 和 `test-results/*/trace.zip`（`npx playwright show-trace`）。Playwright 的 `error-context.md` 取的不一定是出错的那个页面。
5. **分流**：页面文字或结构改了、测试没跟，就同改 `tests/browser`（规则 screens-portal.md、screens-admin-billing.md 有这条）；产品问题一时修不了的 `test.fixme` 与 `PRODUCT_ISSUES` 做法见 `.claude/rules/frontend-browser-e2e.md`；修好了已登记的问题要去掉那一行，否则表里一直显示「失败（产品问题）」。

## 坑

- 一次推多个提交只有最新那个有 run，旧提交显示「没有 run」不代表没红。
- 前端冒烟里 `500 internal_error` 只有 requestId，没有后端堆栈；对应的后端报错常常也在同一提交的 PG18 run 里，先看那边。
