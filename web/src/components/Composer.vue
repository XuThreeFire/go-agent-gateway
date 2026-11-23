<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import type { ModelInfo } from '../api/types'

const props = defineProps<{
  models: ModelInfo[]
  streaming: boolean
  disabled: boolean
}>()

const emit = defineEmits<{
  send: [text: string, model: string]
  cancel: []
}>()

const text = ref('')
const model = ref('')
const box = ref<HTMLTextAreaElement | null>(null)

watch(
  () => props.models,
  (list) => {
    if (!model.value && list.length) model.value = list[0].name
  },
  { immediate: true },
)

function submit() {
  const value = text.value.trim()
  if (!value || props.streaming || props.disabled) return
  emit('send', value, model.value || props.models[0]?.name || '')
  text.value = ''
  void nextTick(() => grow())
}

function onKeydown(e: KeyboardEvent) {
  // Enter 发送、Shift+Enter 换行；输入法组字期间不拦截
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    submit()
  }
}

function grow() {
  const el = box.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = `${Math.min(el.scrollHeight, 200)}px`
}

function focus() {
  box.value?.focus()
}

defineExpose({ focus })
</script>

<template>
  <div class="composer">
    <div class="composer-card" :class="{ streaming }">
      <textarea
        ref="box"
        v-model="text"
        rows="1"
        placeholder="给 Agent 发送消息"
        :disabled="disabled"
        @keydown="onKeydown"
        @input="grow"
      />

      <div class="toolbar">
        <label v-if="models.length" class="model-chip" title="选择模型">
          <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <rect x="2.2" y="2.2" width="11.6" height="11.6" rx="3" stroke="currentColor" />
            <path d="M5.2 6.4h5.6M5.2 9.6h3.6" stroke="currentColor" stroke-linecap="round" />
          </svg>
          <select v-model="model" :disabled="streaming" aria-label="选择模型">
            <option v-for="m in models" :key="`${m.provider}/${m.name}`" :value="m.name">
              {{ m.name }}{{ m.supportsToolCall ? ' · 工具' : '' }}
            </option>
          </select>
          <svg class="chevron" width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
            <path
              d="m3 4.5 3 3 3-3"
              stroke="currentColor"
              stroke-width="1.2"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
        </label>
        <label v-else class="model-chip fallback" title="手填模型名">
          <span class="chip-label">模型</span>
          <input v-model="model" placeholder="手填模型名" :disabled="streaming" />
        </label>

        <span class="hint">Enter 发送 · Shift+Enter 换行</span>

        <button
          v-if="!streaming"
          class="send"
          :disabled="!text.trim() || disabled"
          title="发送"
          aria-label="发送"
          @click="submit"
        >
          <svg width="18" height="18" viewBox="0 0 20 20" fill="none" aria-hidden="true">
            <path
              d="M10 15V5.4M5.6 9.8 10 5.4l4.4 4.4"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
        </button>
        <button v-else class="stop" title="停止生成" aria-label="停止生成" @click="emit('cancel')">
          <span class="stop-square" />
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.composer {
  width: 100%;
  max-width: 760px;
  margin: 0 auto;
  padding: 0 24px 18px;
}

.composer-card {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 92px;
  padding: 14px 14px 10px;
  border: 1px solid var(--border);
  border-radius: 18px;
  background: var(--panel);
  box-shadow: var(--shadow-sm);
  transition:
    border-color 0.16s ease,
    box-shadow 0.16s ease;
}

.composer-card:focus-within {
  border-color: #cfd6ff;
  box-shadow: 0 6px 26px rgba(77, 107, 254, 0.08);
}

.composer-card.streaming {
  border-color: #d9dfff;
}

textarea {
  width: 100%;
  min-height: 26px;
  max-height: 200px;
  resize: none;
  padding: 2px 4px;
  border: 0;
  outline: 0;
  background: transparent;
  color: var(--text);
  font-family: inherit;
  font-size: 15px;
  line-height: 1.7;
}

textarea::placeholder {
  color: #a6a6a6;
}

textarea:disabled {
  cursor: not-allowed;
}

.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 30px;
}

.model-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 30px;
  max-width: 240px;
  padding: 0 9px;
  border-radius: 999px;
  background: #f7f7f8;
  color: var(--muted-strong);
  font-size: 12.5px;
  cursor: pointer;
}

.model-chip:hover {
  background: #f0f0f1;
  color: var(--text);
}

select,
input {
  min-width: 0;
  max-width: 190px;
  padding: 0;
  border: 0;
  outline: 0;
  background: transparent;
  color: inherit;
  font-size: 12.5px;
  cursor: pointer;
  appearance: none;
  text-overflow: ellipsis;
}

select:disabled,
input:disabled {
  cursor: not-allowed;
  opacity: 0.7;
}

.chevron {
  flex: none;
  opacity: 0.6;
}

.fallback {
  cursor: text;
}

.chip-label {
  flex: none;
  color: var(--muted);
}

.hint {
  margin-left: auto;
  overflow: hidden;
  color: #a0a0a0;
  font-size: 11.5px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.send,
.stop {
  display: grid;
  place-items: center;
  flex: none;
  width: 34px;
  height: 34px;
  padding: 0;
  border: 0;
  border-radius: 50%;
  color: #fff;
  cursor: pointer;
  transition:
    background 0.16s ease,
    transform 0.16s ease,
    opacity 0.16s ease;
}

.send {
  background: var(--accent);
}

.send:hover:not(:disabled) {
  background: var(--accent-hover);
  transform: translateY(-1px);
}

.send:disabled {
  background: #e8e8e8;
  color: #b8b8b8;
  cursor: not-allowed;
}

.stop {
  background: var(--text);
}

.stop:hover {
  background: #000;
}

.stop-square {
  width: 10px;
  height: 10px;
  border-radius: 2px;
  background: #fff;
}

@media (max-width: 800px) {
  .composer {
    padding: 0 16px 12px;
  }

  .hint {
    display: none;
  }

  .model-chip {
    max-width: 190px;
  }
}
</style>
