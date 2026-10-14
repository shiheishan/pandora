SHELL := /bin/bash
GO ?= go
DEPLOY := deploy
BIN := bin
# 本机开发环境：变量取自 dev/.env（从 dev/.env.example 复制后填）。数据基座、pandora、psql.sh 读同一份
DEV_ENV := dev/.env
DEV_COMPOSE := docker compose -f dev/docker-compose.yml --env-file $(DEV_ENV)
# 先把 dev/.env 导进环境再跑 pandora（viper 按 PANDORA_ 前缀读）
PANDORA := set -a && . ./$(DEV_ENV) && set +a && $(GO) run ./cmd/pandora
PSQL := PANDORA_DEPLOY_ENV=$(DEV_ENV) $(DEPLOY)/psql.sh

# CGO 关闭：产出静态二进制，发布包分发不必带 glibc
export CGO_ENABLED := 0

.PHONY: help
help:
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## 整理并下载依赖
	$(GO) mod tidy

.PHONY: build
build: ## 编译 cmd/ 下全部可执行入口
	@mkdir -p $(BIN)
	$(GO) build -trimpath -ldflags="-s -w" -o $(BIN)/ ./cmd/...
	@ls -la $(BIN)/

.PHONY: release-linux
release-linux: ## 生成带校验和的 linux/amd64 与 linux/arm64 发布包
	$(DEPLOY)/build-release.sh

.PHONY: vet
vet: ## 静态检查
	$(GO) vet ./...

.PHONY: test
test: ## 运行 Go 单元测试
	CGO_ENABLED=1 $(GO) test -race -count=1 ./...

.PHONY: migrate
migrate: ## 迁移开发库到最新
	$(PANDORA) migrate up

.PHONY: migrate-status
migrate-status: ## 查看迁移状态
	$(PANDORA) migrate status

.PHONY: serve
serve: ## 本机起面板（门户 :9000、后台 :9001、节点 gRPC :9443）
	$(PANDORA) serve

.PHONY: worker
worker: ## 本机起队列与定时任务
	$(PANDORA) worker

.PHONY: invariants
invariants: ## 运行数据层不变量测试（PRD 验收标准的可执行版本）
	$(PSQL) -X -v ON_ERROR_STOP=1 < tests/invariants.sql

.PHONY: e2e
e2e: ## 端到端链路测试：注册→下单→支付→账本→订阅→配置
	tests/e2e.sh

.PHONY: up
up: ## 启动开发数据基座（dev/docker-compose.yml，等到健康为止）
	$(DEV_COMPOSE) up -d --wait

.PHONY: down
down: ## 停止开发数据基座（保留数据卷）
	$(DEV_COMPOSE) down

.PHONY: logs
logs: ## 查看开发数据基座日志
	$(DEV_COMPOSE) logs -f --tail=100

.PHONY: frontend-check
frontend-check: ## 面板前端：npm ci + lint + typecheck + vitest + 双 mode 构建
	cd frontend && npm ci && npm run check

.PHONY: frontend-embed
frontend-embed: ## 面板前端：npm ci + 构建，把 dist/{admin,portal} 同步进 web/{admin,portal} 供 go:embed 在网关根 / 下发
	@command -v npm >/dev/null || { echo "frontend-embed 需要 Node 22.12+ 与 npm" >&2; exit 1; }
	cd frontend && npm ci && npm run build
	@for app in admin portal; do \
	  test -f frontend/dist/$$app/index.html || { echo "frontend/dist/$$app/index.html 缺失" >&2; exit 1; }; \
	  rm -rf web/$$app && mkdir -p web/$$app && \
	  cp -R frontend/dist/$$app/. web/$$app/ && rm -rf web/$$app/.vite && \
	  echo "embedded frontend/dist/$$app -> web/$$app"; \
	done

.PHONY: verify
verify: vet test invariants ## 提交前的完整校验（invariants 要先 make up、make migrate）
	@echo "全部校验通过"
