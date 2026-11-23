import type {
  AppendTurnResponse,
  CreateTaskRequest,
  CreateTaskResponse,
  ModelListResponse,
  RetryRunResponse,
  RunEvent,
  RunEventListResponse,
  TaskDetail,
  TaskListResponse,
} from "./types";

// 同源相对路径：dev 由 vite proxy 转发，生产由 nginx 反代（05 第七节：不做 CORS）。
const BASE = import.meta.env.VITE_API_BASE ?? "";

// 身份由服务端签名的 HttpOnly Cookie 承载，不在 URL 或 localStorage 保存。

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    /** 原始错误体：409 里有 runId + status，调用方据此收敛到服务端状态 */
    readonly body: unknown = null,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Content-Type", "application/json");

  const resp = await fetch(BASE + path, { ...init, headers });
  const text = await resp.text();
  const body = text ? safeJSON(text) : null;

  if (!resp.ok) {
    throw new ApiError(
      resp.status,
      errorCode(body),
      errorMessage(body, resp.statusText),
      body,
    );
  }
  // 202 也可能没有响应体（cancel 就是），此时返回 null 由调用方忽略
  return body as T;
}

/**
 * 后端统一错误体是 `{"error": 中文摘要, "code": 稳定枚举}`：
 * code ∈ invalid_argument | not_found | conflict | rate_limited | internal。
 * 程序只认 code（文案会改，code 不改）；未知值一律当 unknown，不抛错。
 */
function errorCode(body: unknown): string {
  if (body && typeof body === "object") {
    const code = (body as Record<string, unknown>)["code"];
    if (typeof code === "string" && code) return code;
  }
  return "unknown";
}

function errorMessage(body: unknown, fallback: string): string {
  if (body && typeof body === "object") {
    const err = (body as Record<string, unknown>)["error"];
    if (typeof err === "string" && err) return err;
  }
  return fallback;
}

function safeJSON(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

export const api = {
  session: () => request<{ clientId: string }>("/api/session"),

  health: () => request<{ status: string; db?: string }>("/healthz"),

  /** UC-01：202 立即返回，不等执行。idempotencyKey 防连点重复建任务 */
  createTask: (req: CreateTaskRequest) =>
    request<CreateTaskResponse>("/api/tasks", {
      method: "POST",
      body: JSON.stringify(req),
    }),

  /**
   * UC-04：在原 Task 内追加一轮，产生新 Run。
   * model 必须显式带上：省略时后端会退回配置里的默认模型。
   */
  appendTurn: (
    taskId: number,
    input: string,
    model?: string,
    idempotencyKey?: string,
  ) =>
    request<AppendTurnResponse>(`/api/tasks/${taskId}/turns`, {
      method: "POST",
      body: JSON.stringify({ input, model, idempotencyKey }),
    }),

  /** UC-05：游标翻页，不用 offset（任务列表会持续新增） */
  listTasks: (cursor?: string, limit = 20) =>
    request<TaskListResponse>(
      `/api/tasks?limit=${limit}` +
        (cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""),
    ),

  /** UC-07 / UC-11：详情含每轮 Run 的 token、首 token 时间、错误分类 */
  getTask: (taskId: number) => request<TaskDetail>(`/api/tasks/${taskId}`),

  /** UC-08：取消当前活跃 Run；已终态返回 409，调用方自己收敛 */
  cancelRun: (runId: number) =>
    request<{ runId: number; status: string } | null>(
      `/api/runs/${runId}/cancel`,
      {
        method: "POST",
      },
    ),

  /** UC-09：重试产生新 Run，旧 Run 终态不改写 */
  retryRun: (runId: number) =>
    request<RetryRunResponse>(`/api/runs/${runId}/retry`, { method: "POST" }),

  /** UC-06：可用模型与能力声明；能力不匹配时后端明确报错，不静默降级 */
  listModels: () => request<ModelListResponse>("/api/models"),

  /**
   * UC-02 的批量读法：同一份事件、同一套 seq，但不建长连接。
   * 历史回放用它（N 轮并行 GET），SSE 只留给实时与续订阅。
   *
   * 分页是防御性的：单页默认 500（服务端上限 2000），hasMore 为真就接着拉。
   * 万一服务端 nextAfterSeq 不前进（不该发生）也停，别把浏览器转死。
   */
  listRunEvents: async (runId: number, afterSeq = 0): Promise<RunEvent[]> => {
    const all: RunEvent[] = [];
    let cursor = afterSeq;
    for (;;) {
      const page = await request<RunEventListResponse>(
        `/api/runs/${runId}/events?format=json&afterSeq=${cursor}&limit=500`,
      );
      all.push(...page.items);
      if (!page.hasMore || page.nextAfterSeq <= cursor) break;
      cursor = page.nextAfterSeq;
    }
    // 按 seq 排序：分页本身有序，但调用方可能并行拼接，排序成本远低于渲染错序
    return all.sort((a, b) => a.seq - b.seq);
  },
};
