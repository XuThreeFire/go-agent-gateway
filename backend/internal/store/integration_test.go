package store_test

import (
	"context"
	"fmt"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/store"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/testutil"
	"sync"
	"testing"
	"time"
)

func TestMySQLTransactionsIdempotencyAndCAS(t *testing.T) {
	st := testutil.Database(t)
	ctx := context.Background()
	key := "create"
	client := fmt.Sprintf("store-%d", time.Now().UnixNano())
	task, _, run, created, err := st.CreateTaskWithRun(ctx, store.CreateTaskParams{ClientID: client, Input: "hi", Title: "fixture", ModelSnapshot: []byte(`{"name":"demo"}`), IdempotencyKey: &key})
	if err != nil || !created {
		t.Fatalf("创建事务失败: %v", err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"event", "step"} {
			st.DB().Exec("DELETE FROM "+table+" WHERE run_id IN (SELECT id FROM run WHERE task_id = ?)", task.ID)
		}
		st.DB().Exec("DELETE FROM run WHERE task_id = ?", task.ID)
		st.DB().Exec("DELETE FROM turn WHERE task_id = ?", task.ID)
		st.DB().Exec("DELETE FROM task WHERE id = ?", task.ID)
	})
	_, _, again, created, err := st.CreateTaskWithRun(ctx, store.CreateTaskParams{ClientID: client, Input: "hi", ModelSnapshot: []byte(`{}`), IdempotencyKey: &key})
	if err != nil || created || again.ID != run.ID {
		t.Fatal("幂等重放创建了额外执行")
	}
	if _, _, _, err := st.AppendTurnWithRun(ctx, task.ID, "next", []byte(`{}`), nil); err == nil {
		t.Fatal("活跃 Run 期间不得追加")
	}
	moved, err := st.CASStatus(ctx, run.ID, domain.StatusPending, domain.StatusRunning, nil)
	if err != nil || !moved {
		t.Fatal("状态迁移失败")
	}
	var wg sync.WaitGroup
	wins := make(chan bool, 2)
	for _, status := range []string{domain.StatusCompleted, domain.StatusAborted} {
		wg.Add(1)
		go func(status string) {
			defer wg.Done()
			ok, err := st.CASStatus(ctx, run.ID, domain.StatusRunning, status, nil)
			wins <- ok && err == nil
		}(status)
	}
	wg.Wait()
	close(wins)
	count := 0
	for ok := range wins {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatal("终态竞争必须只有一个赢家")
	}
	event := domain.Event{RunID: run.ID, Seq: 1, Type: domain.EventTurnDelta, Payload: []byte(`{"delta":"hello"}`)}
	if err := st.InsertEvents(ctx, []domain.Event{event}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertEvents(ctx, []domain.Event{event}); err != nil {
		t.Fatal("事件批次应支持不确定提交后的幂等重试")
	}
	events, err := st.ListEvents(ctx, run.ID, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatal("事件不得重复")
	}
	if err := st.AppendStep(ctx, domain.Step{RunID: run.ID, Seq: 0, Type: domain.StepModelCall, InputTokens: 12, OutputTokens: 3}); err != nil {
		t.Fatal(err)
	}
	fresh, _ := st.GetRun(ctx, run.ID)
	if fresh.InputTokens != 12 || fresh.OutputTokens != 3 {
		t.Fatal("Step 与 Run token 汇总不一致")
	}
	clientTokens, err := st.UsageTokens(ctx, store.ScopeClientDay, store.DayKey(client, time.Now()))
	if err != nil || clientTokens != 15 {
		t.Fatalf("客户端事务记账错误：%d %v", clientTokens, err)
	}
	globalTokens, err := st.UsageTokens(ctx, store.ScopeGlobalDay, store.GlobalDayKey(time.Now()))
	if err != nil || globalTokens < 15 {
		t.Fatalf("全局事务记账错误：%d %v", globalTokens, err)
	}

}
func TestMySQLConcurrentCreateUsesUniqueIndex(t *testing.T) {
	st := testutil.Database(t)
	ctx := context.Background()
	client := fmt.Sprintf("parallel-%d", time.Now().UnixNano())
	key := "same"
	var wg sync.WaitGroup
	ids := make(chan int64, 6)
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, r, _, err := st.CreateTaskWithRun(ctx, store.CreateTaskParams{ClientID: client, Input: "hi", ModelSnapshot: []byte(`{}`), IdempotencyKey: &key})
			ids <- r.ID
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := int64(0)
	for next := range ids {
		if id == 0 {
			id = next
		}
		if next != id {
			t.Fatal("同幂等键创建了多个 Run")
		}
	}
}
