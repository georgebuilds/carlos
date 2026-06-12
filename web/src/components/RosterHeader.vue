<script setup lang="ts">
defineProps<{ count: number }>()
const emit = defineEmits<{ new: [] }>()
</script>

<template>
  <div class="roster-head">
    <span class="label">threads</span>
    <span class="count">{{ count }}</span>
    <span class="spacer"></span>
    <button class="btn-new" @click="emit('new')">+ new</button>
  </div>
</template>
