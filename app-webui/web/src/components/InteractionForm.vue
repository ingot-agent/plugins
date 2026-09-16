<script setup lang="ts">
import { computed, ref, useId, watch, watchEffect } from 'vue'
import { LoaderCircle } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { Interaction } from '../protocol'
import { useRuntime } from '../stores/runtime'
import { APIError, errorMessage } from '../api'
import { initialInteractionValues, interactionValues, InteractionValueError } from '../forms'
import FieldControl from './FieldControl.vue'
import OperationFields from './OperationFields.vue'

const props = defineProps<{ interaction: Interaction; disabled?: boolean; id?: string; hideSubmit?: boolean }>()
const emit = defineEmits<{ settled: []; state: [value: { busy: boolean; disabled: boolean }]; navigate: [depth: number] }>()
const runtime = useRuntime()
const { t } = useI18n()
const values = ref<Record<string, unknown>>({})
const editor = ref<InstanceType<typeof OperationFields>>()
const busy = ref(false)
const error = ref('')
const settled = ref(false)
const localId = useId()
const formId = computed(() => props.id || localId)
const operation = computed(() => !!props.interaction.scope?.operation)
const submitDisabled = computed(() => !!props.disabled || busy.value || settled.value || runtime.connection !== 'online')
watchEffect(() => emit('state', { busy: busy.value, disabled: submitDisabled.value }))

function reset() {
  values.value = initialInteractionValues(props.interaction.fields)
  error.value = ''
  settled.value = false
}
watch(() => props.interaction.id, reset, { immediate: true })

async function submit() {
  if (submitDisabled.value) return
  error.value = ''
  let result: Record<string, unknown>
  try { result = interactionValues(props.interaction.fields, values.value) }
  catch (cause) {
    error.value = t(errorMessage(cause))
    if (cause instanceof InteractionValueError) await editor.value?.revealError(cause.path, error.value)
    return
  }
  busy.value = true
  try {
    await runtime.respond(props.interaction.id, result)
    settled.value = true
    emit('settled')
  } catch (cause) {
    error.value = cause instanceof APIError && cause.status === 409 ? t('settled') : errorMessage(cause)
  } finally { busy.value = false }
}
</script>

<template>
  <form :id="formId" class="interaction-form" :class="{ 'operation-interaction-form': operation }" @submit.prevent="submit">
    <OperationFields v-if="operation" :id="formId + '-' + interaction.id" :key="interaction.id" ref="editor" v-model="values" :fields="interaction.fields" :disabled="disabled || busy || settled" @navigate="$emit('navigate', $event)" />
    <template v-else><FieldControl v-for="field in interaction.fields" :id="formId + '-' + interaction.id + '-' + field.name" :key="field.name" v-model="values[field.name]" :field="field" :disabled="disabled || busy || settled" /></template>
    <p v-if="error" role="alert" class="error-text">{{ error }}</p>
    <button v-if="!hideSubmit" class="btn primary" type="submit" :disabled="submitDisabled"><LoaderCircle v-if="busy" class="spin" :size="15" />{{ t('submit') }}</button>
  </form>
</template>
