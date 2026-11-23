import type { ErrorCategory, RunStatus } from "./types";

/**
 * 事件 payload 的 schema。**唯一来源是 backend/internal/domain/payload.go。**
 *
 * 上一版这里是「同时接受 text/delta/content 等多种拼写」的容错提取，那只是
 * 后端还没定字段时的临时代码。现在 Go 侧的 json tag 已经定稿，这里收紧成
 * 精确类型：字段改名时前端会立刻显示空白或类型错误，而不是默默猜错。
 *
 * 只保留一层防御：payload 不是对象时返回 null。这是网络/协议层的现实情况
 * （例如半截 JSON），不是对字段名的猜测。
 */

function asRecord(v: unknown): Record<string, unknown> | null {
  return v !== null && typeof v === "object"
    ? (v as Record<string, unknown>)
    : null;
}

function str(o: Record<string, unknown>, k: string): string | undefined {
  const v = o[k];
  return typeof v === "string" ? v : undefined;
}

function num(o: Record<string, unknown>, k: string): number | undefined {
  const v = o[k];
  return typeof v === "number" && Number.isFinite(v) ? v : undefined;
}

/** run.started */
export interface RunStartedPayload {
  taskId: number;
  turnId: number;
  model: string;
}

export function parseRunStarted(payload: unknown): RunStartedPayload | null {
  const o = asRecord(payload);
  if (!o) return null;
  return {
    taskId: num(o, "taskId") ?? 0,
    turnId: num(o, "turnId") ?? 0,
    model: str(o, "model") ?? "",
  };
}

/** turn.delta：补发时重建正文的唯一来源 */
export function parseDelta(payload: unknown): string {
  const o = asRecord(payload);
  if (!o) return typeof payload === "string" ? payload : "";
  return str(o, "delta") ?? "";
}

/** step.started：model 与 tool 互斥，step 是 Run 内的步骤序号（0 起，与后端 NextStepSeq 一致） */
export interface StepStartedPayload {
  step: number;
  type: "model_call" | "tool_call";
  model?: string;
  tool?: string;
}

export function parseStepStarted(payload: unknown): StepStartedPayload | null {
  const o = asRecord(payload);
  if (!o) return null;
  const step = num(o, "step");
  if (step === undefined) return null;
  return {
    step,
    type: str(o, "type") === "tool_call" ? "tool_call" : "model_call",
    model: str(o, "model"),
    tool: str(o, "tool"),
  };
}

/** step.finished：一次模型调用的耗时与 token */
export interface StepFinishedPayload {
  step: number;
  latencyMs: number;
  httpStatus?: number;
  inputTokens: number;
  outputTokens: number;
}

export function parseStepFinished(
  payload: unknown,
): StepFinishedPayload | null {
  const o = asRecord(payload);
  if (!o) return null;
  const step = num(o, "step");
  if (step === undefined) return null;
  return {
    step,
    latencyMs: num(o, "latencyMs") ?? 0,
    httpStatus: num(o, "httpStatus"),
    inputTokens: num(o, "inputTokens") ?? 0,
    outputTokens: num(o, "outputTokens") ?? 0,
  };
}

/**
 * tool.result：成功与失败都发。
 * input / result / error 都是**字符串**（后端把 JSON 参数序列化后裁剪过），不要再 JSON.parse。
 */
export interface ToolResultPayload {
  step: number;
  tool: string;
  callId: string;
  input?: string;
  result?: string;
  error?: string;
  durationMs: number;
}

export function parseToolResult(payload: unknown): ToolResultPayload | null {
  const o = asRecord(payload);
  if (!o) return null;
  const step = num(o, "step");
  if (step === undefined) return null;
  return {
    step,
    tool: str(o, "tool") ?? "tool",
    callId: str(o, "callId") ?? "",
    input: str(o, "input"),
    result: str(o, "result"),
    error: str(o, "error"),
    durationMs: num(o, "durationMs") ?? 0,
  };
}

/**
 * run.terminal：客户端收到它就关连接，不再重连。
 *
 * ttftMs 是**权威**首 token 时延（first_token_at - started_at，服务端落库值），
 * 有它就覆盖实时链路本地估的值；没首 token 的终态（首字前取消、连接直接失败、
 * 重启补的 interrupted）该字段整体不出现——「没测到」和「0ms」是两件事，
 * 所以这里保持 undefined，绝不 `?? 0`。
 */
export interface RunTerminalPayload {
  status: RunStatus;
  errorCategory?: ErrorCategory;
  errorMessage?: string;
  inputTokens: number;
  outputTokens: number;
  durationMs: number;
  ttftMs?: number;
}

const TERMINAL_STATUS: RunStatus[] = [
  "completed",
  "failed",
  "aborted",
  "interrupted",
];

export function parseTerminal(payload: unknown): RunTerminalPayload | null {
  const o = asRecord(payload);
  if (!o) return null;
  const raw = str(o, "status");
  if (!TERMINAL_STATUS.includes(raw as RunStatus)) return null;
  const status = raw as RunStatus;
  return {
    status,
    errorCategory: str(o, "errorCategory") as ErrorCategory | undefined,
    errorMessage: str(o, "errorMessage"),
    inputTokens: num(o, "inputTokens") ?? 0,
    outputTokens: num(o, "outputTokens") ?? 0,
    durationMs: num(o, "durationMs") ?? 0,
    ttftMs: num(o, "ttftMs"),
  };
}
