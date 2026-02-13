package scheduler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func newTestScheduler(t *testing.T) (*Scheduler, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	lg := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return New(lg), buf
}

func TestRegister_EmptyNameOrNilHandler(t *testing.T) {
	s, _ := newTestScheduler(t)
	if err := s.Register("", "* * * * *", func(ctx context.Context) error { return nil }); err == nil {
		t.Error("empty name should fail")
	}
	if err := s.Register("x", "* * * * *", nil); err == nil {
		t.Error("nil handler should fail")
	}
}

func TestRegister_DuplicateName(t *testing.T) {
	s, _ := newTestScheduler(t)
	h := func(ctx context.Context) error { return nil }
	if err := s.Register("dup", "* * * * *", h); err != nil {
		t.Fatal(err)
	}
	if err := s.Register("dup", "* * * * *", h); err == nil {
		t.Error("duplicate name should fail")
	}
}

func TestRegister_InvalidCronExpr(t *testing.T) {
	s, _ := newTestScheduler(t)
	err := s.Register("bad", "not a cron", func(ctx context.Context) error { return nil })
	if err == nil {
		t.Fatal("invalid expression should fail")
	}
}

func TestRunner_SkipIfStillRunning(t *testing.T) {
	s, _ := newTestScheduler(t)

	var running atomic.Int32
	release := make(chan struct{})

	j := &job{
		name: "slow",
		expr: "@every 1s",
		h: func(ctx context.Context) error {
			running.Add(1)
			<-release
			return nil
		},
	}
	runner := s.makeRunner(j)

	// 第一次启动并阻塞在 release
	go runner()
	for running.Load() == 0 {
		time.Sleep(2 * time.Millisecond)
	}

	// 第二次调用：mutex 拿不到，应被跳过
	runner()
	st := j.snapshot()
	if st.SkipCount != 1 || st.LastStatus != "skipped(running)" {
		t.Errorf("expected skip, got %+v", st)
	}

	close(release)
	// 等第一次完成，避免泄漏
	time.Sleep(20 * time.Millisecond)
	st = j.snapshot()
	if st.RunCount != 1 || st.LastStatus != "success" {
		t.Errorf("after release expected one success, got %+v", st)
	}
}

func TestRunner_PanicRecoveredAndCounted(t *testing.T) {
	s, _ := newTestScheduler(t)
	j := &job{name: "panicky", expr: "* * * * *", h: func(ctx context.Context) error {
		panic("boom")
	}}
	s.makeRunner(j)() // 直接调一次

	st := j.snapshot()
	if st.LastStatus != "failed" || st.FailCount != 1 {
		t.Errorf("panic should count as failure, got %+v", st)
	}
	if st.LastError == "" {
		t.Error("LastError should describe the panic")
	}
}

func TestRunner_ErrorCountedAndLatencyRecorded(t *testing.T) {
	s, _ := newTestScheduler(t)
	j := &job{name: "errjob", expr: "* * * * *", h: func(ctx context.Context) error {
		time.Sleep(5 * time.Millisecond)
		return errors.New("nope")
	}}
	s.makeRunner(j)()

	st := j.snapshot()
	if st.LastStatus != "failed" || st.FailCount != 1 || st.RunCount != 1 {
		t.Errorf("error path counters wrong: %+v", st)
	}
	if st.LastLatency < 5*time.Millisecond {
		t.Errorf("latency = %v, want >= 5ms", st.LastLatency)
	}
}

func TestStartStop_Lifecycle(t *testing.T) {
	s, _ := newTestScheduler(t)
	ran := make(chan struct{}, 4)
	// @every 1s 比亚秒级稳定（robfig/cron 在 Windows 上对 100ms 级粒度偶有 miss）
	if err := s.Register("tick", "@every 1s", func(ctx context.Context) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	s.Start(context.Background())
	s.Start(context.Background()) // 重复 Start 应被忽略

	select {
	case <-ran:
	case <-time.After(3 * time.Second):
		t.Fatal("job did not fire within 3s")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// 二次 Stop 是 no-op
	if err := s.Stop(stopCtx); err != nil {
		t.Errorf("second Stop should be no-op, got %v", err)
	}
}

func TestStatus_ListsAllJobsSorted(t *testing.T) {
	s, _ := newTestScheduler(t)
	h := func(ctx context.Context) error { return nil }
	_ = s.Register("zeta", "* * * * *", h)
	_ = s.Register("alpha", "* * * * *", h)

	st := s.Status()
	if len(st) != 2 {
		t.Fatalf("len = %d", len(st))
	}
	if st[0].Name != "alpha" || st[1].Name != "zeta" {
		t.Errorf("expected sorted by name, got %+v", st)
	}
	// 没跑过 → never
	if st[0].LastStatus != "never" {
		t.Errorf("LastStatus default = %q", st[0].LastStatus)
	}
}

type fakeClock struct{ open bool }

func (f fakeClock) IsTradingTime(_ time.Time) bool { return f.open }

func TestIfTradingTime_SkipsWhenClosed(t *testing.T) {
	var called atomic.Int32
	h := IfTradingTime(fakeClock{open: false}, func(ctx context.Context) error {
		called.Add(1)
		return nil
	})
	if err := h(context.Background()); err != nil {
		t.Errorf("err = %v", err)
	}
	if called.Load() != 0 {
		t.Error("handler should not be called when market closed")
	}
}

func TestIfTradingTime_RunsWhenOpen(t *testing.T) {
	var called atomic.Int32
	h := IfTradingTime(fakeClock{open: true}, func(ctx context.Context) error {
		called.Add(1)
		return nil
	})
	if err := h(context.Background()); err != nil {
		t.Errorf("err = %v", err)
	}
	if called.Load() != 1 {
		t.Errorf("handler called %d times, want 1", called.Load())
	}
}

func TestIfTradingTime_NilGuards(t *testing.T) {
	// nil clock 或 nil handler 应当退化回原 handler，不应 panic
	h := IfTradingTime(nil, func(ctx context.Context) error { return nil })
	if err := h(context.Background()); err != nil {
		t.Error(err)
	}
}
