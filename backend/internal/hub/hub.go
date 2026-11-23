package hub

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// Store 是 hub 需要的两个能力，接口开在这里而不是 import store，
// 保证 hub 不依赖持久化实现（测试里塞个 fake 就能跑）。
type Store interface {
	InsertEvents(ctx context.Context, events []domain.Event) error
	ListEvents(ctx context.Context, runID, afterSeq int64, limit int) ([]domain.Event, error)
}

type Options struct {
	BatchSize       int           // 攒够多少条立刻落库
	FlushInterval   time.Duration // 最多攒多久
	SubscriberBuf   int           // 订阅缓冲，满了踢人
	HistoryPageSize int           // 补发分页大小
}

// Hub 承担三件事：分配 seq、批量落库、广播给订阅者。
// 不参与业务判断——它不知道 run 是什么状态，只负责把事件按序发出去。
type Hub struct {
	mu      sync.Mutex
	streams map[int64]*stream

	store Store
	opt   Options
	log   *slog.Logger
	now   func() time.Time
}

type stream struct {
	runID int64

	flushMu   sync.Mutex
	closed    bool
	mu        sync.Mutex
	nextSeq   int64
	pending   []domain.Event
	subs      map[*subscriber]struct{}
	lastFlush time.Time
}

type subscriber struct {
	ch   chan domain.Event
	done chan struct{}
}

func New(st Store, opt Options, log *slog.Logger) *Hub {
	if opt.BatchSize <= 0 {
		opt.BatchSize = 32
	}
	if opt.FlushInterval <= 0 {
		opt.FlushInterval = 500 * time.Millisecond
	}
	if opt.SubscriberBuf <= 0 {
		opt.SubscriberBuf = 256
	}
	if opt.HistoryPageSize <= 0 {
		opt.HistoryPageSize = 500
	}
	return &Hub{
		streams: make(map[int64]*stream),
		store:   st,
		opt:     opt,
		log:     log,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// Run 是全局唯一的定时 flush 协程：扫所有 stream，把攒超时的批次落库。
// 每个 Run 起一个 timer 的做法在几百并发下就是几百个 goroutine，没必要。
func (h *Hub) Run(ctx context.Context) {
	interval := h.opt.FlushInterval / 5
	if interval < 50*time.Millisecond {
		interval = 50 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			finalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			h.FlushAll(finalCtx)
			cancel()
			return
		case <-ticker.C:
			h.flushAll(ctx)
		}
	}
}

// Publish 分配 seq、落内存、广播。锁内只做内存操作，落库在锁外。
//
// 订阅者 channel 满时直接踢掉（A-02）：执行器绝不能因为某个慢客户端停下来，
// 客户端重连后用 Last-Event-ID 补发即可。
func (h *Hub) Publish(ctx context.Context, runID int64, typ string, payload []byte) (domain.Event, error) {
	s := h.streamOf(runID)
	ev := domain.Event{RunID: runID, Type: typ, Payload: payload, CreatedAt: h.now()}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ev, errors.New("事件流已结束")
	}
	flush := false
	ev.Seq = s.nextSeq
	s.nextSeq++
	s.pending = append(s.pending, ev)
	if len(s.pending) == 1 {
		s.lastFlush = h.now()
	}
	if len(s.pending) >= h.opt.BatchSize {
		flush = true
	}
	for sub := range s.subs {
		select {
		case sub.ch <- ev:
		default:
			close(sub.done)
			delete(s.subs, sub)
			h.log.Warn("订阅者处理不过来，已断开，等客户端重连补发",
				"runId", runID, "seq", ev.Seq, "buf", h.opt.SubscriberBuf)
		}
	}
	if typ == domain.EventRunTerminal {
		s.closed = true
	}
	s.mu.Unlock()

	if flush {
		if err := h.Flush(ctx, runID); err != nil {
			return ev, err
		}
	}
	return ev, nil
}

// Subscribe 先建立订阅、后读库补历史，顺序不能反：
// 反过来的话，读库期间产生的事件既不在历史里也不在 channel 里，会永久丢（A-03）。
func (h *Hub) Subscribe(ctx context.Context, runID, afterSeq int64) (<-chan domain.Event, func(), error) {
	s := h.streamOf(runID)
	sub := &subscriber{
		ch:   make(chan domain.Event, h.opt.SubscriberBuf),
		done: make(chan struct{}),
	}

	s.mu.Lock()
	s.subs[sub] = struct{}{}
	pending := append([]domain.Event(nil), s.pending...)
	cutoff := s.nextSeq - 1
	if cutoff == 0 {
		cutoff = int64(^uint64(0) >> 1)
	}
	s.mu.Unlock()

	out := make(chan domain.Event, h.opt.SubscriberBuf)
	go func() {
		defer close(out)
		last := afterSeq

		// 1) 补历史
		for {
			history, err := h.store.ListEvents(ctx, runID, last, h.opt.HistoryPageSize)
			if err != nil {
				h.log.Error("补发读取事件失败", "runId", runID, "afterSeq", last, "err", err)
				return
			}
			if len(history) == 0 {
				break
			}
			for _, ev := range history {
				if ev.Seq > cutoff {
					break
				}
				if ev.Seq <= last {
					continue
				}
				select {
				case out <- ev:
					last = ev.Seq
				case <-ctx.Done():
					return
				case <-sub.done:
					return
				}
			}
			if len(history) < h.opt.HistoryPageSize || history[len(history)-1].Seq >= cutoff {
				break
			}
		}

		// 1.5) 补发「还在内存里没落库」的那批。
		// 只读库会漏掉它们：hub 是攒批写库的，订阅者来得早的时候，
		// 前几条事件还在 pending 里，库里根本查不到，而实时通道又只收
		// 订阅之后产生的事件——这段空窗会让客户端直接从第 5 条开始看到内容。

		for _, ev := range pending {
			if ev.Seq <= last {
				continue
			}
			select {
			case out <- ev:
				last = ev.Seq
			case <-ctx.Done():
				return
			case <-sub.done:
				return
			}
		}

		// 2) 转实时。窗口期重复投递的事件靠 seq 去重（历史里已经发过）。
		for {
			select {
			case <-ctx.Done():
				return
			case <-sub.done:
				return
			case ev := <-sub.ch:
				if ev.Seq <= last {
					continue
				}
				select {
				case out <- ev:
					last = ev.Seq
				case <-ctx.Done():
					return
				case <-sub.done:
					return
				}
			}
		}
	}()

	cancel := func() {
		s.mu.Lock()
		if _, ok := s.subs[sub]; ok {
			delete(s.subs, sub)
			close(sub.done)
		}
		s.mu.Unlock()
		h.collect(runID)
	}
	return out, cancel, nil
}

// Flush 把某个 Run 的待落库事件立刻写掉。
// 执行器写完 run.terminal 后调用它：否则客户端断线重连时可能读不到终态事件。
func (h *Hub) Flush(ctx context.Context, runID int64) error {
	s := h.lookup(runID)
	if s == nil {
		return nil
	}
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.Lock()
	batch := append([]domain.Event(nil), s.pending...)
	s.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	if err := h.store.InsertEvents(ctx, batch); err != nil {
		return err
	}
	s.mu.Lock()
	s.pending = s.pending[len(batch):]
	if len(s.pending) == 0 {
		s.lastFlush = time.Time{}
	}
	s.mu.Unlock()
	h.collect(runID)
	return nil
}

// Drop 在 Run 结束后回收 stream 内存：没有订阅者时直接从 map 摘掉。
// 之后如果有人再来订阅，会新建 stream 并从库里补历史，不影响正确性。
func (h *Hub) Drop(runID int64) {
	if s := h.lookup(runID); s != nil {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
	}
	h.collect(runID)
}
func (h *Hub) collect(runID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.streams[runID]; ok {
		s.mu.Lock()
		empty := s.closed && len(s.subs) == 0 && len(s.pending) == 0
		s.mu.Unlock()
		if empty {
			delete(h.streams, runID)
		}
	}
}
func (h *Hub) flushAll(ctx context.Context) {
	h.mu.Lock()
	ids := make([]int64, 0, len(h.streams))
	for id := range h.streams {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		s := h.lookup(id)
		if s == nil {
			continue
		}
		s.mu.Lock()
		due := len(s.pending) > 0 && h.now().Sub(s.lastFlush) >= h.opt.FlushInterval
		s.mu.Unlock()
		if due {
			if err := h.Flush(ctx, id); err != nil {
				h.log.Error("定时落库失败，批次保留等待重试", "runId", id)
			}
		}
	}
}

// FlushAll 在执行器退出后强制写出所有尾批，不受 flush_interval 限制。
func (h *Hub) FlushAll(ctx context.Context) error {
	h.mu.Lock()
	ids := make([]int64, 0, len(h.streams))
	for id := range h.streams {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	var errs []error
	for _, id := range ids {
		if err := h.Flush(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (h *Hub) streamOf(runID int64) *stream {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.streams[runID]; ok {
		return s
	}
	// 新 Run 的 seq 从 1 开始。重启后 running 一律置 interrupted（D-02），
	// 不会有「继续给旧 Run 追加事件」的场景，所以不需要从库里恢复 nextSeq。
	s := &stream{
		runID:   runID,
		nextSeq: 1,
		subs:    make(map[*subscriber]struct{}),
	}
	h.streams[runID] = s
	return s
}

func (h *Hub) lookup(runID int64) *stream {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.streams[runID]
}
