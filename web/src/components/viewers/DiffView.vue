<script setup lang="ts">
// Unified diff viewer for the write/edit tool cards. Edits pass their
// search/replace pair; writes pass empty old + full content (a new file
// reads as all-additions). Line numbers ride a muted gutter; adds and
// removes carry a faint tinted ground (no left stripe, per the carlos
// design rules) plus a +/- sign column so the change reads at a glance
// even in monochrome.
import { computed } from 'vue'
import { lineDiff } from '@/lib/diff'

const props = defineProps<{
  oldText: string
  newText: string
}>()

const result = computed(() => lineDiff(props.oldText, props.newText))
</script>

<template>
  <div class="diff" role="group" aria-label="file diff">
    <div
      v-for="(ln, i) in result.lines"
      :key="i"
      class="diff-row"
      :class="`op-${ln.op}`"
    >
      <template v-if="ln.op === 'gap'">
        <span class="diff-gap">{{ ln.count }} unchanged {{ ln.count === 1 ? 'line' : 'lines' }}</span>
      </template>
      <template v-else>
        <span class="diff-no diff-old">{{ ln.oldNo ?? '' }}</span>
        <span class="diff-no diff-new">{{ ln.newNo ?? '' }}</span>
        <span class="diff-sign">{{ ln.op === 'add' ? '+' : ln.op === 'del' ? '-' : '' }}</span>
        <span class="diff-text">{{ ln.text === '' ? ' ' : ln.text }}</span>
      </template>
    </div>
  </div>
</template>

<style scoped>
.diff {
  font-family: var(--mono, 'JetBrains Mono', monospace);
  font-size: 11.5px;
  line-height: 1.55;
  background: var(--code-ground);
  color: var(--code-ink);
  border-radius: 6px;
  overflow: auto;
  max-height: 420px;
}
.diff-row {
  display: grid;
  grid-template-columns: 3ch 3ch 1.5ch 1fr;
  column-gap: 6px;
  padding: 0 10px;
  white-space: pre;
}
.diff-no {
  text-align: right;
  color: var(--code-trunc);
  opacity: 0.7;
  user-select: none;
}
.diff-sign {
  text-align: center;
  user-select: none;
  opacity: 0.85;
}
.diff-text {
  white-space: pre-wrap;
  word-break: break-word;
}
/* Tints are full-row grounds, not left stripes (carlos design rule). The
   diff always sits on the dark --code-ground (dark in both themes), so the
   add/del text uses fixed light-on-dark colors rather than light-dark(),
   which would flip to dark text on the dark well in light mode. */
.op-add {
  background: color-mix(in srgb, var(--sage) 26%, transparent);
}
.op-add .diff-sign,
.op-add .diff-text {
  color: #c2e3bd;
}
.op-del {
  background: color-mix(in srgb, var(--clay) 26%, transparent);
}
.op-del .diff-sign,
.op-del .diff-text {
  color: #f0b3a0;
}
.op-gap {
  padding: 2px 10px;
}
.diff-gap {
  display: inline-block;
  font-size: 10.5px;
  color: var(--code-trunc);
  opacity: 0.8;
  font-style: italic;
}
</style>
