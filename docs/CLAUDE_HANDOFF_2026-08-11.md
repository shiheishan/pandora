# PandoraPanel → Claude Code 交接记录

更新时间：2026-08-11  
项目目录：`C:\Users\AUAS\Documents\AI公司\pandora-repo`  
项目：PandoraPanel / Pandora Native  
基线提交：`b53a55db2519588638d2608ddf5906feeaefda69`

## 1. 接手前必须知道

- 当前分支为 `main`，工作树约有 137 个既有未提交路径，包含 NativeCore、panel、迁移、测试和发布脚本改动。
- 不得执行 `git reset --hard`、`git checkout --`、删除用户改动、commit、push、部署或重启生产服务。
- 接手第一步只读执行：

```powershell
cd "C:\Users\AUAS\Documents\AI公司\pandora-repo"
git status --short
git diff --check
```

- 只使用 CLI，不依赖屏幕点击；不要恢复其他 Claude/Kimi 会话。
- Linux 测试机（地址见本机 ops-local/，不入库）：x86_64，1 vCPU，约 3.9 GB 内存；现有 `aegis-nodeagent` 正常运行。远端测试必须 `GOMAXPROCS=1`、`-p 1`，并使用 `/tmp/pandora-verify-current-*` 隔离目录。

## 2. 本轮已验证

### 当前工作树 → Linux amd64 外部互操作

当前工作树的 `pdnd` 源码曾被复制到远端隔离目录：  
`/tmp/pandora-verify-current-20260811-interop-v2`

执行：

```bash
GOMAXPROCS=1 go test -mod=readonly -tags interop -p 1 -count=1 -v ./kernel \
  -run '^TestExternalXrayVLESSXHTTPH3Interop$'
```

结果：`PASS`，用时约 1.212 秒。该测试验证当前工作树 Linux amd64 代码与外部 Xray 的 REALITY + XHTTP + H3 互操作。

验收后已确认：

- 隔离目录已清理；
- 本地临时归档已清理；
- 未重启 `aegis-nodeagent`；
- 测试后远端负载约 `0.67 0.34 0.20`。

### WebDAV 备份恢复（远端快照）

在 `/root/pandora-fullgate-20260810-1240` 快照上完成隔离 WebDAV + PostgreSQL 18 演练：

- HTTPS WebDAV 上传通过；
- 签名 manifest 和 SHA-256 校验通过；
- Age 加密/解密通过；
- 全新 PostgreSQL 18 实例恢复通过；
- 大整数恢复标记 `9007199254740993:pandora-webdav-recovery-ok` 通过；
- RTO 约 2 秒；
- 临时容器、hook、目录全部清理。

注意：这是远端旧快照工具证据，不等同于当前工作树已完成 WebDAV 生产验收。

### 本地 Go 门

以下已通过（本地 Go 1.26.5，低并发）：

- panel：`go test -mod=readonly -p 1 -count=1 ./...`
- pdnd：`go test -mod=readonly -p 1 -count=1 ./...`
- pdnd compat：`go test -mod=readonly -tags compat -p 1 -count=1 ./...`
- panel/pdnd：`go vet -mod=readonly ./...`
- 发布脚本语法、NativePanel parity、release/migration/WebDAV mock gates。

Windows `-race` 未运行，因为没有 gcc/clang；不要把它写成通过。

## 3. 当前仍未关闭的门禁

验证记录在：`.ai-company/verification.json`。验证器仍应拒绝 COMPLETE，剩余主要项目：

1. 当前工作树真实安装式 TLS + PostgreSQL 18 + systemd + NativeCore E2E；
2. ARM64 实机运行验证；
3. 独立公网第三方 REALITY + XHTTP + H3 端点验证；当前已完成 Linux amd64 外部 Xray 测试，但不是公网独立端点；
4. 历史 client-auth CA42/CA43 辅助门缺少当前 handoff，不得误报通过；
5. G0 release intent 尚未授权。

不得因为 Linux amd64 互操作通过，就宣称“全协议、ARM64、生产发布或全部安装链路完成”。

## 4. 推荐后续顺序

1. 先审阅 `verification.json`、本文件和实际 `git diff`，确认没有覆盖既有改动。
2. 若要验证当前安装链路，先取得明确的隔离上传/测试授权，固定目标为 `/tmp/pandora-verify-current-*`，禁止写 `/etc`、生产数据库或重启现有服务。
3. 若没有 ARM64 实机，不要用交叉编译或静态 ELF 检查替代 ARM64 运行验收。
4. 每轮测试结束后检查临时目录、容器、进程、服务状态和负载；失败时保留最小诊断证据，不扩大清理范围。

## 5. 证据边界

- FACT：本文件列出的命令和结果来自本轮实际执行。
- INFERENCE：当前工作树的 Linux amd64 REALITY + XHTTP + H3 路径具备可运行证据。
- UNKNOWN：ARM64、当前工作树完整安装 E2E、公网第三方端点、生产发布资格。

本交接记录不是 release approval，也不是 production-ready 声明。

