import { afterEach, describe, expect, it, vi } from 'vitest'
import { streamRun } from './events'
class FakeEventSource {
  static CLOSED = 2
  static current: FakeEventSource
  readyState = 1
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  listeners = new Map<string, (e: { data: string; lastEventId: string }) => void>()
  closed = false
  constructor(readonly url: string) { FakeEventSource.current = this }
  addEventListener(type: string, fn: (e: { data: string; lastEventId: string }) => void) { this.listeners.set(type, fn) }
  close() { this.closed = true }
  emit(type: string, id: string, payload: unknown) { this.listeners.get(type)?.({ data: JSON.stringify(payload), lastEventId: id }) }
}
describe('EventSource 生命周期', () => {
  afterEach(() => vi.unstubAllGlobals())
  it('重复增量去重，终态始终收敛并关闭连接', () => {
    vi.stubGlobal('EventSource', FakeEventSource)
    const received: string[] = []
    streamRun(7, { onEvent: e => received.push(e.type) })
    const source = FakeEventSource.current
    source.emit('turn.delta', '7:1', { delta: 'hello' })
    source.emit('turn.delta', '7:1', { delta: 'hello' })
    source.emit('run.terminal', '7:1', { status: 'completed' })
    expect(received).toEqual(['turn.delta', 'run.terminal'])
    expect(source.closed).toBe(true)
    expect(source.url).not.toContain('clientId')
  })
  it('有限次重连后报告失败并关闭', () => {
    vi.stubGlobal('EventSource', FakeEventSource)
    const ended = vi.fn()
    streamRun(8, { onEvent: vi.fn(), onEnd: ended }, { maxRetries: 1 })
    FakeEventSource.current.onerror?.()
    FakeEventSource.current.onerror?.()
    expect(ended).toHaveBeenCalledWith('exhausted')
    expect(FakeEventSource.current.closed).toBe(true)
  })
})
