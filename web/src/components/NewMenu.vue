<script setup lang="ts">
// The "+ new" dropdown: carlos thread / detected coding agents / open an
// existing CC session. Shared by the roster header and the home board so the
// create affordance is identical in both surfaces.
import { computed, ref } from 'vue'
import { useConnectionStore } from '@/stores/connection'
import BackendMark from './BackendMark.vue'

const emit = defineEmits<{ new: [backend?: string]; importCc: [] }>()

const conn = useConnectionStore()
const open = ref(false)

// Agents that can start a new thread (detected + create-capable), carlos
// first. Falls back to carlos alone if the server sent no agent list.
const creatable = computed(() => {
  const list = (conn.meta?.agents ?? []).filter((a) => a.can_create)
  return list.length ? list : [{ name: 'carlos', display: 'carlos thread', can_create: true }]
})
const primary = computed(() => creatable.value[0]) // carlos (the default)
const others = computed(() => creatable.value.slice(1)) // detected coding agents

function pick(backend?: string): void {
  open.value = false
  emit('new', backend)
}

function pickImport(): void {
  open.value = false
  emit('importCc')
}
</script>

<template>
  <div class="new-wrap">
    <button class="btn-new" :aria-expanded="open" @click.stop="open = !open">
      + new <span class="chev">▾</span>
    </button>
    <template v-if="open">
      <div class="nm-backdrop" @click="open = false"></div>
      <div class="new-menu" @click.stop>
        <button v-if="primary" class="nm-item" @click="pick(undefined)">
          <BackendMark :backend="primary.name" :size="16" />
          <span>{{ primary.display }}</span>
        </button>
        <template v-if="others.length">
          <div class="nm-sep"></div>
          <div class="nm-cap">coding agents</div>
          <button v-for="a in others" :key="a.name" class="nm-item" @click="pick(a.name)">
            <BackendMark :backend="a.name" :size="16" />
            <span>{{ a.display }}</span>
          </button>
        </template>
        <div class="nm-sep"></div>
        <button class="nm-item nm-import" @click="pickImport">
          <span>open existing CC session</span>
          <span class="nm-sub">browse history</span>
        </button>
      </div>
    </template>
  </div>
</template>
