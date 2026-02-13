// Package scheduler 实现 FR-PL-06：进程内 cron 调度框架。
//
// # 设计要点
//
//   - 基于 robfig/cron/v3（5 字段标准 cron）
//   - 每个 job 自带 mutex：上次未跑完时跳过本次，避免雪崩
//   - panic 必须 recover，写错误日志，绝不拖死进程
//   - 起止/失败 / 耗时一律走 [logger.FromContext]，与请求链路风格一致
//
// # 用法
//
//	sch := scheduler.New(logger)
//	sch.Register("calendar.refresh", "0 2 1 * *", func(ctx context.Context) error { ... })
//	sch.Register("market.refresh_realtime", "*/5 * * * *",
//	    scheduler.IfTradingTime(cal, marketRefreshHandler))
//	sch.Start(ctx)
//	defer sch.Stop(shutdownCtx)
//
// # 不在本包内
//
//   - 业务任务实现：在各领域模块自己写 handler，注册时注入
//   - 交易时段判断：通过 [TradingClock] 接口注入（典型实现是 calendar.Service）
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/namelyzz/FundPilot/internal/platform/logger"
)

// Handler 是单次任务执行体；ctx 由 [Scheduler.Start] 传入，建议任务尊重取消。
type Handler func(ctx context.Context) error

// JobStatus 是 /health 等场景的快照（值类型，外部读取安全）。
type JobStatus struct {
	Name        string        `json:"name"`
	Cron        string        `json:"cron"`
	LastRunAt   time.Time     `json:"last_run_at,omitempty"`
	LastStatus  string        `json:"last_status"` // success / failed / skipped(running) / never
	LastError   string        `json:"last_error,omitempty"`
	LastLatency time.Duration `json:"last_latency,omitempty"`
	RunCount    int64         `json:"run_count"`
	SkipCount   int64         `json:"skip_count"`
	FailCount   int64         `json:"fail_count"`
}

// job 是 Scheduler 内部状态；JobStatus 是其只读快照。
type job struct {
	name string
	expr string
	h    Handler

	mu sync.Mutex // 互斥：上次未完成时跳过本次

	statMu      sync.RWMutex
	lastRunAt   time.Time
	lastStatus  string
	lastError   string
	lastLatency time.Duration
	runCount    int64
	skipCount   int64
	failCount   int64
}

func (j *job) snapshot() JobStatus {
	j.statMu.RLock()
	defer j.statMu.RUnlock()
	status := j.lastStatus
	if status == "" {
		status = "never"
	}
	return JobStatus{
		Name:        j.name,
		Cron:        j.expr,
		LastRunAt:   j.lastRunAt,
		LastStatus:  status,
		LastError:   j.lastError,
		LastLatency: j.lastLatency,
		RunCount:    j.runCount,
		SkipCount:   j.skipCount,
		FailCount:   j.failCount,
	}
}

// Scheduler 是进程级单例（在 main 里建一个，全局复用）。
type Scheduler struct {
	cron   *cron.Cron
	log    *slog.Logger
	jobsMu sync.RWMutex
	jobs   map[string]*job

	startMu sync.Mutex
	started bool
}

// New 构造 Scheduler；log 为 nil 时回落到 slog.Default。
func New(log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	// cron.WithChain 这里不用，自己做 recover 与 mutex 更可控
	return &Scheduler{
		cron: cron.New(),
		log:  log,
		jobs: make(map[string]*job),
	}
}

// Register 注册一个定时任务。
//   - name 用作日志与 Status() 的 key，必须唯一
//   - expr 是标准 5 字段 cron 表达式（分 时 日 月 周）
//
// 启动后再 Register 也允许，会立即被纳入调度。
func (s *Scheduler) Register(name, expr string, h Handler) error {
	if name == "" {
		return errors.New("scheduler: empty name")
	}
	if h == nil {
		return errors.New("scheduler: nil handler")
	}

	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if _, exists := s.jobs[name]; exists {
		return fmt.Errorf("scheduler: duplicate job %q", name)
	}

	j := &job{name: name, expr: expr, h: h}
	if _, err := s.cron.AddFunc(expr, s.makeRunner(j)); err != nil {
		return fmt.Errorf("scheduler: parse cron %q: %w", expr, err)
	}
	s.jobs[name] = j
	s.log.Info("scheduler job registered", "name", name, "cron", expr)
	return nil
}

// makeRunner 为 job 构造 cron 触发时的执行闭包。
//
// robfig/cron 每次到达 cron 表达式指定的时间点，就会调用这个闭包。
// 一个 job 可能同时被多次触发（例如"每 5 分钟"的 job 如果某次跑了 7 分钟），
// 因此闭包内部必须处理重叠、panic 等边界情况。
//
// 执行流程（按代码顺序）：
//
//   1. TryLock 防重叠
//      用互斥锁保证同一个 job 不会并发执行。TryLock 是非阻塞的：
//      如果上次触发还没跑完（锁未释放），本次直接跳过，skipCount++。
//      这样避免了任务积压雪崩——如果用 Lock 阻塞等待，多个触发会排队，
//      等上一次跑完后连续执行多次，可能导致资源耗尽。
//
//   2. 构造带 trace_id 的 context
//      格式 "cron:<job名>:<时间戳>"，例如 "cron:market.refresh_realtime:20260525T093000"。
//      这样该 job 一次执行内打印的所有日志都携带相同的 trace_id，
//      在日志系统中用 trace_id 过滤即可看到单次执行的完整链路。
//
//   3. panic recover
//      业务 handler 内部如果 panic（比如空指针、数组越界），必须被捕获，
//      否则会导致整个进程崩溃。用匿名函数隔离 recover 作用域：
//      defer recover() 只能捕获同一层函数的 panic，
//      所以把 j.h(ctx) 包在一个独立的匿名函数里调用，
//      外层的 defer recover 才能兜住它。
//      panic 被捕获后转为 error，与正常返回的错误统一处理。
//
//   4. 统计更新
//      每次执行完毕后更新 runCount / failCount / lastLatency 等统计字段，
//      这些数据被 Status() 快照后暴露给 /health 接口，用于运维监控。
//      统计字段用独立的 statMu 保护，避免和 /health 的读取产生数据竞争。
//
// 返回的闭包由 robfig/cron 在每次触发时调用；调用方无需关心内部状态管理。
func (s *Scheduler) makeRunner(j *job) func() {
	return func() {
		// ── Step 1: TryLock 防重叠 ──────────────────────────────
		// TryLock 尝试获取锁，成功返回 true，锁已被持有时返回 false（不阻塞）。
		// 如果上次 cron 触发的 handler 还在跑（比如行情刷新花了 8 分钟），
		// 新的 5 分钟触发到来时这里会直接跳过，而不是排队等上一次完成。
		if !j.mu.TryLock() {
			j.statMu.Lock()
			j.skipCount++
			j.lastStatus = "skipped(running)"
			j.statMu.Unlock()
			s.log.Warn("scheduler job skipped: still running",
				"name", j.name,
			)
			return
		}
		// 无论 handler 成功、失败还是 panic，都必须释放锁，
		// 否则该 job 后续所有触发都会被跳过。
		defer j.mu.Unlock()

		// ── Step 2: 构造带 trace_id 的 context ──────────────────
		// context.Background() 是进程级背景 context，不关联任何请求。
		// trace_id 格式: "cron:<job名>:<时间戳>"
		// 例如 "cron:calendar.refresh:20260601T020000"
		ctx := s.runContext()
		ctx = logger.WithTraceID(ctx, s.log, "cron:"+j.name+":"+time.Now().Format("20060102T150405"))
		lg := logger.FromContext(ctx)

		lg.Info("scheduler job start", "name", j.name)
		start := time.Now()
		var err error

		// ── Step 3: panic recover + 执行 handler ────────────────
		// 为什么用匿名函数包一层？
		//   Go 的 defer/recover 只能捕获当前函数（直接调用者）的 panic。
		//   如果直接写 "defer recover(); err = j.h(ctx)"，
		//   recover 捕获的是 makeRunner 返回的闭包自身的 panic，
		//   而不是 j.h(ctx) 内部的 panic。
		//   用匿名函数调用 j.h(ctx)，匿名函数的 defer 就能捕获 j.h 的 panic。
		//   捕获后把 panic 值转为 error 赋给外层 err，统一进入后续的错误处理流程。
		func() {
			defer func() {
				if rv := recover(); rv != nil {
					err = fmt.Errorf("panic: %v", rv)
					lg.Error("scheduler job panic",
						"name", j.name,
						"value", fmt.Sprint(rv),
						"stack", string(debug.Stack()),
					)
				}
			}()
			// 执行业务 handler。如果 handler panic，上面 defer 会捕获；
			// 如果 handler 返回 error，err 非 nil；如果正常返回，err 为 nil。
			err = j.h(ctx)
		}()
		dur := time.Since(start)

		// ── Step 4: 更新统计字段 ────────────────────────────────
		// statMu 与 job.mu 是两把独立的锁：
		//   job.mu  保护"是否正在执行"（防重叠）
		//   statMu  保护"执行统计"（供 /health 读取）
		// 分离后 /health 读取统计时不会被 job 执行阻塞，反之亦然。
		j.statMu.Lock()
		j.runCount++
		j.lastRunAt = start
		j.lastLatency = dur
		if err != nil {
			j.failCount++
			j.lastStatus = "failed"
			j.lastError = err.Error()
		} else {
			j.lastStatus = "success"
			j.lastError = ""
		}
		j.statMu.Unlock()

		// ── 日志输出 ────────────────────────────────────────────
		// 失败打 Error（包含错误原因），成功打 Info。
		// 两条日志都携带 trace_id，与 handler 内部的日志串联。
		// duration_ms 可用于监控任务是否变慢（如行情刷新从 2s 涨到 30s）。
		if err != nil {
			lg.Error("scheduler job failed",
				"name", j.name,
				"duration_ms", dur.Milliseconds(),
				"error", err.Error(),
			)
			return
		}
		lg.Info("scheduler job done",
			"name", j.name,
			"duration_ms", dur.Milliseconds(),
		)
	}
}

// runContext 返回 job 执行用的 ctx；当前一个进程级 background。
// 留出独立方法是为了未来想接 Stop 时整体打断（短期任务不用，定时任务通常自己跑完）。
func (s *Scheduler) runContext() context.Context {
	return context.Background()
}

// Start 启动 cron 调度。重复调用会被忽略。
func (s *Scheduler) Start(ctx context.Context) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.started {
		return
	}
	s.cron.Start()
	s.started = true
	s.log.Info("scheduler started", "jobs", len(s.jobs))
	_ = ctx // 当前不在 Start 里 watch ctx；Stop 由调用方显式做
}

// Stop 优雅停调度：阻止新任务被触发，并等待当前正在跑的任务结束。
//
// ctx 超时则丢下未完成任务返回错误。
func (s *Scheduler) Stop(ctx context.Context) error {
	s.startMu.Lock()
	if !s.started {
		s.startMu.Unlock()
		return nil
	}
	stopCtx := s.cron.Stop()
	s.started = false
	s.startMu.Unlock()

	select {
	case <-stopCtx.Done():
		s.log.Info("scheduler stopped")
		return nil
	case <-ctx.Done():
		return fmt.Errorf("scheduler: stop timeout: %w", ctx.Err())
	}
}

// Status 返回所有 job 的快照，按 name 字典序。
func (s *Scheduler) Status() []JobStatus {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()
	out := make([]JobStatus, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j.snapshot())
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name < out[k].Name })
	return out
}

// TradingClock 由 [IfTradingTime] 中间件使用；通常注入 calendar.Service。
type TradingClock interface {
	IsTradingTime(t time.Time) bool
}

// IfTradingTime 装饰 handler：非交易时段直接 return nil（计入 success），并打 debug 日志。
//
// 这种实现选择让 /health 不会因为"非交易时段没跑"而误报。
func IfTradingTime(clock TradingClock, h Handler) Handler {
	if clock == nil || h == nil {
		return h
	}
	return func(ctx context.Context) error {
		if !clock.IsTradingTime(time.Now()) {
			logger.FromContext(ctx).Debug("scheduler skipped: outside trading hours")
			return nil
		}
		return h(ctx)
	}
}
