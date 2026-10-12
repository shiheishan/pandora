---
name: run-pandora
description: Run, start, build and screenshot pandora locally (Mac, no docker/PG). Drives the panel frontend admin and portal on the dev mock API with Playwright, screenshots pages, reads page text, builds panel and pdnd Go binaries. 启动、运行、截图、构建面板前端与 Go 二进制；真网关与数据库要测试机或 CI。
---

# 本机运行与驱动 pandora

路径都相对仓库根。本机没有 docker、psql、Valkey，所以**真网关起不来**（`aegis-public` 缺 `AEGIS_DATABASE_URL` 等四个变量直接退出）。本机能跑、能看的是三样：前端假后端（`panel/frontend/dev/mock-api.ts`，状态在内存里）、panel 与 pdnd 的 Go 构建、前端单测。真栈（PG18、冒烟栈、Playwright 购买路径）走测试机或 CI，见 verify skill。

## 先决条件

- Node ≥ 22.12，`panel/frontend/node_modules` 已装（装依赖时不要同时跑 go）。
- Playwright 的 chromium 已在 `~/Library/Caches/ms-playwright`（`@playwright/test` 是前端的 devDependency）。
- Go：本机缺省 1.27.1，pdnd 链接会失败，所有 go 命令加 `GOTOOLCHAIN=go1.26.9`（取 go.mod 的版本）。

## 驱动（首选）

```bash
node .claude/skills/run-pandora/driver.mjs shots --out /tmp/pandora-run
node .claude/skills/run-pandora/driver.mjs text portal '#/plans'
```

- `shots`：起后台（5182）与门户（5181）两个 vite，登录假账号，后台截首页、`#/users`、`#/nodes`，门户截首页、`#/plans`，PNG 写到 `--out`。
- `text <admin|portal> <hash>`：登录后打开该地址，打印页面可见文字。
- 5181/5182 已有进程就复用，不会关；驱动自己起的在结束时关掉。
- 假账号：后台 `admin@pandora.dev`，只读管理员 `viewer@pandora.dev`，门户 `user@pandora.dev`，口令都是 `pandora-dev-pass`。
- 门户另有 empty / multi / error / slow 等假场景，用法见 `.claude/rules/frontend-mock.md`（本 skill 未验证）。
- 手动看：`preview_start` 用 `.claude/launch.json` 里的 `admin-mock` / `portal-mock`。

## 构建 Go

```bash
GOTOOLCHAIN=go1.26.9 go build -o /tmp/pandora-run/bin/ ./cmd/...   # 在 panel/ 下，约 10 秒，出 6 个 aegis-* 二进制
GOTOOLCHAIN=go1.26.9 go build -o /tmp/pandora-run/bin/pdnd .       # 在 pdnd/ 下，约 5 秒
/tmp/pandora-run/bin/pdnd -self-check   # 能力矩阵自检，本机能跑
/tmp/pandora-run/bin/pdnd -version
```

## 测试

```bash
cd panel/frontend && npx vitest run   # 68 个文件、862 条通过，约 3 秒
```

Go 全量与 PG18 用例以 GitHub 为准，本机跳过不等于通过。

## 坑

- vite 只监听 `[::1]`：用 `localhost`，`127.0.0.1` 连不上（驱动里探测端口也是 localhost）。
- 不要等 `networkidle`：后台有 SSE 长连接，永远不空闲，会超时 30 秒。驱动用 `load` 加 800ms。
- 登录后用 `page.getByLabel('邮箱')` 变隐藏判断登录成功；选择器只用角色与文字，项目约定不加 data-testid。
- 假后端状态在内存，vite 重启即复原；两个入口是两个进程，主题经 `node_modules/.cache/pandora-mock/appearance.json` 互通。
- 本机没有 `timeout` 命令。

## 排错

- `admin vite 30 秒内没起来`：端口被占或 node_modules 缺失，先 `lsof -nP -iTCP:5182`，再手动 `npm run dev:admin --prefix panel/frontend`。
- `配置不完整，拒绝启动`：真网关需要数据库、Valkey 与密钥，本机不跑。
