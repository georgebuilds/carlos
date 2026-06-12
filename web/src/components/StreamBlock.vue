<script setup lang="ts">
// Live delta buffer rendered as an in-progress assistant block. The blinking
// caret signals the stream is open; it is sealed (this block disappears, the
// assistant_message row lands) when the assistant_message event arrives.
defineProps<{ text: string }>()
</script>

<template>
  <div class="msg-asst">
    <div class="who">carlos</div>
    <div class="body">{{ text }}<span class="caret"></span></div>
  </div>
</template>
