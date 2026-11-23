package store

import (
	"context"
	"gorm.io/gorm/clause"
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// InsertEvents 批量落库。调用方（hub）负责按 32 条或 500ms 攒批，
// 这里只做一次多值 insert，不再拆得更细。
func (s *Store) InsertEvents(ctx context.Context, events []domain.Event) error {
	if len(events) == 0 {
		return nil
	}
	rows := make([]eventRow, 0, len(events))
	for _, ev := range events {
		createdAt := ev.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		rows = append(rows, eventRow{
			RunID:     ev.RunID,
			Seq:       ev.Seq,
			Type:      ev.Type,
			Payload:   string(ev.Payload),
			CreatedAt: createdAt.UTC(),
			UpdatedAt: createdAt.UTC(),
		})
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, 100).Error
}

// ListEvents 从 afterSeq 之后按 seq 升序读，补发查询的主路径。
// 走 uk_run_seq(run_id, seq) 覆盖，不需要额外索引。
func (s *Store) ListEvents(ctx context.Context, runID, afterSeq int64, limit int) ([]domain.Event, error) {
	if limit <= 0 {
		limit = 500
	}
	var rows []eventRow
	if err := s.db.WithContext(ctx).
		Where("run_id = ? AND seq > ?", runID, afterSeq).
		Order("seq ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// MaxEventSeq 用于排查「事件发了但没落库」的洞，以及流式重放时的对齐检查。
func (s *Store) MaxEventSeq(ctx context.Context, runID int64) (int64, error) {
	var maxSeq int64
	if err := s.db.WithContext(ctx).Raw("SELECT COALESCE(MAX(seq), 0) FROM event WHERE run_id = ?", runID).Scan(&maxSeq).Error; err != nil {
		return 0, err
	}
	return maxSeq, nil
}
