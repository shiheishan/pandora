---
paths:
  - "panel/tools/refactorcheck/**"
  - "pdnd/linelimit_test.go"
---

# 纯挪动重构的自证工具（refactorcheck）

拆超长文件、SQL 跨包下沉都要求「只挪代码、不改行为」，用这个工具给出机器证据；在 module 根 `panel/` 下 `go run ./tools/refactorcheck <子命令>`。

## 用法上容易漏的点

- 拆测试文件时 compare 要加 `-tests`，否则 `_test.go` 不参与比对，改了测试也报 OK。
- pdnd 是另一个 module，从 panel/ 下用 `-C ../pdnd` 比对，不要在 pdnd 里另写一份工具。
- SQL 从 handler 挪进 domain 时 compare 必然报不同（声明换了包），改用 `sqlset`：只有输出 `SQL UNCHANGED` 才算一字未改；有 DIFF 行就要在交付报告里逐条说明理由。
- `shatter` 打散后在副本里跑 vet 与全量测试；按文件名读源码的测试所在的包要用 `-skip` 排除，否则必然误报。`-keep` 缺省原样保留文件名为 `router*.go` 的文件，为的是 `api/admin` 的路由表：路由契约测试按文件名读它们。
- compare 报出的游离注释（分节标题）差异只是提示，不判失败。

## 维护约束

- 只依赖标准库与本机的 git、go；main 包不被任何包 import，不进发布包。
- 行数守卫有两份：panel 的 `tools/refactorcheck/linelimit_test.go`（逐文件豁免表 `lineLimitExemptFiles`）与 pdnd 根的 `linelimit_test.go`（整目录豁免两个 fork）。两者口径相同（按 wc -l 数换行、含测试、跳过隐藏目录与 node_modules），改一份要对照另一份；豁免过期也会变红。
