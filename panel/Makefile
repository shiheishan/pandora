SHELL := /bin/bash
GO ?= go
DEPLOY := deploy
BIN := bin

# CGO 关闭：产出静态二进制，容器与 Node Agent 分发都不必带 glibc
export CGO_ENABLED := 0

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## 整理并下载依赖
	$(GO) mod tidy

.PHONY: build
build: ## 编译全部网关
	@mkdir -p $(BIN)
	$(GO) build -trimpath -ldflags="-s -w" -o $(BIN)/ ./cmd/...
	@ls -la $(BIN)/

.PHONY: release-linux
release-linux: ## 生成带校验和的 linux/amd64 与 linux/arm64 发布包
	$(DEPLOY)/build-release.sh

.PHONY: preflight-linux
preflight-linux: ## 检查当前 Linux 宿主是否在支持范围内
	$(DEPLOY)/preflight-linux.sh

.PHONY: vet
vet: ## 静态检查
	$(GO) vet ./...

.PHONY: test
test: ## 运行 Go 单元测试
	$(GO) test -race -count=1 ./...

.PHONY: settlement-pg18
settlement-pg18: ## Run the disposable PostgreSQL 18 schema-40 settlement gate
	bash $(DEPLOY)/test-settlement-00040-pg18.sh

.PHONY: migrate
migrate: ## 迁移数据库（含临时库预检）
	$(DEPLOY)/migrate.sh up

.PHONY: migrate-status
migrate-status: ## 查看迁移状态
	$(DEPLOY)/migrate.sh status

.PHONY: check-migrations
check-migrations: ## 只在临时库演练迁移，不动主库
	$(DEPLOY)/check-migrations.sh

.PHONY: invariants
invariants: ## 运行数据层不变量测试（PRD 验收标准的可执行版本）
	$(DEPLOY)/psql.sh -v ON_ERROR_STOP=1 < tests/invariants.sql

.PHONY: e2e
e2e: ## 端到端链路测试：注册→下单→支付→账本→订阅→配置
	tests/e2e.sh

.PHONY: up
up: ## 启动数据基座
	cd $(DEPLOY) && docker compose up -d

.PHONY: down
down: ## 停止数据基座（保留数据卷）
	cd $(DEPLOY) && docker compose down

.PHONY: logs
logs: ## 查看数据基座日志
	cd $(DEPLOY) && docker compose logs -f --tail=100

.PHONY: frontend-check
frontend-check: ## React 候选前端：npm ci + typecheck + vitest（含 API 契约检查）+ 双 mode 构建
	cd frontend && npm ci && npm run check

.PHONY: frontend-embed
frontend-embed: ## React 候选前端：npm ci + 构建，把 dist/{admin,portal} 同步进 web/*/app 供 go:embed 下发到 /app/
	@command -v npm >/dev/null || { echo "frontend-embed 需要 Node 22.12+ 与 npm" >&2; exit 1; }
	cd frontend && npm ci && npm run build
	@for app in admin portal; do \
	  test -f frontend/dist/$$app/index.html || { echo "frontend/dist/$$app/index.html 缺失" >&2; exit 1; }; \
	  rm -rf web/$$app/app && mkdir -p web/$$app/app && \
	  cp -R frontend/dist/$$app/. web/$$app/app/ && rm -rf web/$$app/app/.vite && \
	  echo "embedded frontend/dist/$$app -> web/$$app/app"; \
	done

.PHONY: verify
verify: vet check-migrations invariants ## 提交前的完整校验
	@echo "全部校验通过"
