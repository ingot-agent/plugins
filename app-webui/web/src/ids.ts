// newRequestId returns a version 4 UUID used as the idempotency key for a turn
// submission. crypto.randomUUID is only exposed in secure contexts, so a phone
// that opens the UI over plain HTTP (e.g. http://192.168.x.x:7316) would throw
// "crypto.randomUUID is not a function" before the first request is sent.
// getRandomValues works in insecure contexts; Math.random only covers browsers
// without any Web Crypto implementation.
export function newRequestId(): string {
  const source = globalThis.crypto
  if (typeof source?.randomUUID === 'function') return source.randomUUID()
  const bytes = new Uint8Array(16)
  if (typeof source?.getRandomValues === 'function') source.getRandomValues(bytes)
  else for (let index = 0; index < bytes.length; index++) bytes[index] = Math.floor(Math.random() * 256)
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, value => value.toString(16).padStart(2, '0')).join('')
  return [hex.slice(0, 8), hex.slice(8, 12), hex.slice(12, 16), hex.slice(16, 20), hex.slice(20)].join('-')
}
