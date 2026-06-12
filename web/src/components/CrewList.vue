<script setup lang="ts">
import type { ChildSnapshot } from '@/api/types'
import { stateVar } from '@/stores/threads'

defineProps<{ children: ChildSnapshot[] }>()

function tokensLabel(n: number): string {
  return n >= 1000 ? `${Math.round(n / 1000)}k` : String(n)
}
function costLabel(cents: number): string {
  return `$${(cents / 100).toFixed(2)}`
}
</script>

<template>
  <div class="rail-card">
    <span class="rc-tag">the crew</span>
    <h3>sub-agents</h3>
    <template v-if="children.length">
      <div v-for="c in children" :key="c.id" class="crew-item">
        <span class="c-dot" :style="{ background: stateVar(c.state) }"></span>
        <span class="c-body">
          <span class="c-title">{{ c.title }}</span>
          <span class="c-sub">{{ c.state }} · {{ c.last_tool }} · {{ tokensLabel(c.tokens) }} tok</span>
        </span>
        <span class="c-cost">{{ costLabel(c.cost_cents) }}</span>
      </div>
    </template>
    <div v-else class="crew-empty">
      No sub-agents on this thread. carlos spawns them with the Agent tool when a
      task is worth delegating.
    </div>
  </div>
</template>
