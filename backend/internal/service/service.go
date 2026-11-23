// Package service 是用例编排层：建任务、继续、取消、重试。
//
// 它只做三件 api 层不该管的事：
//   - 把「创建三行 + 提交执行」串成一个用例（提交执行属于业务语义，不是 HTTP 语义）；
//   - 决定幂等重放时**不再提交执行**（否则同一个 Run 会跑两遍）；
//   - 把 store 与 exec 的错误归一化，让 api 只关心「映射成哪个状态码」。
//
// 不做协议处理（那是 api），也不直接写 SQL（那是 store）。
// 读路径（列表、详情、模型清单）只是一次查询，没有编排，直接由 api 调 store——
// 为一行调用再包一层方法只会增加跳转成本。
package service

import (
	"context"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/store"
)

// Runner 是执行器在本层暴露的能力。只依赖这两个方法，service 不需要知道
// 执行器有没有信号量、用几个 goroutine。
type Runner interface {
	Submit(runID int64)
	Cancel(ctx context.Context, runID int64) (domain.Run, error)
}

// Store 是业务编排所需的事务接口，真实 MySQL 与测试替身共用同一接口。
type Store interface {
	CreateTaskWithRun(context.Context, store.CreateTaskParams) (domain.Task, domain.Turn, domain.Run, bool, error)
	AppendTurnWithRun(context.Context, int64, string, []byte, *string) (domain.Turn, domain.Run, bool, error)
	CreateRetryRun(context.Context, int64) (domain.Run, error)
}

type Service struct {
	store  Store
	runner Runner
}

func New(st Store, r Runner) *Service {
	return &Service{store: st, runner: r}
}

// CreateTaskInput 建会话的入参。ModelSnapshot 是 api 层按当前配置算好的快照，
// 落库后就不再随配置变化——历史记录要能解释「当时用的哪个模型」。
type CreateTaskInput struct {
	ClientID       string
	Title          string
	Input          string
	ModelSnapshot  []byte
	IdempotencyKey *string
}

type CreateTaskOutput struct {
	Task domain.Task
	Run  domain.Run
	// Created 为 false 表示幂等命中，本次没有新建任何记录，也没有提交执行。
	Created bool
}

// CreateTask 建会话并起执行。
//
// 幂等命中时**不提交执行**：重放请求只该拿到原来的 id，重复跑一遍会重复烧 token，
// 也会让客户端收到两条 run.terminal。
func (s *Service) CreateTask(ctx context.Context, in CreateTaskInput) (CreateTaskOutput, error) {
	task, _, run, created, err := s.store.CreateTaskWithRun(ctx, store.CreateTaskParams{
		ClientID:       in.ClientID,
		Title:          in.Title,
		Input:          in.Input,
		ModelSnapshot:  in.ModelSnapshot,
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return CreateTaskOutput{}, err
	}
	if created {
		s.runner.Submit(run.ID)
	}
	return CreateTaskOutput{Task: task, Run: run, Created: created}, nil
}

type AppendTurnInput struct {
	TaskID         int64
	Input          string
	ModelSnapshot  []byte
	IdempotencyKey *string
}

type AppendTurnOutput struct {
	Turn    domain.Turn
	Run     domain.Run
	Created bool
}

// AppendTurn 追加一轮。规则 1（同一 Task 只有一个活跃 Run）由 store 在事务里守，
// 冲突时会带回当前活跃 Run 的状态供 api 映射成 409。
func (s *Service) AppendTurn(ctx context.Context, in AppendTurnInput) (AppendTurnOutput, error) {
	turn, run, created, err := s.store.AppendTurnWithRun(ctx, in.TaskID, in.Input, in.ModelSnapshot, in.IdempotencyKey)
	if err != nil {
		return AppendTurnOutput{}, err
	}
	if created {
		s.runner.Submit(run.ID)
	}
	return AppendTurnOutput{Turn: turn, Run: run, Created: created}, nil
}

// Cancel 取消一次执行。交给执行器做，是因为只有它知道这个 Run 有没有在跑的 goroutine。
func (s *Service) Cancel(ctx context.Context, runID int64) (domain.Run, error) {
	return s.runner.Cancel(ctx, runID)
}

// Retry 基于一个终态 Run 新建尝试。源 Run 不改：终态只能写一次。
func (s *Service) Retry(ctx context.Context, runID int64) (domain.Run, error) {
	run, err := s.store.CreateRetryRun(ctx, runID)
	if err != nil {
		return domain.Run{}, err
	}
	s.runner.Submit(run.ID)
	return run, nil
}
