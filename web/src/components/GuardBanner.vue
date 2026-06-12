<script setup lang="ts">
// Foreign-owned (TUI) threads render read-only. The banner explains the fresh
// heartbeat; attach attempts surface the 409 thread_owned payload in a toast.
defineProps<{ heartbeatAge?: string }>()
</script>

<template>
  <div class="guard-banner">
    <span class="g-tag">heads up</span>
    <span>
      This thread is live in your TUI session<template v-if="heartbeatAge">
        (heartbeat {{ heartbeatAge }} fresh)</template>. The transcript below is
      the persisted log, read-only. Live deltas stay in the owning process.
    </span>
  </div>
</template>
