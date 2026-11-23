<script setup lang="ts">
defineProps<{
  events: { seq: number; type: string; raw: string }[]
  connected: boolean
}>()
</script>

<template>
  <div class="panel">
    <div class="head">
      <span class="dot" :class="connected ? 'on' : 'off'" />
      <strong>事件流</strong>
      <span class="count">{{ events.length }} 条</span>
      <span class="tip">seq 连续说明没丢</span>
    </div>
    <ol v-if="events.length" class="list">
      <li v-for="e in events" :key="`${e.seq}-${e.type}`">
        <span class="seq">#{{ e.seq }}</span>
        <span class="type">{{ e.type }}</span>
        <span class="raw">{{ e.raw }}</span>
      </li>
    </ol>
    <p v-else class="empty">还没有事件。</p>
  </div>
</template>

<style scoped>
.panel {
  position: fixed;
  right: 18px;
  bottom: 18px;
  width: 480px;
  max-height: 46vh;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  z-index: 20;
  border: 1px solid var(--border);
  border-radius: 14px;
  background: var(--panel);
  box-shadow: var(--shadow-md);
}

.head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 11px 14px;
  border-bottom: 1px solid var(--border);
  font-size: 12.5px;
}

.head strong {
  font-weight: 500;
}

.count {
  color: var(--muted);
  font-size: 11.5px;
}

.tip {
  margin-left: auto;
  color: var(--muted);
  font-size: 11px;
}

.dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #c9ced6;
}

.dot.on {
  background: var(--ok);
}

.dot.off {
  background: var(--err);
}

.list {
  list-style: none;
  margin: 0;
  padding: 0;
  overflow-y: auto;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11.5px;
}

.list li {
  display: grid;
  grid-template-columns: 50px 122px 1fr;
  gap: 8px;
  padding: 6px 14px;
  border-bottom: 1px solid #f3f3f3;
}

.seq {
  color: var(--muted);
}

.type {
  color: var(--accent);
}

.raw {
  overflow: hidden;
  color: var(--muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.empty {
  margin: 0;
  padding: 14px;
  color: var(--muted);
  font-size: 12.5px;
}

@media (max-width: 800px) {
  .panel {
    right: 10px;
    bottom: 10px;
    width: calc(100vw - 20px);
  }
}
</style>
