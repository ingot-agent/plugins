import { afterEach, describe, expect, it, vi } from 'vitest'
import { newRequestId } from './ids'

const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

describe('newRequestId', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('uses crypto.randomUUID when available', () => {
    const randomUUID = vi.fn(() => '0f0e0d0c-0b0a-4908-8706-050403020100')
    vi.stubGlobal('crypto', { randomUUID })
    expect(newRequestId()).toBe('0f0e0d0c-0b0a-4908-8706-050403020100')
    expect(randomUUID).toHaveBeenCalledOnce()
  })

  it('builds a v4 uuid from getRandomValues without randomUUID', () => {
    const getRandomValues = vi.fn((bytes: Uint8Array) => bytes.fill(0xab))
    vi.stubGlobal('crypto', { getRandomValues })
    expect(newRequestId()).toBe('abababab-abab-4bab-abab-abababababab')
    expect(getRandomValues).toHaveBeenCalledOnce()
  })

  it('falls back to Math.random when the browser has no Web Crypto', () => {
    vi.stubGlobal('crypto', undefined)
    const id = newRequestId()
    expect(id).toMatch(UUID_V4)
    expect(newRequestId()).not.toBe(id)
  })

  it('returns distinct ids from getRandomValues', () => {
    const real = globalThis.crypto
    vi.stubGlobal('crypto', { getRandomValues: (bytes: Uint8Array) => real.getRandomValues(bytes) })
    const ids = new Set(Array.from({ length: 64 }, () => newRequestId()))
    expect(ids.size).toBe(64)
  })
})
