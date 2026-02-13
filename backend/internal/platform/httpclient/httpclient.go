// Package httpclient 实现 FR-PL-05：访问外部数据源的统一出口。
//
// 每个上游（"source"）共享一份 Policy：超时、重试、限流、熔断。
// 默认 Policy 与 REQ-01 对齐：5s 超时、最多 2 次重试（指数退避起步 200ms）、
// 5 QPS 限流、连续 5 次失败后熔断 60s。
//
// # 调用约定
//
//	c := httpclient.New(httpclient.DefaultPolicy())
//	resp, err := c.Do(ctx, "eastmoney", req)  // source 名是限流/熔断的 key
//
// 调用方负责 resp.Body.Close()。失败的请求会在熔断器和限流器内被记账，重试不计入限流。
//
// # 不在本包内
//
//   - 业务语义的失败判定（如"返回 200 但 JSON 里 errCode!=0"）：调用方自己解释
//   - 缓存：估值层 / 行情层自己用 PG 表或 in-memory 处理
//   - Fallback 链：上层（market.fetchWithFallback）实现，本包只负责单源稳定性
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/sony/gobreaker"
	"golang.org/x/time/rate"

	"github.com/namelyzz/FundPilot/internal/platform/logger"
)

// UserAgent 是品牌字符串，所有出站请求都会带上（FR-PL-05）。
const UserAgent = "FundPilot/0.1 (+local)"

// Policy 所有出站请求的身份标识。
// 控制每个 source 的稳定性策略。零值不可用，请用 [DefaultPolicy]。
type Policy struct {
	Timeout     time.Duration // 单次请求超时（不含重试）
	Retries     int           // 失败重试次数；0 表示不重试，仅尝试一次
	RetryBase   time.Duration // 指数退避基数：第 n 次重试睡 RetryBase << n
	RPS         float64       // 限流 QPS；<=0 视为无限流
	Burst       int           // 限流桶容量；<=0 时回落到 max(1, RPS)
	BreakerN    uint32        // 连续失败多少次熔断；0 表示禁用熔断
	BreakerCool time.Duration // 熔断打开持续时长
}

// DefaultPolicy 返回 FR-PL-05 基线策略。
// 默认值：超时 5s、重试 2 次、退避 200ms、5 QPS、桶容量 5、连续 5 次失败熔断 60s
func DefaultPolicy() Policy {
	return Policy{
		Timeout:     5 * time.Second,
		Retries:     2,
		RetryBase:   200 * time.Millisecond,
		RPS:         5,
		Burst:       5,
		BreakerN:    5,
		BreakerCool: 60 * time.Second,
	}
}

// Client 是 http.Client 的稳定性包装。线程安全。
type Client struct {
	base       *http.Client
	defaults   Policy
	overrides  map[string]Policy
	overrideMu sync.RWMutex

	state   map[string]*sourceState
	stateMu sync.Mutex // 仅 lazy init 时持有
}

type sourceState struct {
	limiter *rate.Limiter
	breaker *gobreaker.CircuitBreaker
	policy  Policy
}

// New 构造 Client。defaults 应来自 [DefaultPolicy] 或经业务覆盖。
func New(defaults Policy) *Client {
	return &Client{
		base:      &http.Client{}, // 超时通过 ctx 控制，避免与重试机制竞争
		defaults:  defaults,
		overrides: make(map[string]Policy),
		state:     make(map[string]*sourceState),
	}
}

// WithSourcePolicy 为特定 source 覆盖默认 Policy。
// 须在该 source 首次被 Do() 调用前设置，否则不会重建已经初始化的 limiter/breaker。
func (c *Client) WithSourcePolicy(source string, p Policy) *Client {
	c.overrideMu.Lock()
	c.overrides[source] = p
	c.overrideMu.Unlock()
	return c
}

func (c *Client) policyFor(source string) Policy {
	c.overrideMu.RLock()
	defer c.overrideMu.RUnlock()
	if p, ok := c.overrides[source]; ok {
		return p
	}
	return c.defaults
}

// 懒初始化
func (c *Client) stateFor(source string) *sourceState {
	// 加锁检查：已存在就返回, 不存在就创建
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if st, ok := c.state[source]; ok {
		return st
	}
	p := c.policyFor(source)

	// 1. 限流器
	var lim *rate.Limiter
	if p.RPS > 0 {
		burst := p.Burst
		if burst <= 0 {
			if p.RPS < 1 {
				burst = 1
			} else {
				burst = int(p.RPS)
			}
		}
		lim = rate.NewLimiter(rate.Limit(p.RPS), burst)
	}

	// 2. 熔断器
	var br *gobreaker.CircuitBreaker
	if p.BreakerN > 0 {
		br = gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:    "httpclient/" + source,
			Timeout: p.BreakerCool,
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.ConsecutiveFailures >= p.BreakerN
			},
		})
	}

	st := &sourceState{limiter: lim, breaker: br, policy: p}
	c.state[source] = st
	return st
}

// ErrCircuitOpen 由 [Client.Do] 在熔断器打开时返回（包装 gobreaker.ErrOpenState）。
var ErrCircuitOpen = errors.New("httpclient: circuit open")

// Do 主入口，发起一次请求
// 自动应用 source 的 timeout/retry/rate-limit/circuit-breaker。
//
// 重试规则：
//   - 4xx：直接返回响应给调用方，不重试（多数 4xx 重试无意义）
//   - 5xx + 网络/超时错误：按 Policy.Retries 重试，指数退避
//
// 限流只对"首次尝试"扣 token；重试不扣，避免限流惩罚已经失败的请求。
func (c *Client) Do(ctx context.Context, source string, req *http.Request) (*http.Response, error) {
	// 第一步：source 校验
	if source == "" {
		return nil, errors.New("httpclient: empty source")
	}
	st := c.stateFor(source)

	// 第二步：限流（首次尝试）：受 ctx 取消影响
	// Wait(ctx) 会阻塞到有令牌为止。如果 ctx 被取消（比如请求超时），直接返回错误，不会无限等下去。
	// 限流只在首次尝试时扣令牌，重试不扣
	if st.limiter != nil {
		if err := st.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("httpclient: rate limit wait: %w", err)
		}
	}

	// 第三步：注入 User-Agent（不覆盖调用方已设置的值，便于测试场景定制）
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", UserAgent)
	}

	attempt := func() (*http.Response, error) {
		return c.doOnceWithRetry(ctx, source, req, st)
	}

	if st.breaker == nil {
		return attempt()
	}

	// 第四步：进入熔断器
	res, err := st.breaker.Execute(func() (interface{}, error) {
		resp, err := attempt()
		if err != nil {
			return nil, err
		}
		// 5xx 计入熔断失败；4xx 不计（避免下游一直 4xx 时熔断把链路打断）
		if resp.StatusCode >= 500 {
			_ = drainAndClose(resp)
			return nil, fmt.Errorf("httpclient: upstream %s status %d", source, resp.StatusCode)
		}
		return resp, nil
	})
	if err != nil {
		if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
			return nil, fmt.Errorf("%w: %s", ErrCircuitOpen, source)
		}
		return nil, err
	}
	resp, _ := res.(*http.Response)
	return resp, nil
}

func (c *Client) doOnceWithRetry(ctx context.Context, source string, req *http.Request, st *sourceState) (*http.Response, error) {
	lg := logger.FromContext(ctx)
	maxAttempts := st.policy.Retries + 1
	var lastErr error
	for n := 0; n < maxAttempts; n++ {
		// 每次尝试用独立超时
		attemptCtx, cancel := context.WithTimeout(ctx, st.policy.Timeout)
		started := time.Now()
		resp, err := c.base.Do(req.Clone(attemptCtx))
		duration := time.Since(started)
		cancel()

		switch {
		case err != nil:
			lastErr = err
			lg.Warn("httpclient request failed",
				"source", source,
				"attempt", n+1,
				"duration_ms", duration.Milliseconds(),
				"error", err.Error(),
			)
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("upstream %s status %d", source, resp.StatusCode)
			lg.Warn("httpclient upstream 5xx",
				"source", source,
				"attempt", n+1,
				"status_code", resp.StatusCode,
				"duration_ms", duration.Milliseconds(),
			)
			_ = drainAndClose(resp)
		default:
			// 2xx / 3xx / 4xx → 返回给调用方
			return resp, nil
		}

		// 还有重试机会：退避
		if n+1 < maxAttempts {
			sleep := st.policy.RetryBase << n
			select {
			case <-time.After(sleep):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return nil, lastErr
}

// drainAndClose 为什么要 drain？
// HTTP 连接是复用的（连接池）。
// 如果你只读了一半 body 就 Close()，Go 的连接池不知道 body 剩了多少
// 会直接断开这个连接而不是放回池里。
func drainAndClose(resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	_, _ = io.Copy(io.Discard, resp.Body) // 把剩余数据读完再关，连接才能被复用。
	return resp.Body.Close()
}
