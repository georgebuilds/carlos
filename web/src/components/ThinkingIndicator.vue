<script setup lang="ts">
// Activity indicator for the wait between a user submission and the first
// streamed token (TUI parity: internal/tui/chat/thinking.go). Three muted
// dots pulse in the assistant reply position; once the wait crosses the
// threshold a dim mono elapsed counter appears, ticking once per second, so
// the user can gauge stalled vs. just slow. Quick replies stay quiet.
//
// The elapsed clock anchors at mount. TranscriptFeed keys this component by
// the last persisted seq, so every new transcript entry (tool result, etc.)
// remounts it and resets the clock, matching the TUI's "time since the last
// transcript entry" semantics.
//
// Reduced motion: the global prefers-reduced-motion rule in app.css
// collapses the wave animation to a single instant run, leaving the dots
// static at their base style. The timer is plain text and keeps working.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

// Seconds before the elapsed trailer appears (TUI: thinkingElapsedThreshold).
const THRESHOLD_S = 3

const elapsed = ref(0)
let startedAt = 0
let timer: ReturnType<typeof setInterval> | null = null

onMounted(() => {
  startedAt = Date.now()
  timer = setInterval(() => {
    elapsed.value = Math.floor((Date.now() - startedAt) / 1000)
  }, 1000)
})

onBeforeUnmount(() => {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
})

const showElapsed = computed(() => elapsed.value >= THRESHOLD_S)
</script>

<template>
  <div class="msg-asst thinking" role="status" aria-label="carlos is thinking">
    <div class="who">carlos</div>
    <div class="body">
      <span class="dots" aria-hidden="true">
        <span class="dot"></span><span class="dot"></span><span class="dot"></span>
      </span>
      <span class="label">thinking</span>
      <span v-if="showElapsed" class="t-elapsed">{{ elapsed }}s</span>
    </div>
  </div>
</template>
