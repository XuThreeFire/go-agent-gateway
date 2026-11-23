package store

import (
	"context"
	"encoding/json"
	"strings"

	"gorm.io/gorm"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// LoadConversation 拼某个 Turn 的上下文：按 seq 升序把历史轮次摊成消息列表。
//
// 设计上 Turn 只存 user_input，助手回答只存在于事件流里（turn.delta）。
// 所以历史轮次的 assistant 内容是「从事件表重放出来」的，而不是另存一列：
// 好处是只有一份真相，代价是多一次查询（N+1，本轮可接受，量大再换成按 run 批量聚合）。
func (s *Store) LoadConversation(ctx context.Context, taskID, turnID int64) ([]domain.Message, error) {
	var current turnRow
	if err := s.db.WithContext(ctx).Where("id = ? AND task_id = ?", turnID, taskID).Take(&current).Error; err != nil {
		return nil, err
	}

	var turns []turnRow
	if err := s.db.WithContext(ctx).
		Where("task_id = ? AND seq <= ?", taskID, current.Seq).
		Order("seq ASC").Find(&turns).Error; err != nil {
		return nil, err
	}

	msgs := make([]domain.Message, 0, len(turns)*2)
	for _, t := range turns {
		msgs = append(msgs, domain.Message{Role: domain.RoleUser, Content: t.UserInput})
		if t.ID == turnID {
			// 当前轮的回答正在生成，不能提前拼进上下文。
			continue
		}
		text, err := s.assistantText(ctx, t.ID)
		if err != nil && err != gorm.ErrRecordNotFound {
			return nil, err
		}
		if text != "" {
			msgs = append(msgs, domain.Message{Role: domain.RoleAssistant, Content: text})
		}
	}
	return msgs, nil
}

// assistantText 取该轮「最近一次成功执行」的完整回答。
// 失败或中断的 Run 不参与拼接：半句话进上下文会污染后续轮次。
func (s *Store) assistantText(ctx context.Context, turnID int64) (string, error) {
	var run runRow
	if err := s.db.WithContext(ctx).
		Where("turn_id = ? AND status = ?", turnID, domain.StatusCompleted).
		Order("attempt_no DESC").Take(&run).Error; err != nil {
		return "", err
	}

	var events []eventRow
	if err := s.db.WithContext(ctx).
		Where("run_id = ? AND type = ?", run.ID, domain.EventTurnDelta).
		Order("seq ASC").Find(&events).Error; err != nil {
		return "", err
	}

	var b strings.Builder
	for _, ev := range events {
		var payload domain.TurnDeltaPayload
		if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
			// 单条脏数据不该毁掉整个上下文，跳过并继续。
			continue
		}
		b.WriteString(payload.Delta)
	}
	return b.String(), nil
}
