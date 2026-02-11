# script/

FundPilot 的 Python 辅助工程。Go 后端负责服务与业务编排，**所有外部数据抓取/解析的脏活脱给 Python**。

## 子目录

| 目录 | 阶段 | 用途 |
|---|---|---|
| `data_probe/` | V0.1 | 一次性的数据源接口探测脚本——验证 AkShare/东财等上游字段、返回结构、稳定性；探测结果会沉淀到 REQ-03/04 文档 |
| `ocr_sidecar/` | V0.2 | 支付宝持仓截图 OCR 服务（FastAPI + PaddleOCR） |

## 环境

```bash
cd script
python -m venv .venv
source .venv/Scripts/activate   # Git Bash on Windows
# 或：source .venv/bin/activate  # Linux/macOS
pip install -e ".[dev]"
```

## 运行探测脚本

```bash
python -m data_probe.probe_trade_calendar
```

每个 `probe_*.py` 脚本必须自带 `--help`，并在文件顶部注明：
- **目的**：要回答的问题
- **上游**：被探测的数据源 & 接口
- **被谁消费**：对应 REQ-XX 中的哪个 FR

## 与 Go 后端的协作约定

V0.1 阶段：Python 脚本一次性产出探测报告 → 决定 Go 端实现细节。Go 不直接调用这些脚本。

V0.2 之后：长驻服务（如 `ocr_sidecar`）通过 HTTP 暴露给 Go 后端调用，遵守 REQ-01 FR-PL-05 的 `httpclient` 失败语义。
