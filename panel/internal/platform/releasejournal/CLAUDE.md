# panel/internal/platform/releasejournal/
> L2 | 父级: /panel/internal/platform/CLAUDE.md

发布控制器的持久化日志原语，只支持 Linux amd64 / arm64 与 root：只记录「已持久准备 / 已观察到」的事实，自己不能执行 SQL、Goose、systemd、shell、网络、删除、回滚或部署（deploy/pandora-release-journal_mock_test.sh 静态禁止这些能力）。模型与导出跨平台；落盘、会话与 v3 发布器是 linux 专用。v1 / v2 与 v3 两套模型刻意并行。文件系统原语与 cmd/pandora-cic-journal 的同名函数刻意各持一份，不合并（安全关键）。

成员清单
model.go: v1 / v2 日志模型：记录文法、哈希链、状态机与恢复态（stateRecoveryRequired），终态之后不许有尾随字节
model_v3.go: v3 日志模型，与 v1 / v2 并行而非替换
receipt.go: 授权侧只读投影 Receipt，只在整条链校验通过后产出
export.go: 日志导出
store_linux.go: v1 命令行入口 RunCLI（prepare / inspect / advance）、参数与设备白名单、证据 fd、故障注入点与 stage / temp 名称文法
store_journal_linux.go: v1 准备（幂等，samePreparedRequest）与推进（持 flock，期望哈希 CAS，rename 之后的崩溃窗口经 recoverAdvance 精确补齐）、快照加载与同步
store_fs_linux.go: v1 可信文件系统原语：openat2（RESOLVE_BENEATH / NO_SYMLINKS / NO_MAGICLINKS）、renameat2 NOREPLACE、不可变文件 root 所有 0600 单链接、稳定读与 dev/inode 复核
session_linux.go: 保留已信任的根与日志目录描述符的 v2 会话（OpenV2Session、AdvanceAdmissionAttempted），绑定变化即拒绝
session_v3_linux.go / publisher_v3_linux.go / bootstrap_v3_linux.go / root_capability_v3_linux.go / artifact_boundary_v3_linux.go: v3 会话、带 CAS 检查点的发布器、预备态引导、根目录能力与制品边界交叉绑定
ca42e2e_fixture_linux.go: CA42 端到端测试用的日志夹具，只含构造所需身份
syscall_linux_amd64.go / syscall_linux_arm64.go: 两个架构的系统调用号
*_test.go: 模型与 v2 / v3 单元测试（跨平台），*_linux_test.go 做 Linux 上的崩溃恢复、残留 stage / temp 与 CAS 分歧测试

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
