package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newMockStore(t *testing.T, now func() time.Time) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		sqlDB.Close()
	})
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{NowFunc: now, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return &Store{db: db}, mock
}

func expectOwner(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT t.client_id FROM task t JOIN run").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"client_id"}).AddRow("fixture-client"))
}
func TestAppendStepCommitsBothQuotasWithStepAndRun(t *testing.T) {
	clock := time.Date(2026, 11, 23, 23, 59, 59, 0, time.UTC)
	st, mock := newMockStore(t, func() time.Time { return clock })
	for _, day := range []string{"20261123", "20261124"} {
		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO `step`").WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec("UPDATE `run` SET").WillReturnResult(sqlmock.NewResult(0, 1))
		expectOwner(mock)
		for _, counter := range []struct{ scope, key string }{{ScopeGlobalDay, "all:" + day}, {ScopeClientDay, "fixture-client:" + day}} {
			mock.ExpectExec("INSERT INTO `usage_counter`").WithArgs(counter.scope, counter.key, int64(15), clock, clock, int64(15), clock).WillReturnResult(sqlmock.NewResult(1, 1))
		}
		mock.ExpectCommit()
		if err := st.AppendStep(context.Background(), domain.Step{RunID: 1, Seq: 1, Type: domain.StepModelCall, InputTokens: 10, OutputTokens: 5}); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(time.Second)
	}
}
func TestAppendStepRollsBackOnWriteFailure(t *testing.T) {
	for _, failure := range []string{"step", "run", "owner", "global", "client"} {
		t.Run(failure, func(t *testing.T) {
			st, mock := newMockStore(t, func() time.Time { return time.Now().UTC() })
			writeErr := errors.New("injected transaction failure")
			mock.ExpectBegin()
			step := mock.ExpectExec("INSERT INTO `step`")
			if failure == "step" {
				step.WillReturnError(writeErr)
			} else {
				step.WillReturnResult(sqlmock.NewResult(1, 1))
				run := mock.ExpectExec("UPDATE `run` SET")
				if failure == "run" {
					run.WillReturnError(writeErr)
				} else {
					if failure == "owner" {
						mock.ExpectQuery("SELECT t.client_id FROM task t JOIN run").WillReturnError(writeErr)
					} else {
						expectOwner(mock)
						global := mock.ExpectExec("INSERT INTO `usage_counter`")
						if failure == "global" {
							global.WillReturnError(writeErr)
						} else {
							global.WillReturnResult(sqlmock.NewResult(1, 1))
							mock.ExpectExec("INSERT INTO `usage_counter`").WillReturnError(writeErr)
						}
					}
					run.WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			mock.ExpectRollback()
			err := st.AppendStep(context.Background(), domain.Step{RunID: 1, Seq: 1, Type: domain.StepModelCall, InputTokens: 10, OutputTokens: 5})
			if !errors.Is(err, writeErr) {
				t.Fatalf("原始错误应保留：%v", err)
			}
		})
	}
}
func TestAppendStepDoesNotChargeToolsOrUnknownUsage(t *testing.T) {
	for _, step := range []domain.Step{{RunID: 1, Type: domain.StepToolCall}, {RunID: 1, Type: domain.StepModelCall}} {
		st, mock := newMockStore(t, func() time.Time { return time.Now().UTC() })
		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO `step`").WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec("UPDATE `run` SET").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		if err := st.AppendStep(context.Background(), step); err != nil {
			t.Fatal(err)
		}
	}
}
