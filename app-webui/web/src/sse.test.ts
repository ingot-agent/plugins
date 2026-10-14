import { afterEach, describe, expect, it, vi } from 'vitest'
import { SSEDecoder, subscribe } from './sse'
import type { WebEvent } from './protocol'
describe('SSE framing', () => {
  it('handles split CRLF, heartbeats, multiple records, and Unicode text', () => {
    const received: [number, WebEvent][] = []
    const decoder = new SSEDecoder((id, event) => received.push([id, event]))
    const wire = ': ping\r\n\r\nid: 2\r\ndata: {"type":"agent.output.delta",\r\ndata: "data":{"text":"你好"}}\r\n\r\nid: 3\ndata: {"type":"session.created","data":{}}\n\n'
    for (const character of wire) decoder.push(character)
    expect(received.map(item => item[0])).toEqual([2, 3])
    expect(received[0][1].data.text).toBe('你好')
  })
  it('does not dispatch an incomplete event and rejects an invalid envelope', () => {
    const received: unknown[] = []
    const decoder = new SSEDecoder((id, event) => received.push([id, event]))
    decoder.push('id: 1\ndata: {"type":"x","data":{}}\n')
    expect(received).toHaveLength(0)
    expect(() => decoder.push('\nid: NaN\ndata: {"type":"x"}\n\n')).toThrow('Invalid event')
    expect(received).toHaveLength(1)
  })
})

describe('SSE liveness', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

  function stream(heartbeatMs = 15000) {
    let body!: ReadableStreamDefaultController<Uint8Array>
    vi.stubGlobal('fetch', vi.fn(async (_url, init: RequestInit) => new Response(new ReadableStream<Uint8Array>({
      start(controller) {
        body = controller
        init.signal!.addEventListener('abort', () => controller.error(init.signal!.reason), { once: true })
      },
    }), { headers: { 'Content-Type': 'text/event-stream', 'X-Ingot-Heartbeat-Ms': String(heartbeatMs) } })))
    return (text: string) => body.enqueue(new TextEncoder().encode(text))
  }

  it('aborts a stream that remains open without delivering data', async () => {
    vi.useFakeTimers()
    stream()
    const ready = vi.fn()
    const settled = expect(subscribe(4, new AbortController().signal, vi.fn(), ready)).rejects.toThrow('Event stream timed out')
    await vi.advanceTimersByTimeAsync(44999)
    expect(ready).toHaveBeenCalledOnce()
    await vi.advanceTimersByTimeAsync(1)
    await settled
    expect(vi.getTimerCount()).toBe(0)
  })

  it('keeps heartbeats alive using the configured server interval and clears the timer on disconnect', async () => {
    vi.useFakeTimers()
    const push = stream(30000)
    const stopped = new AbortController()
    const receive = vi.fn()
    const settled = expect(subscribe(4, stopped.signal, receive, vi.fn())).rejects.toThrow('disconnected')
    await vi.advanceTimersByTimeAsync(60000)
    push(': ping\n\n')
    await vi.advanceTimersByTimeAsync(60000)
    push('id: 5\ndata: {"type":"session.created","data":{}}\n\n')
    await vi.advanceTimersByTimeAsync(0)
    expect(receive).toHaveBeenCalledWith(5, { type: 'session.created', data: {} })
    stopped.abort(new Error('disconnected'))
    await settled
    expect(vi.getTimerCount()).toBe(0)
  })

  it('also bounds a request that never returns response headers', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn((_url, init: RequestInit) => new Promise((_resolve, reject) => {
      init.signal!.addEventListener('abort', () => reject(init.signal!.reason), { once: true })
    })))
    const settled = expect(subscribe(0, new AbortController().signal, vi.fn(), vi.fn())).rejects.toThrow('Event stream timed out')
    await vi.advanceTimersByTimeAsync(45000)
    await settled
    expect(vi.getTimerCount()).toBe(0)
  })
})
