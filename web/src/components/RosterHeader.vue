<script setup lang="ts">
import NewMenu from './NewMenu.vue'

defineProps<{ count: number }>()
const emit = defineEmits<{ new: [backend?: string]; importCc: [] }>()
</script>

<template>
  <div class="roster-head">
    <span class="label">threads</span>
    <span class="count">{{ count }}</span>
    <span class="spacer"></span>
    <NewMenu @new="emit('new', $event)" @import-cc="emit('importCc')" />
  </div>
</template>
