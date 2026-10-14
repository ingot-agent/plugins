<script setup lang="ts">
import type { Part } from '../protocol'

import MediaPart from './MediaPart.vue'
defineProps<{ parts: Part[] }>()
</script>
<template>
  <template v-for="(part, index) in parts" :key="index">
    <p v-if="part.kind==='text'">
      {{ part.text }}
    </p>
    <MediaPart v-else :part="part" />
  </template>
</template>
