# panel/deploy/
> L2 | 父级: /panel/CLAUDE.md

面板从源码到一台 Linux 主机的全部路径：打包、安装与升级、迁移、备份恢复、边缘入口、巡检，以及证明这些路径正确的隔离门禁
  - 这里是产品的一部分，任何人下载后照同一套脚本部署，所以脚本与模板里不出现任何具体部署的值：域名、后台前缀、主密钥都在安装时产生并落在主机的 `.env`，模板只含占位符（`__AEGIS_ADMIN_PATH__`、`__AEGIS_DOMAIN__`）
  - 运行时服务永远拿不到迁移 DSN
  - 破坏性脚本先校验全部输入、再做第一次破坏动作
  - 迁移类脚本按自身位置找与 deploy/ 并排的 migrations/，不写死安装路径（/opt/aegispanel 与 /opt/pandora 两种布局都成立）
  - 维护者操作自己服务器的一次性脚本不进仓库，需要时放在仓库根被忽略的 ops-local/（按需创建）。

成员清单

安装与升级
install.sh: 一键安装 / 升级（Docker 数据基座）
  - 发布包装出来的就是生产：首装写 AEGIS_ENV=production，并先拿到 https 公网域名（PANDORA_PUBLIC_BASE_URL 或现场询问，不合规即在动手前停下）
  - 升级不改现有运行模式，非 production 只提示
  - 升级前自动全量备份，不替人造管理员
install-native.sh: 无 Docker 的直装版（/opt/pandora），首装先取合规的对外地址再生成 .env（全部机密随机、AEGIS_ENV=production）；已有 .env 即升级，从中读回口令，.env 一字不动
public-base-url.sh: 两个安装脚本 source 的共用段：首装对外地址的取值（PANDORA_PUBLIC_BASE_URL 或终端现场问）与校验（与 render-nginx.sh 同一规则），不合规给中文原因与重跑命令；另带不 source 地读 .env 单键
install-linux-binaries.sh: 按带外获得的 SHA-256 摘要校验后，以可回滚事务安装发布包二进制、运维脚本、systemd 单元与 release-artifact.env（到 /opt/aegispanel/deploy/）；render-nginx.sh 读的同目录模板 nginx-aegis.conf 与 update-cloudflare-realip.sh 一并装上
platform.sh / preflight-linux.sh: 发行版与依赖探测（被其他脚本 source），装前环境预检
migrate-to-new-host.sh: 新主机一键迁移：
  - 恢复 Age 密文备份、重建 aegis_app 角色、校验账本无漂移，目标 .env 为 AEGIS_ENV=production 时在动手之前拒绝源码模式（不产 pdnd-dist 与发布物绑定，节点接入会被拒），要 AEGIS_RELEASE_DIR 指向发布包
  - 第 5 步先拦下缺失的 AEGIS_PUBLIC_BASE_URL
  - 第 6 步源码模式先 make frontend-embed（无 npm 即停），两种模式都查 aegis-public/admin 二进制里没有前端占位标记
.env.example: 运行配置模板，机密与域名全是 CHANGE_ME 占位；AEGIS_ENV 默认 development 给本地开发，install.sh 首装改成 production
  - 三个 AEGIS_*_PPROF_ADDR 留空即关闭 pprof，打开只许回环 IP（校验在 platform/config）
docker-compose.yml: 本地数据基座 PostgreSQL 18 + Valkey 8，只绑 127.0.0.1
systemd/: aegis-public/admin/node 三网关、备份 service+timer 单元；aegis-node 在 .env 之后再加载 release-artifact.env（节点接入的发布物绑定）
logrotate-aegis: 三个网关的日志轮转，路径 /var/log/aegis/*.log 与 systemd 单元的 append: 一致，copytruncate；随发布包分发，install-linux-binaries.sh 装到 /etc/logrotate.d/aegis

发布与切换
build-release.sh: 打发布包，先以 PANDORA_RELEASE=$VERSION 跑 make frontend-embed（无 npm 即失败；版本号注入后台登录页与侧栏），拒绝占位前端进入发布物；迁移工具版本随包固定
release-stop-the-world.sh: 改表发布的停机切换控制器
release-artifact.env.example: 发布物绑定样例；真实文件由 build-release.sh 按本包 pdnd-dist 生成（版本 + 两架构 SHA-256，不含 AEGIS_ENV），全链路见 docs/RELEASE-ARTIFACT-BINDING.md，release-artifact-binding_mock_test.sh 守
renewal-cutover.md: 续费幂等切换闸门手册

数据库
migrate.sh: 特权 goose 包装器，运行时服务永远拿不到迁移 DSN
  - 迁移编号只要求严格递增、不重复（主序列有 00073、00091、00092 历史空号，同号拒绝），与 check-migrations.sh 同一规则
  - up 之前先调 check-migrations.sh 在克隆库演练
  - 拒绝 down/redo
check-migrations.sh: 在一次性库克隆上证明精确的生产升级路径（make check-migrations 与 migrate.sh up 都走它）
bootstrap.sh / configure-app-role.sql: 迁移后配置最小权限运行角色 aegis_app（NOSUPERUSER NOBYPASSRLS）
  - 末尾在「列级提升回表级」之后收回证据流水与守卫表的 UPDATE/DELETE（traffic_pack_grants、gift_card_batches 只收 DELETE，业务要 UPDATE），顺序由 platform/db 的契约测试守
psql.sh: 从 .env 读凭据的 psql 封装，避免口令出现在命令行
seed-catalog-cny.sql / seed-demo.sql: 确定性演示商品目录（schema 35+），本地与 E2E 用

备份与恢复
backup-postgres.sh / restore-postgres.sh / verify-backup.sh: 加密备份、恢复（校验和之外还做恢复演练防坏块）、备份校验
backup-webdav.example.json: WebDAV 远端备份配置样例（example.com）
BACKUP.md / ADMIN-PASSWORD-RESET.md / LINUX-COMPATIBILITY.md: 备份恢复、管理员密码重置、发行版兼容运维手册

边缘入口
nginx-aegis.conf: 统一边缘模板，公网只听 80（跳 443）与 443，另有本机回环运维入口 127.0.0.1:9080；只含占位符：后台前缀 __AEGIS_ADMIN_PATH__、域名 __AEGIS_DOMAIN__（server_name 与 Let's Encrypt 路径）
render-nginx.sh: 只读解析 .env（不 source）的 AEGIS_ADMIN_PATH 与 AEGIS_PUBLIC_BASE_URL，校验后原子写出 nginx 配置；域名与面板拼链接用的是同一个值
  - 模板 include 的 /etc/aegispanel/cloudflare-realip.conf 缺失时写一份只有注释、不信任任何代理的默认文件（全新安装 nginx -t 才过得去，且不在 Cloudflare 后面的站点不会误信客户端填的 CF-Connecting-IP）；已存在绝不覆盖，升级保留 Cloudflare 网段
update-cloudflare-realip.sh: 站点在 Cloudflare 后面时的显式启用步骤：取官方 ips-v4/v6，校验后原子写成 set_real_ip_from + real_ip_header CF-Connecting-IP，坏列表不动旧文件；随发布包与两个安装脚本装到 deploy/，两个安装脚本收尾都提示它

巡检
healthcheck.sh / aegis-health.service / aegis-health.timer: 面板健康巡检，错开整点运行，巡检自身有超时
clear-ratelimit.sh: 清空限流计数，仅开发与集成测试用

测试（只用虚构数据与一次性环境，不连任何真实部署）
migrate-to-new-host_mock_test.sh: 迁新主机的生产闸门：production + 源码模式以中文原因拒绝，production + 发布包、development + 源码都放过
  - docker compose 桩保证不走到第 2 步
  - 越过闸门按 pandora-platform: 前缀认平台探测的任一失败，不依赖宿主有 systemd（无 systemd 的 Linux 容器上也通过）
public-base-url_mock_test.sh: 对外地址闸门的规则矩阵、取值与报错、两个安装脚本共用一份、install-native.sh 不写示例值且 .env 只在首装写
logrotate-aegis_static_test.sh: 轮转 glob 覆盖三个网关单元 append: 的全部日志文件，规则随包分发并装到 /etc/logrotate.d/aegis
render-nginx_test.sh: 渲染器契约：虚构域名 panel.example.test 填入正确、后台前缀不带尾斜杠只做 301、非法 AEGIS_PUBLIC_BASE_URL 全部拒绝（且不留下信任表）、模板不残留占位符或具体域名、listen 只许 80/443 与回环 9080
cloudflare-realip_mock_test.sh: 真实来源 IP 信任表：模板、渲染器、更新脚本三处是同一路径；全新渲染写出无指令的默认文件（0644）、已有文件与 Cloudflare 网段重渲染不变；桩 curl 下更新脚本写出网段、坏列表失败且不动旧文件；更新脚本与模板随发布包和两个安装脚本落到 deploy/
run-pg18-gates.sh: 一次跑完全部 PostgreSQL 18 集成门禁，CI 的 panel-pg18.yml 每次推送都跑
  - 每域 go test -v，有用例跳过或一个都没跑同样判失败（缺环境变量的测试会 t.Skip 报 ok），同包两域靠精确 -run 过滤互不拉入（billing 包里另有 payment_query 域跑 TestPaymentQueryPG18）
  - 容器就绪经 TCP 探测（镜像初始化的临时实例只听 unix socket），60 秒不就绪即失败
run-smoke-stack.sh: 前端联调冒烟的底座（panel-smoke.yml 调用）：
  - up 起一次性 PG18 + Valkey，goose 迁移、configure-app-role.sql 配运行角色、aegis-adminctl 建管理员，配置用 openssl 现场生成，从源码起 aegis-public/admin/node 并以 readyz（node 为 healthz）与管理员真登录验收，入口写进状态目录的 smoke.env
  - 库名 aegis_smoke_test（带 test 段，过 e2e 脚本的一次性库守卫），PG 容器名可由 PANDORA_SMOKE_PG_CONTAINER 覆盖（CI 设成 aegis-postgres 让 psql.sh 直接可用）
  - down 只拆自己记下的进程与容器
run-smoke-e2e.sh: 联调冒烟第 ⑤ 步（panel-smoke.yml 在读表与写路径之后调用）：在冒烟栈上逐个跑 tests/*_e2e.sh 与 tests/e2e.sh，第 ⑥ 步起失败即变红
  - 脚本一字不改，只把它们声明要的环境搭出来（/opt/aegispanel 布局链到仓库 deploy/、公开网关日志链到单元的实际路径 /var/log/aegis/public.log、deploy/.env 由网关配置加库超级账号拼成、aegis-payctl 编进 bin 并配易支付测试商户、admin / uniproxy / risk 三组一次性库确认变量），脚本之间空一个限流窗口
  - 顺序 admin → epay → support → uniproxy → risk → e2e：e2e.sh 的限流探测会打满登录额度，放最后
  - 每个脚本一行写进 e2e-results.md（结果、OK/FAIL 数、首个失败的步骤与原文），全部跑完、表格写完后有任何失败就以 1 退出
  - 只肯在 GitHub Actions 上跑
test-*-pg18.sh: 各业务的 PG18 集成门禁，每次新建隔离容器与库、结束即删；口令为 *-test-only 字样
test-install.sh: 安装链端到端，发现库里已有用户即拒绝执行
*_mock_test.sh / *_static_test.sh / release-stop-the-world_test.ps1: 对上面各脚本的桩测试与静态检查，不需要数据库
  - CI 的 panel-deploy.yml 逐个点名跑其中与安装、迁移、nginx、发布物绑定相关的几个（清单在 workflow 里，新增相关桩测试要补进去）
  - 其中 release-stop-the-world、verify-backup_manifest 两个 mock 测试需要 Linux root
fixtures/: billing、idempotency 两份 PG18 门禁种子数据

已移出主线
CLIENT-AUTH（00042–00044）的发布门禁、隔离 PG18 预检及其租约登记与残留清理、端到端与桩测试脚本已随代码一起删除，原样归档在 git 标签 archive/client-auth；冻结的两个迁移仍留在 ../migrations/frozen-client-auth/

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
