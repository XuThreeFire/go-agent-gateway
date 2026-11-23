package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 配额用的 scope 常量。写成 scope + scope_key 两列而不是一张宽表，
// 是为了以后加「按 Task 限」「按模型限」时不用改表结构。
const (
	ScopeClientDay = "client_day"
	ScopeGlobalDay = "global_day"
)

// DayKey 把 UTC 日期拼进 scope_key：跨零点自然换一个计数器，
// 不需要定时清零任务（零点是算出来的，不是扫出来的）。
func DayKey(clientID string, at time.Time) string {
	return clientID + ":" + at.UTC().Format("20060102")
}

// UsageTokens 读当天已消耗的 token；没有记录返回 0。
func (s *Store) UsageTokens(ctx context.Context, scope, scopeKey string) (int64, error) {
	var row usageRow
	err := s.db.WithContext(ctx).Where("scope = ? AND scope_key = ?", scope, scopeKey).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return row.Tokens, nil
}

// ClientIDOfTask 取会话所属客户端。配额按客户端算，执行器只拿到 Run，得反查一次。
func (s *Store) ClientIDOfTask(ctx context.Context, taskID int64) (string, error) {
	var clientID string
	if err := s.db.WithContext(ctx).Raw("SELECT client_id FROM task WHERE id = ?", taskID).Scan(&clientID).Error; err != nil {
		return "", fmt.Errorf("查询 task 所属客户端失败: %w", err)
	}
	if clientID == "" {
		return "", ErrNotFound
	}
	return clientID, nil
}

func GlobalDayKey(at time.Time) string { return "all:" + at.UTC().Format("20060102") }

// AddUsageTokens 供 AppendStep 使用同一事务写入各维度计数。
func (s *Store) AddUsageTokens(ctx context.Context, scope, key string, delta int64) error {
	if delta <= 0 {
		return nil
	}
	row := usageRow{Scope: scope, ScopeKey: key, Tokens: delta}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"tokens": gorm.Expr("tokens + ?", delta), "updated_at": s.db.NowFunc().UTC()})}).Create(&row).Error
}
