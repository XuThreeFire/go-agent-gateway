// Package store 是持久化层：七张表的读写，唯一与 MySQL 打交道的地方。
//
// 用 GORM 只为了省事务与批量插入的样板代码，SQL 语义仍按 docs/03 的规则来：
//   - 不用外键，关联字段的索引在迁移脚本里显式建
//   - token 累加走 `x = x + ?` 的原子更新，不先读后写
//   - 状态迁移一律条件更新（CAS），影响行数为 0 就是抢输了
//
// 表结构见 backend/migrations/000001_init.up.sql；改字段先改文档与迁移，再改这里的 row 结构。
package store

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// ErrNotFound 由 gorm.ErrRecordNotFound 归一化而来，上层只判断这一个。
var ErrNotFound = gorm.ErrRecordNotFound

type Options struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	SlowThreshold   time.Duration
	Debug           bool
}

type Store struct {
	db *gorm.DB
}

func Open(dsn string, opt Options) (*Store, error) {
	level := glogger.Warn
	if opt.Debug {
		level = glogger.Info
	}
	gormLog := glogger.New(log.New(os.Stdout, "", log.LstdFlags), glogger.Config{
		ParameterizedQueries:      true,
		SlowThreshold:             opt.SlowThreshold,
		LogLevel:                  level,
		IgnoreRecordNotFoundError: true,
		Colorful:                  false,
	})

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		// 统一 UTC：写入用 UTC，读出来也是 UTC，和 DSN 的 loc=UTC 对齐。
		NowFunc: func() time.Time { return time.Now().UTC() },
		Logger:  gormLog,
	})
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层连接池失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(opt.MaxOpenConns)
	sqlDB.SetMaxIdleConns(opt.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(opt.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(opt.ConnMaxIdleTime)

	return &Store{db: db}, nil
}

// Ping 供 /healthz 探活，只验证连接可达，不查业务表。
func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// DB 暴露 *gorm.DB 给测试与运维脚本用，业务代码不要直接拿。
func (s *Store) DB() *gorm.DB { return s.db }

// ConflictError 用于「该 Task 已有活跃 Run」这类规则冲突。
// 实现 Is(domain.ErrConflict)，上层用 errors.Is 就能映射到 409，
// 同时把当前状态带回给客户端，让它自己收敛（见 05 接口约定）。
type ConflictError struct {
	ActiveRunID int64
	Status      string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("conflict: 当前 Run %d 处于 %s", e.ActiveRunID, e.Status)
}

func (e *ConflictError) Is(target error) bool { return target == domain.ErrConflict }

// isDuplicateKey 判断是否唯一键冲突（MySQL 1062）。
// 幂等提交与「同一 Turn 只能有一个成功 Run」都靠它兜底，不做先查后插。
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Error 1062") || strings.Contains(msg, "Duplicate entry")
}
