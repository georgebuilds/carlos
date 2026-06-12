<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { buildRows } from '@/api/render'
import type { WireEvent } from '@/api/types'
import UserMessage from './UserMessage.vue'
import AssistantMessage from './AssistantMessage.vue'
import ToolCard from './ToolCard.vue'
import EventLine from './EventLine.vue'
import StreamBlock from './StreamBlock.vue'
import EmptyState from './EmptyState.vue'

const props = defineProps<{ events: WireEvent[]; delta: string }>()

const rows = computed(() => buildRows(props.events))
const isEmpty = computed(() => rows.value.length === 0 && !props.delta)

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
      </template>
    </div>
  </div>
</template>
