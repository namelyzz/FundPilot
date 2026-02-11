# FundPilot 顶层 Makefile（V0.1）
# 编排跨语言子工程；具体任务委托到 backend/ 与未来的 frontend/。

SHELL := /bin/bash

.PHONY: help up down backend frontend lint

help: ## 列出可用目标
	@awk 'BEGIN {FS = ":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  %-12s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

up: ## 启动本地 PG + TimescaleDB（docker compose up -d）
	docker compose up -d

down: ## 关闭本地依赖
	docker compose down

backend: ## 进入 backend/ 执行默认目标
	$(MAKE) -C backend

frontend: ## 占位：V0.2 启用 React 工程
	@echo "frontend not implemented in V0.1"

lint: ## 全栈静态检查（当前仅 backend vet，后续扩展 golangci-lint / ruff）
	$(MAKE) -C backend vet
