// Package failure 实现 FR-PL-08 的失败语义与置信度模型。
//
// 这是 FundPilot 区别于"普通爬虫"的核心：任何从外部数据源得到的值都必须**带上元数据**
// （来源、抓取时间、TTL、新鲜度、是否走过 fallback 链），调用方据此决定如何使用、如何
// 向用户暴露、是否计入估值。
//
// # 三个核心类型
//
//   - [SourcedValue]：值 + 元数据的载体
//   - [Staleness]：值的"陈旧度"——Fresh / Stale / Expired
//   - [Confidence]：估值层使用的"置信级别"——High / Mid / Low / Unsupported
//
// # 谁该用什么
//
//   - 数据获取层（fund/market）拿到上游数据后，应该构造 SourcedValue 返回，
//     并由 [ComputeStaleness] 根据 fetchedAt+TTL+now 计算 Staleness
//   - 估值层（valuation）在合并多份 SourcedValue 后，通过 [ComputeConfidence]
//     根据"是否有 stale" + "覆盖率"得到一个 Confidence，再写入响应
//
// # 不在本包里
//
//   - 各类数据的具体 TTL：在 REQ-03 / REQ-04 / REQ-05 各自定义
//   - 抓取实现：由 [internal/platform/httpclient] 负责
package failure

import (
	"fmt"
	"time"
)

// Staleness 表示一个外部值的陈旧度。零值为 [Fresh]。
type Staleness uint8

const (
	// Fresh 在 TTL 内，可正常使用。
	Fresh Staleness = iota
	// Stale 超过 TTL 但仍未到完全失效阈值，可在告警的前提下使用。
	Stale
	// Expired 完全过期，禁止使用（应触发 fallback 或降级）。
	Expired
)

// String 实现 fmt.Stringer，便于日志与序列化。
func (s Staleness) String() string {
	switch s {
	case Fresh:
		return "fresh"
	case Stale:
		return "stale"
	case Expired:
		return "expired"
	default:
		return fmt.Sprintf("staleness(%d)", s)
	}
}

// IsUsable 返回 true 当且仅当陈旧度不是 [Expired]。
//
// 业务层不应自己写 `s != Expired`，而应该走这个方法——后续若引入第四档（如
// "Quarantined"），只需要改这里。
func (s Staleness) IsUsable() bool {
	return s != Expired
}

// Confidence 是估值层最终输出的"置信级别"，对应 REQ-05 响应里的 confidence 字段。
//
// 零值为 [ConfidenceUnsupported]，避免"忘了设置"被当成 High。
type Confidence uint8

const (
	// ConfidenceUnsupported 基金类型不在策略覆盖内 / 数据全部不可用。
	ConfidenceUnsupported Confidence = iota
	// ConfidenceLow 覆盖率 < 50%，或主链路曾走过 fallback。
	ConfidenceLow
	// ConfidenceMid 至少一项 stale，或覆盖率 50%–80%。
	ConfidenceMid
	// ConfidenceHigh 数据全 fresh 且覆盖率 ≥ 80%。
	ConfidenceHigh
)

// String 实现 fmt.Stringer。返回的字符串与 REQ-05 响应字段一致（小写）。
func (c Confidence) String() string {
	switch c {
	case ConfidenceHigh:
		return "high"
	case ConfidenceMid:
		return "mid"
	case ConfidenceLow:
		return "low"
	case ConfidenceUnsupported:
		return "unsupported"
	default:
		return fmt.Sprintf("confidence(%d)", c)
	}
}

// SourcedValue 承载一个外部值及其元数据。
//
// 设计要点：
//   - 泛型 T 允许承载任意业务值（净值 float64、行情快照 struct 等），避免到处类型断言
//   - FetchedAt 是抓取**完成**的时间，作为陈旧度计算的基准；不要用上游响应里的时间
//   - FallbackChain 记录实际走过的源（按时间顺序），失败排查时定位"为什么最终是这条线"
//   - Reason 仅在 FallbackUsed=true 或 Staleness!=Fresh 时填写，作为人读的告警原因
type SourcedValue[T any] struct {
	Value         T
	Source        string        // 实际产出值的源名（最后成功的那条）
	FetchedAt     time.Time     // UTC 推荐
	TTL           time.Duration // 该值的有效期；0 表示"调用方自行判断"
	Staleness     Staleness
	FallbackUsed  bool
	FallbackChain []string // 主源 + 各 fallback，按尝试顺序
	Reason        string   // FallbackUsed/Staleness 触发时的可读原因
}

// IsUsable 是 [Staleness.IsUsable] 的便捷代理。
func (v SourcedValue[T]) IsUsable() bool {
	return v.Staleness.IsUsable()
}

// Age 返回 now 与 FetchedAt 的差值；FetchedAt 零值时返回 0。
func (v SourcedValue[T]) Age(now time.Time) time.Duration {
	if v.FetchedAt.IsZero() {
		return 0
	}
	return now.Sub(v.FetchedAt)
}

// ComputeStaleness 根据 fetchedAt + ttl + now 计算 Staleness。
//
// 规则：
//   - ttl == 0：永远 Fresh（调用方放弃了 TTL 概念）
//   - age <= ttl：Fresh
//   - ttl < age <= 2*ttl：Stale（缓冲区，仍可降级使用）
//   - age > 2*ttl：Expired
//
// 2× 这个常量来自 FR-PL-08 的描述："stale 时可用，expired（>30 分钟）禁用"——
// 行情 TTL 5min，30min 即 6×，但这是上限；本包提供 2× 作为通用兜底，业务可在
// 外层用更严格的阈值（如 [ComputeStalenessWithExpire]）。
func ComputeStaleness(fetchedAt time.Time, ttl time.Duration, now time.Time) Staleness {
	if ttl <= 0 {
		return Fresh
	}
	age := now.Sub(fetchedAt)
	switch {
	case age <= ttl:
		return Fresh
	case age <= 2*ttl:
		return Stale
	default:
		return Expired
	}
}

// ComputeStalenessWithExpire 允许显式指定"完全失效"阈值（如行情的 30min 上限）。
//
//	staleAfter = ttl 后变 Stale
//	expireAfter = expireAfter 后变 Expired
//
// 要求 expireAfter > ttl，否则降级为 [ComputeStaleness]。
func ComputeStalenessWithExpire(fetchedAt time.Time, ttl, expireAfter time.Duration, now time.Time) Staleness {
	if ttl <= 0 {
		return Fresh
	}
	if expireAfter <= ttl {
		return ComputeStaleness(fetchedAt, ttl, now)
	}
	age := now.Sub(fetchedAt)
	switch {
	case age <= ttl:
		return Fresh
	case age <= expireAfter:
		return Stale
	default:
		return Expired
	}
}

// ComputeConfidence 由"是否有 stale 输入"+"覆盖率"+"是否走过 fallback"得到 Confidence。
//
//   - anyStale==true 或 fallbackUsed==true → 最高只能 Mid
//   - coverage 范围裁剪到 [0, 1]
//   - coverage < 0.5：Low
//   - 0.5 ≤ coverage < 0.8：Mid
//   - coverage ≥ 0.8：High（但若有 stale/fallback 则降为 Mid）
//
// 注意：本函数不考虑"基金类型完全不支持"——那种场景由调用方直接返回
// [ConfidenceUnsupported]，避免本函数被迫扩展到难以维护的状态机。
func ComputeConfidence(coverage float64, anyStale, fallbackUsed bool) Confidence {
	if coverage < 0 {
		coverage = 0
	}
	if coverage > 1 {
		coverage = 1
	}
	switch {
	case coverage < 0.5:
		return ConfidenceLow
	case coverage < 0.8:
		return ConfidenceMid
	default:
		if anyStale || fallbackUsed {
			return ConfidenceMid
		}
		return ConfidenceHigh
	}
}
