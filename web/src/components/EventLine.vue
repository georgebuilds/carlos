<script setup lang="ts">
defineProps<{ text: string }>()
</script>

<template>
  <div class="evt-line">{{ text }}</div>
</template>
