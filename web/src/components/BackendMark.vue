<script setup lang="ts">
// Per-thread backend identity glyph (multi-backend B-5). carlos threads
// carry a cap-navy baseball cap; Claude Code threads carry an amber-ochre
// terminal. Inline SVG (not emoji) so it tints from the --backend-* token,
// stays crisp at roster size, and renders identically across OSes. Unknown
// backends fall back to a neutral muted dot (forward-compat for opencode et
// al. before their glyph exists). The 🧢 emoji stays the Caveat wordmark in
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
  >
    <!-- terminal window: rounded outline, > prompt, blinking-cursor underline -->
    <rect x="1.6" y="2.6" width="12.8" height="10.8" rx="2.2" fill="none" stroke="var(--bm)" stroke-width="1.3" />
    <path d="M4.2 6.4 L6.2 8.1 L4.2 9.8" fill="none" stroke="var(--bm)" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" />
    <line x1="7.7" y1="9.9" x2="11" y2="9.9" stroke="var(--bm)" stroke-width="1.3" stroke-linecap="round" />
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
