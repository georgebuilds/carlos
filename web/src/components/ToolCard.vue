<script setup lang="ts">
// A tool_call paired with its tool_result. Collapsible: head shows
// `name · input` one-liner, body is the capped preview on the code-well ground,
// with the 2 KB cap note when truncated (spec F12).
import { ref } from 'vue'

const props = defineProps<{
  name: string
  input: string
  output: string
  isError: boolean
  truncated: boolean
}>()

const open = ref(false)
</script>

<template>
  <div class="tool-card" :class="{ err: props.isError, open }">
    <span class="tc-tag">{{ props.isError ? 'tool · error' : 'tool' }}</span>
    <div class="tc-head" @click="open = !open">
      <span class="tc-name">{{ props.name }}</span>
      <span class="tc-input">{{ props.input }}</span>
    </div>
    <div class="tc-out">{{ props.output
      }}<span v-if="props.truncated" class="tc-trunc">preview capped at 2 KB; the model saw the full output (spec F12)</span></div>
  </div>
</template>
