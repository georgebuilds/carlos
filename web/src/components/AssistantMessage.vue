<script setup lang="ts">
withDefaults(defineProps<{ text: string; error?: boolean; who?: string }>(), {
  who: 'carlos',
})
</script>

<template>
  <div class="msg-asst" :class="{ error }">
    <div class="who">{{ who }}<template v-if="error"> · error</template></div>
    <div class="body">{{ text }}</div>
  </div>
</template>
