import DOMPurify from 'dompurify'
import { marked } from 'marked'

// gfm 支持表格与删除线；breaks 让单个换行也成立，符合聊天场景的直觉
marked.setOptions({ gfm: true, breaks: true })

/**
 * 渲染助手输出。
 *
 * 必须经过 DOMPurify：模型输出是不可信内容，而 v-html 会直接执行 HTML。
 * 这里只放行常规 HTML 标签，不放行脚本、事件属性与 javascript: 链接。
 */
export function renderMarkdown(src: string): string {
  if (!src) return ''
  const html = marked.parse(src, { async: false })
  return DOMPurify.sanitize(html, { USE_PROFILES: { html: true } })
}
