<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { ArrowLeft, ChevronRight, Layers, List, Plus, RotateCcw, Settings2, Trash2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { InteractionField } from '../protocol'
import { cloneInteractionValue, newFieldValue } from '../forms'
import FieldControl from './FieldControl.vue'

type Path = (string | number)[]
const props = defineProps<{ fields: InteractionField[]; modelValue: Record<string, unknown>; id: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: Record<string, unknown>]; navigate: [depth: number] }>()
const { t } = useI18n()
const path = ref<Path>([])
const surface = ref<HTMLElement>()
const heading = ref<HTMLElement>()
const issue = ref<{ path: Path; message: string }>()
const root = computed<InteractionField>(() => ({ name: '', label: t('configuration'), kind: 'object', fields: props.fields, required: false, sensitive: false, hasDefault: false }))
const trail = computed(() => {
  let field = root.value
  let value: unknown = props.modelValue
  const entries = [{ field, value, label: t('configuration') }]
  for (const key of path.value) {
    const next = typeof key === 'number' ? field.element : field.fields?.find(member => member.name === key)
    if (!next) break
    value = childValue(value, key)
    field = next
    entries.push({ field, value, label: typeof key === 'number' ? itemLabel(field, value, key) : field.label || field.name })
  }
  return entries
})
const current = computed(() => trail.value[trail.value.length - 1])
const entries = computed<unknown[]>(() => Array.isArray(current.value.value) ? current.value.value : [])
const pageKey = computed(() => JSON.stringify(path.value))
const fieldId = (keys: Path) => props.id + '-' + keys.map(key => encodeURIComponent(String(key))).join('-')
const compound = (field: InteractionField) => field.kind === 'object' || field.kind === 'list'

function childValue(value: unknown, key: string | number): unknown {
  if (Array.isArray(value) && typeof key === 'number') return value[key]
  if (value && typeof value === 'object') return (value as Record<string, unknown>)[key]
  return undefined
}
function itemLabel(field: InteractionField, value: unknown, index: number): string {
  if (!field.sensitive && field.kind === 'object') {
    const member = field.fields?.find(item => !item.sensitive && item.kind === 'string' && typeof childValue(value, item.name) === 'string' && String(childValue(value, item.name)).trim())
    if (member) return String(childValue(value, member.name))
  }
  return t('listItem', { index: index + 1 })
}
function summary(field: InteractionField, value: unknown): string {
  if (field.kind === 'list') return t('itemCount', { count: Array.isArray(value) ? value.length : 0 })
  return t('fieldCount', { count: field.fields?.length || 0 })
}
function replaceAt(value: unknown, keys: Path, next: unknown): unknown {
  if (!keys.length) return next
  const [key, ...rest] = keys
  if (typeof key === 'number') {
    const result = Array.isArray(value) ? [...value] : []
    result[key] = replaceAt(result[key], rest, next)
    return result
  }
  const result = value && typeof value === 'object' && !Array.isArray(value) ? { ...value as Record<string, unknown> } : {}
  const updated = replaceAt(result[key], rest, next)
  if (updated === undefined) delete result[key]
  else result[key] = updated
  return result
}
function update(keys: Path, value: unknown) {
  emit('update:modelValue', replaceAt(props.modelValue, keys, value) as Record<string, unknown>)
  issue.value = undefined
}
async function navigate(keys: Path, focusId?: string) {
  path.value = keys
  emit('navigate', keys.length)
  await nextTick()
  const input = focusId ? document.getElementById(focusId) : null
  ;(input || heading.value)?.focus({ preventScroll: true })
  surface.value?.closest('.overlay-body')?.scrollTo?.({ top: 0 })
}
function enter(key: string | number) { void navigate([...path.value, key]) }
function back(depth: number) {
  const previous = path.value.slice(0, depth + 1)
  void navigate(path.value.slice(0, depth), fieldId(previous) + '-open')
}
async function add() {
  const field = current.value.field.element
  if (!field) return
  const index = entries.value.length
  update(path.value, [...entries.value, newFieldValue(field)])
  if (compound(field)) await navigate([...path.value, index])
  else {
    await nextTick()
    document.getElementById(fieldId([...path.value, index]))?.focus()
  }
}
async function remove(index: number) {
  update(path.value, entries.value.filter((_, position) => position !== index))
  await nextTick()
  heading.value?.focus({ preventScroll: true })
}
function restore() {
  update(path.value, current.value.field.sensitive ? undefined : cloneInteractionValue(current.value.field.default))
}
function invalid(keys: Path) { return JSON.stringify(issue.value?.path) === JSON.stringify(keys) }
async function revealError(keys: Path, message: string) {
  issue.value = { path: keys, message }
  await navigate(keys.slice(0, -1), fieldId(keys))
}
defineExpose({ revealError })
</script>

<template>
  <section ref="surface" class="operation-fields">
    <nav v-if="path.length" class="field-breadcrumbs" :aria-label="t('configurationPath')">
      <button type="button" class="icon-button" :aria-label="t('previousLevel')" :title="t('previousLevel')" @click="back(path.length - 1)"><ArrowLeft :size="16" /></button>
      <ol>
        <li v-for="(entry, index) in trail" :key="index">
          <ChevronRight v-if="index" :size="12" aria-hidden="true" />
          <span v-if="index === trail.length - 1" aria-current="page" :title="entry.label">{{ entry.label }}</span>
          <button v-else type="button" :title="entry.label" @click="back(index)">{{ entry.label }}</button>
        </li>
      </ol>
    </nav>
    <div class="field-page-heading">
      <div class="field-page-title"><h3 ref="heading" tabindex="-1">{{ current.label }}</h3><span class="count">{{ current.field.kind === 'list' ? entries.length : current.field.fields?.length || 0 }}</span></div>
      <div class="field-page-actions">
        <button v-if="current.field.hasDefault" type="button" class="icon-button" :disabled="disabled" :aria-label="t('useDefault')" :title="t('useDefault')" @click="restore"><RotateCcw :size="15" /></button>
        <button v-if="current.field.kind === 'list'" type="button" class="btn small" :disabled="disabled" @click="add"><Plus :size="15" />{{ t('addListItem') }}</button>
      </div>
    </div>
    <p v-if="path.length && current.field.description" class="field-page-description">{{ current.field.description }}</p>
    <div :key="pageKey" class="field-page">
      <template v-if="current.field.kind === 'object'">
        <template v-for="field in current.field.fields" :key="field.name">
          <div v-if="compound(field)" class="field-section-row" :class="{ 'field-invalid': invalid([...path, field.name]) }">
            <button :id="fieldId([...path, field.name]) + '-open'" type="button" class="field-open" @click="enter(field.name)">
              <span class="field-kind-icon"><List v-if="field.kind === 'list'" :size="18" /><Settings2 v-else :size="18" /></span>
              <span class="field-row-copy"><strong>{{ field.label || field.name }}<span v-if="field.required" class="accent"> *</span></strong><span v-if="field.description">{{ field.description }}</span></span>
              <span class="field-row-count">{{ summary(field, childValue(current.value, field.name)) }}</span><ChevronRight :size="16" class="muted" />
            </button>
            <p v-if="invalid([...path, field.name])" class="error-text">{{ issue?.message }}</p>
          </div>
          <div v-else :class="{ 'field-invalid': invalid([...path, field.name]) }">
            <FieldControl :id="fieldId([...path, field.name])" :field="field" :model-value="childValue(current.value, field.name)" :disabled="disabled" single-line @update:model-value="update([...path, field.name], $event)" />
            <p v-if="invalid([...path, field.name])" class="error-text">{{ issue?.message }}</p>
          </div>
        </template>
      </template>
      <template v-else-if="current.field.kind === 'list' && current.field.element">
        <div v-if="!entries.length" class="field-list-empty"><List :size="26" :stroke-width="1.3" /><span>{{ t('emptyList') }}</span></div>
        <div v-for="(entry, index) in entries" :key="index" class="field-entry" :class="{ 'field-invalid': invalid([...path, index]) }">
          <button v-if="compound(current.field.element)" :id="fieldId([...path, index]) + '-open'" type="button" class="field-open" @click="enter(index)">
            <span class="field-entry-index">{{ String(index + 1).padStart(2, '0') }}</span>
            <span class="field-row-copy"><strong>{{ itemLabel(current.field.element, entry, index) }}</strong><span>{{ summary(current.field.element, entry) }}</span></span>
            <ChevronRight :size="16" class="muted" />
          </button>
          <div v-else class="field-entry-input"><span class="field-entry-index">{{ String(index + 1).padStart(2, '0') }}</span><FieldControl :id="fieldId([...path, index])" :field="{ ...current.field.element, label: t('listItem', { index: index + 1 }) }" :model-value="entry" :disabled="disabled" single-line @update:model-value="update([...path, index], $event)" /></div>
          <button type="button" class="icon-button field-remove" :disabled="disabled" :aria-label="t('removeListItem', { index: index + 1 })" :title="t('removeListItem', { index: index + 1 })" @click="remove(index)"><Trash2 :size="15" /></button>
          <p v-if="invalid([...path, index])" class="error-text">{{ issue?.message }}</p>
        </div>
      </template>
      <div v-if="current.field.kind === 'object' && !current.field.fields?.length" class="field-list-empty"><Layers :size="26" :stroke-width="1.3" /><span>{{ t('noFields') }}</span></div>
    </div>
  </section>
</template>
