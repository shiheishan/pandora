---
name: run-pandora
description: Run, start, build and screenshot pandora. On this Mac (no docker/PG/Redis) drives the panel frontend admin and portal on the dev mock API with Playwright, screenshots pages, reads page text, builds the pandora and pdnd Go binaries; on a machine with Docker starts the real panel (dev/.env, make up/migrate/serve/worker); on a test machine installs it with install.sh. 启动、运行、截图、构建面板前端与 Go 二进制，起真面板。
---

# 运行与驱动 pandora

路径都相对仓库根。面板是一个二进制 `pandora`（`panel/cmd/pandora`），子命令 `serve`（门户 :9000、后台 :9001、节点 gRPC :9443）、`worker`（队列与定时任务）、`migrate up|down|status`、`admin …`。它要 PostgreSQL 18 与 Redis。

三种环境，按手头的机器选：

| 环境 | 能看什么 | 起法 |
|---|---|---|
| 本机（这台 Mac，没有 docker、psql、redis-cli） | 前端假后端（`panel/frontend/dev/mock-api.ts`，状态在内存）、Go 构建、前端单测 | 下面「驱动」「构建 Go」 |
| 有 Docker 的开发机 | 真面板 + 真库，接口与页面一起看 | 下面「本地起真面板」 |
| 测试机 | 生产方式安装（systemd、nginx、证书） | 下面「测试机」 |

真栈上的自动化（PG 用例、e2e、冒烟栈、Playwright 购买路径）走 CI，见 verify skill。

## 先决条件（本机）

- Node ≥ 22.12，`panel/frontend/node_modules` 已装（装依赖时不要同时跑 go）。
- Playwright 的 chromium 已在 `~/Library/Caches/ms-playwright`（`@playwright/test` 是前端的 devDependency）。
- Go：本机缺省 1.27.1，pdnd 链接会失败，所有 go 命令加 `GOTOOLCHAIN=go1.26.9`（取 go.mod 的版本）。

## 驱动假后端（首选）

```bash
node .agents/skills/run-pandora/driver.mjs shots --out /tmp/pandora-run
node .agents/skills/run-pandora/driver.mjs text portal '#/plans'
```

- `shots`：起后台（5182）与门户（5181）两个 vite，登录假账号，后台截首页、`#/users`、`#/nodes`，门户截首页、`#/plans`，PNG 写到 `--out`。
- `text <admin|portal> <hash>`：登录后打开该地址，打印页面可见文字。
- 5181/5182 已有进程就复用，不会关；驱动自己起的在结束时关掉。
- 假账号：后台 `admin@example.test`，门户 `user@example.test`，口令都是 `pandora-dev-pass`（以 `panel/frontend/dev/mock-api.ts` 的 `MOCK_ACCOUNTS` 为准）。
- 门户另有 empty / multi / error / slow 等假场景，用法见 `.claude/rules/frontend-mock.md`（本 skill 未验证）。
- 手动看：`preview_start` 用 `.claude/launch.json` 里的 `admin-mock` / `portal-mock`。

## 构建 Go

```bash
GOTOOLCHAIN=go1.26.9 go build -o /tmp/pandora-run/bin/ ./cmd/pandora   # 在 panel/ 下，出一个 pandora 二进制
GOTOOLCHAIN=go1.26.9 go build -o /tmp/pandora-run/bin/pdnd .           # 在 pdnd/ 下
/tmp/pandora-run/bin/pandora --help
/tmp/pandora-run/bin/pdnd -self-check   # 能力矩阵自检，本机能跑
/tmp/pandora-run/bin/pdnd -version
```

`pandora serve` 在本机起不来：没有 PG 和 Redis。

## 本地起真面板（有 Docker 的机器）

在 `panel/` 下：

```bash
cp dev/.env.example dev/.env        # 把 CHANGE_ME 换成随机值（文件里写了怎么生成、哪几处要一致）
make up                             # PG18 + Redis 8，只绑 127.0.0.1:5433 / 6380
make migrate                        # pandora migrate up（以 pandora_owner）
make frontend-embed                 # 可选：把真前端嵌进二进制，否则 :9000 / :9001 的根路径只有占位页
make serve                          # 门户 http://127.0.0.1:9000，后台 http://127.0.0.1:9001
make worker                         # 另开一个终端：队列与定时任务
```

- 建管理员：`set -a && . ./dev/.env && set +a && go run ./cmd/pandora admin create --email <邮箱> --password-stdin`，口令从标准输入给（`printf '%s\n' "$pw" | …`），不写在命令行参数里。
- `dev/.env` 被 git 忽略；`make` 的各目标先把它导进环境（viper 按 `PANDORA_` 前缀读）。`make invariants` 跑数据层不变量，`deploy/psql.sh` 经 `PANDORA_DEPLOY_ENV=dev/.env` 进库。
- `make down` 停数据基座（保留数据卷）。

## 测试机

按生产方式装，不在测试机上 `make serve`：

1. 本机 `make -C panel release-linux`（要 Go、npm、GNU tar，出 `panel/dist/pandora-panel_<版本>_linux_<架构>.tar.gz`）；或用已发布的包。
2. 传到测试机，解到 root 独占的目录，`sudo bash <发布目录>/deploy/install.sh`（无人值守的变量见脚本头）。装到 `/opt/pandora`，配置在 `/etc/pandora/`，服务 `pandora-serve`、`pandora-worker`。
3. 后台地址：`sudo /opt/pandora/deploy/admin-url.sh`；进库：`sudo /opt/pandora/deploy/psql.sh`；排障按 `panel/deploy/RUNBOOK.md`。
4. 机器怎么开、登记在哪见 test-machine skill；现场值（IP、域名、后台前缀、口令）只放 `~/ai/servers/` 与 `ops-local/`，不进仓库。

## 测试

```bash
cd panel/frontend && npx vitest run   # 前端单测，几秒
```

Go 全量与 PG 用例以 GitHub 为准，本机跳过不等于通过。

## 坑

- vite 只监听 `[::1]`：用 `localhost`，`127.0.0.1` 连不上（驱动里探测端口也是 localhost）。
- 不要等 `networkidle`：后台有 SSE 长连接，永远不空闲，会超时 30 秒。驱动用 `load` 加 800ms。
- 登录后用 `page.getByLabel('邮箱')` 变隐藏判断登录成功；选择器只用角色与文字，项目约定不加 data-testid。
- 假后端状态在内存，vite 重启即复原；两个入口是两个进程，主题经 `node_modules/.cache/pandora-mock/appearance.json` 互通。
- 本机没有 `timeout` 命令。

## 排错

- `admin vite 30 秒内没起来`：端口被占或 node_modules 缺失，先 `lsof -nP -iTCP:5182`，再手动 `npm run dev:admin --prefix panel/frontend`。
- `pandora serve` 起来就退出：多半是配置校验没过（`PANDORA_DATABASE_URL`、`PANDORA_REDIS_URL`、三把密钥缺一个）或连不上 PG / Redis，看它打的第一条错误。
