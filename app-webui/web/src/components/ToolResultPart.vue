<script setup lang="ts">
import type { Part } from '../protocol'

import MediaPart from './MediaPart.vue'
defineProps<{ parts: Part[] }>()
</script>
<template>
  <template v-for="(part, index) in parts" :key="index">
    <p v-if="part.kind==='text'" class="tool-result-text">
      {{ part.text }}
    </p>
    <MediaPart v-else :part="part" />
  </template>
</template>
<style scoped>
.tool-result-text {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-family: var(--font-mono);
}
</style>
