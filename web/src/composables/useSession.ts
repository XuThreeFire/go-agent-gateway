import { computed, ref } from "vue";
import { ApiError, api } from "../api/client";
import { streamRun, type StreamHandle } from "../api/events";
import {
  parseDelta,
  parseRunStarted,
  parseStepFinished,
  parseStepStarted,
  parseTerminal,
  parseToolResult,
} from "../api/payload";
import type {
  RunStatus,
  StreamEvent,
  TaskDetail,
  TaskSummary,
} from "../api/types";
import { ttftOf } from "../api/types";

export interface StepLog {
  id: string;
  /** Run 内的步骤序号（后端 step 字段，0 起：COALESCE(MAX(seq),-1)+1）。按它匹配，不靠「最后一个未完成」猜 */
  step?: number;
  kind: "model" | "tool";
  label: string;
  detail?: string;
  latencyMs?: number;
  tokens?: string;
  done: boolean;
}

export interface ChatMessage {
  id: string;
  role: "user" | "assistant";
  text: string;
  steps: StepLog[];
  runId?: number;
  /** 本次执行用的模型，来自 run.started（run 行里存的是快照，事件里带对外名） */
  model?: string;
  status?: RunStatus;
  ttftMs?: number;
  inputTokens?: number;
  outputTokens?: number;
  errorCategory?: string;
  errorMessage?: string;
  /** 正在接收（实时或回放中） */
  active?: boolean;
}

function uid(): string {
  return crypto.randomUUID();
}

/**
 * 实时帧（SSE）与批量读（?format=json）共有的形状。
 * 渲染只关心 seq/type/payload，runId 是连接层的事——所以两者能走同一段逻辑。
 */
type EventLike = Pick<StreamEvent, "seq" | "type" | "payload">;

export function useSession() {
  const tasks = ref<TaskSummary[]>([]);
  const nextCursor = ref<string | null>(null);
  const hasMore = ref(false);
  const tasksLoading = ref(false);

  const taskId = ref<number | null>(null);
  const messages = ref<ChatMessage[]>([]);
  const activeRunId = ref<number | null>(null);
  const activeStatus = ref<RunStatus | null>(null);
  const connected = ref(false);
  const notice = ref("");

  /** 调试用：最近收到的原始事件，便于核对 seq 与 payload */
  const eventLog = ref<{ seq: number; type: string; raw: string }[]>([]);

  let handle: StreamHandle | null = null;
  let revision = 0;
  const submitting = ref(false);

  const isStreaming = computed(
    () => activeStatus.value === "pending" || activeStatus.value === "running",
  );

  const canCancel = computed(
    () => isStreaming.value && activeRunId.value !== null,
  );

  // ---------- 任务列表 ----------

  async function loadTasks(reset = true) {
    if (tasksLoading.value) return;
    tasksLoading.value = true;
    try {
      const page = await api.listTasks(
        reset ? undefined : (nextCursor.value ?? undefined),
      );
      tasks.value = reset ? page.items : [...tasks.value, ...page.items];
      nextCursor.value = page.nextCursor ?? null;
      hasMore.value = page.hasMore;
    } catch (e) {
      notice.value = describe(e);
    } finally {
      tasksLoading.value = false;
    }
  }

  // ---------- 会话 ----------

  async function openTask(id: number) {
    const version = ++revision;
    activeRunId.value = null;
    stopStream();
    taskId.value = id;
    messages.value = [];
    eventLog.value = [];
    notice.value = "";
    activeStatus.value = null;

    let detail: TaskDetail;
    try {
      detail = await api.getTask(id);
      if (version !== revision) return;
    } catch (e) {
      if (version === revision) notice.value = describe(e);
      return;
    }

    // 助手正文只存在于事件流里（turn 表只有 user_input），所以逐轮回放。
    // runs 是平铺在顶层的，按 turnId 归组；同一轮有多次尝试时只展示最新那次。
    const jobs = detail.turns.map((turn) => ({
      turn,
      run: detail.runs
        .filter((r) => r.turnId === turn.id)
        .sort((a, b) => b.attemptNo - a.attemptNo)[0],
    }));

    // 回放走批量读（?format=json）：N 轮并行 GET，渲染顺序仍按 turn 走。
    // 之前逐轮开 SSE，打开 10 轮会话就是 10 次串行建连 + 10 次全量补发。
    const pages = await Promise.all(
      jobs.map(async (j) => {
        if (!j.run) return [];
        try {
          return await api.listRunEvents(j.run.id);
        } catch (e) {
          if (version === revision) notice.value = describe(e);
          return [];
        }
      }),
    );

    if (version !== revision) return;
    let lastMsg: ChatMessage | null = null;
    jobs.forEach((j, i) => {
      pushMessage({
        id: uid(),
        role: "user",
        text: j.turn.userInput,
        steps: [],
      });
      if (!j.run) return;

      const msg = pushMessage({
        id: uid(),
        role: "assistant",
        text: "",
        steps: [],
        runId: j.run.id,
        status: j.run.status,
        // 落库值兜底：老 Run 的终态帧里没有 ttftMs，这里也能显示
        ttftMs: ttftOf(j.run),
        inputTokens: j.run.inputTokens,
        outputTokens: j.run.outputTokens,
        errorCategory: j.run.errorCategory,
        errorMessage: j.run.errorMessage,
        active: true,
      });

      for (const ev of pages[i] ?? []) {
        pushEventLog(ev);
        applyEvent(msg, ev);
      }
      msg.active = false;
      lastMsg = msg;
    });

    // 刷新时最后一轮可能还在跑：批量读只拿到已经产生的部分，剩下的用 SSE 续订阅。
    // afterSeq 必须带上，否则服务端从 0 重放，正文会渲染两遍。
    const lastRun = jobs[jobs.length - 1]?.run;
    if (lastRun && lastMsg && isRunning(lastRun.status)) {
      const afterSeq = (pages[pages.length - 1] ?? []).reduce(
        (m, ev) => Math.max(m, ev.seq),
        0,
      );
      attach(lastRun.id, lastMsg, afterSeq);
    }
  }

  async function createTask(model: string, input: string) {
    if (submitting.value) return;
    submitting.value = true;
    const version = revision;
    notice.value = "";
    try {
      const resp = await api.createTask({
        model,
        input,
        idempotencyKey: uid(),
      });
      if (version !== revision) return;
      taskId.value = resp.taskId;
      messages.value = [];
      pushMessage({ id: uid(), role: "user", text: input, steps: [] });
      const msg = pushMessage(newAssistant(resp.runId));
      await loadTasks(true);
      if (version === revision) attach(resp.runId, msg);
    } catch (e) {
      if (version === revision) notice.value = describe(e);
    } finally {
      submitting.value = false;
    }
  }

  async function continueTask(input: string, model: string) {
    if (taskId.value === null || submitting.value) return;
    submitting.value = true;
    const version = revision;
    notice.value = "";
    try {
      // 必须带上 model：不传时后端会退回配置里的默认模型，
      // 用户在下拉框里选的模型会在第二轮之后被悄悄换掉。
      const resp = await api.appendTurn(taskId.value, input, model, uid());
      if (version !== revision) return;
      pushMessage({ id: uid(), role: "user", text: input, steps: [] });
      const msg = pushMessage(newAssistant(resp.runId));
      attach(resp.runId, msg);
      await loadTasks(true);
    } catch (e) {
      if (version === revision) notice.value = describe(e);
    } finally {
      submitting.value = false;
    }
  }

  async function cancel() {
    if (!activeRunId.value) return;
    const version = revision;
    try {
      await api.cancelRun(activeRunId.value);
      if (version !== revision) return;
      // 不立即改状态：等 run.terminal(aborted) 到达，避免与执行器竞态
    } catch (e) {
      if (version !== revision) return;
      if (e instanceof ApiError && e.code === "conflict") {
        // 服务端已经收敛（已终态 / 已有别的活跃 Run）：409 里带 runId + status，
        // 按它落状态，不自己猜「取消成功没有」。
        const st = runStatusOf(e.body);
        const runId = activeRunId.value;
        const last = [...messages.value]
          .reverse()
          .find((m) => m.role === "assistant" && m.runId === runId);
        if (st && last) {
          last.status = st;
          last.active = false;
          for (const s of last.steps) s.done = true;
        }
        stopStream();
        activeRunId.value = null;
        if (st) activeStatus.value = st;
        notice.value = "这一轮已有终态，无需取消";
        void loadTasks(true);
      } else {
        notice.value = describe(e);
      }
    }
  }

  async function retry() {
    if (submitting.value) return;
    const version = revision;
    const last = [...messages.value]
      .reverse()
      .find((m) => m.role === "assistant" && m.runId);
    if (!last?.runId) return;
    submitting.value = true;
    try {
      const resp = await api.retryRun(last.runId);
      if (version !== revision) return;
      const msg = pushMessage(newAssistant(resp.runId));
      attach(resp.runId, msg);
    } catch (e) {
      if (version === revision) notice.value = describe(e);
    } finally {
      submitting.value = false;
    }
  }

  // ---------- 事件流 ----------

  /**
   * push 之后必须用数组里返回的那个**代理对象**继续写，不要用 push 之前的原始对象。
   * Vue 只跟踪经过 proxy 的读写：直接改原始对象不报错、也不触发重渲染，
   * 表现就是界面停在初始状态（例如一直显示「等待首个 token」）。这个坑很隐蔽。
   */
  function pushMessage(m: ChatMessage): ChatMessage {
    messages.value.push(m);
    return messages.value[messages.value.length - 1];
  }

  function newAssistant(runId: number): ChatMessage {
    return {
      id: uid(),
      role: "assistant",
      text: "",
      steps: [],
      runId,
      status: "pending",
      active: true,
    };
  }

  /**
   * 订阅当前 Run（SSE 只走实时路径）。
   *
   * 新建时**不传 afterSeq**：服务端默认从 0「先补库、再转实时」，比前端算起点更稳——
   * POST 返回 202 时执行器已经开跑，等连接建好前几条事件可能已落库，从 0 补能一条不漏，
   * 重复部分由 seq 去重。
   *
   * 历史回放只在最后一轮还在跑时带 afterSeq 续订阅：那时已有的部分已经批量读渲染过，
   * 再从 0 补发会把正文渲染两遍。
   */
  function attach(runId: number, msg: ChatMessage, afterSeq?: number) {
    stopStream();
    activeRunId.value = runId;
    activeStatus.value = msg.status === "running" ? "running" : "pending";
    handle = streamRun(
      runId,
      handlersFor(msg),
      afterSeq !== undefined ? { afterSeq } : {},
    );
  }

  /** 实时订阅的事件处理：写消息 + 维护连接与列表状态 */
  function handlersFor(msg: ChatMessage) {
    const startedAt = Date.now();
    let gotFirstDelta = false;

    return {
      onOpen: () => {
        connected.value = true;
      },
      onEvent: (ev: StreamEvent) => {
        pushEventLog(ev);
        applyEvent(msg, ev, () => {
          if (gotFirstDelta) return;
          gotFirstDelta = true;
          // 实时流只能本地估：订阅时刻 → 首字到达，含建连与补发开销，会偏大。
          // 终态帧里的 ttftMs 是服务端落库值，到了就覆盖它。
          if (msg.ttftMs === undefined) msg.ttftMs = Date.now() - startedAt;
        });

        if (ev.type === "run.terminal") {
          activeStatus.value = msg.status ?? "completed";
          activeRunId.value = null;
          connected.value = false;
          void loadTasks(true);
        }
      },
      onError: (err: Error, _attempt: number, fatal: boolean) => {
        connected.value = false;
        notice.value = fatal
          ? err.message
          : `连接中断，正在重连：${err.message}`;
      },
    };
  }

  /**
   * 把单条事件写进消息。**实时与回放共用同一段逻辑**，
   * 否则「刷新前」和「刷新后」的渲染结果会慢慢走偏。
   *
   * 只改消息本身；连接状态（activeStatus / activeRunId / loadTasks）留在 handlersFor。
   */
  function applyEvent(
    msg: ChatMessage,
    ev: EventLike,
    onFirstDelta?: () => void,
  ) {
    switch (ev.type) {
      case "run.started": {
        const p = parseRunStarted(ev.payload);
        msg.status = "running";
        if (p?.model) msg.model = p.model;
        break;
      }
      case "turn.delta": {
        const piece = parseDelta(ev.payload);
        onFirstDelta?.();
        if (piece) msg.text += piece;
        break;
      }
      case "step.started": {
        const p = parseStepStarted(ev.payload);
        if (!p) break;
        const kind = p.type === "tool_call" ? "tool" : "model";
        msg.steps.push({
          id: `s${p.step}`,
          step: p.step,
          kind,
          label:
            kind === "tool"
              ? (p.tool ?? "工具调用")
              : `模型调用 · ${p.model ?? ""}`.trim(),
          done: false,
        });
        break;
      }
      case "step.finished": {
        const p = parseStepFinished(ev.payload);
        if (!p) break;
        // 按 step 号精确匹配：`step.started` 已建过卡，这里只补结果
        const step = msg.steps.find((s) => s.step === p.step);
        if (!step) break;
        step.done = true;
        step.latencyMs = p.latencyMs;
        step.tokens = `in ${p.inputTokens} / out ${p.outputTokens}`;
        break;
      }
      case "tool.result": {
        const p = parseToolResult(ev.payload);
        if (!p) break;
        const existing = msg.steps.find((s) => s.step === p.step);
        const step: StepLog = existing ?? {
          id: `s${p.step}`,
          step: p.step,
          kind: "tool",
          label: p.tool,
          done: false,
        };
        step.label = p.tool;
        step.detail = formatToolDetail(p);
        step.latencyMs = p.durationMs;
        step.done = true;
        if (!existing) msg.steps.push(step);
        break;
      }
      case "run.terminal": {
        const p = parseTerminal(ev.payload);
        if (!p) break;
        msg.status = p.status;
        msg.active = false;
        // 取消 / 失败可能停在某个 step 中间，别让它一直显示「运行中」
        for (const s of msg.steps) s.done = true;
        msg.inputTokens = p.inputTokens;
        msg.outputTokens = p.outputTokens;
        msg.errorCategory = p.errorCategory;
        msg.errorMessage = p.errorMessage;
        // 权威 TTFT。没有首 token 时字段整体不出现，保持 undefined——
        // 兜底成 0 会让界面显示「TTFT 0ms」，那是另一件事。
        if (p.ttftMs !== undefined) msg.ttftMs = p.ttftMs;
        break;
      }
    }
  }

  function pushEventLog(ev: EventLike) {
    eventLog.value.push({
      seq: ev.seq,
      type: ev.type,
      raw:
        typeof ev.payload === "string"
          ? ev.payload
          : JSON.stringify(ev.payload),
    });
    if (eventLog.value.length > 200) eventLog.value.shift();
  }

  function stopStream() {
    handle?.abort();
    handle = null;
    connected.value = false;
  }

  function resetSession() {
    revision++;
    stopStream();
    taskId.value = null;
    messages.value = [];
    eventLog.value = [];
    notice.value = "";
    activeRunId.value = null;
    activeStatus.value = null;
  }

  return {
    submitting,
    resetSession,
    // 列表
    tasks,
    hasMore,
    tasksLoading,
    loadTasks,
    // 会话
    taskId,
    messages,
    notice,
    eventLog,
    isStreaming,
    canCancel,
    connected,
    activeRunId,
    activeStatus,
    openTask,
    createTask,
    continueTask,
    cancel,
    retry,
    stopStream,
  };
}

/** tool.result 的 input / result / error 都是字符串（后端已把 JSON 参数序列化并截断） */
function formatToolDetail(p: {
  input?: string;
  result?: string;
  error?: string;
}): string | undefined {
  const lines: string[] = [];
  if (p.input) lines.push(`入参: ${p.input}`);
  if (p.result) lines.push(`结果: ${p.result}`);
  if (p.error) lines.push(`错误: ${p.error}`);
  return lines.length ? lines.join("\n") : undefined;
}

function describe(e: unknown): string {
  if (e instanceof ApiError) {
    // 只看稳定 code，不解析中文文案（文案会改，code 不改）
    const hint =
      e.code === "conflict"
        ? "（已有活跃 Run 或已终态，以服务端状态为准）"
        : e.code === "rate_limited"
          ? "（触发限流，稍后重试）"
          : e.code === "not_found"
            ? "（资源不存在）"
            : e.code === "invalid_argument"
              ? "（请求参数不合法）"
              : "";
    return `${e.status} ${e.message}${hint}`;
  }
  return e instanceof Error ? e.message : String(e);
}

function isRunning(s: RunStatus | undefined): boolean {
  return s === "pending" || s === "running";
}

const STATUSES: RunStatus[] = [
  "pending",
  "running",
  "completed",
  "failed",
  "aborted",
  "interrupted",
];

/** 从 409 的响应体里取 run 状态：body.status 是服务端当前真相 */
function runStatusOf(body: unknown): RunStatus | undefined {
  if (!body || typeof body !== "object") return undefined;
  const s = (body as Record<string, unknown>)["status"];
  return typeof s === "string" && STATUSES.includes(s as RunStatus)
    ? (s as RunStatus)
    : undefined;
}
