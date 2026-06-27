<script setup lang="ts">
// A tool_call paired with its tool_result, redesigned (spec: condense the
// invocation, keep the full output a click away). Collapsed, it is a single
// quiet chip: glyph + tool name + the primary argument + a status dot, plus
// a +/- stat for diffable tools. Expanded, it routes to a per-type viewer:
// a colored diff for write/edit (built from the input, which is full), a
// terminal-style well for bash, a code well for read, and a pretty
// input/output well for everything else (MCP tools included).
import { ref, computed, useId } from 'vue'
import { toolMeta } from '@/lib/toolMeta'
import { lineDiff } from '@/lib/diff'
import DiffView from './viewers/DiffView.vue'

const props = defineProps<{
  name: string
  input: string
  inputRaw?: unknown
  output: string
  isError: boolean
  truncated: boolean
}>()

const open = ref(false)
// useId gives a per-instance stable id so two identical tool calls don't
// collide on aria-controls (a hash of name+input would).
const bodyId = `tc-body-${useId()}`

const meta = computed(() => toolMeta(props.name, props.inputRaw))

const inputObj = computed<Record<string, unknown>>(() =>
  props.inputRaw && typeof props.inputRaw === 'object' && !Array.isArray(props.inputRaw)
    ? (props.inputRaw as Record<string, unknown>)
    : {},
)

const fieldStr = (k: string): string => {
  const v = inputObj.value[k]
  return typeof v === 'string' ? v : ''
}

// Diff inputs: edits compare search -> replace; writes show content as a new
// file (empty old side).
const diffOld = computed(() => (meta.value.kind === 'edit' ? fieldStr('search') : ''))
const diffNew = computed(() =>
  meta.value.kind === 'edit' ? fieldStr('replace') : fieldStr('content'),
)
const isDiff = computed(
  () => meta.value.kind === 'edit' || (meta.value.kind === 'write' && diffNew.value !== ''),
)
const diffStat = computed(() => (isDiff.value ? lineDiff(diffOld.value, diffNew.value) : null))

const filePath = computed(() => fieldStr('path'))

// Pretty JSON of the input for the generic well.
const inputJson = computed(() => {
  const keys = Object.keys(inputObj.value)
  if (keys.length === 0) return ''
  try {
    return JSON.stringify(inputObj.value, null, 2)
  } catch {
    return props.input
  }
})

const bashCmd = computed(() => fieldStr('cmd') || fieldStr('command'))
</script>

<template>
  <div class="tool-card" :class="{ err: props.isError, open }">
    <button
      class="tc-head"
      type="button"
      :aria-expanded="open"
      :aria-controls="bodyId"
      @click="open = !open"
    >
      <span class="tc-glyph" :class="`k-${meta.kind}`" aria-hidden="true">{{ meta.glyph }}</span>
      <span v-if="meta.server" class="tc-server">{{ meta.server }}</span>
      <span class="tc-name">{{ meta.display }}</span>
      <span v-if="meta.primary" class="tc-primary">{{ meta.primary }}</span>
      <span class="tc-spacer" />
      <span
        v-if="diffStat && (diffStat.added || diffStat.removed)"
        class="tc-stat"
        aria-label="lines changed"
      >
        <span v-if="diffStat.added" class="add">+{{ diffStat.added }}</span>
        <span v-if="diffStat.removed" class="del">-{{ diffStat.removed }}</span>
      </span>
      <span
        class="tc-dot"
        :class="props.isError ? 'bad' : 'ok'"
        :title="props.isError ? 'error' : 'ok'"
        aria-hidden="true"
      />
      <span class="tc-chev" aria-hidden="true">{{ open ? '▾' : '▸' }}</span>
    </button>

    <div v-if="open" :id="bodyId" class="tc-body" role="region">
      <!-- write / edit: colored diff built from the (full) input -->
      <template v-if="isDiff">
        <div v-if="filePath" class="tc-sub">{{ filePath }}</div>
        <DiffView :old-text="diffOld" :new-text="diffNew" />
        <div v-if="props.output" class="tc-receipt">{{ props.output }}</div>
      </template>

      <!-- bash: command echo then output -->
      <template v-else-if="meta.kind === 'bash'">
        <div v-if="bashCmd" class="tc-cmd">{{ bashCmd }}</div>
        <pre v-if="props.output" class="tc-well" :class="{ err: props.isError }">{{ props.output }}</pre>
        <div v-else class="tc-empty">no output</div>
      </template>

      <!-- everything else (read, search, fetch, MCP, ...): input + output -->
      <template v-else>
        <pre v-if="inputJson && meta.kind !== 'read'" class="tc-well subtle">{{ inputJson }}</pre>
        <pre v-if="props.output" class="tc-well" :class="{ err: props.isError }">{{ props.output }}</pre>
        <div v-else class="tc-empty">no output</div>
      </template>

      <div v-if="props.truncated" class="tc-trunc">
        preview capped; the model saw the full output (spec F12)
      </div>
    </div>
  </div>
</template>

<style scoped>
.tool-card {
  align-self: stretch;
  max-width: 92%;
  background: var(--card);
  border: 1px solid var(--rule);
  border-radius: 8px;
  overflow: hidden;
}
.tool-card.err {
  border-color: color-mix(in srgb, var(--clay) 45%, var(--rule));
}

.tc-head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 7px 11px;
  background: none;
  border: 0;
  cursor: pointer;
  text-align: left;
  font-family: var(--mono, 'JetBrains Mono', monospace);
  color: var(--ink-soft);
  min-width: 0;
}
.tc-head:hover {
  background: var(--paper-hi);
}
.tc-head:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: -2px;
}
.tc-glyph {
  flex: none;
  font-size: 12px;
  color: var(--navy);
}
.tc-glyph.k-bash {
  color: var(--ink-soft);
}
.tc-glyph.k-edit,
.tc-glyph.k-write {
  color: var(--sage);
}
.tc-glyph.k-mcp {
  color: var(--sun);
}
.tc-server {
  flex: none;
  font-size: 10.5px;
  color: var(--muted);
  background: var(--navy-soft);
  padding: 1px 5px;
  border-radius: 4px;
}
.tc-name {
  flex: none;
  font-size: 12px;
  font-weight: 600;
  color: var(--navy);
}
.tc-primary {
  min-width: 0;
  font-size: 11.5px;
  color: var(--muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.tc-spacer {
  flex: 1 1 auto;
}
.tc-stat {
  flex: none;
  font-size: 11px;
  display: inline-flex;
  gap: 5px;
}
.tc-stat .add {
  color: var(--sage);
}
.tc-stat .del {
  color: var(--clay);
}
.tc-dot {
  flex: none;
  width: 6px;
  height: 6px;
  border-radius: 50%;
}
.tc-dot.ok {
  background: var(--sage);
}
.tc-dot.bad {
  background: var(--clay);
}
.tc-chev {
  flex: none;
  font-size: 10px;
  color: var(--subtle);
}

.tc-body {
  border-top: 1px solid var(--rule-soft);
  padding: 9px 11px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.tc-sub {
  font-family: var(--mono, 'JetBrains Mono', monospace);
  font-size: 11px;
  color: var(--muted);
}
.tc-cmd {
  font-family: var(--mono, 'JetBrains Mono', monospace);
  font-size: 11.5px;
  color: var(--code-ink);
  background: var(--code-ground);
  border-radius: 6px;
  padding: 7px 10px;
}
.tc-cmd::before {
  content: '❯ ';
  color: var(--sage);
}
.tc-well {
  margin: 0;
  font-family: var(--mono, 'JetBrains Mono', monospace);
  font-size: 11.5px;
  line-height: 1.6;
  background: var(--code-ground);
  color: var(--code-ink);
  border-radius: 6px;
  padding: 9px 11px;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 320px;
  overflow: auto;
}
.tc-well.subtle {
  opacity: 0.92;
}
.tc-well.err {
  /* fixed light-on-dark: the well is always on --code-ground (dark in both
     themes), so this must not flip with the page color-scheme. */
  color: #f0b3a0;
}
.tc-receipt {
  font-family: var(--mono, 'JetBrains Mono', monospace);
  font-size: 10.5px;
  color: var(--muted);
}
.tc-empty {
  font-size: 11px;
  color: var(--subtle);
  font-style: italic;
}
.tc-trunc {
  font-size: 10.5px;
  color: var(--code-trunc);
  font-style: italic;
}
</style>
