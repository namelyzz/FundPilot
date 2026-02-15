# backend/Makefile（V0.1）
# 仅暴露稳定接口；具体实现随 REQ-01 各批次落地。

SHELL := /bin/bash

GO            ?= go
BIN_DIR       := bin
BIN_NAME      := fundpilot
MAIN_PKG      := ./cmd/fundpilot
MIGRATIONS    := ./migrations

# 尝试引入本地的 .env 文件
#（前面的减号 - 表示如果找不到该文件，也不报错。我在目录下放了 .env.example ，改为 .env 并填入自己的账号密码等信息作为环境变量）
-include .env
export

# 数据库 DSN：优先读环境变量，否则用本地默认值
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

migrate: ## 应用所有待执行迁移
	FUNDPILOT_DATABASE_DSN="$(DB_DSN)" $(GO) run ./cmd/migrate up -dir $(MIGRATIONS)

migrate-down: ## 回滚一步迁移
	FUNDPILOT_DATABASE_DSN="$(DB_DSN)" $(GO) run ./cmd/migrate down -dir $(MIGRATIONS)

migrate-status: ## 查看迁移状态
	FUNDPILOT_DATABASE_DSN="$(DB_DSN)" $(GO) run ./cmd/migrate status -dir $(MIGRATIONS)

openapi: ## 生成 OpenAPI 契约（REQ-06 实现）
	@echo "TODO[REQ-06]: emit api/openapi.yaml"
	@exit 1
