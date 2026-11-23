<script setup lang="ts">
import type { ChatMessage } from '../composables/useSession'
import { renderMarkdown } from '../utils/markdown'
import BrandMark from './BrandMark.vue'

defineProps<{ messages: ChatMessage[] }>()

const STATUS_LABEL: Record<string, string> = {
  pending: '排队中',
  running: '生成中',
  completed: '完成',
  failed: '失败',
  aborted: '已取消',
  interrupted: '已中断',
}

const ERROR_LABEL: Record<string, string> = {
  rate_limit: '上游限流',
  timeout: '上游超时',
  protocol: '协议错误',
  tool_error: '工具错误',
  quota_exceeded: '配额超限',
}
</script>

<template>
  <div class="messages">
    <div v-for="m in messages" :key="m.id" class="row" :class="m.role">
      <div v-if="m.role === 'user'" class="bubble user">{{ m.text }}</div>

      <template v-else>
        <BrandMark :size="26" class="avatar" />
        <div class="assistant-body">
          <ul v-if="m.steps.length" class="steps">
            <li v-for="s in m.steps" :key="s.id" :class="s.kind">
              <div class="step-head">
                <span class="label">{{ s.label }}</span>
                <span v-if="!s.done" class="running">运行中…</span>
                <span v-if="s.latencyMs !== undefined" class="dim">{{ s.latencyMs }}ms</span>
                <span v-if="s.tokens" class="dim">{{ s.tokens }}</span>
              </div>
              <details v-if="s.detail" class="step-detail">
                <summary>入参 / 结果</summary>
                <pre>{{ s.detail }}</pre>
              </details>
            </li>
          </ul>

          <div v-if="m.text" class="text markdown" v-html="renderMarkdown(m.text)" />
          <span v-if="m.text && m.active" class="caret" />
          <p v-if="!m.text && m.active" class="thinking">
            {{ m.status === 'pending' ? '排队中…' : '等待首个 token…' }}
          </p>

          <div class="meta">
            <span v-if="m.status" class="badge" :class="m.status">
              {{ STATUS_LABEL[m.status] ?? m.status }}
            </span>
            <span v-if="m.ttftMs !== undefined" class="dim">TTFT {{ m.ttftMs }}ms</span>
            <span v-if="m.inputTokens !== undefined" class="dim">
              in {{ m.inputTokens }} / out {{ m.outputTokens ?? 0 }}
            </span>
            <span v-if="m.errorCategory" class="err">
              {{ ERROR_LABEL[m.errorCategory] ?? m.errorCategory }}
            </span>
          </div>

          <p v-if="m.errorMessage" class="err detail-line">{{ m.errorMessage }}</p>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.messages {
  display: flex;
  flex-direction: column;
  gap: 24px;
  width: 100%;
  max-width: 840px;
  margin: 0 auto;
  padding: 18px 0 30px;
}

.row {
  display: flex;
  min-width: 0;
}

.row.user {
  justify-content: flex-end;
}

.row.assistant {
  display: grid;
  grid-template-columns: 26px minmax(0, 1fr);
  align-items: start;
  gap: 12px;
}

.avatar {
  margin-top: 1px;
}

.bubble.user {
  max-width: 78%;
  padding: 10px 15px;
  border-radius: 18px 18px 4px 18px;
  background: var(--accent-soft);
  color: var(--text);
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 14.5px;
  line-height: 1.7;
}

.assistant-body {
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-width: 0;
}

.text {
  margin: 0;
  font-size: 15px;
  line-height: 1.8;
  color: var(--text);
}

/* 模型输出按 markdown 渲染，样式在这里兜住；内容已由 DOMPurify 消毒 */
.markdown :deep(p) {
  margin: 0 0 12px;
}

.markdown :deep(p:last-child) {
  margin-bottom: 0;
}

.markdown :deep(ul),
.markdown :deep(ol) {
  margin: 0 0 12px;
  padding-left: 22px;
}

.markdown :deep(li) {
  margin: 3px 0;
}

.markdown :deep(strong) {
  font-weight: 600;
}

.markdown :deep(h1),
.markdown :deep(h2),
.markdown :deep(h3) {
  margin: 18px 0 8px;
  font-size: 16px;
  font-weight: 600;
  line-height: 1.5;
}

.markdown :deep(h1:first-child),
.markdown :deep(h2:first-child),
.markdown :deep(h3:first-child) {
  margin-top: 0;
}

.markdown :deep(code) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.88em;
  padding: 1px 5px;
  border-radius: 4px;
  background: #f1f2f4;
}

.markdown :deep(pre) {
  margin: 0 0 12px;
  padding: 12px 14px;
  border: 1px solid #ededed;
  border-radius: 12px;
  background: #f7f7f8;
  overflow: auto;
}

.markdown :deep(pre code) {
  padding: 0;
  background: none;
  font-size: 12.5px;
  line-height: 1.65;
}

.markdown :deep(blockquote) {
  margin: 0 0 12px;
  padding-left: 12px;
  border-left: 3px solid var(--border);
  color: var(--muted-strong);
}

.markdown :deep(a) {
  color: var(--accent);
  text-decoration: none;
}

.markdown :deep(a:hover) {
  text-decoration: underline;
}

.markdown :deep(table) {
  margin: 0 0 12px;
  border-collapse: collapse;
  font-size: 13px;
}

.markdown :deep(th),
.markdown :deep(td) {
  padding: 6px 10px;
  border: 1px solid var(--border);
  text-align: left;
}

.markdown :deep(th) {
  background: #f7f7f8;
  font-weight: 500;
}

.caret {
  display: inline-block;
  width: 2px;
  height: 1.05em;
  margin-left: 2px;
  vertical-align: text-bottom;
  border-radius: 2px;
  background: var(--accent);
  animation: blink 1s steps(2, start) infinite;
}

@keyframes blink {
  to {
    visibility: hidden;
  }
}

.thinking {
  margin: 0;
  color: var(--muted);
  font-size: 14px;
}

.steps {
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 0;
  padding: 0;
}

.steps li {
  display: flex;
  flex-direction: column;
  gap: 5px;
  padding: 8px 10px;
  border: 1px solid #ededed;
  border-radius: 10px;
  background: #fafafa;
  color: var(--muted);
  font-size: 12.5px;
}

.steps li.tool {
  background: #fffaf2;
  border-color: #f1e6d2;
}

.step-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex-wrap: wrap;
}

.label {
  color: var(--text);
}

.running {
  color: var(--accent);
}

.step-detail summary {
  width: fit-content;
  color: var(--accent);
  font-size: 12px;
  cursor: pointer;
}

.step-detail pre {
  margin: 6px 0 0;
  padding: 8px 10px;
  max-height: 180px;
  overflow: auto;
  border-radius: 8px;
  background: #f1f2f4;
  font-size: 11.5px;
  line-height: 1.55;
  white-space: pre-wrap;
}

.meta {
  display: flex;
  align-items: baseline;
  gap: 10px;
  flex-wrap: wrap;
  font-size: 11.5px;
}

.badge {
  padding: 1px 7px;
  border-radius: 999px;
  background: #f1f2f4;
  color: var(--muted-strong);
}

.badge.running,
.badge.pending {
  background: var(--accent-soft);
  color: var(--accent);
}

.badge.completed {
  background: #eaf5ee;
  color: var(--ok);
}

.badge.failed {
  background: #fdeeee;
  color: var(--err);
}

.dim {
  color: var(--muted);
}

.err {
  color: var(--err);
}

.detail-line {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.6;
}

@media (max-width: 800px) {
  .messages {
    gap: 20px;
    padding: 12px 0 24px;
  }

  .row.assistant {
    grid-template-columns: 22px minmax(0, 1fr);
    gap: 10px;
  }

  .avatar {
    width: 22px;
    height: 22px;
  }

  .bubble.user {
    max-width: 88%;
  }
}
</style>
