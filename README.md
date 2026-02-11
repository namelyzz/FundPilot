# FundPilot

个人基金投研平台。本地后端服务，对外仅 REST API。

> 当前阶段：**V0.1 / B1 骨架已就位**
> 详细需求见 `docs/版本计划/V0.1/REQ-00 ~ REQ-06`，范围以 `REQ-00` 为准。

## 目录概览

```
fundpilot/
├── backend/        # Go 后端服务（REQ-02 ~ REQ-06 的实现）
│   ├── cmd/fundpilot/   # 应用入口
│   ├── internal/        # 领域 + 横切平台能力
│   ├── api/             # OpenAPI 契约输出（REQ-06）
│   ├── migrations/      # goose SQL 迁移
│   └── Makefile
├── frontend/       # V0.2 React 应用（V0.1 仅占位）
├── script/         # Python 工具：data_probe（V0.1）+ ocr_sidecar（V0.2）
├── docs/           # 项目计划书 / 版本计划 / 调查结果
├── docker-compose.yml   # PG + TimescaleDB
├── config.example.yaml  # 复制为 config.yaml 后修改
├── Makefile
└── README.md
```

## 快速开始（B1 阶段）

```bash
# 1. 复制配置
cp config.example.yaml config.yaml

# 2. 起本地依赖（需要 Docker）
make up                      # 或：docker compose up -d

# 3. 跑空壳服务（B1 阶段仅打印启动日志并阻塞）
make backend run             # 或：cd backend && go run ./cmd/fundpilot
```

> `make` 在 Git Bash 默认环境中未安装；可通过 `choco install make` 或直接用每个目标后面注释的等价命令。

## 数据探测脚本（Python）

```bash
cd script
python -m venv .venv && source .venv/Scripts/activate
pip install -e ".[dev]"
python -m data_probe.probe_trade_calendar --year 2026
```

## 开发顺序

REQ-01（平台）→ REQ-02 / REQ-03 / REQ-04 可并行 → REQ-05（估值）→ REQ-06（API）。
当前 REQ-01 进度：B1 ✅，下一步 B2（config/logger/db/errors + 第一条迁移）。
