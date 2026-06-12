<script setup lang="ts">
import { ref } from 'vue'

const props = defineProps<{ disabled: boolean; placeholder: string }>()
const emit = defineEmits<{ send: [text: string] }>()

const text = ref('')
const box = ref<HTMLTextAreaElement | null>(null)

function onKeydown(e: KeyboardEvent): void {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    send()
  }
}

function send(): void {
  const v = text.value.trim()
  if (!v || props.disabled) return
  emit('send', v)
  text.value = ''
  if (box.value) box.value.style.height = 'auto'
}

function autogrow(): void {
  const el = box.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = Math.min(el.scrollHeight, 140) + 'px'
}

defineExpose({ focus: () => box.value?.focus() })
</script>

<template>
  <div class="composer">
    <div class="composer-inner">
      <textarea
        ref="box"
        v-model="text"
        rows="1"
        :placeholder="placeholder"
        :disabled="disabled"
        @keydown="onKeydown"
        @input="autogrow"
      ></textarea>
      <button class="send" :disabled="disabled || !text.trim()" @click="send">send</button>
    </div>
    <div class="hint">
      Enter to send · Shift+Enter for a newline · messages mid-turn queue as the
      next turn (spec §9.2)
    </div>
  </div>
</template>
