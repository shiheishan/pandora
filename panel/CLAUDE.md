# panel/
> L2 | 父级: /CLAUDE.md

面板主体：一个 Go module（github.com/aegispanel/aegis），产出三个 HTTP 域网关和一组运维命令，共用 internal/ 下的业务域与平台层。安全与财务不变量下沉到 PostgreSQL（RLS、追加写触发器、DEFERRABLE 配平、回调唯一约束），网关只是策略的执行者，不是策略的来源。

成员清单
cmd/: 14 个可执行入口（aegis-admin、aegis-adminctl、aegis-public、pandora-cic-journal 各带 CLAUDE.md）。aegis-public/admin/node 三个 HTTP 网关（节点侧由 pdnd 的 pandora-native 走两阶段接入，面板不再带节点代理）；aegis-adminctl 后台账号与角色、aegis-payctl 支付渠道、aegis-backup-webdav 备份上传；pandora-* 八个为 CLIENT-AUTH 校验器、root runner、journal、设备公钥分类器与路径信任工具，其中 cic-journal、00044-root-runner、device-key-classifier、pathtrust、release-journal 五个带 README
internal/: 全部业务与平台代码，四层 api → domain → platform，middleware 横切；见 internal/CLAUDE.md
migrations/: goose SQL 迁移，按序号递增，数据库层的安全与财务不变量（RLS、追加写触发器、DEFERRABLE 配平、回调唯一约束）以触发器与约束落在这里。现状到 00095，共 92 个 .sql；00073、00091、00092 空号，而 deploy/migrate.sh 与 check-migrations.sh 做连续性检查——已知问题，另行处理。00067 删除 21 张无依赖孤儿表且 Down 拒绝回滚；RESERVED-TABLES.md 登记仍保留、Go 从不引用的 16 张表及各自的锁定原因，由 platform/db 的 schema 契约测试守住；frozen-client-auth/ 为冻结的 CLIENT-AUTH 迁移，带 README
deploy/: 发布包 build-release.sh（先 make frontend-embed，无 npm 即失败），安装链 install.sh/migrate.sh/render-nginx.sh（nginx-aegis.conf 模板只含占位符，后台前缀取 AEGIS_ADMIN_PATH、域名取 AEGIS_PUBLIC_BASE_URL），备份 backup-postgres.sh/restore-postgres.sh/verify-backup.sh + WebDAV 配置样例，systemd 单元与 logrotate，PG18 隔离门禁脚本群 test-*-pg18.sh，client-auth 生成/校验/探针脚本群；BACKUP.md、LINUX-COMPATIBILITY.md、ADMIN-PASSWORD-RESET.md 为运维手册；见 deploy/CLAUDE.md
frontend/: 面板前端源码，React + TypeScript + Vite 一个工程两个入口（--mode admin|portal 分两次构建到 dist/{admin,portal}），按设计稿重写完成：底座、组件库、两边外框（登录态、reauth 对话框、实时事件、按权限隐藏入口）与全部模块页，模块页经 screens/ 登记表懒加载；本机无数据库时 dev 挂按模块拆分的假后端；见 frontend/CLAUDE.md
web/: 面板前端的 go:embed 嵌入点 app.go，admin/、portal/ 两个目录由两个网关经 platform/webapp 挂在根 /（入口）与 /assets/*；仓库只存占位入口，真实产物由 make frontend-embed 从 frontend/ 构建覆盖（旧的手写单页与 React 候选已于 2026-09-23 删除）；见 web/CLAUDE.md
docs/: redesign/api-contract.md 为现行前后端接口契约（被大量代码注释按节号与修订号 Rn 引用）；adr/0001 技术选型（文末补了后续变更）；DASH-01 冻结契约、CLIENT-AUTH-01 冻结契约及 R1 刷新重放附录、CLIENT-AUTH-00042 实现清单为历史冻结稿（只读；CLIENT-AUTH 三份的正文被测试解析或按 SHA-256 钉死，一字不改）
tools/: 开发期工具，main 包只经 go run 使用，不进发布包、不被任何包 import；refactorcheck/ 为第 5 阶段重构的纯挪动 AST 比对（compare）与打散验证（shatter），另带全 module 的 800 行守卫测试（随 go test ./... 跑），见 tools/refactorcheck/CLAUDE.md
tests/: invariants.sql 数据层不变量（make invariants）；e2e.sh 注册→下单→支付→账本→订阅→配置主链路；admin/uniproxy/epay/support 各自的 e2e 脚本（节点接入由冒烟 seed.ts 的两阶段接入覆盖）；panel-smoke.yml 经 deploy/run-smoke-e2e.sh 在冒烟栈上逐个跑它们，任一失败即 job 变红；见 tests/CLAUDE.md
Makefile: up/down/logs 数据基座、migrate/migrate-status/check-migrations、invariants、build/test/vet、e2e、release-linux、preflight-linux、settlement-pg18、verify、frontend-check（面板前端：npm ci + lint + typecheck + vitest + 构建）、frontend-embed（构建并同步 dist/{admin,portal} 到 web/{admin,portal}，release-linux 的前置）；CGO_ENABLED=0 产静态二进制
go.mod / go.sum: Go 1.26 module

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
