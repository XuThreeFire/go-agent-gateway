// Package testutil 只供正式集成测试使用，不自动连接开发/生产数据库。
package testutil

import (
	"context"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/config"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/store"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func Database(t *testing.T) *store.Store {
	t.Helper()
	raw := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("需要 GATEWAY_TEST_DATABASE_URL 和已迁移的独立 _test 数据库")
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("拒绝连接：测试数据库名称必须以 _test 结尾")
	}
	dsn, err := (config.DatabaseConfig{URL: raw}).DSN()
	if err != nil {
		t.Fatal("测试数据库 URL 无效")
	}
	st, err := store.Open(dsn, store.Options{MaxOpenConns: 8, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, SlowThreshold: time.Second})
	if err != nil {
		t.Fatal("测试数据库连接失败")
	}
	t.Cleanup(func() { st.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := st.Ping(ctx); err != nil {
		t.Fatal("测试数据库不可达")
	}
	return st
}
