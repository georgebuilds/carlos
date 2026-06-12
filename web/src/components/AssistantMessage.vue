<script setup lang="ts">
defineProps<{ text: string; error?: boolean }>()
</script>

<template>
  <div class="msg-asst" :class="{ error }">
    <div class="who">carlos<template v-if="error"> · error</template></div>
    <div class="body">{{ text }}</div>
  </div>
</template>
