import type { EventType, StreamEvent } from "./types";

const BASE = import.meta.env.VITE_API_BASE ?? "";

/**
 * 服务端会命名的事件全集（backend/internal/domain/model.go 的 Event* 常量）。
 *
 * 这里必须逐个 addEventListener：后端把类型写在 `event:` 字段上，这类**命名事件
 * 不会触发 onmessage**。只写 onmessage 的话一条都收不到，而且不报错——是静默空流。
 */
const EVENT_TYPES: EventType[] = [
  "run.started",
  "turn.delta",
  "step.started",
  "step.finished",
  "tool.result",
  "run.terminal",
];

export interface StreamHandlers {
  onOpen?: () => void;
  onEvent: (ev: StreamEvent) => void;
  /** fatal=true 表示不会再重连（服务端拒绝或重试耗尽） */
  onError?: (err: Error, attempt: number, fatal: boolean) => void;
  onEnd?: (reason: "terminal" | "aborted" | "exhausted") => void;
}

export interface StreamOptions {
  /**
   * 从哪条之后开始收。省略时服务端从 0 开始，也就是**回放全量历史再转实时**。
   *
   * 这个默认值是刷新页面能重建正文的前提：刷新后浏览器是新建连接，没有上一帧
   * 的 id，只能从头补。所以「打开历史任务」和「新建后订阅」都不需要传参。
   */
  afterSeq?: number;
  /** 连续失败多少次就放弃（EventSource 自己会无限重连，得由我们叫停） */
  maxRetries?: number;
}

export interface StreamHandle {
  abort: () => void;
}

/**
 * 用原生 EventSource 订阅某个 Run 的事件流（UC-02 / UC-03）。
 *
 * 为什么能回到 EventSource：后端已经把三件事让出来了——
 *   - 身份使用 HttpOnly Cookie，不用自定义 header；
 *   - 续传位置走 `?afterSeq=` / `?lastEventId=`，且重连时浏览器自动带的
 *     `Last-Event-ID` 依然被识别（三路来源，header 优先）；
 *   - 终态 Run 会重放一帧 `run.terminal` 再关流，客户端收到就能 close() 收敛，
 *     不会留下每 3 秒一次的空重连。
 *
 * 仍然要做两件 EventSource 不负责的事：
 *   1. 收到 run.terminal 主动 close（否则浏览器会按 retry 继续重连）；
 *   2. 区分「正在重连」和「已经放弃」——两者在 onerror 里都只是一个事件。
 */
export function streamRun(
  runId: number,
  handlers: StreamHandlers,
  opts: StreamOptions = {},
): StreamHandle {
  const maxRetries = opts.maxRetries ?? 5;

  const params = new URLSearchParams();
  if (opts.afterSeq !== undefined)
    params.set("afterSeq", String(opts.afterSeq));
  const url = `${BASE}/api/runs/${runId}/events?${params.toString()}`;

  const es = new EventSource(url);
  let lastSeq = -1;
  let ended = false;
  let attempt = 0;

  const finish = (reason: "terminal" | "aborted" | "exhausted") => {
    if (ended) return;
    ended = true;
    es.close();
    handlers.onEnd?.(reason);
  };

  es.onopen = () => {
    // 连上就清零：重连成功说明链路恢复，不该把上一次的失败继续累加
    attempt = 0;
    handlers.onOpen?.();
  };

  const onEvent = (type: EventType) => (e: MessageEvent<string>) => {
    if (ended) return;
    attempt = 0;

    const seq = parseSeq(e.lastEventId, runId);
    // 补发与实时的交界处可能重复投递（hub 按 seq 去过重，这里再兜一层）
    if (
      type !== "run.terminal" &&
      seq !== null &&
      lastSeq >= 0 &&
      seq <= lastSeq
    )
      return;
    if (seq !== null) lastSeq = seq;

    let payload: unknown = null;
    if (typeof e.data === "string" && e.data.length > 0) {
      try {
        payload = JSON.parse(e.data);
      } catch {
        payload = e.data; // 半截 JSON：原样交给上层，至少事件面板里能看到
      }
    }

    handlers.onEvent({ runId, seq: seq ?? -1, type, payload });

    if (type === "run.terminal") finish("terminal");
  };

  for (const type of EVENT_TYPES) {
    es.addEventListener(type, onEvent(type) as EventListener);
  }

  es.onerror = () => {
    if (ended) return;

    // readyState 是这里唯一能拿到的诊断信息（onerror 不带 HTTP 状态码）：
    //   CONNECTING(0) —— 浏览器正按 retry 间隔重连，属于可恢复；
    //   CLOSED(2)     —— 服务端给了非 200 或非 text/event-stream，浏览器已放弃。
    if (es.readyState === EventSource.CLOSED) {
      finish("exhausted");
      handlers.onError?.(
        new Error("订阅被服务端拒绝（Run 不存在或响应不是 SSE），已停止重连"),
        0,
        true,
      );
      return;
    }

    attempt++;
    const fatal = attempt > maxRetries;
    handlers.onError?.(
      new Error(`连接中断，浏览器自动重连中（第 ${attempt} 次）`),
      attempt,
      fatal,
    );
    if (fatal) finish("exhausted");
  };

  return {
    abort: () => finish("aborted"),
  };
}

/**
 * 解析 `id: {runId}:{seq}`。
 *
 * 必须校验 runId：只认 seq 的话，客户端换了 Run 还带着旧 seq，服务端会补到
 * 另一个 Run 的事件上（串流）。对不上就当作「没有位置」，让服务端从头补。
 *
 * 返回 null 的情况包括：首帧没有 id（服务端重放终态时的保底帧故意不带 id）。
 */
function parseSeq(lastEventId: string, runId: number): number | null {
  if (!lastEventId) return null;
  const sep = lastEventId.lastIndexOf(":");
  if (sep < 0) return null;

  const rid = Number(lastEventId.slice(0, sep));
  const seq = Number(lastEventId.slice(sep + 1));
  if (!Number.isInteger(seq) || seq < 0) return null;
  if (!Number.isInteger(rid) || rid !== runId) return null;
  return seq;
}
