# backend/Makefile（V0.1）
# 仅暴露稳定接口；具体实现随 REQ-01 各批次落地。

SHELL := /bin/bash

GO            ?= go
BIN_DIR       := bin
BIN_NAME      := fundpilot
MAIN_PKG      := ./cmd/fundpilot
MIGRATIONS    := ./migrations

# 数据库 DSN：优先读环境变量，否则用本地 docker compose 默认值
DB_DSN ?= postgres://fundpilot:fundpilot@localhost:5432/fundpilot?sslmode=disable

.PHONY: help build run test vet tidy migrate migrate-down openapi

help: ## 列出可用目标
	@awk 'BEGIN {FS = ":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  %-14s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## 编译二进制到 bin/
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/$(BIN_NAME) $(MAIN_PKG)

run: ## 直接运行（开发用）
	$(GO) run $(MAIN_PKG)

test: ## 跑全部测试
	$(GO) test ./...

vet: ## go vet
	$(GO) vet ./...

tidy: ## 整理依赖
	$(GO) mod tidy

migrate: ## 应用所有迁移（REQ-01 B2 启用 goose）
	@echo "TODO[REQ-01 B2]: integrate goose; will run: goose -dir $(MIGRATIONS) postgres \"$(DB_DSN)\" up"
	@exit 1

migrate-down: ## 回滚一步迁移（REQ-01 B2 启用 goose）
	@echo "TODO[REQ-01 B2]: integrate goose; will run: goose -dir $(MIGRATIONS) postgres \"$(DB_DSN)\" down"
	@exit 1

openapi: ## 生成 OpenAPI 契约（REQ-06 实现）
	@echo "TODO[REQ-06]: emit api/openapi.yaml"
	@exit 1
