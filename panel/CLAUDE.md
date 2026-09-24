# panel/
> L2 | 父级: /CLAUDE.md

面板主体：一个 Go module（github.com/aegispanel/aegis），产出三个 HTTP 域网关、一个节点代理和一组运维命令，共用 internal/ 下的业务域与平台层。安全与财务不变量下沉到 PostgreSQL（RLS、追加写触发器、DEFERRABLE 配平、回调唯一约束），网关只是策略的执行者，不是策略的来源。

成员清单
cmd/: 15 个可执行入口。aegis-public/admin/node 三个 HTTP 网关，aegis-agent 运行在节点上的代理；aegis-adminctl 后台账号与角色、aegis-payctl 支付渠道、aegis-backup-webdav 备份上传；pandora-* 七个为 CLIENT-AUTH 校验器、root runner、journal 与路径信任工具，各自带 README
internal/: 全部业务与平台代码，四层 api → domain → platform，middleware 横切；见 internal/CLAUDE.md
migrations/: goose SQL 迁移按序号递增，已合入主线到 00067；第 3 阶段各会话按号段编号（后端二 00074 起：00074 注册验证码模板种子、00075 内置主题换成「默认 · 纸白」、00076 流量余额移出变更通知），合并时由协调会话压紧，不变量以触发器与约束落在这里；00067 删除 21 张无依赖孤儿表且 Down 拒绝回滚；RESERVED-TABLES.md 登记仍保留、Go 从不引用的 16 张表及各自的锁定原因，由 platform/db 的 schema 契约测试守住；frozen-client-auth/ 为冻结的 CLIENT-AUTH 迁移，带 README
deploy/: 发布包 build-release.sh（先 make frontend-embed，无 npm 即失败），安装链 install.sh/migrate.sh/render-nginx.sh（nginx-aegis.conf 模板只含占位符，后台前缀取 AEGIS_ADMIN_PATH、域名取 AEGIS_PUBLIC_BASE_URL），备份 backup-postgres.sh/restore-postgres.sh/verify-backup.sh + WebDAV 配置样例，systemd 单元与 logrotate，PG18 隔离门禁脚本群 test-*-pg18.sh，client-auth 生成/校验/探针脚本群；BACKUP.md、LINUX-COMPATIBILITY.md、ADMIN-PASSWORD-RESET.md 为运维手册；见 deploy/CLAUDE.md
frontend/: 面板前端源码，React + TypeScript + Vite 一个工程两个入口（--mode admin|portal 分两次构建到 dist/{admin,portal}），按设计稿重写中；见 frontend/CLAUDE.md
web/: 面板前端的 go:embed 嵌入点 app.go，admin/、portal/ 两个目录由两个网关经 platform/webapp 挂在根 /（入口）与 /assets/*；仓库只存占位入口，旧的手写单页与 React 候选已于 2026-09-23 删除，前端按设计稿在 frontend/ 重写；见 web/CLAUDE.md
docs/: redesign/ 面板重构各阶段开工说明与接口契约（协调会话维护）；XBoard 功能对标与实施计划、并行实施路线图、DASH-01 与 CLIENT-AUTH-01 冻结契约及 R1 附录、CLIENT-AUTH-00042 实现清单；adr/0001 技术选型
tests/: invariants.sql 数据层不变量（make invariants）；e2e.sh 注册→下单→支付→账本→订阅→配置主链路；admin/node/uniproxy/epay/support 各自的 e2e 脚本
Makefile: up/down/logs 数据基座、migrate/migrate-status/check-migrations、invariants、build/test/vet、e2e、release-linux、preflight-linux、settlement-pg18、verify、frontend-check（面板前端：npm ci + lint + typecheck + vitest + 构建）、frontend-embed（构建并同步 dist/{admin,portal} 到 web/{admin,portal}，release-linux 的前置）；CGO_ENABLED=0 产静态二进制
go.mod / go.sum: Go 1.26 module

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
