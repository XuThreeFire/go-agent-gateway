package service

import (
	"context"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/store"
	"testing"
)

type transactionStore struct{ created bool }

func (s transactionStore) CreateTaskWithRun(context.Context, store.CreateTaskParams) (domain.Task, domain.Turn, domain.Run, bool, error) {
	return domain.Task{ID: 1}, domain.Turn{ID: 2}, domain.Run{ID: 3}, s.created, nil
}
func (s transactionStore) AppendTurnWithRun(context.Context, int64, string, []byte, *string) (domain.Turn, domain.Run, bool, error) {
	return domain.Turn{ID: 2}, domain.Run{ID: 3}, s.created, nil
}
func (s transactionStore) CreateRetryRun(context.Context, int64) (domain.Run, error) {
	return domain.Run{ID: 4}, nil
}

type runner struct{ ids []int64 }

func (r *runner) Submit(id int64)                                   { r.ids = append(r.ids, id) }
func (r *runner) Cancel(context.Context, int64) (domain.Run, error) { return domain.Run{}, nil }
func TestIdempotentReplayDoesNotSubmitTwice(t *testing.T) {
	for _, created := range []bool{false, true} {
		r := &runner{}
		s := New(transactionStore{created}, r)
		if _, err := s.CreateTask(context.Background(), CreateTaskInput{}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendTurn(context.Background(), AppendTurnInput{}); err != nil {
			t.Fatal(err)
		}
		want := 0
		if created {
			want = 2
		}
		if len(r.ids) != want {
			t.Fatalf("created=%v submits=%v", created, r.ids)
		}
	}
}
