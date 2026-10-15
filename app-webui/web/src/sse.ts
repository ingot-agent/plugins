import { APIError } from './api'
import type { WebEvent } from './protocol'

// Stateful decoding preserves UTF-8 and CRLF boundaries across network chunks.
export class SSEDecoder {
  private buffer = ''
  private data: string[] = []
  private id = ''
  constructor(private receive: (id: number, event: WebEvent) => void) {}

  push(text: string) {
    this.buffer += text
    for (;;) {
      const match = /[\r\n]/.exec(this.buffer)
      if (!match) break
      const at = match.index
      if (this.buffer[at] === '\r' && at + 1 === this.buffer.length) break
      const line = this.buffer.slice(0, at)
      const length = this.buffer.slice(at, at + 2) === '\r\n' ? 2 : 1
      this.buffer = this.buffer.slice(at + length)
      this.line(line)
    }
  }

  private line(line: string) {
    if (!line) {
      if (this.data.length) {
        const id = Number(this.id)
        const event = JSON.parse(this.data.join('\n')) as WebEvent
        if (!Number.isSafeInteger(id) || id < 0 || typeof event.type !== 'string') {
          throw new Error('Invalid event envelope')
        }
        this.receive(id, event)
      }
      this.data = []
      return
    }
    if (line.startsWith(':')) return
    const colon = line.indexOf(':')
    const field = colon < 0 ? line : line.slice(0, colon)
    const value = colon < 0 ? '' : line.slice(colon + 1).replace(/^ /, '')
    if (field === 'data') this.data.push(value)
    if (field === 'id' && !value.includes('\0')) this.id = value
  }
}

export async function subscribe(cursor: number, signal: AbortSignal, receive: (id: number, event: WebEvent) => void, ready: () => void) {
  const stalled = new AbortController()
  const streamSignal = AbortSignal.any([signal, stalled.signal])
  let timeoutMs = 45000
  let timer: ReturnType<typeof setTimeout>
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined
  const touch = () => {
    clearTimeout(timer)
    timer = setTimeout(() => stalled.abort(new Error('Event stream timed out')), timeoutMs)
  }
  touch()
  try {
    const response = await fetch('/api/events?after=' + cursor, { signal: streamSignal, cache: 'no-store' })
    if (!response.ok) {
      const body = await response.json().catch(() => null)
      throw new APIError(response.status, body?.error || { code: 'event_stream_error', message: response.statusText })
    }
    if (!response.body || !response.headers.get('content-type')?.includes('text/event-stream')) {
      throw new Error('Invalid event stream response')
    }
    const heartbeatMs = Number(response.headers.get('X-Ingot-Heartbeat-Ms'))
    if (Number.isFinite(heartbeatMs) && heartbeatMs > 0) timeoutMs = Math.min(2147483647, Math.max(timeoutMs, heartbeatMs * 3))
    touch()
    reader = response.body.getReader()
    const decoder = new TextDecoder()
    const sse = new SSEDecoder(receive)
    ready()
    while (!streamSignal.aborted) {
      const chunk = await reader.read()
      if (chunk.done) break
      touch() // Comments/heartbeats count even when there are no model events.
      sse.push(decoder.decode(chunk.value, { stream: true }))
    }
    sse.push(decoder.decode())
  } catch (error) {
    if (stalled.signal.aborted) throw stalled.signal.reason
    throw error
  } finally {
    clearTimeout(timer!)
    stalled.abort()
    await reader?.cancel().catch(() => undefined)
    reader?.releaseLock()
  }
}
