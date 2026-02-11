"""探测 AkShare 交易日历接口（A 股）。

目的
    回答：``tool_trade_date_hist_sina`` 是否能稳定提供 FR-PL-07 所需的
    "交易日 / 是否开市 / 上一交易日" 三要素？返回结构是什么样？

上游
    AkShare ``ak.tool_trade_date_hist_sina()``
    （新浪历史交易日列表，免登录、无频控压力）

被谁消费
    REQ-01 FR-PL-07：交易日历表 ``trade_calendar`` 的主数据来源
    （fallback 是仓库内手工导入的 csv）

用法
    python -m data_probe.probe_trade_calendar              # 完整探测
    python -m data_probe.probe_trade_calendar --year 2026  # 仅看指定年份
    python -m data_probe.probe_trade_calendar --json       # 机器可读输出
"""

from __future__ import annotations

import argparse
import json
import sys
from dataclasses import asdict, dataclass
from datetime import date, datetime


@dataclass
class ProbeReport:
    source: str
    fetched_at: str
    total_rows: int
    earliest: str
    latest: str
    sample_columns: list[str]
    sample_head: list[dict]
    sample_year: int | None
    sample_year_open_days: int | None
    notes: list[str]


def _load_calendar():
    try:
        import akshare as ak  # 延迟导入：未安装依赖时给清晰错误
    except ImportError as exc:  # pragma: no cover
        sys.stderr.write(
            "[probe_trade_calendar] akshare 未安装，请先：pip install -e .\n"
        )
        raise SystemExit(2) from exc

    df = ak.tool_trade_date_hist_sina()
    # AkShare 历史上字段名变过：trade_date / trade_date_sina / date 都见过
    # 这里探测式地找日期列，避免脚本随上游小变动炸掉
    candidate_cols = [c for c in df.columns if "date" in c.lower()]
    if not candidate_cols:
        raise RuntimeError(
            f"返回结构异常，没有 date 列；当前列：{list(df.columns)}"
        )
    date_col = candidate_cols[0]
    df[date_col] = df[date_col].astype(str)
    return df, date_col


def build_report(year: int | None) -> ProbeReport:
    df, date_col = _load_calendar()

    notes: list[str] = []
    if date_col != "trade_date":
        notes.append(
            f"日期列名为 {date_col!r}（非 trade_date），FR-PL-07 落库时要做字段映射"
        )

    sample_year = year
    sample_year_open_days: int | None = None
    if year is not None:
        year_df = df[df[date_col].str.startswith(f"{year:04d}")]
        sample_year_open_days = int(len(year_df))
        if sample_year_open_days == 0:
            notes.append(f"指定年份 {year} 在返回结果中无数据")

    head_rows = df.head(5).to_dict(orient="records")
    head_rows = [{k: str(v) for k, v in r.items()} for r in head_rows]

    return ProbeReport(
        source="akshare.tool_trade_date_hist_sina",
        fetched_at=datetime.now().isoformat(timespec="seconds"),
        total_rows=int(len(df)),
        earliest=str(df[date_col].min()),
        latest=str(df[date_col].max()),
        sample_columns=list(df.columns),
        sample_head=head_rows,
        sample_year=sample_year,
        sample_year_open_days=sample_year_open_days,
        notes=notes,
    )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--year", type=int, default=date.today().year, help="抽样统计的年份（默认当前年）")
    parser.add_argument("--json", action="store_true", help="以 JSON 输出（便于落档）")
    args = parser.parse_args(argv)

    report = build_report(args.year)

    if args.json:
        print(json.dumps(asdict(report), ensure_ascii=False, indent=2))
        return 0

    print(f"source         : {report.source}")
    print(f"fetched_at     : {report.fetched_at}")
    print(f"total_rows     : {report.total_rows}")
    print(f"date range     : {report.earliest} ~ {report.latest}")
    print(f"columns        : {report.sample_columns}")
    if report.sample_year is not None:
        print(f"open days {report.sample_year}: {report.sample_year_open_days}")
    print("head(5):")
    for row in report.sample_head:
        print(f"  {row}")
    if report.notes:
        print("notes:")
        for n in report.notes:
            print(f"  - {n}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
