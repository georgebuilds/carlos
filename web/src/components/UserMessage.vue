<script setup lang="ts">
defineProps<{ text: string }>()
</script>

<template>
  <div class="msg-user">
    <div class="bubble">{{ text }}</div>
    <div class="who">george</div>
  </div>
</template>
