<script setup lang="ts">
import type { Part } from '../protocol'

import MediaPart from './MediaPart.vue'
defineProps<{ parts: Part[] }>()
</script>
<template>
  <template v-for="(part, index) in parts" :key="index">
    <div v-if="part.kind==='text'">
      {{ part.text+'\n' }}
    </div>
    <MediaPart v-else :part="part" />
  </template>
</template>
