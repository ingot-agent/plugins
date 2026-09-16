import { describe, expect, it } from 'vitest'
import { canRoundtripForm, initialInteractionValues, interactionValues, parseObject, supportsForm } from './forms'
import type { InteractionField, Schema } from './protocol'
const field = (values: Partial<InteractionField>): InteractionField => ({ name: 'answer', kind: 'string', required: true, sensitive: false, hasDefault: false, ...values })
describe('interaction responses', () => {
  it('omits untouched defaults and preserves explicit false, zero, and empty strings', () => {
    const fields = [field({ name: 'secret', sensitive: true, hasDefault: true }), field({ name: 'toggle', kind: 'boolean' }), field({ name: 'count', kind: 'integer' }), field({ name: 'text' })]
    expect(interactionValues(fields, { toggle: false, count: '0', text: '' })).toEqual({ toggle: false, count: 0, text: '' })
  })
  it('requires an explicit value without a default and does not round large integers', () => {
    expect(() => interactionValues([field({})], {})).toThrow('requiredField')
    expect(() => interactionValues([field({ kind: 'integer' })], { answer: '9007199254740993' })).toThrow('invalidInteger')
    expect(() => interactionValues([field({ kind: 'number' })], { answer: '' })).toThrow('invalidNumber')
  })
  it('allows a string answer outside suggestion options', () => {
    expect(interactionValues([field({ options: [{ value: 'a' }] })], { answer: 'custom' })).toEqual({ answer: 'custom' })
  })
  it('initializes public defaults and recursively converts objects and lists', () => {
    const fields: InteractionField[] = [{
      ...field({ name: 'rules', kind: 'list', hasDefault: true, default: [{ tool: 'shell', limit: 2 }] }),
      element: { ...field({ name: 'rule', kind: 'object' }), fields: [field({ name: 'tool' }), field({ name: 'limit', kind: 'integer' })] },
    }]
    const values = initialInteractionValues(fields)
    expect(values).toEqual({ rules: [{ tool: 'shell', limit: 2 }] })
    expect(interactionValues(fields, { rules: [{ tool: 'edit', limit: '3' }] })).toEqual({ rules: [{ tool: 'edit', limit: 3 }] })
  })
  it('does not initialize sensitive defaults in the browser', () => {
    expect(initialInteractionValues([field({ sensitive: true, hasDefault: true, default: 'hidden' })])).toEqual({})
  })
  it('keeps untouched compound fields absent when the plugin supplied no default', () => {
    const list = field({ name: 'rules', kind: 'list', required: false })
    list.element = field({ name: 'rule', kind: 'object' })
    expect(initialInteractionValues([list])).toEqual({})
    expect(interactionValues([list], {})).toEqual({})
  })
})
describe('operation forms', () => {
  const schema: Schema = { type: 'object', properties: { text: { type: 'string' }, count: { type: 'integer' } } }
  it('falls back to JSON for complex schemas', () => {
    expect(supportsForm(schema)).toBe(true)
    expect(supportsForm({ ...schema, oneOf: [] })).toBe(false)
    expect(supportsForm({ type: 'object', properties: { values: { type: 'array' } } })).toBe(false)
    expect(supportsForm({ type: 'object', properties: { value: { $ref: '#/$defs/x' } } })).toBe(false)
  })
  it('prevents lossy form transitions', () => {
    expect(canRoundtripForm('{"text":"hello","count":0}', schema)).toBe(true)
    expect(canRoundtripForm('{"count":9007199254740993}', schema)).toBe(false)
    expect(canRoundtripForm('{"unknown":"keep me"}', schema)).toBe(false)
    expect(() => parseObject('[]')).toThrow('jsonObject')
    expect(() => parseObject('null')).toThrow('jsonObject')
  })
})
