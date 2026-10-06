# panel/internal/platform/config/
> L2 | 父级: /panel/internal/platform/CLAUDE.md

整个 panel 唯一读进程环境变量的生产包：每个变量的名字、缺省值、解析与校验只在这里出现一次，其余包经 Config 字段或构造参数拿值，领域包不依赖本包的任何全局状态
  - 网关与带数据库的命令行工具走 Load（缺一项拒绝启动，NFR-006）
  - 不连数据库的独立小二进制各有自己的小加载函数，不给它们多出必填项
  - envaccess_test.go 用 sourcetest 扫 panel/internal 与 panel/cmd 守住这条边界。

成员清单
config.go: Load 与 Config：数据库、Valkey、三个网关地址（public / admin / node）与各网关的 pprof 地址（pprof.go）、对外地址、主密钥、public 与 admin 两域令牌密钥（互不相同）、配置签名种子与轮换中的旧种子、限流档位、令牌时长；CanonicalPublicOrigin 生产要求公网 HTTPS
deployment.go: Config 内嵌的 Deployment（备份目录与解密私钥路径、pdnd 分发目录、GeoIP 两个库、NativeCore 按架构的产物摘要与发布版本），全部可缺省
  - AdminGeoIPDB 给 aegis-admin 补缺省路径（aegis-node 没有缺省）
  - LoadBackupWebDAV 给 aegis-backup-webdav
  - DefaultBackupWebDAVConfigPath 也被系统状态页用来判断异地备份
pprof.go: 三个网关各自的 pprof 诊断端口地址 AEGIS_{PUBLIC,ADMIN,NODE}_PPROF_ADDR（三个单元共用一份 .env，故按域分变量），缺省关闭
  - 只收回环 IP 字面量（127.0.0.0/8、::1，含 4in6 映射），主机名与 localhost、0.0.0.0、带 zone、端口 0 一律让 Load 失败；与三个网关业务端口或彼此重复同样失败
  - 结果规范化后放进 Config.PprofAddrs，监听在 platform/profiling
envaccess_test.go: 源码守卫：config 之外出现 os/syscall/unix 的 Getenv、LookupEnv、Environ、ExpandEnv（调用或取函数值）即红
  - 豁免 envAccessExemptions 逐文件写理由（现只有 PG18 夹具 pg18test.go 一项），豁免失效同样红
*_test.go: 令牌时长、严格时长解析、部署项变量名与缺省值、pprof 地址校验（缺省关闭、非回环让 Load 失败、判重）的单测

法则: 成员完整·一行一文件·父级链接·技术词前置
