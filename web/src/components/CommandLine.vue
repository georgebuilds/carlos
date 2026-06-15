<script setup lang="ts">
// Compact rendering of a Claude Code slash command (and its output), so a
// command interaction reads as one quiet line instead of a stack of verbose
// user bubbles. A mono pill carries the invocation; the output, if any,
// trails it muted on the same line (clamped). Centered like the other
// hairline event rows.

const props = defineProps<{
  name?: string
  args?: string
  output?: string
  stream?: string
}>()

const label = props.name ? [props.name, props.args].filter(Boolean).join(' ') : ''
</script>

<template>
  <div class="cmd-line">
    <span v-if="label" class="cmd-pill">{{ label }}</span>
    <span v-if="output" class="cmd-out" :class="{ err: stream === 'stderr' }" :title="output">{{
      output
    }}</span>
  </div>
</template>

<style scoped>
.cmd-line {
  align-self: center;
  display: flex;
  align-items: baseline;
  gap: 8px;
  width: 100%;
  max-width: 560px;
  min-width: 0;
}
.cmd-pill {
  flex: none;
  font-family: var(--mono, 'JetBrains Mono', monospace);
  font-size: 11px;
  font-weight: 500;
  color: var(--navy);
  background: var(--navy-soft);
  padding: 1px 7px;
  border-radius: 5px;
}
.cmd-out {
  min-width: 0;
  font-family: var(--mono, 'JetBrains Mono', monospace);
  font-size: 11px;
  color: var(--muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.cmd-out.err {
  color: var(--clay);
}
</style>
