<script setup lang="ts">
// Per-thread backend identity glyph (multi-backend B-5). carlos threads
// carry a cap-navy baseball cap; Claude Code threads carry their pixel-art
// mascot (the orange critter) tinted from the --backend-* token. Inline SVG,
// not emoji, so it tints, stays crisp at avatar size, and renders identically
// across OSes. Unknown backends fall back to a neutral muted dot
// (forward-compat for opencode). The 🧢 emoji stays the Caveat wordmark in
// the top bar (implementation-plan L3); this is the per-thread badge.

withDefaults(defineProps<{ backend: string; size?: number }>(), {
  size: 14,
})

const label = (b: string): string =>
  b === 'carlos' ? 'carlos thread' : b === 'cc' ? 'claude code thread' : b + ' thread'
</script>

<template>
  <svg
    v-if="backend === 'carlos'"
    class="backend-mark"
    :width="size"
    :height="size"
    viewBox="0 0 16 16"
    role="img"
    :aria-label="label(backend)"
    :style="{ '--bm': 'var(--backend-carlos)' }"
  >
    <!-- baseball cap: crown dome, brim to the right, top button -->
    <path d="M3 10 C3 4.6 5.2 3.1 8 3.1 C10.8 3.1 12 5.6 12 10 Z" fill="var(--bm)" />
    <path d="M10.6 9.7 C13.7 9.5 15.2 10 15.2 10.9 C15.2 11.4 13 11.1 10.6 11.1 Z" fill="var(--bm)" />
    <circle cx="8" cy="2.8" r="0.85" fill="var(--bm)" />
  </svg>

  <svg
    v-else-if="backend === 'cc'"
    class="backend-mark"
    :width="size"
    :height="size"
    viewBox="0 0 16 16"
    role="img"
    :aria-label="label(backend)"
    :style="{ '--bm': 'var(--backend-cc)' }"
    shape-rendering="crispEdges"
  >
    <!-- Claude Code mascot: a blocky pixel critter. two top nubs, a chunky
         body, four little legs, all in the backend tint; two dark eyes. -->
    <g fill="var(--bm)">
      <rect x="3.4" y="1.7" width="2.4" height="2.6" />
      <rect x="10.2" y="1.7" width="2.4" height="2.6" />
      <rect x="2.4" y="3.9" width="11.2" height="7.4" />
      <rect x="3.4" y="11.2" width="1.7" height="2.7" />
      <rect x="6.0" y="11.2" width="1.7" height="2.7" />
      <rect x="8.6" y="11.2" width="1.7" height="2.7" />
      <rect x="11.2" y="11.2" width="1.7" height="2.7" />
    </g>
    <g fill="var(--bm-eye, #2b1d12)">
      <rect x="4.9" y="5.9" width="2.0" height="2.3" />
      <rect x="9.1" y="5.9" width="2.0" height="2.3" />
    </g>
  </svg>

  <span
    v-else
    class="backend-mark backend-dot"
    role="img"
    :aria-label="label(backend)"
    :style="{ width: size * 0.6 + 'px', height: size * 0.6 + 'px' }"
  ></span>
</template>

<style scoped>
.backend-mark {
  display: inline-block;
  vertical-align: middle;
  flex: none;
}
.backend-dot {
  border-radius: 50%;
  background: var(--muted);
  opacity: 0.6;
}
</style>
