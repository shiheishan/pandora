# panel/internal/domain/dbbackup/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

数据库备份的离机与保留：把本地「归档 + .sha256」配对签名成清单，经 WebDAV 上传并回读校验，再按保留策略逐步删除远端旧备份。所有私密输入（WebDAV 口令、清单签名种子、检查点、复制 Hook）都只能从 root 所有、0600、单硬链接、父目录 0700 且不经符号链接的路径读入；这组校验只在 Linux 生效，其他平台是空实现，所以 Linux 行为只能在 Linux 上验证（CI panel-unit 以非 root 跑，测试经 secureTempDir 把当前用户设为可信属主）。

成员清单
config.go: WebDAV 配置加载与本地配对校验，openSecureRegular/validateSecureParent 是私密文件读取的唯一入口，先查父目录再查已打开的 fd，防 TOCTOU
target.go: WebDAV 端点解析，Resolver 可注入以便不触网测试，拒绝私网地址除非显式放行
webdav.go: 上传→回读→发布的 WebDAV 客户端，部分对象失败即清理，已存在且逐字节一致的终态视为幂等成功
manifest.go: Ed25519 签名清单与检查点事务，清单成链、同一备份可原样续传；读私密文件复用 file_owner 的校验
manifest_lock_linux.go / manifest_lock_other.go: 检查点文件的 flock 互斥，同一时刻只允许一个上传任务
checkpoint_hook.go: 检查点复制 Hook 的固定根目录 checkpointHookRoot（包级变量，仅测试改写），Hook 路径不可配置到别处
checkpoint_hook_linux.go / checkpoint_hook_other.go: 经已校验的 fd（/proc/self/fd/3）执行 Hook，回执限长，杜绝按路径二次打开
file_open_linux.go / file_open_other.go: O_NOFOLLOW|O_CLOEXEC|O_NONBLOCK 只读打开，符号链接与 FIFO 阻塞都不放行
file_owner_linux.go / file_owner_other.go: 私密路径属主（secureFileOwnerUID，生产恒为 root）、权限位、单链接、父目录解析校验与 RequireRootRuntime；other 为空实现
retention.go: 保留策略与删除三元组，删除顺序固定为清单先行，崩溃也不会留下指向缺失数据的清单
retention_state.go: 删除意图与远端观测的对账状态机，远端最多领先持久意图一步
retention_executor.go: 每次至多执行一次远端删除，意图存储与远端接口都封在包内，外部无法注入按名删除的权力
*_test.go: 单元测试；secure_tempdir_test.go 是公共夹具，file_owner_linux_test.go 提供可信属主注入并证明非 root 所有的私密文件在生产默认下被拒

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
