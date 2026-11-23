// 接口契约的唯一来源：
//   - 事件 payload：backend/internal/domain/payload.go（字段名的唯一真相）
//   - 接口清单与语义：docs/05-详细设计.md「一、接口清单」「六、SSE handler」§2.1
//   - HTTP 响应体形状：backend/internal/api/api.go
// 改字段必须同时改后端，否则前端会静默错位——这类 bug 不报错，只是显示空白。

export type RunStatus =
  | 'pending'
  | 'running'
  | 'completed'
  | 'failed'
  | 'aborted'
  | 'interrupted'

/**
 * SSE 的 `event:` 字段取值，对应 backend/internal/domain/model.go 的 Event* 常量。
 *
 * 注意：服务端的「心跳」是一条 `: ping` 注释帧，不会触发任何事件回调，
 * 所以这里不列 heartbeat——列了也是永远收不到的死代码。
 */
export type EventType =
  | 'run.started'
  | 'turn.delta'
  | 'step.started'
  | 'step.finished'
  | 'tool.result'
  | 'run.terminal'

export type ErrorCategory =
  | 'rate_limit'
  | 'timeout'
  | 'protocol'
  | 'tool_error'
  | 'quota_exceeded'

export interface ModelInfo {
  name: string
  provider: string
  supportsToolCall: boolean
  contextWindow: number
}

/** GET /api/models：`{ items, default }`，不是裸数组 */
export interface ModelListResponse {
  items: ModelInfo[]
  /** 客户端不传 model 时服务端用的名字 */
  default: string
}

export interface TaskSummary {
  id: number
  clientId: string
  title: string
  createdAt: string
  updatedAt: string
  /**
   * 本会话最近一次 Run（重试多次时取 id 最大那次）。后端 omitempty：
   * 从来没跑过的会话两个字段都不出现，所以侧栏要区分「没记录」与「运行中」。
   */
  lastRunId?: number
  lastRunStatus?: RunStatus
}

/** GET /api/tasks：游标翻页（不用 offset，列表会持续新增） */
export interface TaskListResponse {
  items: TaskSummary[]
  hasMore: boolean
  /** 后端 omitempty：没有下一页时这个字段直接不出现，不是 null */
  nextCursor?: string
}

export interface RunSummary {
  id: number
  turnId: number
  attemptNo: number
  status: RunStatus
  errorCategory?: ErrorCategory
  errorMessage?: string
  inputTokens: number
  outputTokens: number
  startedAt?: string
  firstTokenAt?: string
  endedAt?: string
  createdAt: string
}

export interface TurnItem {
  id: number
  seq: number
  userInput: string
  createdAt: string
}

/**
 * GET /api/tasks/{taskId}。
 *
 * 两个容易踩的结构点：
 *   - task 单独一层，不是顶层平铺；
 *   - runs 平铺在顶层，**不嵌在 turn 里**，要用 turnId 自己归组。
 */
export interface TaskDetail {
  task: TaskSummary
  turns: TurnItem[]
  runs: RunSummary[]
  usage: { inputTokens: number; outputTokens: number }
}

export interface CreateTaskRequest {
  model: string
  input: string
  /** 同一 Task 内重复提交返回同一个 runId（后端 uk_task_idem 保证） */
  idempotencyKey?: string
}

export interface CreateTaskResponse {
  taskId: number
  runId: number
}

export interface AppendTurnResponse {
  turnId: number
  runId: number
}

/** POST /api/runs/{runId}/retry：新 Run 的 id 在 runId 上，没有 newRunId 这个字段 */
export interface RetryRunResponse {
  runId: number
  sourceRunId: number
  attemptNo: number
}

export interface ToolSpec {
  name: string
  description: string
  parameters: unknown
}

export interface ToolListResponse {
  items: ToolSpec[]
}

/**
 * 一条 SSE 事件的统一形式。
 *
 * 线上格式：`id: {runId}:{seq}` + `event: {type}` + `data: {payload 的 JSON}`。
 * EventSource 已经把信封拆好了，这里拼回一个对象方便消费。
 */
export interface StreamEvent {
  runId: number
  seq: number
  type: EventType
  payload: unknown
}

/**
 * `GET /api/runs/{runId}/events?format=json` 的单条事件。
 * 与 SSE 同一张 event 表、同一套 seq，只是不建长连接。
 *
 * payload 已经是 **JSON 对象**（后端用 json.RawMessage 直出），别再 JSON.parse；
 * 只有库里有脏行时后端才降级成字符串，所以类型仍是 unknown。
 */
export interface RunEvent {
  seq: number
  type: EventType
  payload: unknown
}

/** 故意不是裸数组：hasMore 用来发现截断，nextAfterSeq 是下一页的起点 */
export interface RunEventListResponse {
  items: RunEvent[]
  hasMore: boolean
  nextAfterSeq: number
}

/** TTFT = firstTokenAt - startedAt。只在两个时间都在时成立，否则交给实时流自己测 */
export function ttftOf(run: RunSummary): number | undefined {
  if (!run.startedAt || !run.firstTokenAt) return undefined
  const ms = Date.parse(run.firstTokenAt) - Date.parse(run.startedAt)
  return Number.isFinite(ms) && ms >= 0 ? ms : undefined
}
