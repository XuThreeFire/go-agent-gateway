package hub

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

type faultStore struct {
	*fakeStore
	fail    atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *faultStore) InsertEvents(ctx context.Context, events []domain.Event) error {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.fail.Load() {
		return errors.New("injected write failure")
	}
	return s.fakeStore.InsertEvents(ctx, events)
}
func TestFailedBatchIsRetainedAndRetried(t *testing.T) {
	st := &faultStore{fakeStore: &fakeStore{}}
	st.fail.Store(true)
	h := New(st, Options{BatchSize: 1}, testLogger())
	if _, err := h.Publish(context.Background(), 1, domain.EventTurnDelta, []byte(`{"delta":"x"}`)); err == nil {
		t.Fatal("应报告写入失败")
	}
	ch, cancel, _ := h.Subscribe(context.Background(), 1, 0)
	defer cancel()
	ev, _ := recv(t, ch, time.Second)
	if ev.Seq != 1 {
		t.Fatal("失败批次仍应可补发")
	}
	st.fail.Store(false)
	if err := h.Flush(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(st.events) != 1 {
		t.Fatal("重试后应恰好保存一次")
	}
}
func TestSubscribeDuringInFlightFlushDoesNotLoseEvent(t *testing.T) {
	st := &faultStore{fakeStore: &fakeStore{}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	h := New(st, Options{BatchSize: 1}, testLogger())
	done := make(chan error, 1)
	go func() {
		_, err := h.Publish(context.Background(), 1, domain.EventTurnDelta, []byte(`{"delta":"x"}`))
		done <- err
	}()
	select {
	case <-st.entered:
	case <-time.After(time.Second):
		t.Fatal("写入未开始")
	}
	ch, cancel, _ := h.Subscribe(context.Background(), 1, 0)
	defer cancel()
	ev, _ := recv(t, ch, time.Second)
	if ev.Seq != 1 {
		t.Fatal("落库中的事件必须仍可补发")
	}
	close(st.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestShutdownFlushesYoungTail(t *testing.T) {
	st := &fakeStore{}
	h := New(st, Options{BatchSize: 100, FlushInterval: time.Hour}, testLogger())
	h.Publish(context.Background(), 1, domain.EventTurnDelta, []byte(`{"delta":"tail"}`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.Run(ctx)
	if len(st.events) != 1 {
		t.Fatal("退出必须强制刷新未到期尾批")
	}
}
