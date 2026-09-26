# panel/internal/platform/clientauth/ca42runner/
> L2 | 父级: /panel/internal/platform/CLAUDE.md（clientauth/ 一行）

CA42（CLIENT-AUTH 00042）的只读校验运行器：在 Linux amd64 / arm64 上保留授权、发布、执行计划、信任胶囊、外部清单与制品的全部描述符（文件须 root 所有），逐项复核身份与哈希后只算出下一条账本记录（RunReadOnlyVerification）。设计原则是「能力即类型」：生产组合只接受生产类型，测试缝返回刻意不同的类型，拿不到生产来源；对外 API 不暴露根密钥、描述符或可信时间。其余平台一律 fail closed（*_other.go、roots_unprovisioned.go）。

成员清单
cli.go / config.go: ParseCLI 只让调用方控制一个值，根、路径、身份、超时与执行策略全部编译期固定
runner_linux.go / runner_other.go: RunReadOnlyVerification：只读校验整条合约链并算下一条账本记录；其余平台直接失败
verify.go / attestation.go: VerifyBundle 与准入时间窗、保留制品的 v2 证明校验与规范字段解析
roots.go / roots_unprovisioned.go / roots_ca42e2e.go / production_roots.go: 编译期根密钥集：普通构建不含任何授权材料并 fail closed，只有打了标签的一次性 E2E 二进制带测试公钥；productionRootLease 不暴露根 ID 与公钥字节
journal.go: BindLayoutSwitchedJournal 证明 v2 准入边界，纯函数，账本预留与日志推进在别处
ledger_linux.go / ledger_root_linux.go: 授权账本（非阻塞锁，ErrLedgerBusy）与账本根能力（绑定规范路径、挂载与 supervisor 命名空间，互斥锁贯穿整个租约）
files_linux.go / files_other.go: 可信目录与 root 所有的普通文件 / 可执行文件的打开与复核原语、retainedIdentity、运行中可执行文件的 SHA-256
artifact_manifest.go / artifact_stream.go: CA42 迁移清单的解析、不随制品大小分配内存的定长哈希
artifact_set_linux.go: 制品集 RetainedArtifactSet 的打开与检查，信任胶囊与外部清单复用 VerificationSession 已保留的描述符
artifact_set_chain_linux.go: 固定制品目录与条目精确匹配、从根到制品的绝对路径链摘要
artifact_set_revalidate_linux.go: 制品集的复核（锁内逐个制品比对身份与路径链）、按时刻校验证明与关闭
artifact_handoff_v3_linux.go / core_binding_v3_linux.go: v3 控制包到解析图的绑定交接、证明核心的两遍绑定与复核
control_bundle_v3_linux.go: v3 控制包的保留与回调作用域内的单角色读取
control_bundle_v3_revalidate_linux.go: v3 控制包整包复核：命名空间、目录重绑定、逐条目身份与哈希、最终规范绑定
control_bundle_v3_entries_linux.go: v3 控制包的目录与文件核验、精确目录列表、描述符提升与释放
control_data_v3_linux.go: 从保留的控制包取数据角色并在用后清零
external_witness_v3_linux.go: 从保留的生产清单只采集一次的回调作用域外部见证
production_authority_v3_linux.go: v3 生产授权租约的入口与生命周期
production_authority_v3_revalidate_linux.go: 授权租约的复核与最终规范绑定（关上解析与取时之间的 TOCTOU 窗口）
production_authority_v3_fs_linux.go: 授权命名空间与 supervisor 一致性、授权文件核验、关闭时清零签名密钥
production_composer_v3_linux.go: OpenProductionV3VerificationSession，v3 生产组合的唯一公开入口，调用方只控制 attemptID
verification_session_linux.go / verification_session_v3_linux.go / verification_session_v3_other.go: v2 与 v3 的只读校验会话，拥有所用的全部描述符；其余平台的 v3 桩
platform_error.go: 不支持平台的统一错误
*_test.go: 单元、Linux 故障与 E2E 测试；verification_session_v3_linux_test 扫本包全部非测试源码，守住「调用方不能传入 ArtifactSet」

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
