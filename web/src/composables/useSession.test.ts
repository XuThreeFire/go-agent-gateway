import { beforeEach, describe, expect, it, vi } from "vitest";
const { api, streamRun } = vi.hoisted(() => ({
  api: {
    getTask: vi.fn(),
    listTasks: vi.fn(),
    listRunEvents: vi.fn(),
    createTask: vi.fn(),
    appendTurn: vi.fn(),
  },
  streamRun: vi.fn(),
}));
vi.mock("../api/client", () => ({ api, ApiError: class extends Error {} }));
vi.mock("../api/events", () => ({ streamRun }));
import { useSession } from "./useSession";
describe("会话切换", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    api.listTasks.mockResolvedValue({ items: [], hasMore: false });
    api.listRunEvents.mockResolvedValue([]);
    streamRun.mockReturnValue({ abort: vi.fn() });
  });
  it("忽略切换前发出的旧详情响应", async () => {
    let resolveOld!: (v: unknown) => void;
    api.getTask.mockImplementation((id: number) =>
      id === 1
        ? new Promise((resolve) => {
            resolveOld = resolve;
          })
        : Promise.resolve({ turns: [], runs: [] }),
    );
    const session = useSession();
    const old = session.openTask(1);
    await session.openTask(2);
    resolveOld({ turns: [{ id: 11, userInput: "旧会话" }], runs: [] });
    await old;
    expect(session.taskId.value).toBe(2);
    expect(session.messages.value).toEqual([]);
  });
  it("新建会话清除旧执行状态", () => {
    const session = useSession();
    session.activeStatus.value = "running";
    session.activeRunId.value = 4;
    session.resetSession();
    expect(session.isStreaming.value).toBe(false);
    expect(session.activeRunId.value).toBeNull();
  });
  it("发送期间阻止重复提交", async () => {
    let resolve!: (v: unknown) => void;
    api.createTask.mockReturnValue(
      new Promise((r) => {
        resolve = r;
      }),
    );
    const session = useSession();
    const first = session.createTask("demo", "hi");
    await session.createTask("demo", "hi");
    expect(api.createTask).toHaveBeenCalledTimes(1);
    resolve({ taskId: 1, runId: 2 });
    await first;
    expect(session.submitting.value).toBe(false);
  });
});
