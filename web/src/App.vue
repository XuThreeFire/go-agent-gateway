<script setup lang="ts">
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  watch,
} from "vue";
import { api } from "./api/client";
import type { ModelInfo } from "./api/types";
import AppSidebar from "./components/AppSidebar.vue";
import BrandMark from "./components/BrandMark.vue";
import Composer from "./components/Composer.vue";
import EventLog from "./components/EventLog.vue";
import MessageList from "./components/MessageList.vue";
import { useSession } from "./composables/useSession";

const {
  tasks,
  hasMore,
  tasksLoading,
  loadTasks,
  taskId,
  messages,
  notice,
  eventLog,
  isStreaming,
  connected,
  openTask,
  createTask,
  continueTask,
  cancel,
  retry,
  stopStream,
  resetSession,
  submitting,
} = useSession();

const models = ref<ModelInfo[]>([]);
const health = ref<"checking" | "ok" | "fail">("checking");
const showEvents = ref(new URLSearchParams(location.search).has("debug"));
const scroller = ref<HTMLElement | null>(null);
const composer = ref<InstanceType<typeof Composer> | null>(null);

const lastAssistant = computed(() =>
  [...messages.value].reverse().find((m) => m.role === "assistant"),
);
const canRetry = computed(
  () =>
    ["failed", "interrupted", "aborted"].includes(
      lastAssistant.value?.status ?? "",
    ) && !isStreaming.value,
);

onMounted(async () => {
  try {
    await api.session();
  } catch {
    health.value = "fail";
    notice.value = "无法建立会话，请检查访问密码或服务状态";
    return;
  }
  void loadModels();
  void loadTasks(true);
  try {
    const r = await api.health();
    health.value = r.status === "ok" ? "ok" : "fail";
  } catch {
    health.value = "fail";
  }

  // 支持 ?task=123 直接打开某个会话，方便刷新后回到原处、也方便贴给别人看
  const initial = Number(new URLSearchParams(location.search).get("task"));
  if (Number.isInteger(initial) && initial > 0) void openTask(initial);
});

onBeforeUnmount(stopStream);

async function loadModels() {
  try {
    const resp = await api.listModels();
    models.value = resp.items;
  } catch {
    // 模型清单拉不到时不阻塞使用，Composer 会退化为手填模型名
    models.value = [];
  }
}

function newChat() {
  resetSession();
  void nextTick(() => composer.value?.focus());
}

function selectTask(id: number) {
  void openTask(id);
}

async function onSend(text: string, model: string) {
  if (taskId.value === null) {
    await createTask(model, text);
  } else {
    await continueTask(text, model);
  }
  scrollToBottom();
}

function scrollToBottom() {
  void nextTick(() => {
    const el = scroller.value;
    if (el) el.scrollTop = el.scrollHeight;
  });
}

// 当前会话写进 URL：刷新/复制链接都能回到同一个 task（UC-03 的另一半）。
// 用 replaceState 而不是 pushState：切换会话不该在浏览器历史里堆一堆条目。
watch(taskId, (id) => {
  const url = new URL(location.href);
  if (id === null) url.searchParams.delete("task");
  else url.searchParams.set("task", String(id));
  history.replaceState(null, "", url);
});

// 流式过程中跟随到底部；只依赖长度，避免深监听整棵消息树
watch(
  () =>
    messages.value.length +
    (messages.value[messages.value.length - 1]?.text.length ?? 0),
  scrollToBottom,
);
</script>

<template>
  <div class="app">
    <AppSidebar
      :tasks="tasks"
      :active-id="taskId"
      :loading="tasksLoading"
      :has-more="hasMore"
      @select="selectTask"
      @create="newChat"
      @more="loadTasks(false)"
    />

    <section class="main">
      <header class="topbar">
        <div class="right">
          <span class="status-pill" :class="health">
            <span class="dot" :class="health" />
            <span>
              {{
                health === "ok"
                  ? "服务正常"
                  : health === "checking"
                    ? "检查中"
                    : "服务不可用"
              }}
            </span>
          </span>
          <button class="ghost" @click="showEvents = !showEvents">
            <svg
              width="15"
              height="15"
              viewBox="0 0 16 16"
              fill="none"
              aria-hidden="true"
            >
              <path
                d="M2.5 4.5h6M2.5 8h9M2.5 11.5h5"
                stroke="currentColor"
                stroke-width="1.4"
                stroke-linecap="round"
              />
              <circle cx="12" cy="4.5" r="1.25" fill="currentColor" />
              <circle cx="13.5" cy="11.5" r="1.25" fill="currentColor" />
            </svg>
            <span>{{ showEvents ? "隐藏事件流" : "事件流" }}</span>
          </button>
        </div>
      </header>

      <main class="chat" :class="{ empty: !messages.length }">
        <div ref="scroller" class="scroll">
          <div v-if="!messages.length" class="welcome">
            <BrandMark :size="52" class="welcome-mark" />
            <h1>你好，让我们开始聊天吧</h1>
            <p>服务端独立执行 agent 循环，页面只是观察窗口。</p>
            <p v-if="models.some((m) => m.provider === 'mock')">
              模拟模式无需密钥。试试「计算 (12+3)*4」或「现在的时间」。
            </p>
            <div class="welcome-meta">断线可续 · 取消真停止 · 全程可追踪</div>
          </div>

          <MessageList v-else :messages="messages" />
        </div>

        <div v-if="notice || canRetry" class="footer-bar">
          <span v-if="notice" class="notice">{{ notice }}</span>
          <button v-if="canRetry" class="retry" @click="retry">
            重试这一轮
          </button>
        </div>

        <Composer
          ref="composer"
          :models="models"
          :streaming="isStreaming"
          :disabled="submitting"
          @send="onSend"
          @cancel="cancel"
        />
      </main>
    </section>

    <EventLog v-if="showEvents" :events="eventLog" :connected="connected" />
  </div>
</template>

<style scoped>
.app {
  height: 100vh;
  display: grid;
  grid-template-columns: 260px minmax(0, 1fr);
  overflow: hidden;
  background: var(--bg);
}

.main {
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
}

.topbar {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  flex: none;
  height: 54px;
  padding: 0 22px;
  background: var(--bg);
}

.right {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 12.5px;
  color: var(--muted);
}

.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  height: 30px;
  padding: 0 11px;
  border: 1px solid transparent;
  border-radius: 999px;
  color: var(--muted-strong);
  transition:
    background 0.16s ease,
    border-color 0.16s ease;
}

.status-pill:hover {
  border-color: var(--border);
  background: #fafafa;
}

.dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #c9ced6;
}

.dot.ok {
  background: var(--ok);
}

.dot.fail {
  background: var(--err);
}

.ghost {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 30px;
  padding: 0 11px;
  border: 1px solid transparent;
  border-radius: 999px;
  background: transparent;
  color: var(--muted-strong);
  font-size: 12.5px;
  cursor: pointer;
  transition:
    background 0.16s ease,
    border-color 0.16s ease,
    color 0.16s ease;
}

.ghost:hover {
  border-color: var(--border);
  background: #fafafa;
  color: var(--text);
}

.chat {
  display: grid;
  grid-template-rows: 1fr auto auto;
  flex: 1;
  min-height: 0;
  background: var(--bg);
}

.chat.empty {
  display: flex;
  flex-direction: column;
  justify-content: center;
}

.scroll {
  overflow-y: auto;
  padding: 0 28px;
}

.chat.empty .scroll {
  flex: none;
  overflow: visible;
  padding: 0 24px;
}

.welcome {
  width: 100%;
  max-width: 720px;
  margin: 0 auto;
  padding: 0 4px 28px;
  text-align: center;
}

.welcome-mark {
  margin: 0 auto 18px;
}

.welcome h1 {
  margin: 0 0 10px;
  font-size: 26px;
  font-weight: 500;
  letter-spacing: -0.02em;
  color: var(--text);
}

.welcome p {
  margin: 0;
  color: var(--muted-strong);
  font-size: 14px;
  line-height: 1.7;
}

.welcome-meta {
  margin-top: 10px;
  color: var(--muted);
  font-size: 12px;
  letter-spacing: 0.01em;
}

.footer-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
  max-width: 760px;
  margin: 0 auto;
  padding: 0 24px 8px;
  font-size: 12.5px;
}

.notice {
  color: #8a6100;
}

.retry {
  margin-left: auto;
  height: 30px;
  padding: 0 13px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--panel);
  color: var(--text);
  font-size: 12.5px;
  cursor: pointer;
}

.retry:hover {
  border-color: var(--border-strong);
}

@media (max-width: 800px) {
  .app {
    grid-template-columns: 1fr;
  }

  .app > aside {
    display: none;
  }

  .topbar {
    padding: 0 14px;
  }

  .welcome h1 {
    font-size: 22px;
  }

  .scroll {
    padding: 0 16px;
  }

  .chat.empty .scroll {
    padding: 0 16px;
  }
}
</style>
