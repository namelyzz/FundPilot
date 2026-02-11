# ocr_sidecar/

V0.2 启用：基于 PaddleOCR 的支付宝持仓截图识别服务。

V0.1 阶段不开发，仅占位以固定目录结构。届时预期：

- 以独立 HTTP 服务运行（FastAPI），Go 后端通过 `httpclient` 调用
- 输入：截图文件；输出：结构化候选行（fund_code/name/shares/cost_value 等）+ 置信度
- 与确认页配合：识别结果必须经用户确认才能写入持仓表
