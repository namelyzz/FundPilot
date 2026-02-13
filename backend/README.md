# backend/

FundPilot 的 Go 后端模块。V0.1 阶段是整个项目唯一对外提供服务的进程。

- **Go 版本**：1.22+（go.mod 锁 1.22.5；依赖版本均 pin 在兼容 1.22 的范围，避免触发 toolchain 切换）
- **Module path**：`github.com/namelyzz/FundPilot`
- **运行依赖**：PostgreSQL 15 + TimescaleDB 2.x（由仓库根的 `docker-compose.yml` 提供）

项目整体背景与版本规划见仓库根的 [`README.md`](../README.md) 与 [`docs/`](../docs/)。

## 目录速查

| 目录 | 用途 |
|---|---|
| [`cmd/`](./cmd/) | 所有可执行入口（`fundpilot` 主服务、`migrate`、`calendar-seed`）。详见 [`cmd/README.md`](./cmd/README.md) |
| `internal/` | 服务内部代码，分为「横切平台能力」和「业务领域」两层 |
| `internal/platform/` | 横切平台能力：配置、日志、DB、HTTP server/client、调度器、交易日历、错误规范、失败语义模型。**不依赖任何业务领域** |
| `internal/asset/` | 持仓领域（REQ-02）。V0.1-B4 仍是占位 |
| `internal/fund/` | 基金元数据领域（REQ-03）。V0.1-B4 仍是占位 |
| `internal/market/` | 行情领域（REQ-04）。V0.1-B4 仍是占位 |
| `internal/valuation/` | 估值领域（REQ-05）。V0.1-B4 仍是占位 |
| `api/` | OpenAPI 契约输出目录（REQ-06）。`make openapi` 的产物落此 |
| `migrations/` | goose 管理的 SQL 迁移；命名 `NNNN_xxx.sql`，必须含 `+goose Up` 与 `+goose Down` 双向脚本 |
| `bin/` | `make build` 的产物（.gitignore 已忽略） |

## 分层约束

```
            cmd/* (main 包)
                 │ 装配
                 ▼
   internal/{asset,fund,market,valuation}  ← 业务领域，互不 import
                 │ 通过接口
                 ▼
        internal/platform/*  ← 横切能力，不反向依赖业务
```

硬约束：

- 业务领域之间**不得**互相 import；跨域协作通过在调用方定义接口，由 `cmd/fundpilot/main.go` 注入实现
- `internal/platform/*` 不得 import 任何 `internal/{asset,fund,market,valuation}` 包
- 任何写业务代码的人都必须遵守 [REQ-01 平台能力的使用约定](#req-02-写代码的必读约定)（见下文）

## internal/platform 子包一览

| 子包 | FR | 一句话 |
|---|---|---|
| `config` | FR-PL-02 | YAML + `FUNDPILOT_*` 环境变量覆盖；进程级只读 |
| `logger` | FR-PL-03 | `log/slog` + ctx 传 trace_id；业务侧 `logger.FromContext(ctx)` |
| `errors` | FR-PL-09 | 响应壳 `Envelope` + `WriteOK / WriteError` + 错误码 |
| `db` | FR-PL-04 | pgxpool 连接池 + Ping |
| `httpserver` | FR-PL-10 | chi 路由 + RequestID/TraceLogger/AccessLog/Recover 中间件 + `/health` |
| `failure` | FR-PL-08 | `SourcedValue[T]` / `Staleness` / `Confidence`——数据真实性核心 |
| `httpclient` | FR-PL-05 | 出站统一出口：超时 / 重试 / 限流 / 熔断 / UA |
| `scheduler` | FR-PL-06 | 进程内 cron + `IfTradingTime` 装饰器 |
| `calendar` | FR-PL-07 | A 股交易日历（PG-backed + 内存 cache） |

每个子包的 godoc 注释里写明了"该用什么、不该用什么"，开发前先翻一眼。

## 本地开发

```bash
# 1. 起依赖（在仓库根做一次）
cd ..
make up                                 # docker compose up -d

# 2. 准备配置（可选；默认配置就足够本地跑）
cp config.example.yaml config.yaml      # 按需改

# 3. 应用迁移
cd backend
make migrate

# 4. 灌交易日历（calendar 子包要求）
python -m data_probe.probe_trade_calendar --csv --from 2026-01-01 --to 2026-12-31 > /tmp/cal.csv
go run ./cmd/calendar-seed -input /tmp/cal.csv

# 5. 起服务
make run

# 验证
curl -s http://localhost:8080/health | jq .
```

`make help` 列出所有 backend 目标；仓库根 `make help` 列出顶层目标。

## 测试

```bash
make test            # 全部单测
go test ./internal/platform/scheduler/... -v -run TestRunner  # 单包单用例
go test ./... -count=1                                        # 关 cache
go test ./... -race                                           # race detector
```

V0.1-B4 累计 66 单测，全部不依赖真实数据库（db 包只覆盖错误路径；calendar 测试用 `LoadSnapshot` 注入内存数据）。需要真 DB 的集成测试目前没有，等 REQ-02 起会用 dockertest 之类引入。

## REQ-02 起写代码的必读约定

| 约束 | 怎么做 |
|---|---|
| 不要 `log.Println` | `logger.FromContext(ctx).Info(...)` |
| 不要 `os.Getenv` 业务配置 | 加字段到 `config.Config`，由 main 注入 |
| 不要直接 `http.Client.Do` 访问外部 | 注入 `*httpclient.Client`，按 source 调用 |
| 不要在领域包 import scheduler | 导出 `scheduler.Handler` 函数，让 `cmd/fundpilot/main.go` 注册 |
| 不要 `panic` 当错误处理 | 返回 `*errors.Error`；中间件会 recover 真正的 panic |
| 不要直接返回上游原值 | 包成 `failure.SourcedValue[T]`，明确 source/TTL/staleness |
| 不要让领域包互相 import | 调用方定义接口、main 注入实现 |
| 不要用全功能 ORM | 直接写 SQL；pgx 接口足够 |
| 不要自动跑迁移 | 服务启动时**不**触发；显式 `make migrate` |

新建领域 HTTP 端点的模板：

```go
// internal/asset/http.go
type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Register(r chi.Router) {
    r.Route("/v1/positions", func(r chi.Router) {
        r.Get("/", h.list)
        r.Post("/", h.create)
    })
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
    items, err := h.svc.List(r.Context())
    if err != nil { errors.WriteError(w, r, err); return }
    errors.WriteOK(w, r, items)
}
```

在 `cmd/fundpilot/main.go` 装配：

```go
assetHandler := asset.NewHandler(asset.NewService(pool))
assetHandler.Register(srv.Handler.(*chi.Mux))
```

## 加新东西时同步更新这份文档

- 新增 `cmd/<binary>` → 更新 [`cmd/README.md`](./cmd/README.md) 的命令表
- 新增 `internal/<新领域>` → 在本文件"目录速查"加一行
- 新增 `internal/platform/<新横切能力>` → 在本文件"internal/platform 子包一览"加一行
- 新增迁移 → 不需要改本文件；命名遵循 `NNNN_xxx.sql` 即可

不更新文档的提交不算完成。
