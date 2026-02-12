-- +goose Up
-- 启用 TimescaleDB 扩展（FR-PL-04）；幂等。
-- trade_calendar 自身不需要 hypertable，但后续 position_valuations / portfolio_snapshots
-- 会依赖该扩展，提前在 0001 启用更符合"地基迁移"的语义。
CREATE EXTENSION IF NOT EXISTS timescaledb;

-- trade_calendar：A 股交易日历（FR-PL-07）。
-- 数据来源：AkShare tool_trade_date_hist_sina，Python 探测脚本验证字段。
-- 兜底：仓库内手工 csv。
CREATE TABLE trade_calendar (
    trade_date       DATE        PRIMARY KEY,
    is_open          BOOLEAN     NOT NULL,
    prev_trade_date  DATE        NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE  trade_calendar IS 'FR-PL-07 A股交易日历，单条记录代表一个自然日是否开市';
COMMENT ON COLUMN trade_calendar.is_open IS 'true=交易日；false=休市（周末或节假日）';
COMMENT ON COLUMN trade_calendar.prev_trade_date IS '上一个交易日；用于 T-1 净值口径，is_open=false 时也可有值';

-- +goose Down
DROP TABLE IF EXISTS trade_calendar;
-- 不在 Down 里删除 timescaledb 扩展：可能被后续迁移建的 hypertable 依赖。
