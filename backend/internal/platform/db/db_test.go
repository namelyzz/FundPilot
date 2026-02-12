package db

import (
	"context"
	"testing"
	"time"

	"github.com/namelyzz/FundPilot/internal/platform/config"
)

// 单测策略：B2 阶段只覆盖参数/错误路径；真实 DB 集成等冒烟阶段手测。

func TestOpen_EmptyDSN(t *testing.T) {
	_, err := Open(context.Background(), config.Database{DSN: ""})
	if err == nil {
		t.Fatal("expected error for empty dsn")
	}
}

func TestOpen_InvalidDSN(t *testing.T) {
	_, err := Open(context.Background(), config.Database{DSN: "not-a-dsn"})
	if err == nil {
		t.Fatal("expected error for invalid dsn")
	}
}

func TestOpen_UnreachableHostFastFail(t *testing.T) {
	// 不可达地址 + 短超时：验证 Ping 失败时 Open 返回错误且不挂起
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Open(ctx, config.Database{
		DSN:     "postgres://x:x@127.0.0.1:1/x?sslmode=disable&connect_timeout=1",
		MaxOpen: 2,
		MaxIdle: 1,
	})
	if err == nil {
		t.Fatal("expected error for unreachable host")
	}
}

func TestPing_NilPool(t *testing.T) {
	if err := Ping(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil pool")
	}
}
