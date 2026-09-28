<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, LoaderCircle } from 'lucide-vue-next'
import { PopoverContent, PopoverPortal, PopoverRoot, PopoverTrigger } from 'reka-ui'
import { errorMessage } from '../api'
import { useRuntime } from '../stores/runtime'

const runtime = useRuntime()
const { t } = useI18n()
const open = ref(false)
const busy = ref(false)
const provider = ref('')
const model = ref('')
const effort = ref('')
const providerDefault = 'providerDefault'
const snapshot = computed(() => runtime.modelSelection)
const selectedProvider = computed(() => snapshot.value?.providers.find(item => item.name === provider.value))
const selectedModel = computed(() => selectedProvider.value?.models.find(item => item.name === model.value))
const canSave = computed(() => !!snapshot.value && !!selectedModel.value && !busy.value && runtime.connection === 'online')

function resetDraft() {
  const state = snapshot.value
  provider.value = state?.configured ? state.current.provider : state?.providers.find(item => item.models.length)?.name || ''
  model.value = state?.configured ? state.current.model : state?.providers.find(item => item.name === provider.value)?.models[0]?.name || ''
  effort.value = state?.configured ? state.current.reasoningEffort || providerDefault : providerDefault
}
watch(open, async value => {
  if (!value) return
  busy.value = true
  await runtime.refreshModelSelection()
  resetDraft()
  busy.value = false
})
function changeProvider() {
  model.value = selectedProvider.value?.models[0]?.name || ''
  effort.value = providerDefault
}
function changeModel() { effort.value = providerDefault }
async function save() {
  if (!canSave.value || !snapshot.value) return
  busy.value = true
  try {
    await runtime.updateModelSelection({ provider: provider.value, model: model.value, reasoningEffort: effort.value }, snapshot.value.revision)
    open.value = false
  } catch (error) {
    runtime.notify(errorMessage(error))
    resetDraft()
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PopoverRoot v-if="snapshot" v-model:open="open">
    <PopoverTrigger type="button" class="model-selection-trigger" :disabled="runtime.connection !== 'online'" :aria-label="t('modelSelection')" :title="t('modelSelection')">
      <span class="tiny-square" />
      <span class="model-selection-current">{{ snapshot.configured ? snapshot.current.model : t('modelUnconfigured') }}</span>
      <span v-if="snapshot.configured" class="model-selection-effort">{{ !snapshot.current.reasoningEffort || snapshot.current.reasoningEffort === providerDefault ? t('providerDefault') : snapshot.current.reasoningEffort }}</span>
      <ChevronDown :size="13" />
    </PopoverTrigger>
    <PopoverPortal>
      <PopoverContent class="model-selection-popover" align="start" side="top" :side-offset="8">
        <div class="model-selection-heading">{{ t('modelSelection') }}</div>
        <label class="form-field"><span class="field-label">{{ t('provider') }}</span>
          <select v-model="provider" class="field" :disabled="busy" @change="changeProvider">
            <option v-for="item in snapshot.providers" :key="item.name" :value="item.name">{{ item.name }}</option>
          </select>
        </label>
        <label class="form-field"><span class="field-label">{{ t('model') }}</span>
          <select v-model="model" class="field" :disabled="busy || !selectedProvider?.models.length" @change="changeModel">
            <option v-for="item in selectedProvider?.models || []" :key="item.name" :value="item.name">{{ item.name }}</option>
          </select>
        </label>
        <p v-if="selectedProvider && !selectedProvider.models.length" class="model-selection-note">{{ t('modelDirectoryEmpty') }}</p>
        <label class="form-field"><span class="field-label">{{ t('reasoningEffort') }}</span>
          <select v-model="effort" class="field" :disabled="busy || !selectedModel">
            <option :value="providerDefault">{{ t('providerDefault') }}</option>
            <option v-for="value in selectedModel?.reasoningEfforts || []" :key="value" :value="value">{{ value }}</option>
          </select>
        </label>
        <div class="model-selection-actions">
          <button type="button" class="btn small" @click="open = false">{{ t('cancel') }}</button>
          <button type="button" class="btn primary small" :disabled="!canSave" @click="save"><LoaderCircle v-if="busy" :size="13" class="spin" />{{ t('save') }}</button>
        </div>
      </PopoverContent>
    </PopoverPortal>
  </PopoverRoot>
  <span v-else class="composer-agent"><span class="tiny-square" />Ingot</span>
</template>
