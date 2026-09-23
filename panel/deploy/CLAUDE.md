# panel/deploy/
> L2 | 父级: /panel/CLAUDE.md

面板从源码到一台 Linux 主机的全部路径：打包、安装与升级、迁移、备份恢复、边缘入口、巡检，以及证明这些路径正确的隔离门禁。这里是产品的一部分，任何人下载后照同一套脚本部署，所以脚本与模板里不出现任何具体部署的值：域名、后台前缀、主密钥都在安装时产生并落在主机的 `.env`，模板只含占位符（`__AEGIS_ADMIN_PATH__`、`__AEGIS_DOMAIN__`）。运行时服务永远拿不到迁移 DSN；破坏性脚本先校验全部输入、再做第一次破坏动作；迁移类脚本按自身位置找与 deploy/ 并排的 migrations/，不写死安装路径（/opt/aegispanel 与 /opt/pandora 两种布局都成立）。维护者操作自己服务器的一次性脚本不在这里，在仓库根被忽略的 ops-local/。

成员清单

安装与升级
install.sh: 一键安装 / 升级（Docker 数据基座），升级前自动全量备份，不替人造管理员；收尾提示先填 AEGIS_PUBLIC_BASE_URL 再渲染 nginx
install-native.sh: 无 Docker 的直装版，首装生成 .env 的全部 CHANGE_ME 机密
install-linux-binaries.sh: 按带外获得的 SHA-256 摘要校验后安装发布包二进制
platform.sh / preflight-linux.sh: 发行版与依赖探测（被其他脚本 source），装前环境预检
migrate-to-new-host.sh: 新主机一键迁移：恢复 Age 密文备份、重建 aegis_app 角色、校验账本无漂移，第 5 步先拦下缺失的 AEGIS_PUBLIC_BASE_URL
.env.example: 运行配置模板，机密与域名全是 CHANGE_ME 占位
docker-compose.yml: 本地数据基座 PostgreSQL 18 + Valkey 8，只绑 127.0.0.1
systemd/: aegis-public/admin/node 三网关、aegis-nodeagent、备份 service+timer 单元；overrides/ 为 1 核 2G 共享机的节点端资源上限
logrotate-aegis: 三个服务的日志轮转

发布与切换
build-release.sh: 打发布包，先 make frontend-embed（无 npm 即失败），拒绝占位前端进入发布物；迁移工具版本随包固定
release-stop-the-world.sh: 改表发布的停机切换控制器
release-artifact.env.example: 发布物 SHA256SUMS 绑定样例（docs/RELEASE-ARTIFACT-BINDING.md）
renewal-cutover.md: 续费幂等切换闸门手册
pandora-preflight-lease-registry.sh: 预检租约登记，防止并发发布

数据库
migrate.sh: 特权 goose 包装器，运行时服务永远拿不到迁移 DSN；up 之前先调 check-migrations.sh 在克隆库演练；拒绝 down/redo
check-migrations.sh: 在一次性库克隆上证明精确的生产升级路径（make check-migrations 与 migrate.sh up 都走它）
check-migrations-isolated-pg18.sh: CLIENT-AUTH-00042 那一代的历史隔离预检，钉死冻结迁移的 SHA-256；当前迁移序列的 00042 已换人，它会主动以 NOT_RUN（exit 77）拒绝运行
bootstrap.sh / configure-app-role.sql: 迁移后配置最小权限运行角色 aegis_app（NOSUPERUSER NOBYPASSRLS）
psql.sh: 从 .env 读凭据的 psql 封装，避免口令出现在命令行
seed-catalog-cny.sql / seed-demo.sql: 确定性演示商品目录（schema 35+），本地与 E2E 用
reap-isolated-pg18.sh: 清理隔离 PG18 残留容器，先全量校验候选再做第一次删除

备份与恢复
backup-postgres.sh / restore-postgres.sh / verify-backup.sh: 加密备份、恢复（校验和之外还做恢复演练防坏块）、备份校验
backup-webdav.example.json: WebDAV 远端备份配置样例（example.com）
BACKUP.md / ADMIN-PASSWORD-RESET.md / LINUX-COMPATIBILITY.md: 备份恢复、管理员密码重置、发行版兼容运维手册

边缘入口
nginx-aegis.conf: 统一边缘模板，只含占位符：后台前缀 __AEGIS_ADMIN_PATH__、域名 __AEGIS_DOMAIN__（server_name 与 Let's Encrypt 路径）
render-nginx.sh: 只读解析 .env（不 source）的 AEGIS_ADMIN_PATH 与 AEGIS_PUBLIC_BASE_URL，校验后原子写出 nginx 配置；域名与面板拼链接用的是同一个值
update-cloudflare-realip.sh: 刷新可信 Cloudflare 回源网段（模板 include 的 cloudflare-realip.conf）

巡检
healthcheck.sh / aegis-health.service / aegis-health.timer: 面板健康巡检，错开整点运行，巡检自身有超时
clear-ratelimit.sh: 清空限流计数，仅开发与集成测试用

CLIENT-AUTH 发布门禁（00042–00044，未接生产路由）
client-auth-*、generate-client-auth-*、probe-client-auth-*、verify-client-auth-*、run-client-auth-00043-indexes.sh: 冻结契约的清单生成、离线 Ed25519 证明、OID 无关目录探针与校验、可续跑索引执行器；各 README 说明输入输出
client-auth-00044-verifier-gate.py / verify-client-auth-00044-evidence-vectors.ps1: 00044 证据信封与向量的独立生成与校验，不导入被测实现

测试（只用虚构数据与一次性环境，不连任何真实部署）
render-nginx_test.sh: 渲染器契约：虚构域名 panel.example.test 填入正确、非法 AEGIS_PUBLIC_BASE_URL 全部拒绝、模板不残留占位符或具体域名
run-pg18-gates.sh: 一次跑完全部 PostgreSQL 18 集成门禁
test-*-pg18.sh: 各业务的 PG18 集成门禁，每次新建隔离容器与库、结束即删；口令为 *-test-only 字样
test-*-ui-playwright.*: 真实浏览器 UI 验收，对本机临时服务；test-vps-public-ui-playwright 的目标地址由 PANDORA_VPS_UI_TARGET 运行时给出，不入库
test-install.sh / test-ca42-*-e2e.sh / test-client-auth-*: 安装链与 CLIENT-AUTH 端到端；test-install.sh 发现库里已有用户即拒绝执行
*_mock_test.sh / *_static_test.sh / *_linux_test.sh / *_linux_fault_test.sh / release-stop-the-world_test.ps1: 对上面各脚本的桩测试与静态检查，不需要数据库或 root
fixtures/: billing、idempotency 两份 PG18 门禁种子数据

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
