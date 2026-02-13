"""探测 AkShare 交易日历接口（A 股），并能输出落库就绪的 CSV。

目的
    回答：``tool_trade_date_hist_sina`` 是否能稳定提供 FR-PL-07 所需的
    "交易日 / 是否开市 / 上一交易日" 三要素？返回结构是什么样？

上游
    AkShare ``ak.tool_trade_date_hist_sina()``
    （新浪历史交易日列表，免登录、无频控压力）

被谁消费
    REQ-01 FR-PL-07：交易日历表 ``trade_calendar`` 的主数据来源。
    Go 端 ``cmd/calendar-seed`` 消费本脚本的 ``--csv`` 输出。

用法
    python -m data_probe.probe_trade_calendar                       # 探测概览
    python -m data_probe.probe_trade_calendar --year 2026           # 指定年份
    python -m data_probe.probe_trade_calendar --json                # 机器可读概览
    python -m data_probe.probe_trade_calendar --csv > cal.csv       # 全量 CSV
    python -m data_probe.probe_trade_calendar --csv --from 2026-01-01 --to 2026-12-31

CSV 列：``trade_date,is_open,prev_trade_date``
    - trade_date 用 YYYY-MM-DD
    - is_open 用 true/false（cmd/calendar-seed 期望该格式）
    - prev_trade_date 在第一行可为空
    AkShare 仅给出"开市日"，本脚本会逐日补齐 ``is_open=false`` 的休市日。
"""

from __future__ import annotations

import argparse
import csv
import json
import sys
from dataclasses import asdict, dataclass
from datetime import date, datetime, timedelta


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
    candidate_cols = [c for c in df.columns if "date" in c.lower()]
    if not candidate_cols:
        raise RuntimeError(
            f"返回结构异常，没有 date 列；当前列：{list(df.columns)}"
        )
    date_col = candidate_cols[0]
    df[date_col] = df[date_col].astype(str)
    return df, date_col


def _parse_iso(s: str) -> date:
    return datetime.strptime(s, "%Y-%m-%d").date()


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


def emit_csv(
    *,
    out,
    date_from: date | None,
    date_to: date | None,
) -> int:
    """把 AkShare 的开市日 + 自动补齐的休市日写到 out，按日期升序。返回行数。"""
    df, date_col = _load_calendar()
    open_dates = sorted({_parse_iso(s) for s in df[date_col].tolist()})

    if not open_dates:
        sys.stderr.write("[probe_trade_calendar] 上游没有返回任何开市日\n")
        return 0

    # 范围裁剪
    if date_from is None:
        date_from = open_dates[0]
    if date_to is None:
        date_to = open_dates[-1]
    if date_from > date_to:
        raise SystemExit(f"--from {date_from} 大于 --to {date_to}")

    open_set = set(open_dates)

    writer = csv.writer(out, lineterminator="\n")
    writer.writerow(["trade_date", "is_open", "prev_trade_date"])

    prev_open: date | None = None
    count = 0
    d = date_from
    one_day = timedelta(days=1)
    while d <= date_to:
        is_open = d in open_set
        writer.writerow(
            [
                d.isoformat(),
                "true" if is_open else "false",
                prev_open.isoformat() if prev_open is not None else "",
            ]
        )
        if is_open:
            prev_open = d
        d += one_day
        count += 1
    return count


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument("--year", type=int, default=date.today().year, help="抽样统计的年份（默认当前年）")
    parser.add_argument("--json", action="store_true", help="探测概览以 JSON 输出")
    parser.add_argument("--csv", action="store_true", help="输出 trade_calendar 落库就绪的 CSV")
    parser.add_argument("--from", dest="date_from", type=_parse_iso, default=None, help="CSV 起始日期 YYYY-MM-DD（默认上游最早）")
    parser.add_argument("--to", dest="date_to", type=_parse_iso, default=None, help="CSV 终止日期 YYYY-MM-DD（默认上游最晚）")
    args = parser.parse_args(argv)

    if args.csv:
        rows = emit_csv(out=sys.stdout, date_from=args.date_from, date_to=args.date_to)
        sys.stderr.write(f"[probe_trade_calendar] {rows} 行 CSV 已输出\n")
        return 0

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
