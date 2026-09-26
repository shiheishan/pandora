# panel/cmd/pandora-cic-journal/
> L2 | 父级: /panel/CLAUDE.md（cmd/ 一行）

CLIENT-AUTH-00043 清理前的 CIC 日志候选实现：只支持 Linux amd64 / arm64、只用标准库，刻意没有接进 runner、迁移、发布清单与任何生产入口（见 README.md）。安全关键：路径逐级 openat2 在保留的目录描述符下打开、设备白名单、root 所有 0600 单链接，已发布的记录永不追加、截断或覆盖，所有崩溃窗口要么只留下未发布的 stage、要么可按原清单 sha 幂等恢复。文件系统辅助函数与 platform/releasejournal 里的一批同名函数刻意各持一份，不合并。

成员清单
main_linux.go: 命令行与共用模型：四个子命令、退出码、系统调用常量、故障注入点（journalRename 等可被测试替换）、参数与设备白名单解析、规范化记录体与哈希链摘要
journal_linux.go: 日志发布与追加：intent 先在 stage 目录建齐再整目录 renameat2 NOREPLACE 发布，后续阶段持 flock、核对整份日志 sha256 后逐条发布不可变记录段；rename 之后的目录同步失败可按原清单 sha 幂等恢复
fs_linux.go: 可信文件系统原语：openat2（RESOLVE_BENEATH / NO_SYMLINKS / NO_MAGICLINKS）、renameat2 NOREPLACE、目录与日志文件的所有者、权限、链接数与 dev/inode 复核、整写与整读
parse_linux.go: 日志解析与校验：UTF-8、无 CR / NUL、键齐全不重复且按 schema 顺序、哈希链逐条复算、intent → catalog → drop → close 迁移与互相绑定、close 之后不许有尾随字节
syscall_linux_amd64.go / syscall_linux_arm64.go: 两个架构的 renameat2 系统调用号
main_unsupported.go: 其余平台的桩，直接以 70 退出
README.md: 设计说明与崩溃窗口分析
*_test.go: main_linux_test.go 在 Linux 上做故障注入（短写、ENOSPC、fdatasync / fsync 失败、kill 窗口、目录同步失败后的幂等恢复）；deploy/pandora-cic-journal_mock_test.sh 另做静态字面量检查（对整组 *_linux.go）、双架构构建与测试编译，root 下再跑真实四阶段

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
