# backend/cmd

本目录存放 backend 模块的所有**可执行入口**。每个子目录是一个独立的 `main` 包，对应一个二进制。
Go 约定 `cmd/<binary-name>/main.go`，目录名 = 编译产出的二进制名。

## 当前命令

| 命令 | 类型 | 用途 | 何时跑 |
|---|---|---|---|
| [`fundpilot`](./fundpilot/) | 长驻 | FundPilot 后端主服务：HTTP API + 进程内 scheduler + 估值/行情等领域逻辑 | 开发主路径；生产部署 |
| [`migrate`](./migrate/) | 一次性 | 应用 / 回滚 `migrations/` 下的 goose SQL 迁移 | schema 变更后；新环境初始化 |
| [`calendar-seed`](./calendar-seed/) | 一次性 | 把 trade_calendar CSV 入库（FR-PL-07 数据兜底路径） | 首次启动；每月初手工刷新交易日历 |

## 用法速查

```bash
# 主服务（前台运行；Ctrl+C 优雅关闭）
make run                                                 # 等价 go run ./cmd/fundpilot
go run ./cmd/fundpilot -config ../config.yaml            # 指定配置路径
FUNDPILOT_SERVER_PORT=9090 go run ./cmd/fundpilot        # 环境变量覆盖

# 迁移
make migrate              # goose up：应用所有待执行迁移
make migrate-down         # goose down 1：回滚一步
make migrate-status       # 查看当前版本

# 灌交易日历
python -m data_probe.probe_trade_calendar --csv --from 2026-01-01 --to 2026-12-31 > cal.csv
go run ./cmd/calendar-seed -input cal.csv
# 或管道：
python -m data_probe.probe_trade_calendar --csv | go run ./cmd/calendar-seed
```

所有命令都支持三种 DSN 来源（优先级从高到低）：
1. 环境变量 `FUNDPILOT_DATABASE_DSN`
2. 命令行 `-dsn` 标志（一次性命令）
3. `config.yaml` 的 `database.dsn` 字段（通过 `-config` 指定路径）

## 加新命令的步骤

当某个新功能确实需要独立二进制（典型场景：批量数据导入、一次性数据修复、与服务进程职责不同的常驻 sidecar）时：

1. 新建 `cmd/<name>/main.go`，包名一律 `main`
2. **不要**在 `main.go` 里写业务逻辑——只做 flag 解析、依赖装配、调用 `internal/...` 包
3. 优先复用现有平台能力（config / db / logger / errors），不要重复造 DSN 解析等代码
4. 在本 README 的"当前命令"表里加一行
5. 如果需要常驻并对外暴露端口，考虑是不是应该写进 `fundpilot` 而非独立二进制——多进程会让本地部署变复杂

## 不在 cmd/ 里的东西

- 业务领域逻辑：放 `internal/{asset,fund,market,valuation}/`
- 横切能力（日志/配置/调度/HTTP 客户端等）：放 `internal/platform/`
- Python 工具脚本（数据探测 / OCR）：放仓库根的 `script/`，不归 backend 管
