<script setup lang="ts">
import { computed } from 'vue'
import type { RunStatus, TaskSummary } from '../api/types'
import BrandMark from './BrandMark.vue'

const props = defineProps<{
  tasks: TaskSummary[]
  activeId: number | null
  loading: boolean
  hasMore: boolean
}>()

const emit = defineEmits<{
  select: [id: number]
  create: []
  more: []
}>()

/**
 * 侧栏角标只标「需要你注意的」三种：
 *   跑着 / 失败 / 被中断。completed 不标——否则整列都是点，等于噪音。
 * 字段缺失（会话还没跑过）也不标，那是「无记录」不是「运行中」。
 */
function badgeOf(status: RunStatus | undefined): '' | 'live' | 'failed' | 'stalled' {
  switch (status) {
    case 'pending':
    case 'running':
      return 'live'
    case 'failed':
      return 'failed'
    case 'interrupted':
      return 'stalled'
    default:
      return ''
  }
}

function badgeText(status: RunStatus | undefined): string {
  switch (status) {
    case 'pending':
    case 'running':
      return '正在运行'
    case 'failed':
      return '执行失败'
    case 'interrupted':
      return '被中断'
    default:
      return ''
  }
}

function shortTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const now = new Date()
  const sameDay = d.toDateString() === now.toDateString()
  return sameDay
    ? d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
    : d.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' })
}

const groups = computed(() => {
  const now = new Date()
  const todayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime()
  const recentStart = todayStart - 29 * 24 * 60 * 60 * 1000
  const buckets: { label: string; items: TaskSummary[] }[] = [
    { label: '今天', items: [] },
    { label: '30 天内', items: [] },
    { label: '更早', items: [] },
  ]

  for (const task of props.tasks) {
    const ts = new Date(task.updatedAt).getTime()
    if (!Number.isNaN(ts) && ts >= todayStart) buckets[0].items.push(task)
    else if (!Number.isNaN(ts) && ts >= recentStart) buckets[1].items.push(task)
    else buckets[2].items.push(task)
  }
  return buckets.filter((bucket) => bucket.items.length > 0)
})
</script>

<template>
  <aside class="sidebar">
    <div class="sidebar-head">
      <div class="brand">
        <BrandMark :size="28" />
        <span>Agent Gateway</span>
      </div>
    </div>

    <button class="new" @click="emit('create')">
      <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
        <path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
      </svg>
      <span>开启新对话</span>
    </button>

    <nav class="history" aria-label="历史对话">
      <section v-for="group in groups" :key="group.label" class="group">
        <h2>{{ group.label }}</h2>
        <ul class="list">
          <li v-for="t in group.items" :key="t.id">
            <button
              class="item"
              :class="{ active: t.id === activeId }"
              :title="t.title || '未命名对话'"
              @click="emit('select', t.id)"
            >
              <span class="title">{{ t.title || '未命名对话' }}</span>
              <span class="meta">
                <span class="time">{{ shortTime(t.updatedAt) }}</span>
                <span
                  v-if="badgeOf(t.lastRunStatus)"
                  class="badge"
                  :class="badgeOf(t.lastRunStatus)"
                  :title="badgeText(t.lastRunStatus)"
                />
              </span>
            </button>
          </li>
        </ul>
      </section>

      <p v-if="!tasks.length && !loading" class="empty">还没有对话，点击上方按钮开始。</p>
    </nav>

    <button v-if="hasMore" class="more" :disabled="loading" @click="emit('more')">
      {{ loading ? '加载中…' : '加载更多' }}
    </button>

    <div class="workspace" title="本地工作区">
      <span class="workspace-avatar"><BrandMark :size="21" /></span>
      <span class="workspace-info">
        <strong>本地工作区</strong>
        <span>go-agent-gateway</span>
      </span>
    </div>
  </aside>
</template>

<style scoped>
.sidebar {
  display: flex;
  flex-direction: column;
  gap: 8px;
  height: 100%;
  padding: 12px 10px 8px;
  border-right: 1px solid var(--border);
  background: var(--sidebar);
  overflow: hidden;
}

.sidebar-head {
  display: flex;
  align-items: center;
  height: 38px;
  padding: 0 8px;
}

.brand {
  display: flex;
  align-items: center;
  gap: 9px;
  min-width: 0;
  font-size: 16px;
  font-weight: 600;
  letter-spacing: -0.02em;
  color: var(--text);
}

.new {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  flex: none;
  height: 40px;
  margin: 2px 2px 4px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--panel);
  color: var(--text);
  font-size: 14px;
  cursor: pointer;
  box-shadow: var(--shadow-sm);
  transition:
    border-color 0.16s ease,
    color 0.16s ease,
    background 0.16s ease;
}

.new:hover {
  border-color: #cfd6ff;
  background: #f7f9ff;
  color: var(--accent);
}

.history {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 2px 0 10px;
}

.group h2 {
  margin: 14px 8px 5px;
  font-size: 11px;
  font-weight: 500;
  color: var(--muted);
}

.list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.item {
  width: 100%;
  height: 36px;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 10px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--text);
  text-align: left;
  cursor: pointer;
  transition: background 0.14s ease;
}

.item:hover {
  background: #f0f0f0;
}

.item.active {
  background: #e8e8e8;
}

.title {
  flex: 1;
  min-width: 0;
  font-size: 13.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.meta {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: none;
}

.time {
  font-size: 11px;
  color: var(--muted);
  opacity: 0;
  transition: opacity 0.14s ease;
}

.item:hover .time,
.item.active .time {
  opacity: 1;
}

.badge {
  width: 7px;
  height: 7px;
  flex: none;
  border-radius: 50%;
}

.badge.live {
  background: var(--accent);
  animation: breathe 1.4s ease-in-out infinite;
}

.badge.failed {
  background: var(--err);
}

.badge.stalled {
  background: var(--warning);
}

@keyframes breathe {
  0%,
  100% {
    opacity: 0.35;
    transform: scale(0.85);
  }
  50% {
    opacity: 1;
    transform: scale(1.15);
  }
}

/* 关掉动效偏好下只留静态点：信息（在跑）不能因为动画被砍掉 */
@media (prefers-reduced-motion: reduce) {
  .badge.live {
    animation: none;
    opacity: 1;
  }
}

.empty {
  margin: 24px 10px;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--muted);
}

.more {
  flex: none;
  height: 34px;
  margin: 0 2px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--panel);
  color: var(--muted-strong);
  font-size: 12.5px;
  cursor: pointer;
}

.more:hover {
  border-color: var(--border-strong);
  color: var(--text);
}

.workspace {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: none;
  margin-top: auto;
  padding: 12px 8px 6px;
  border-top: 1px solid var(--border);
}

.workspace-avatar {
  width: 32px;
  height: 32px;
  display: grid;
  place-items: center;
  flex: none;
  border: 1px solid var(--border);
  border-radius: 50%;
  background: var(--panel);
}

.workspace-info {
  display: flex;
  flex-direction: column;
  min-width: 0;
  line-height: 1.35;
}

.workspace-info strong {
  font-size: 12.5px;
  font-weight: 500;
}

.workspace-info span {
  overflow: hidden;
  color: var(--muted);
  font-size: 10.5px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
