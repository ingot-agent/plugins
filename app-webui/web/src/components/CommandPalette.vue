<script setup lang="ts">
import { Boxes, CornerDownLeft } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { Operation } from '../protocol'
import type { OperationGroup } from '../commands'

defineProps<{
  phase: 'group' | 'operation'
  groups?: OperationGroup[]
  operations?: Operation[]
  group?: string
  selected: number
}>()
defineEmits<{ group: [group: OperationGroup]; operation: [operation: Operation] }>()
const { t } = useI18n()
const groupLabel = (value?: string) => value || t('ungroupedCommands')
</script>

<template>
  <div class="command-palette" role="listbox" :aria-label="t(phase === 'group' ? 'commandGroups' : 'commandOperations')" @mousedown.prevent>
    <header class="command-palette-heading">
      <span><Boxes :size="14" />{{ t(phase === 'group' ? 'chooseCommandGroup' : 'chooseCommandOperation', { group: groupLabel(group) }) }}</span>
      <span class="command-key"><CornerDownLeft :size="12" />{{ t('select') }}</span>
    </header>
    <div class="command-options">
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
