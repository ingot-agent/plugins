import type { InteractionField, Schema } from './protocol'

export function interactionValues(fields: InteractionField[], values: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const field of fields) {
    let value: unknown
    try { value = fieldValue(field, values[field.name]) }
    catch (cause) { throw valueError(cause, field.name) }
    if (value !== undefined) output[field.name] = value
  }
  return output
}

export function initialInteractionValues(fields: InteractionField[]): Record<string, unknown> {
  return Object.fromEntries(fields.flatMap(field => {
    const value = initialFieldValue(field)
    return value === undefined ? [] : [[field.name, value]]
  }))
}

export function initialFieldValue(field: InteractionField): unknown {
  if (field.hasDefault && !field.sensitive) return cloneInteractionValue(field.default)
  return undefined
}

export function newFieldValue(field: InteractionField): unknown {
  if (field.kind === 'object') return {}
  if (field.kind === 'list') return []
  return initialFieldValue(field)
}

function fieldValue(field: InteractionField, value: unknown): unknown {
  if (value === undefined) {
    if (field.required && !field.hasDefault) throw new Error('requiredField')
    return undefined
  }
  if (field.kind === 'integer' || field.kind === 'number') {
    if (String(value).trim() === '') throw new Error(field.kind === 'integer' ? 'invalidInteger' : 'invalidNumber')
    const number = Number(value)
    if (!Number.isFinite(number)) throw new Error('invalidNumber')
    if (field.kind === 'integer' && !Number.isSafeInteger(number)) throw new Error('invalidInteger')
    return number
  }
  if (field.kind === 'object') {
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('invalidObject')
    return interactionValues(field.fields || [], value as Record<string, unknown>)
  }
  if (field.kind === 'list') {
    if (!Array.isArray(value) || !field.element) throw new Error('invalidList')
    return value.map((item, index) => {
      try { return fieldValue(field.element!, item) }
      catch (cause) { throw valueError(cause, index) }
    })
  }
  return value
}

export function cloneInteractionValue<T>(value: T): T {
  if (Array.isArray(value)) return value.map(item => cloneInteractionValue(item)) as T
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, cloneInteractionValue(item)])) as T
  }
  return value
}

export class InteractionValueError extends Error {
  constructor(message: string, public path: (string | number)[]) { super(message) }
}

function valueError(cause: unknown, key: string | number): InteractionValueError {
  return new InteractionValueError(cause instanceof Error ? cause.message : 'formInvalid', [key, ...(cause instanceof InteractionValueError ? cause.path : [])])
}

// Only render complete, flat forms. All other schemas retain their JSON editor.
export function supportsForm(schema: Schema): boolean {
  if (schema.type !== 'object' || !schema.properties) return false
  const complex = ['$ref', '$dynamicRef', 'allOf', 'anyOf', 'oneOf', 'if', 'then', 'else', 'dependentSchemas', 'patternProperties']
  if (complex.some(key => key in schema)) return false
  return Object.values(schema.properties).every(field =>
    typeof field.type === 'string' && ['string', 'integer', 'number', 'boolean'].includes(field.type) &&
    !complex.some(key => key in field) &&
    (!field.enum || field.enum.every(value => ['string', 'number', 'boolean'].includes(typeof value))))
}

export function schemaFields(schema: Schema): InteractionField[] {
  return Object.entries(schema.properties || {}).map(([name, field]) => ({
    name, label: field.title || name, description: field.description,
    kind: field.type as InteractionField['kind'],
    required: schema.required?.includes(name) || false,
    sensitive: false, hasDefault: false,
  }))
}

export function parseObject(text: string): Record<string, unknown> {
  const value: unknown = JSON.parse(text)
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('jsonObject')
  return value as Record<string, unknown>
}

export function canRoundtripForm(text: string, schema: Schema): boolean {
  try {
    const value = parseObject(text)
    return Object.entries(value).every(([key, item]) => {
      const field = schema.properties?.[key]
      if (!field || item === null || typeof item === 'object') return false
      if (typeof item === 'number' && (!Number.isFinite(item) || (Number.isInteger(item) && !Number.isSafeInteger(item)))) return false
      return (field.type === 'integer' ? typeof item === 'number' && Number.isInteger(item) : typeof item === field.type) &&
        (!field.enum || field.enum.includes(item))
    })
  } catch { return false }
}
