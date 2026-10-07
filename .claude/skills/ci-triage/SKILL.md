---
name: ci-triage
description: pandora 的 CI 变红后怎么查：一条命令把某个提交的检查机结论、GitHub 各失败 run 的失败测试、首条报错、SQL 报错、前端与 e2e 失败压成一页，并按已知偶发项、整合问题、真缺陷分流。wait-status.sh / wait-github.sh 退出非 0、子 agent 报告 CI 红、合并后主线变红时使用。
---

# CI 变红怎么查

目标：几分钟内回答三件事——哪些测试红了、首条报错是什么、是偶发还是要改代码。各 job 管什么见 verify skill；本 skill 只管「红了之后」。

## 一条命令

```bash
bash .claude/skills/ci-triage/scripts/triage.sh <sha 或分支> [输出目录]
```

- 只读。完整失败日志按 run id 存进输出目录（`<id>.log` 原文、`<id>.txt` 去掉前缀与颜色码），默认 `$TMPDIR/ci-triage-<sha>`；会话里放 scratchpad。
- 每个失败 run 打印：失败步骤、Go `--- FAIL` 与它上方最近一条 `_test.go:行号:` 报错、SQLSTATE 汇总、vitest 失败与接口状态码、e2e 脚本的首个失败段、命中的已知偶发项。
- 要查某个测试的上下文：`grep -n -B30 -- '--- FAIL: <测试名>' <输出目录>/<id>.txt`。

## 分流

1. **已知偶发**，不改代码，重跑：
   - `TestIdempotencyMiddlewarePG18` 锁超时 → `gh run rerun <id> --failed`；
   - Actions 因付款失败没启动 → `gh run rerun <id>`；
   - 检查机红、GitHub 同一 job 绿（例：10-07 fda8a31 的 linux-race，GitHub 与本机 `-race -count=3` 都过）→ 先当偶发，`MEMOH_FRESH=1 wait-status.sh <sha>` 重跑一次；第二次还红再查。检查机日志在云电脑 `/data/memoh-ci/logs/<sha>/<job>.log`，本机读不到。
2. **一处报错连带一片**：先看「SQL 报错」和 e2e 首个失败。同一条 SQLSTATE 出现在几个测试里，通常是一条共享查询坏了，修一处全好（10-07：ListLinks 漏逗号 → 门户链接 500、两个 PG18 用例、expiry/risk e2e、两个前端冒烟一起红）。
3. **整合问题**（几路并行合并后才出现）：
   - 计数类断言：内置模板数、后台循环数（`workers.Add` 与两份契约测试）、`run-pg18-gates.sh` 的 DOMAINS、`run-smoke-e2e.sh` 的 SCRIPTS；
   - 同一个函数被两路各自 `CREATE OR REPLACE`（如 `app.seed_tenant_defaults`），后合的那份要包含先合那份的全部内容，Down 还原到先合那份；
   - 一路改了签名或口径，另一路的新测试还按旧的写（返回值个数、状态名、枚举个数）；
   - 夹具 id 撞号（同一 PG18 域库共用），见 accept-task 的坑。
4. **审计、限频类 e2e**：smoke 里各脚本从同一来源 IP 打同一个栈，按 IP 窗口去重的逻辑会被前一个脚本的记录吞掉。先确认是产品语义（要不要豁免）还是脚本假设，再改。
5. **真缺陷**：在本机复现（PG18 用例本机没有 Docker 会跳过，跳过不等于通过）。修完按 verify skill 重跑改到的包，推送后再等两个脚本。

## 坑

- `gh run list --commit` 只认完整 sha（脚本已处理）；一次推多个提交只有最新那个有 run，旧提交显示「没有 run」不代表没红。
- `--log-failed` 每行带「job\tstep\t时间戳」前缀，BSD sed 不认 `\t`，脚本用 perl 去前缀；自己 grep 原文要考虑前缀。
- 前端冒烟里 `500 internal_error` 只有 requestId，没有后端堆栈；对应的后端报错常常也在同一提交的 PG18 run 里，先看那边。
- 推送故障（1Password 签名失败）时不要先在本地把任务分支合进主线：等分支自己的 CI 结论再合（10-07 先合后测，CI 抓出的问题只能在主线上修）。
