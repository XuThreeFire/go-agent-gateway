package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// CreateTaskParams 建一个新会话：Task + Turn(seq=1) + Run(attempt=1)。
type CreateTaskParams struct {
	ClientID       string
	Title          string
	Input          string
	ModelSnapshot  []byte
	IdempotencyKey *string
}

// CreateTaskWithRun 在一个事务里顺序插入三行（自增主键，父表拿到 LastInsertId 才能拼子表）。
//
// 幂等分两层：
//   - 建会话：按 (client_id, idempotency_key) 查 task，命中则直接返回原会话（uk_client_idem）；
//   - 追加轮次：按 (task_id, idempotency_key) 查 run（uk_task_idem）。
//
// 只靠先查后插会有并发窗口，所以插入失败时再用唯一索引兜底回查一次。
// 返回的 created 表示这次是否真的新建了 Run——只有新建才该提交执行，否则会重复跑。
func (s *Store) CreateTaskWithRun(ctx context.Context, p CreateTaskParams) (domain.Task, domain.Turn, domain.Run, bool, error) {
	if p.IdempotencyKey != nil && *p.IdempotencyKey != "" {
		task, turn, run, found, err := s.findTaskByIdempotencyKey(ctx, p.ClientID, *p.IdempotencyKey)
		if err != nil {
			return domain.Task{}, domain.Turn{}, domain.Run{}, false, err
		}
		if found {
			return task, turn, run, false, nil
		}
	}

	var (
		task domain.Task
		turn domain.Turn
		run  domain.Run
	)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		t := &taskRow{ClientID: p.ClientID, Title: p.Title, IdempotencyKey: p.IdempotencyKey}
		if err := tx.Create(t).Error; err != nil {
			return fmt.Errorf("插入 task 失败: %w", err)
		}
		tu := &turnRow{TaskID: t.ID, Seq: 1, UserInput: p.Input}
		if err := tx.Create(tu).Error; err != nil {
			return fmt.Errorf("插入 turn 失败: %w", err)
		}
		r := &runRow{
			TaskID:         t.ID,
			TurnID:         tu.ID,
			AttemptNo:      1,
			IdempotencyKey: p.IdempotencyKey,
			ModelSnapshot:  string(p.ModelSnapshot),
			Status:         domain.StatusPending,
		}
		if err := tx.Create(r).Error; err != nil {
			return fmt.Errorf("插入 run 失败: %w", err)
		}
		task, turn, run = t.toDomain(), tu.toDomain(), r.toDomain()
		return nil
	})
	if err != nil {
		// 并发同键：先查后插没拦住，靠唯一索引兜底，回查后返回已有会话。
		if p.IdempotencyKey != nil && isDuplicateKey(err) {
			t, tu, r, found, ferr := s.findTaskByIdempotencyKey(ctx, p.ClientID, *p.IdempotencyKey)
			if ferr == nil && found {
				return t, tu, r, false, nil
			}
		}
		return domain.Task{}, domain.Turn{}, domain.Run{}, false, err
	}
	return task, turn, run, true, nil
}

// findTaskByIdempotencyKey 回查幂等命中的会话，连同首个轮次与首个 Run 一起返回，
// 保证重放请求拿到的响应和第一次完全一致。
func (s *Store) findTaskByIdempotencyKey(ctx context.Context, clientID, key string) (domain.Task, domain.Turn, domain.Run, bool, error) {
	var t taskRow
	err := s.db.WithContext(ctx).Where("client_id = ? AND idempotency_key = ?", clientID, key).Take(&t).Error
	if err == gorm.ErrRecordNotFound {
		return domain.Task{}, domain.Turn{}, domain.Run{}, false, nil
	}
	if err != nil {
		return domain.Task{}, domain.Turn{}, domain.Run{}, false, err
	}

	var tu turnRow
	if err := s.db.WithContext(ctx).Where("task_id = ?", t.ID).Order("seq ASC").Take(&tu).Error; err != nil {
		return domain.Task{}, domain.Turn{}, domain.Run{}, false, fmt.Errorf("幂等回查轮次失败: %w", err)
	}
	var r runRow
	if err := s.db.WithContext(ctx).Where("task_id = ?", t.ID).Order("id ASC").Take(&r).Error; err != nil {
		return domain.Task{}, domain.Turn{}, domain.Run{}, false, fmt.Errorf("幂等回查 run 失败: %w", err)
	}
	return t.toDomain(), tu.toDomain(), r.toDomain(), true, nil
}

// AppendTurnWithRun 追加一轮对话并启动新 Run。
//
// 规则 1（同一 Task 只有一个活跃 Run）在这里守：先对 task 行加 FOR UPDATE，
// 把并发提交串行化，再检查是否存在 pending/running 的 Run。
// 幂等键命中时直接返回原来的 Turn + Run，不新建、不重复扣配额；
// created=false 表示这是重放，调用方不应再次提交执行（否则同一个 Run 会跑两遍）。
func (s *Store) AppendTurnWithRun(ctx context.Context, taskID int64, input string, snapshot []byte, idemKey *string) (domain.Turn, domain.Run, bool, error) {
	var (
		turn    domain.Turn
		run     domain.Run
		created bool
	)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lockedID int64
		row := tx.Raw("SELECT id FROM task WHERE id = ? FOR UPDATE", taskID).Row()
		if err := row.Scan(&lockedID); err != nil {
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("锁定 task 失败: %w", err)
		}
		if lockedID == 0 {
			return ErrNotFound
		}

		// 幂等：同一 Task 下同一 key 已有 Run，直接复用。
		if idemKey != nil && *idemKey != "" {
			var exist runRow
			err := tx.Where("task_id = ? AND idempotency_key = ?", taskID, *idemKey).Take(&exist).Error
			if err == nil {
				var existTurn turnRow
				if err := tx.Where("id = ?", exist.TurnID).Take(&existTurn).Error; err != nil {
					return err
				}
				turn, run, created = existTurn.toDomain(), exist.toDomain(), false
				return nil
			}
			if err != gorm.ErrRecordNotFound {
				return err
			}
		}

		// 规则 1：已有活跃 Run 就拒绝，把当前状态交回给客户端。
		var active runRow
		err := tx.Where("task_id = ? AND status IN ?", taskID, []string{domain.StatusPending, domain.StatusRunning}).
			Order("id DESC").Take(&active).Error
		if err == nil {
			return &ConflictError{ActiveRunID: active.ID, Status: active.Status}
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}

		var maxSeq int
		if err := tx.Raw("SELECT COALESCE(MAX(seq), 0) FROM turn WHERE task_id = ?", taskID).Scan(&maxSeq).Error; err != nil {
			return fmt.Errorf("查询轮次序号失败: %w", err)
		}

		tu := &turnRow{TaskID: taskID, Seq: maxSeq + 1, UserInput: input}
		if err := tx.Create(tu).Error; err != nil {
			return fmt.Errorf("插入 turn 失败: %w", err)
		}
		r := &runRow{
			TaskID:         taskID,
			TurnID:         tu.ID,
			AttemptNo:      1,
			IdempotencyKey: idemKey,
			ModelSnapshot:  string(snapshot),
			Status:         domain.StatusPending,
		}
		if err := tx.Create(r).Error; err != nil {
			return fmt.Errorf("插入 run 失败: %w", err)
		}
		if err := tx.Model(&taskRow{}).Where("id = ?", taskID).Update("updated_at", time.Now().UTC()).Error; err != nil {
			return err
		}
		turn, run, created = tu.toDomain(), r.toDomain(), true
		return nil
	})
	return turn, run, created, err
}

// GetTask 取会话主记录。
func (s *Store) GetTask(ctx context.Context, taskID int64) (domain.Task, error) {
	var row taskRow
	if err := s.db.WithContext(ctx).Where("id = ?", taskID).Take(&row).Error; err != nil {
		return domain.Task{}, err
	}
	return row.toDomain(), nil
}

type TaskPage struct {
	Items      []domain.Task
	HasMore    bool
	NextCursor string
	// LastRuns 是每个 task 最近一次 Run，按 task_id 索引。
	// 列表页要显示「运行中 / 失败」角标，逐条回查就是 N+1，所以随分页一次取回。
	LastRuns map[int64]domain.Run
}

// ListTasks 按 (client_id, updated_at DESC, id DESC) 游标翻页。
// 游标里带 id 是为了处理「同一毫秒多个会话」的边界，只带时间会漏。
func (s *Store) ListTasks(ctx context.Context, clientID, cursor string, limit int) (TaskPage, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := s.db.WithContext(ctx).Model(&taskRow{}).Where("client_id = ?", clientID)
	if cursor != "" {
		ts, id, err := decodeCursor(cursor)
		if err != nil {
			return TaskPage{}, err
		}
		q = q.Where("(updated_at < ? OR (updated_at = ? AND id < ?))", ts, ts, id)
	}
	var rows []taskRow
	if err := q.Order("updated_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return TaskPage{}, err
	}

	page := TaskPage{Items: make([]domain.Task, 0, len(rows))}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	for _, r := range rows {
		page.Items = append(page.Items, r.toDomain())
	}
	if page.HasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = encodeCursor(last.UpdatedAt, last.ID)
	}

	// 一条 SQL 取回本页每个会话的最新 Run（id 最大 = 最近一次尝试）。
	// 不做「循环里查库」：20 个会话就是 20 次往返，且以后加字段会越滚越多。
	// 子查询按 task_id 分组取 MAX(id)，只回本页行数，代价与页大小同阶。
	if len(page.Items) > 0 {
		lastRuns, err := s.lastRunsByTaskIDs(ctx, rows)
		if err != nil {
			return TaskPage{}, err
		}
		page.LastRuns = lastRuns
	}
	return page, nil
}

// lastRunsByTaskIDs 用一条 join 取每组最新一条 run。
// 单独抽出来是为了让 ListTasks 的读路径只有两次往返：分页 + 最新 Run。
func (s *Store) lastRunsByTaskIDs(ctx context.Context, rows []taskRow) (map[int64]domain.Run, error) {
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	var lastRows []runRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT r.*
		FROM run r
		JOIN (
			SELECT task_id, MAX(id) AS max_id
			FROM run
			WHERE task_id IN ?
			GROUP BY task_id
		) m ON m.max_id = r.id`, ids).Scan(&lastRows).Error; err != nil {
		return nil, err
	}
	out := make(map[int64]domain.Run, len(lastRows))
	for _, r := range lastRows {
		out[r.TaskID] = r.toDomain()
	}
	return out, nil
}

// TaskDetail 是 GET /api/tasks/{id} 的返回体，一次把轮次与 Run 都带上。
type TaskDetail struct {
	Task  domain.Task
	Turns []domain.Turn
	Runs  []domain.Run
}

func (s *Store) GetTaskDetail(ctx context.Context, taskID int64) (TaskDetail, error) {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return TaskDetail{}, err
	}
	var turnRows []turnRow
	if err := s.db.WithContext(ctx).Where("task_id = ?", taskID).Order("seq ASC").Find(&turnRows).Error; err != nil {
		return TaskDetail{}, err
	}
	var runRows []runRow
	if err := s.db.WithContext(ctx).Where("task_id = ?", taskID).Order("id ASC").Find(&runRows).Error; err != nil {
		return TaskDetail{}, err
	}

	detail := TaskDetail{Task: task}
	for _, r := range turnRows {
		detail.Turns = append(detail.Turns, r.toDomain())
	}
	for _, r := range runRows {
		detail.Runs = append(detail.Runs, r.toDomain())
	}
	return detail, nil
}

func encodeCursor(ts time.Time, id int64) string {
	raw := fmt.Sprintf("%d:%d", ts.UTC().UnixMilli(), id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (time.Time, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("游标格式非法: %w", err)
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return time.Time{}, 0, fmt.Errorf("游标格式非法: %q", string(raw))
	}
	ms, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("游标时间非法: %w", err)
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("游标 id 非法: %w", err)
	}
	return time.UnixMilli(ms).UTC(), id, nil
}
