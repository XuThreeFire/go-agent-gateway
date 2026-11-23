package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// GetRun 取一次执行尝试。
func (s *Store) GetRun(ctx context.Context, runID int64) (domain.Run, error) {
	var row runRow
	if err := s.db.WithContext(ctx).Where("id = ?", runID).Take(&row).Error; err != nil {
		return domain.Run{}, err
	}
	return row.toDomain(), nil
}

// GetTurn 取一轮对话。
func (s *Store) GetTurn(ctx context.Context, turnID int64) (domain.Turn, error) {
	var row turnRow
	if err := s.db.WithContext(ctx).Where("id = ?", turnID).Take(&row).Error; err != nil {
		return domain.Turn{}, err
	}
	return row.toDomain(), nil
}

// CASStatus 条件更新状态：`WHERE id = ? AND status = ?`，影响行数为 0 即抢输。
//
// 为什么 WHERE 里不带 version：状态机没有回边（终态不可回到 pending/running），
// 不存在 ABA 问题，status 本身已经足够做条件。version 仍然每次 +1，留给排查与
// 后续「同状态内多次更新」（例如重试计数）使用。
func (s *Store) CASStatus(ctx context.Context, runID int64, from, to string, patch map[string]any) (bool, error) {
	updates := map[string]any{
		"status":  to,
		"version": gorm.Expr("version + 1"),
	}
	for k, v := range patch {
		updates[k] = v
	}
	res := s.db.WithContext(ctx).Model(&runRow{}).
		Where("id = ? AND status = ?", runID, from).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// AppendStep 将步骤、Run token 和日用量放在同一事务，避免崩溃造成偏账。
func (s *Store) AppendStep(ctx context.Context, step domain.Step) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := newStepRow(step)
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("插入 step 失败: %w", err)
		}
		if err := tx.Model(&runRow{}).Where("id = ?", step.RunID).Updates(map[string]any{
			"input_tokens":  gorm.Expr("input_tokens + ?", step.InputTokens),
			"output_tokens": gorm.Expr("output_tokens + ?", step.OutputTokens),
		}).Error; err != nil {
			return err
		}
		total := step.InputTokens + step.OutputTokens
		if total <= 0 {
			return nil
		}
		var owner string
		if err := tx.Raw("SELECT t.client_id FROM task t JOIN run r ON r.task_id=t.id WHERE r.id=?", step.RunID).Scan(&owner).Error; err != nil {
			return err
		}
		if owner == "" {
			return ErrNotFound
		}
		usage := usageRow{Scope: ScopeClientDay, ScopeKey: DayKey(owner, time.Now()), Tokens: total}
		return tx.Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"tokens": gorm.Expr("tokens + ?", total), "updated_at": time.Now().UTC()})}).Create(&usage).Error
	})
}

// SetFirstToken 只在尚未记录时写一次，TTFT 因此不会被后续 chunk 覆盖。
func (s *Store) SetFirstToken(ctx context.Context, runID int64, at time.Time) error {
	return s.db.WithContext(ctx).Model(&runRow{}).
		Where("id = ? AND first_token_at IS NULL", runID).
		Update("first_token_at", at.UTC()).Error
}

// NextStepSeq 返回 Run 内下一个步骤序号（从 0 开始）。
// 执行器对同一 Run 串行调用模型和工具，因此这里不承担并发分配。
func (s *Store) NextStepSeq(ctx context.Context, runID int64) (int, error) {
	var maxSeq int
	if err := s.db.WithContext(ctx).Raw("SELECT COALESCE(MAX(seq), -1) FROM step WHERE run_id = ?", runID).Scan(&maxSeq).Error; err != nil {
		return 0, err
	}
	return maxSeq + 1, nil
}

// CreateRetryRun 基于一个已终态的 Run 新建一次尝试（attempt_no + 1），轮次与模型快照沿用。
//
// 三个约束都在事务里守：
//   - 源 Run 必须是终态，跑着的不能重试（否则同一 Turn 两路并发，规则 1 直接破）；
//   - 该 Task 不能有活跃 Run（先 FOR UPDATE 锁 task，把并发重试串行化）；
//   - attempt_no 由 uk_turn_attempt 兜底，并发重试最多一条能插进去。
//
// 幂等键不带过来：源 Run 已经占用了 (task_id, idempotency_key)，复制会导致唯一键冲突；
// 重试本身就是新的尝试，语义上也不该复用同一个键。
func (s *Store) CreateRetryRun(ctx context.Context, runID int64) (domain.Run, error) {
	var out domain.Run
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var src runRow
		if err := tx.Where("id = ?", runID).Take(&src).Error; err != nil {
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		// 只有 failed / interrupted / aborted 需要重试：
		// completed 重跑等于白烧 token（客户端要「再说一次」应该新提一个轮次）；
		// pending / running 重试会让同一 Turn 两路并发，直接破规则 1。
		switch src.Status {
		case domain.StatusFailed, domain.StatusInterrupted, domain.StatusAborted:
		default:
			return &ConflictError{ActiveRunID: src.ID, Status: src.Status}
		}

		var lockedID int64
		if err := tx.Raw("SELECT id FROM task WHERE id = ? FOR UPDATE", src.TaskID).Row().Scan(&lockedID); err != nil {
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("锁定 task 失败: %w", err)
		}

		var active runRow
		err := tx.Where("task_id = ? AND status IN ?", src.TaskID, []string{domain.StatusPending, domain.StatusRunning}).
			Order("id DESC").Take(&active).Error
		if err == nil {
			return &ConflictError{ActiveRunID: active.ID, Status: active.Status}
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}

		var nextAttempt int
		if err := tx.Raw("SELECT COALESCE(MAX(attempt_no), 0) + 1 FROM run WHERE turn_id = ?", src.TurnID).
			Scan(&nextAttempt).Error; err != nil {
			return fmt.Errorf("计算尝试序号失败: %w", err)
		}

		row := &runRow{
			TaskID:        src.TaskID,
			TurnID:        src.TurnID,
			AttemptNo:     nextAttempt,
			ModelSnapshot: src.ModelSnapshot,
			Status:        domain.StatusPending,
		}
		if err := tx.Create(row).Error; err != nil {
			// 并发重试撞 uk_turn_attempt：回查已存在的那一条，当作幂等结果返回。
			if isDuplicateKey(err) {
				var exist runRow
				if err := tx.Where("turn_id = ? AND attempt_no = ?", src.TurnID, nextAttempt).Take(&exist).Error; err == nil {
					out = exist.toDomain()
					return nil
				}
			}
			return fmt.Errorf("插入重试 run 失败: %w", err)
		}
		if err := tx.Model(&taskRow{}).Where("id = ?", src.TaskID).Update("updated_at", time.Now().UTC()).Error; err != nil {
			return err
		}
		out = row.toDomain()
		return nil
	})
	if err != nil {
		return domain.Run{}, err
	}
	return out, nil
}

// InterruptRunning 是启动扫描：把上个进程残留的 running / pending 一律置为 interrupted。
//
// 为什么要连 pending 一起扫：进程崩在「已插库、还没被调度」的窗口里，会留一条永远
// 不动不了的 pending；而规则 1 只看 pending/running，这条僵尸行会让该 Task 之后
// 每次提交都 409。宁可标成 interrupted 让它能重试，也不要卡住整个会话。
func (s *Store) InterruptRunning(ctx context.Context) ([]int64, error) {
	var ids []int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&runRow{}).
			Where("status IN ?", []string{domain.StatusRunning, domain.StatusPending}).
			Order("id ASC").Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		return tx.Model(&runRow{}).
			Where("id IN ? AND status IN ?", ids, []string{domain.StatusRunning, domain.StatusPending}).
			Updates(map[string]any{
				"status":        domain.StatusInterrupted,
				"version":       gorm.Expr("version + 1"),
				"ended_at":      time.Now().UTC(),
				"error_message": "进程重启，执行中断",
			}).Error
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}
