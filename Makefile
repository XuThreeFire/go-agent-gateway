SHELL := /bin/bash
BIN := $(CURDIR)/bin
export GOCACHE := $(CURDIR)/tmp/go-build
export GOMODCACHE := $(CURDIR)/tmp/go-mod
export npm_config_cache := $(CURDIR)/tmp/npm-cache
CONFIG ?= backend/configs/dev/config.yaml
NPM_REGISTRY ?= https://registry.npmjs.org
.PHONY: help build build-backend build-cli run cli test test-integration vet web-install web-dev web-build web-typecheck web-test up down logs migrate doctor agent-demo privacy-check release-check
help: ## 显示命令
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-22s %s\n", $$1, $$2}'
build: build-backend build-cli ## 构建 Go 服务和 CLI
build-backend:
	@mkdir -p $(BIN)
	go build -trimpath -o $(BIN)/server ./backend/cmd/server
build-cli:
	@mkdir -p $(BIN)
	go build -trimpath -o $(BIN)/cli ./cli
run: ## 本地后端（先启动 MySQL 并 migrate）
	go run ./backend/cmd/server -config $(CONFIG)
cli: build-cli ## 检查后端健康
	$(BIN)/cli health
agent-demo: ## 无数据库、无密钥的最小 Agent 示例
	go run ./backend/cmd/agent-demo '计算 (12+3)*4'
test: ## Go 单元测试（集成测试在未设置测试库时跳过）
	go test ./backend/... ./cli/...
test-integration: ## 显式运行真实 MySQL 集成用例，必须是可丢弃测试库
	@test -n "$$GATEWAY_TEST_DATABASE_URL" || (echo '请设置 GATEWAY_TEST_DATABASE_URL（库名必须以 _test 结尾）'; exit 1)
	go test -count=1 -race ./backend/internal/store ./backend/internal/api
vet: ## Go 静态检查
	go vet ./backend/... ./cli/...
web-install: ## 使用锁文件安装前端依赖
	cd web && npm ci --registry=$(NPM_REGISTRY) --no-audit --no-fund
web-dev: ## 前端开发服务器 :5173
	cd web && npm run dev
web-typecheck:
	cd web && npm run typecheck
web-test: ## 前端行为测试
	cd web && npm test
web-build: ## 类型检查与构建
	cd web && npm run build
up: ## 启动完整本地模拟环境（首次需构建镜像）
	docker compose up -d --build
down: ## 停止本项目容器，保留数据库卷
	docker compose down
logs: ## 查看本地日志
	docker compose logs -f backend
migrate: ## 对 Compose 本地库执行尚未执行的迁移
	docker compose run --rm migrate
doctor: ## 检查依赖及可选的本地服务
	python3 scripts/doctor.py
privacy-check: ## 扫描待发布文件（不打印命中值）
	python3 scripts/privacy_check.py
release-check: privacy-check test vet web-test web-build ## 发布前检查

tools: ## 安装固定版本的迁移与密钥扫描工具到项目 bin/
	GOBIN=$(BIN) go install -tags mysql github.com/golang-migrate/migrate/v4/cmd/migrate@v4.18.3
	GOBIN=$(BIN) go install github.com/zricethezav/gitleaks/v8@v8.24.2
migrate-local: ## 原生 MySQL 迁移，DATABASE_URL 必须显式设置
	@test -n "$$DATABASE_URL" || (echo '请设置 DATABASE_URL'; exit 1)
	$(BIN)/migrate -path backend/migrations -database "$$DATABASE_URL" up
release: privacy-check ## 生成仅含公开文件的源码包
	python3 scripts/release.py

test-container: ## Docker 引擎就绪后，一条命令完成独立 MySQL 集成测试并清理
	bash scripts/test-container.sh
