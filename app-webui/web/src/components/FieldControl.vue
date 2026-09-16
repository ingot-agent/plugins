<script setup lang="ts">
import { ref, watch } from 'vue'
import { Plus, RotateCcw, Trash2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { SwitchRoot, SwitchThumb } from 'reka-ui'
import type { InteractionField } from '../protocol'
import { cloneInteractionValue, newFieldValue } from '../forms'

const props = defineProps<{ field: InteractionField; modelValue?: unknown; id: string; disabled?: boolean; item?: boolean; singleLine?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: unknown] }>()
const { t } = useI18n()
let nextKey = 0
const itemKeys = ref<string[]>([])
watch(() => Array.isArray(props.modelValue) ? props.modelValue.length : 0, length => {
  while (itemKeys.value.length < length) itemKeys.value.push(props.id + '-' + ++nextKey)
  itemKeys.value.splice(length)
}, { immediate: true })

function toggle(value: string, checked: boolean) {
  const selected = Array.isArray(props.modelValue) ? props.modelValue as string[] : []
  emit('update:modelValue', checked ? [...selected, value] : selected.filter(item => item !== value))
}
function updateObject(name: string, value: unknown) {
  const next = { ...objectValue() }
  if (value === undefined) delete next[name]
  else next[name] = value
  emit('update:modelValue', next)
}
function objectValue() {
  return props.modelValue && typeof props.modelValue === 'object' && !Array.isArray(props.modelValue) ? props.modelValue as Record<string, unknown> : {}
}
function listValue(): unknown[] { return Array.isArray(props.modelValue) ? props.modelValue : [] }
function addItem() {
  if (!props.field.element) return
  emit('update:modelValue', [...listValue(), newFieldValue(props.field.element)])
}
function updateItem(index: number, value: unknown) {
  const next = [...listValue()]
  next[index] = value
  emit('update:modelValue', next)
}
function removeItem(index: number) {
  emit('update:modelValue', listValue().filter((_, itemIndex) => itemIndex !== index))
  itemKeys.value.splice(index, 1)
}
function useDefault() {
  emit('update:modelValue', props.field.sensitive ? undefined : cloneInteractionValue(props.field.default))
}
</script>

<template>
  <div class="form-field" :class="{ 'compound-field': field.kind === 'object' || field.kind === 'list', 'item-field': item }">
    <div v-if="!item || field.kind !== 'object'" class="field-heading">
      <div>
        <label :id="id + '-label'" :for="id" class="field-label">{{ field.label || field.name }}<span v-if="field.required" aria-hidden="true" class="accent ml-1">*</span></label>
        <p v-if="field.description" :id="id + '-description'" class="muted text-xs">{{ field.description }}</p>
        <p v-if="field.sensitive && field.hasDefault" class="muted text-xs">{{ t('defaultPresent') }}</p>
      </div>
      <button v-if="singleLine && field.hasDefault && modelValue !== undefined" type="button" class="icon-button field-default" :disabled="disabled" :aria-label="t('useDefault')" :title="t('useDefault')" @click="useDefault"><RotateCcw :size="13" /></button>
      <button v-if="field.kind === 'list'" type="button" class="icon-button" :disabled="disabled" :aria-label="t('addListItem')" :title="t('addListItem')" @click="addItem"><Plus :size="16" /></button>
    </div>
    <div v-if="field.kind === 'object'" class="object-field" role="group" :aria-label="item ? field.label || field.name : undefined" :aria-labelledby="item ? undefined : id + '-label'">
      <FieldControl v-for="member in field.fields" :id="id + '-' + member.name" :key="member.name" :field="member" :model-value="objectValue()[member.name]" :disabled="disabled" @update:model-value="updateObject(member.name, $event)" />
    </div>
    <div v-else-if="field.kind === 'list'" class="list-field" role="group" :aria-labelledby="id + '-label'">
      <p v-if="!listValue().length" class="list-empty">{{ t('emptyList') }}</p>
      <div v-for="(entry, index) in listValue()" :key="itemKeys[index]" class="list-item">
        <div class="list-item-heading"><span>{{ t('listItem', { index: index + 1 }) }}</span><button type="button" class="icon-button" :disabled="disabled" :aria-label="t('removeListItem', { index: index + 1 })" @click="removeItem(index)"><Trash2 :size="14" /></button></div>
        <FieldControl v-if="field.element" :id="id + '-' + itemKeys[index]" :field="field.element" :model-value="entry" :disabled="disabled" item @update:model-value="updateItem(index, $event)" />
      </div>
    </div>
    <div v-else-if="field.kind === 'choice'" :id="id" role="radiogroup" :aria-labelledby="id + '-label'" class="choice-list">
      <label v-for="option in field.options" :key="option.value" class="choice" :class="{ selected: modelValue === option.value }">
        <input type="radio" :name="id" :value="option.value" :checked="modelValue === option.value" :disabled="disabled" @change="emit('update:modelValue', option.value)" />
        <span><span class="font-medium">{{ option.label || option.value }}</span><small v-if="option.description">{{ option.description }}</small></span>
      </label>
    </div>
    <div v-else-if="field.kind === 'multichoice'" :id="id" role="group" :aria-labelledby="id + '-label'" class="choice-list">
      <label v-for="option in field.options" :key="option.value" class="choice">
        <input type="checkbox" :checked="Array.isArray(modelValue) && modelValue.includes(option.value)" :disabled="disabled" @change="toggle(option.value, ($event.target as HTMLInputElement).checked)" />
        <span><span class="font-medium">{{ option.label || option.value }}</span><small v-if="option.description">{{ option.description }}</small></span>
      </label>
    </div>
    <div v-else-if="field.kind === 'boolean' && singleLine" class="field-toggle">
      <SwitchRoot :id="id" class="field-switch" :checked="modelValue === true" :disabled="disabled" :aria-labelledby="id + '-label'" @update:checked="emit('update:modelValue', $event)"><SwitchThumb class="field-switch-thumb" /></SwitchRoot>
      <span>{{ modelValue === undefined ? t('notSet') : t(modelValue ? 'yes' : 'no') }}</span>
    </div>
    <select v-else-if="field.kind === 'boolean'" :id="id" class="field" :value="modelValue === undefined ? '' : String(modelValue)" :disabled="disabled" @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value === '' ? undefined : ($event.target as HTMLSelectElement).value === 'true')">
      <option value="">{{ t('choose') }}</option><option value="true">{{ t('yes') }}</option><option value="false">{{ t('no') }}</option>
    </select>
    <template v-else>
      <div v-if="field.kind === 'string' && field.options?.length" class="suggestions">
        <button v-for="option in field.options" :key="option.value" type="button" class="btn small" :disabled="disabled" :title="option.description" @click="emit('update:modelValue', option.value)">{{ option.label || option.value }}</button>
      </div>
      <input v-if="singleLine || field.sensitive || field.kind === 'number' || field.kind === 'integer'" :id="id" class="field" :type="field.sensitive ? 'password' : 'text'" :inputmode="field.kind === 'integer' ? 'numeric' : field.kind === 'number' ? 'decimal' : 'text'" autocomplete="off" :value="modelValue ?? ''" :disabled="disabled" :aria-describedby="field.description ? id + '-description' : undefined" :placeholder="field.sensitive && field.hasDefault ? t('keepSecret') : field.options?.length ? t('freeText') : undefined" @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)" />
      <textarea v-else :id="id" class="field" rows="2" :value="String(modelValue ?? '')" :disabled="disabled" :placeholder="field.options?.length ? t('freeText') : undefined" @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)" />
    </template>
    <button v-if="!singleLine && field.hasDefault && modelValue !== undefined" type="button" class="text-button" :disabled="disabled" @click="useDefault">{{ t('useDefault') }}</button>
  </div>
</template>
