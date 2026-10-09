<script setup lang="ts">
import { ref, watch } from 'vue'
import { Boxes, CornerDownLeft } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { Operation } from '../protocol'
import type { OperationGroup } from '../commands'

const props = defineProps<{
  phase: 'group' | 'operation'
  groups?: OperationGroup[]
  operations?: Operation[]
  group?: string
  selected: number
}>()
defineEmits<{ group: [group: OperationGroup]; operation: [operation: Operation] }>()
const { t } = useI18n()
const groupLabel = (value?: string) => value || t('ungroupedCommands')
const options = ref<HTMLDivElement>()

watch([options, () => props.selected, () => props.groups, () => props.operations], () => {
  const container = options.value
  const selected = container?.querySelector<HTMLElement>('[aria-selected="true"]')
  if (!container || !selected) return

  const top = container.getBoundingClientRect().top + container.clientTop
  const bottom = top + container.clientHeight
  const bounds = selected.getBoundingClientRect()
  // Scroll only the option list, keeping the composer and transcript in place.
  if (bounds.top < top) container.scrollTop += bounds.top - top
  else if (bounds.bottom > bottom) container.scrollTop += bounds.bottom - bottom
}, { flush: 'post' })
</script>

<template>
  <div class="command-palette" role="listbox" :aria-label="t(phase === 'group' ? 'commandGroups' : 'commandOperations')" @mousedown.prevent>
    <header class="command-palette-heading">
      <span><Boxes :size="14" />{{ t(phase === 'group' ? 'chooseCommandGroup' : 'chooseCommandOperation', { group: groupLabel(group) }) }}</span>
      <span class="command-key"><CornerDownLeft :size="12" />{{ t('select') }}</span>
    </header>
    <div ref="options" class="command-options">
      <button v-for="(item, index) in groups" :key="item.name" type="button" role="option" class="command-option" :class="{ selected: index === selected }" :aria-selected="index === selected" @click="$emit('group', item)">
        <code>/{{ item.name }}</code>
        <span>{{ item.name ? t('operationCount', { count: item.operations.length }) : groupLabel(item.name) + ' · ' + t('operationCount', { count: item.operations.length }) }}</span>
      </button>
      <button v-for="(item, index) in operations" :key="item.id" type="button" role="option" class="command-option" :class="{ selected: index === selected }" :aria-selected="index === selected" @click="$emit('operation', item)">
        <code>{{ item.name }}</code>
        <span>{{ item.description }}</span>
      </button>
      <p v-if="!(groups?.length || operations?.length)" class="command-empty">{{ t('noCommandMatches') }}</p>
    </div>
  </div>
</template>
