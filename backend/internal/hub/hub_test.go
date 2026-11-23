package hub

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

type fakeStore struct {
	mu     sync.Mutex
	events []domain.Event
	seq    int64
}

func (f *fakeStore) InsertEvents(_ context.Context, evs []domain.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evs...)
	if len(evs) > 0 {
		f.seq = evs[len(evs)-1].Seq
	}
	return nil
}

func (f *fakeStore) ListEvents(_ context.Context, runID, afterSeq int64, limit int) ([]domain.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Event, 0, limit)
	for _, ev := range f.events {
		if ev.RunID != runID || ev.Seq <= afterSeq {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func recv(t *testing.T, ch <-chan domain.Event, timeout time.Duration) (domain.Event, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(timeout):
		t.Fatalf("等待事件超时（%s）", timeout)
		return domain.Event{}, false
	}
}

// seq 必须按 Run 严格递增，且是 hub 唯一分配——两处各发一次就会错序。
func TestPublishAssignsMonotonicSeq(t *testing.T) {
	st := &fakeStore{}
	h := New(st, Options{BatchSize: 1, FlushInterval: 10 * time.Millisecond}, testLogger())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		ev, err := h.Publish(ctx, 42, domain.EventTurnDelta, []byte(`{"delta":"x"}`))
		if err != nil {
			t.Fatalf("publish 失败: %v", err)
		}
		if want := int64(i + 1); ev.Seq != want {
			t.Fatalf("第 %d 条 seq = %d，期望 %d", i, ev.Seq, want)
		}
	}

	if len(st.events) != 3 {
		t.Fatalf("BatchSize=1 应逐条落库，实际 %d 条", len(st.events))
	}
}

// 补发要覆盖「先订阅再读库」的两段拼接：历史 + 窗口期实时事件，且不能重复。
func TestSubscribeReplaysHistoryThenLive(t *testing.T) {
	st := &fakeStore{}
	h := New(st, Options{BatchSize: 1, FlushInterval: 10 * time.Millisecond, SubscriberBuf: 8}, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < 3; i++ {
		if _, err := h.Publish(ctx, 7, domain.EventTurnDelta, []byte(`{"delta":"a"}`)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	ch, closeSub, err := h.Subscribe(ctx, 7, 1)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer closeSub()

	// 历史：seq 2、3
	for _, want := range []int64{2, 3} {
		ev, ok := recv(t, ch, 2*time.Second)
		if !ok {
			t.Fatal("补发通道意外关闭")
		}
		if ev.Seq != want {
			t.Fatalf("补发 seq = %d，期望 %d", ev.Seq, want)
		}
	}

	// 实时：紧接着 publish 的第 4 条必须从同一通道出来
	if _, err := h.Publish(ctx, 7, domain.EventTurnDelta, []byte(`{"delta":"b"}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	ev, ok := recv(t, ch, 2*time.Second)
	if !ok {
		t.Fatal("实时通道意外关闭")
	}
	if ev.Seq != 4 {
		t.Fatalf("实时 seq = %d，期望 4（重复投递应被去重）", ev.Seq)
	}
}

// 订阅者来得早、事件还没攒够一批落库时，补发不能只看库——否则前几条会永久丢。
func TestSubscribeReplaysUnflushedPending(t *testing.T) {
	st := &fakeStore{}
	// BatchSize 设大 + flush 间隔很长：Publish 完全不落库，只能靠 pending 补
	h := New(st, Options{BatchSize: 100, FlushInterval: time.Hour, SubscriberBuf: 8}, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < 3; i++ {
		if _, err := h.Publish(ctx, 11, domain.EventTurnDelta, []byte(`{"delta":"a"}`)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	if len(st.events) != 0 {
		t.Fatalf("事件不应该已落库，实际 %d 条", len(st.events))
	}

	ch, closeSub, err := h.Subscribe(ctx, 11, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer closeSub()

	for _, want := range []int64{1, 2, 3} {
		ev, ok := recv(t, ch, 2*time.Second)
		if !ok {
			t.Fatal("补发通道意外关闭")
		}
		if ev.Seq != want {
			t.Fatalf("补发 seq = %d，期望 %d", ev.Seq, want)
		}
	}
}

// 慢订阅者必须被踢掉，且不能阻塞 Publish——否则一个卡住的浏览器能拖停所有任务。
func TestSlowSubscriberIsKicked(t *testing.T) {
	st := &fakeStore{}
	h := New(st, Options{BatchSize: 1, FlushInterval: 10 * time.Millisecond, SubscriberBuf: 1}, testLogger())
	ctx := context.Background()

	ch, closeSub, err := h.Subscribe(ctx, 9, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer closeSub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 5; i++ {
			if _, err := h.Publish(ctx, 9, domain.EventTurnDelta, []byte(`{"delta":"x"}`)); err != nil {
				t.Errorf("publish: %v", err)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish 被慢订阅者阻塞了，反压保护失效")
	}

	// 通道最终会被关闭（订阅被踢后 goroutine 退出）
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("慢订阅者没有被踢掉，通道仍未关闭")
		}
	}
}

// 终态事件 emit 后要能立刻落库：客户端断线重连时靠它判断结束。
func TestFlushPersistsPendingEvents(t *testing.T) {
	st := &fakeStore{}
	// BatchSize 设大，Publish 不会立刻落库，只能靠 Flush 落
	h := New(st, Options{BatchSize: 100, FlushInterval: time.Hour}, testLogger())
	ctx := context.Background()

	if _, err := h.Publish(ctx, 3, domain.EventRunTerminal, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(st.events) != 0 {
		t.Fatalf("未到阈值不应该落库，实际 %d 条", len(st.events))
	}
	if err := h.Flush(ctx, 3); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(st.events) != 1 {
		t.Fatalf("Flush 后应有 1 条，实际 %d 条", len(st.events))
	}
}
