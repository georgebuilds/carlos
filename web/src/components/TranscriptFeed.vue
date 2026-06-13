<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { buildRows } from '@/api/render'
import type { WireEvent } from '@/api/types'
import UserMessage from './UserMessage.vue'
import AssistantMessage from './AssistantMessage.vue'
import ToolCard from './ToolCard.vue'
import EventLine from './EventLine.vue'
import StreamBlock from './StreamBlock.vue'
import ThinkingIndicator from './ThinkingIndicator.vue'
import EmptyState from './EmptyState.vue'

const props = defineProps<{ events: WireEvent[]; delta: string; thinking?: boolean }>()

const rows = computed(() => buildRows(props.events))
const isEmpty = computed(() => rows.value.length === 0 && !props.delta)

// The thinking indicator is keyed by the last persisted seq so each new
// transcript entry remounts it, resetting its elapsed clock (TUI parity:
// elapsed is "time since the last transcript entry").
const lastSeq = computed(() => props.events[props.events.length - 1]?.seq ?? 0)

const scrollEl = ref<HTMLElement | null>(null)

function scrollToBottom(force = false): void {
  const el = scrollEl.value
  if (!el) return
  const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 120
  if (force || nearBottom) {
    nextTick(() => {
      el.scrollTop = el.scrollHeight
    })
  }
}

watch(
  () => props.events.length,
  () => scrollToBottom(true),
)
watch(
  () => props.delta,
  () => scrollToBottom(),
)
watch(
  () => props.thinking,
  (on) => {
    if (on) scrollToBottom()
  },
)
</script>

<template>
  <div class="scroll" ref="scrollEl">
    <div class="feed">
      <EmptyState v-if="isEmpty" />
      <template v-else>
        <template v-for="row in rows" :key="row.key">
          <UserMessage v-if="row.type === 'user'" :text="row.text" />
          <AssistantMessage
            v-else-if="row.type === 'assistant'"
            :text="row.text"
            :error="row.error"
          />
          <ToolCard
            v-else-if="row.type === 'tool'"
            :name="row.name"
            :input="row.input"
            :output="row.output"
            :is-error="row.isError"
            :truncated="row.truncated"
          />
          <EventLine v-else-if="row.type === 'event'" :text="row.text" />
        </template>
        <StreamBlock v-if="delta" :text="delta" />
        <ThinkingIndicator v-else-if="thinking" :key="`think-${lastSeq}`" />
      </template>
    </div>
  </div>
</template>
