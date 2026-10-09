---
name: new-admin-module
description: pandora 后台新建一个模块（侧栏多一项、`#/<模块>` 一整页、配一组 v1 管理接口与权限码）时的登记点清单：domain 包、api/admin 的 router 与 router_<x>.go（权限、重认证、幂等 scope）、cmd/aegis-admin 后台循环与 workers.Add 契约、迁移里的权限码与角色授权、configure-app-role、run-pg18-gates 的 DOMAINS、前端 modules.ts / SCREENS / prefetch、dev 假后端模块与 ADMIN_PERMISSIONS、规则文件，逐处写清改什么、哪条测试会拦、哪处没有守卫。要「加一个后台页 / 后台模块」「新建 xx 管理」「侧栏加一项」，或验收新模块分支查漏登记时使用。迁移本身怎么写用 new-migration；只给已有模块加标签或接口，看第 1 节对应行即可。
---

# 新建后台模块

一个后台模块要在二十来处登记，散在五六条规则里。w9cert（证书模块）合入时漏了假后端的两处登记，CI 没拦住，是总协调手补的（fdc0ae2）。完整实例：`git diff 04bb54b 467ba3f -- panel` 加 `git show fdc0ae2 -- panel/frontend`。

## 0. 先定名字

| 名字 | 用在哪 | 证书模块的取值 |
|---|---|---|
| ModuleKey（前端，短的小写词） | `#/<key>`、`src/admin/screens/<key>/`、`dev/mock/admin/<key>.ts`。守卫按这个名字找假后端文件 | `certs` |
| domain 包 | `panel/internal/domain/<x>/` | `certs` |
| 权限码，读写分开 | 读码给 `modules.ts` 的标签和 GET 路由；写码给写路由和页面里的 `can(...)`。`high_risk` 照 00010 里同类码的取值（证书写码是 true） | `node.certificate.read` / `node.certificate.write` |
| 幂等 scope，每个接口一个 | Go 路由与假后端的 `idempotent(...)` 写同一个字符串 | `certificate_create`、`dns_credential_create` |
| PG18 域名与夹具 id 前缀 | `run-pg18-gates.sh` 的 DOMAINS 加一行，`pg18test.Fixture` 和它一一对应 | 域 `certs`，租户 id 前缀 `ce57` |

已有的能复用就复用，对应的行从「新建」变成「追加」：

- 模块落在已有 domain 包里：不建包，第 19 行改已有的 `domain-<x>.md`。
- 已有合适的权限码（如通知类的 `ops.notification.read` / `write`）：不插新码，第 2 行只剩建表，第 16 行多半已经有了。
- 同包已有 PG18 域：不加 DOMAINS 行，把新顶层函数名加进那一行的 `-run`（或挂成已登记函数的子测试）。

## 1. 登记点 → 守卫

「无守卫」的行漏了 CI 也是绿的，要自己对一遍。只给已有模块加标签或接口时，看第 2–4、9、11、13、14、16 行。

### 后端

| # | 文件 | 改什么 | 拦它的守卫 |
|---|---|---|---|
| 1 | `panel/internal/domain/<x>/`（新包） | 服务、SQL、审计事件。分层按现行规范（第 2 节） | `panel/internal/api/handler_sql_guard_test.go` 的 `TestHandlersRunNoSQL`（处理器里出现 SQL 就红）；跨域依赖方向**无守卫** |
| 2 | `panel/migrations/NNNNN_*.sql` | 建表；`INSERT INTO permissions` 插读写两个码，并授给角色（见第 2 节的坑）。写法按 new-migration skill；合进主线后由总协调把 Up 段登记进 `upsegments.txt` | 路由用了字典里没有的码：`api/admin/permission_catalog_contract_test.go` 的 `TestRoutePermissionsExistInCatalog`。新表没有 Go 引用：`TestSchemaTablesAreReferencedOrRegistered`。码**没授给任何角色**：无守卫 |
| 3 | `panel/internal/api/admin/<x>.go` | 处理器只做 DTO，调 domain。依赖从 `Deps` 取，缺依赖就不挂路由（先例 `newCertHandlers` 没有 Envelope 时返回 nil，守卫 `certificates_route_test.go`） | `TestHandlersRunNoSQL` |
| 4 | `panel/internal/api/admin/router_<x>.go`（新文件） | `register<X>Routes(r, d)`，每条路由逐条声明门槛，顺序和取舍见 `.claude/rules/api-admin-routes.md` | 码存在：`TestRoutePermissionsExistInCatalog`。**门槛顺序、哪条要重认证、scope 不和别的接口重名，都只有按路由组写的测试**：给新模块写一个，先例 `route_groups_routes_test.go` 的 `TestRouteGroupRouteProtections`（经 `security_guards_test.go` 的 `adminRouteChain` 走 `NewRouter`，所以同时证明第 5 行挂上了）。证书模块没写 |
| 5 | `panel/internal/api/admin/router.go` | `NewRouter` 里加一行 `register<X>Routes(r, d)`；要新依赖时给 `Deps` 加字段，并在 `cmd/aegis-admin/main.go` 装配 | **无守卫**，除非第 4 行的路由测试走了 `NewRouter`。漏了整组接口 404，假后端上看不出来 |
| 6 | `panel/cmd/aegis-admin/main.go`（有后台循环才改） | `workers.Add(N)` 加 1，照抄已有的 `go func`：`defer workers.Done()`、`newLoopPacer`、`case <-ctx.Done():`、`context.WithTimeout(ctx, …)` | `main_contract_test.go` 的 `TestAdminWorkersShareSignalContextAndJoinBeforeCleanup`（四处计数都写死 N）与 `main_activity_rollup_contract_test.go` 的 `TestAdminWiresActivityDailyRollup`（写死 `workers.Add(N)`），两份同一提交改 |
| 7 | `panel/internal/platform/config/<x>.go` 与 `config.go` 的 `Load`（要读环境变量才改） | 新配置只经 config 读；`Config` 加字段 | `envaccess_test.go` 的 `TestEnvironmentIsReadOnlyThroughConfig` |
| 8 | `panel/deploy/configure-app-role.sql` 与 `panel/internal/platform/db/configure_role_contract_test.go`（有不许改、不许删的表才改） | 在末尾整表重授之后补 REVOKE，并把这一行加进契约列表 | `TestConfigureAppRoleRevokesDeleteOnGuardedTablesLast`，只核列表里的行：不加进列表就没有守卫 |
| 9 | PG18 用例与 `panel/deploy/run-pg18-gates.sh` | 新域在 DOMAINS 加一行，`-run` 写精确的顶层函数名；以后同域新增顶层函数也要加进去。夹具 id 先查重 | 写法和查重命令见 `.claude/rules/platform-pg18.md`。只在 GitHub 跑，SKIP 与「一个 PASS 都没有」判红；夹具 id 撞号**无守卫**，红了才知道 |
| 10 | 所有新 `.go` 文件 | 单文件 ≤ 800 行 | `panel/tools/refactorcheck/linelimit_test.go` 的 `TestGoFilesStayWithinLineLimit` |
| 10a | `panel/go.mod`、`panel/go.sum`（引入第三方库才改） | 新依赖（证书模块引入了 lego）先说明为什么不自己写、维护状况与许可证，再 `go mod tidy` | **无硬守卫**：CI 的 go-vulncheck 只报告、不挡合并（结论在 job summary）。按 deps-upgrade 第 4 节审新依赖，并会触发 adversarial-review（新依赖） |

### 前端（`panel/frontend/`）

| # | 文件 | 改什么 | 拦它的守卫 |
|---|---|---|---|
| 11 | `src/admin/modules.ts` | `ModuleKey` 联合类型；`MODULES` 加一项（`title`、`group`、`read`，有标签时加 `tabs`，每个标签一个读码）；`NAV_GROUPS` 里放进同名分组。规则见 `.claude/rules/frontend-shells.md` | `src/admin/admin.test.ts` 的 "declares a permission for every tab…"；`tests/admin-module-registry.test.ts` 核 `NAV_GROUPS` |
| 12 | `src/admin/screens/index.ts` 的 `SCREENS`、`src/admin/prefetch.ts` 的 `SCREEN_CHUNKS` | 各加一行 `import('./<key>')` | `Record<ModuleKey, …>` 类型（`npm run typecheck`）；`src/admin/prefetch.test.ts` 的 "covers exactly the modules…" |
| 13 | `src/admin/screens/<key>/` | `index.tsx` 默认导出，按 `AdminScreenProps` 的 `tab` / `rest` 分标签；schemas → queries → logic → 组件，写按钮用 `can('<写码>')` 藏。规则见 `.claude/rules/screens-admin.md` | 纯函数层的单测自己写 |
| 14 | `dev/mock/admin/<key>.ts` | 导出一个 `MockModule`，路由形状、错误码、文案照 Go 处理器写；写接口按 `requirePermission` → `requireReauth` → `idempotent(scope)` 调，scope 与 Go 相同。规则见 `.claude/rules/frontend-mock.md` | `tests/admin-module-registry.test.ts`：文件按 ModuleKey 命名；页面里写死的每个 `api.<方法>('v1/…')` 假后端都要有路由 |
| 15 | `dev/mock/admin/index.ts` | 导入并加进 `ADMIN_MODULES`（顺序有意义：先匹配先得） | `tests/admin-module-registry.test.ts`（w9cert 漏的就是这里） |
| 16 | `dev/mock-api.ts` | `ADMIN_PERMISSIONS` 加读写两个码；只读账号也该看到这一页时，再加进 `VIEWER_PERMISSIONS` | `tests/admin-module-registry.test.ts`：`modules.ts` 的读码、页面里的 `can('…')`、假后端的 `requirePermission('…')` 都要在管理员权限里，而且都要是迁移插过的码（w9cert 漏的另一处） |
| 17 | `tests/mock-admin-<key>.test.ts` | 用页面的 zod schema 核对假后端的响应 | **无守卫**（约定见 `.claude/rules/screens-admin.md`；证书模块目前没有） |
| 18 | `tests/smoke/admin.smoke.ts` | 用主列表的 schema 对真网关读一遍 | **无守卫**，只在 GitHub 冒烟栈跑；证书模块目前没加 |

### 文档与 skill

| # | 文件 | 改什么 | 守卫 |
|---|---|---|---|
| 19 | `.claude/rules/domain-<x>.md`、`.claude/rules/screens-admin-<key>.md` | 头部 `paths:` 分别写 `"panel/internal/domain/<x>/**"`、`"panel/frontend/src/admin/screens/<key>/**"`；只写不看代码就不知道的约束和守卫测试名 | **无守卫**；证书模块两份都没建 |
| 20 | `.claude/skills/flow-walk/SKILL.md` 的「后台流程」表 | 加一行这个模块的主流程 | **无守卫** |
| 21 | `docs/<x>.md`（面向部署者的产品文档，需要时） | 部署者要知道的配置、外部依赖、故障表现；证书模块写在 `docs/node-certificates.md` | **无守卫** |

## 2. 后端写法：只引用现行规范

- 分层、依赖方向、处理器不跑 SQL：`.claude/rules/panel-architecture.md`、`.claude/rules/domain-layering.md`。
- 门槛顺序、重认证的取舍、scope 不共用：`.claude/rules/api-admin-routes.md`。中间件、404 而不是 403：`.claude/rules/panel-middleware.md`。
- 换栈 S0–S9（chi + pgx → gin + GORM，设计稿是主目录里被 git 忽略的 `.claude/stack-migration-design.md`）会改第 3–6 行的写法：路由改为表化注册，§9.2 还要加表属主登记。届时以那一步的规范为准，并同步更新本节。前端和假后端的登记点（第 11–18 行）不受换栈影响。

**坑：新权限码不会自动授给任何人。** `platform_admin` 的「全部权限」是 00010 执行那一刻展开的，之后插的码不会自动带上（00084 的文件头写了这一点）。迁移里必须显式授权，二选一：

- 照 00147：读码授给已有相邻读码的角色，写码授给已有相邻写码的角色（`INSERT INTO role_permissions … SELECT rp.role_id, '<码>' FROM role_permissions rp WHERE rp.permission_code = '<相邻码>'`）。
- 照 00084：授给全部 `is_system` 角色。

这一步没有守卫：假后端的管理员看得到这一页，真机上登录后台却看不到入口。

## 3. 自证

本地跑，go 与 `npm ci` 不要同时跑：

```bash
cd panel && go test ./internal/api/... ./internal/platform/db/ ./internal/platform/config/ ./cmd/aegis-admin/ ./internal/domain/<x>/ ./tools/refactorcheck/
cd panel/frontend && npm run lint && npm run typecheck && npm run test
```

对一遍「无守卫」的几行（在仓库根目录）：

```bash
git grep -n 'register<X>Routes' panel/internal/api/admin/router.go        # 第 5 行
git grep -l "'<读码>'" panel/migrations/ | xargs grep -Hn 'INSERT INTO role_permissions'   # 第 2 行：授给了角色
ls panel/frontend/tests/mock-admin-<key>.test.ts .claude/rules/domain-<x>.md .claude/rules/screens-admin-<key>.md
```

推送后按 verify skill 等 `wait-status.sh` 和 `wait-github.sh`：PG18 域与冒烟栈只在 GitHub 上跑。页面在假后端上点一遍走 flow-walk skill。
