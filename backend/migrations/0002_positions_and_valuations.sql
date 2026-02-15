-- +goose Up
-- REQ-02 持仓管理（Asset 域）—— 一次性建齐三张表：
--   positions             —— 业务可写主表（REQ-02 直接写入）
--   position_valuations   —— 估值时序快照（hypertable；REQ-05 写入）
--   portfolio_snapshots   —— 组合级时序快照（hypertable；REQ-05 写入）
-- timescaledb 扩展已在 0001 启用。

-- ----------------------------------------------------------------------------
-- positions: 用户当前持有的每只基金一条记录（fund_code UNIQUE）。
-- 派生字段（cost_basis / estimated_shares）由应用层填写，不使用生成列：未来若
-- 修改反推口径（详见 REQ-02 §5），迁移会更平滑。
-- ----------------------------------------------------------------------------
CREATE TABLE positions (
    id                  BIGSERIAL    PRIMARY KEY,
    fund_code           VARCHAR(20)  NOT NULL UNIQUE,
    holding_amount      DECIMAL(14,2) NOT NULL CHECK (holding_amount > 0),
    holding_profit      DECIMAL(14,2) NOT NULL,
    cost_basis          DECIMAL(14,2) NOT NULL,
    estimated_shares    DECIMAL(20,4) NULL,
    holding_days        INT          NOT NULL DEFAULT 0 CHECK (holding_days >= 0),
    holding_start_date  DATE         NOT NULL DEFAULT CURRENT_DATE,
    source              VARCHAR(16)  NOT NULL DEFAULT 'manual'
                                     CHECK (source IN ('manual', 'ocr')),
    version             INT          NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT now()
);

COMMENT ON TABLE  positions IS 'REQ-02 Asset 域主表，一只基金一条记录';
COMMENT ON COLUMN positions.fund_code         IS '基金代码，应用层校验 ^[0-9]{6}$（REQ-02 FR-AS-01）';
COMMENT ON COLUMN positions.holding_amount    IS '当前市值（用户支付宝口径录入）';
COMMENT ON COLUMN positions.holding_profit    IS '累计盈亏，可为负';
COMMENT ON COLUMN positions.cost_basis        IS '反推：holding_amount − holding_profit（投入本金）';
COMMENT ON COLUMN positions.estimated_shares  IS '反推：holding_amount / 最新 T-1 净值；净值不可用时为 NULL';
COMMENT ON COLUMN positions.holding_days      IS '持有天数；查询时取 max(stored, today − holding_start_date)（REQ-02 §4 FR-AS-04）';
COMMENT ON COLUMN positions.source            IS '录入来源：manual（REQ-02）/ ocr（V0.2）';
COMMENT ON COLUMN positions.version           IS '乐观锁版本号；每次 UPDATE 在 WHERE 中校验并自增。冲突映射为 ErrPositionVersionConflict / HTTP 409，避免并发 PATCH 互相覆盖派生字段';
COMMENT ON COLUMN positions.updated_at        IS '应用层显式写入，不使用触发器';

-- ----------------------------------------------------------------------------
-- position_valuations: 单条持仓的估值时序快照（REQ-05 写入，REQ-02 读取）。
-- 不在此设置 FK 到 positions(id)：REQ-02 FR-AS-03 要求"物理删除 position，
-- 历史快照保留"，FK ON DELETE NO ACTION 会阻塞删除，CASCADE 又会丢历史，
-- ON DELETE SET NULL 则要求 position_id NULLABLE 破坏分区键完整性。
-- 改由应用层（asset repo）在写入 valuation 前校验 position 存在；
-- 删除 position 后允许 valuation 成为孤儿行，用于审计与未来历史分析。
-- ----------------------------------------------------------------------------
CREATE TABLE position_valuations (
    position_id        BIGINT       NOT NULL,
    as_of              TIMESTAMPTZ  NOT NULL,
    est_nav            DECIMAL(10,4),
    est_change_pct     DECIMAL(8,4),
    est_market_value   DECIMAL(14,2),
    today_pnl          DECIMAL(14,2),
    confidence         VARCHAR(16)  NOT NULL
                                    CHECK (confidence IN ('high','mid','low','unsupported')),
    coverage_ratio     DECIMAL(5,4) CHECK (coverage_ratio IS NULL OR (coverage_ratio >= 0 AND coverage_ratio <= 1)),
    fallback_reason    TEXT,
    explain_json       JSONB,
    PRIMARY KEY (position_id, as_of)
);

SELECT create_hypertable('position_valuations', 'as_of', if_not_exists => TRUE);

COMMENT ON TABLE  position_valuations IS 'REQ-05 写入的持仓估值快照；hypertable，按 as_of 分区。无 FK，详见迁移注释';
COMMENT ON COLUMN position_valuations.position_id     IS '逻辑外键 → positions.id，无 DB 级约束（见迁移注释）';
COMMENT ON COLUMN position_valuations.as_of           IS '估值时间（hypertable 分区键）';
COMMENT ON COLUMN position_valuations.confidence      IS 'high / mid / low / unsupported（REQ-02 §3.2）';
COMMENT ON COLUMN position_valuations.coverage_ratio  IS '估值覆盖度 0.0000–1.0000';
COMMENT ON COLUMN position_valuations.fallback_reason IS '降级原因；NULL 表示走主路径';
COMMENT ON COLUMN position_valuations.explain_json    IS 'top10、行业补全等详细贡献来源';

-- ----------------------------------------------------------------------------
-- portfolio_snapshots: 组合级时序快照（REQ-05 写入，REQ-02 §FR-AS-06 读取）。
-- ----------------------------------------------------------------------------
CREATE TABLE portfolio_snapshots (
    as_of                TIMESTAMPTZ NOT NULL,
    total_assets         DECIMAL(16,2) NOT NULL,
    today_pnl            DECIMAL(14,2) NOT NULL,
    today_return_pct     DECIMAL(8,4)  NOT NULL,
    position_count       INT           NOT NULL CHECK (position_count >= 0),
    confidence_summary   JSONB         NOT NULL,
    PRIMARY KEY (as_of)
);

SELECT create_hypertable('portfolio_snapshots', 'as_of', if_not_exists => TRUE);

COMMENT ON TABLE  portfolio_snapshots IS 'REQ-05 写入的组合级总览快照；hypertable，按 as_of 分区';
COMMENT ON COLUMN portfolio_snapshots.total_assets       IS '所有持仓 est_market_value 求和';
COMMENT ON COLUMN portfolio_snapshots.confidence_summary IS '{"high":N,"mid":N,"low":N,"unsupported":N}';

-- +goose Down
-- TimescaleDB: DROP TABLE 会一并清理 hypertable 的所有 chunks。
DROP TABLE IF EXISTS portfolio_snapshots;
DROP TABLE IF EXISTS position_valuations;
DROP TABLE IF EXISTS positions;
-- 不在此移除 timescaledb 扩展：可能仍被其他表 / chunks 依赖。
