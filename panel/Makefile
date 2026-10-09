SHELL := /bin/bash
GO ?= go
DEPLOY := deploy
BIN := bin
# 开发数据基座：变量取自 deploy/.env（从 .env.example 复制后填），与 deploy/ 下的运维脚本同一份
DEV_COMPOSE := docker compose -f dev/docker-compose.yml --env-file $(DEPLOY)/.env
# migrate.sh / check-migrations.sh 缺省找 /root/go/bin/goose（发布包与安装器的位置）；开发机取 PATH 上的
export GOOSE_BIN ?= $(or $(shell command -v goose 2>/dev/null),$(HOME)/go/bin/goose)

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
	$(GO) test -race -count=1 ./...

.PHONY: settlement-pg18
settlement-pg18: ## Run the disposable PostgreSQL 18 schema-40 settlement gate
	bash $(DEPLOY)/test-settlement-00040-pg18.sh

# 全新的库（没有 goose_db_version）照 install.sh 首装的做法：跳过克隆预检、按「写入者已停」迁移
# （空库没有写入者）；已有迁移记录的库照常先在临时库演练
.PHONY: migrate
migrate: ## 迁移数据库（含临时库预检；全新的开发库照首装跳过预检）
	@fresh="$$($(DEPLOY)/psql.sh -X -tAc "SELECT pg_catalog.to_regclass('public.goose_db_version') IS NULL" | tr -d '[:space:]')"; \
	case "$$fresh" in \
	  t) PANDORA_SKIP_PRECHECK_FRESH_DB=yes-empty-database PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes $(DEPLOY)/migrate.sh up ;; \
	  f) $(DEPLOY)/migrate.sh up ;; \
	  *) echo "查不到库的迁移状态：make up 起了吗？deploy/.env 填好了吗？" >&2; exit 1 ;; \
	esac

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
verify: vet check-migrations invariants ## 提交前的完整校验
	@echo "全部校验通过"
