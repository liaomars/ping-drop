<script setup lang="ts">
import { ref } from 'vue'
import type { DMMessage, PeerInfo } from '../lib/protocol'
import { parseTextSegments } from '../lib/device'

const props = defineProps<{
  peer: PeerInfo
  messages: DMMessage[]
}>()

const emit = defineEmits<{
  close: []
  'send-text': [peerId: string, text: string]
  'send-files': [peerId: string, files: File[]]
}>()

const textInput = ref('')
const fileInputEl = ref<HTMLInputElement | null>(null)

function submitText(): void {
  if (!textInput.value.trim()) return
  emit('send-text', props.peer.id, textInput.value)
  textInput.value = ''
}

function onTextKeydown(ev: KeyboardEvent): void {
  if (ev.isComposing || ev.keyCode === 229) return
  ev.preventDefault()
  submitText()
}

function onPickFiles(ev: Event): void {
  const input = ev.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  if (files.length > 0) {
    emit('send-files', props.peer.id, files)
  }
  input.value = ''
}

function onDropFiles(ev: DragEvent): void {
  ev.preventDefault()
  const files = Array.from(ev.dataTransfer?.files ?? [])
  if (files.length > 0) {
    emit('send-files', props.peer.id, files)
  }
}
</script>

<template>
  <section
    data-testid="direct-panel"
    class="flex flex-col bg-[var(--bg-canvas)]"
    @dragover.prevent
    @drop.stop="onDropFiles"
  >
    <div class="flex items-center justify-between border-b border-[var(--border)] px-5 py-3.5">
      <div class="min-w-0">
        <h3 class="truncate text-sm font-semibold text-[var(--text-primary)]">{{ peer.name }}</h3>
        <p class="text-[11px] text-[var(--text-secondary)]">
          {{ peer.os }} · {{ peer.browser }} · P2P 直传
        </p>
      </div>
      <button
        type="button"
        class="rounded-lg px-2.5 py-1 text-xs text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
        @click="emit('close')"
      >
        关闭
      </button>
    </div>

    <div class="native-scroll flex-1 overflow-y-auto px-5 py-5">
      <div class="mx-auto flex max-w-3xl flex-col gap-2.5">
        <div v-if="messages.length === 0" class="py-16 text-center text-xs text-[var(--text-tertiary)]">
          可发送私密文本，或直接拖拽文件到此窗口直传
        </div>
        <div
          v-for="msg in messages"
          :key="msg.id"
          class="max-w-[80%] px-3.5 py-2 text-[13px] leading-relaxed"
          :class="
            msg.outgoing
              ? 'ml-auto rounded-2xl rounded-br-sm bg-[var(--accent)] text-white'
              : 'mr-auto rounded-2xl rounded-bl-sm bg-[var(--bg-subtle)] text-[var(--text-primary)]'
          "
        >
          <p class="whitespace-pre-wrap break-words">
            <template v-for="(seg, idx) in parseTextSegments(msg.text)" :key="idx">
              <a
                v-if="seg.kind === 'link'"
                :href="seg.href"
                target="_blank"
                rel="noopener noreferrer"
                class="underline underline-offset-2"
                :class="msg.outgoing ? 'text-white/95' : 'text-[var(--accent)]'"
              >{{ seg.text }}</a>
              <span v-else>{{ seg.text }}</span>
            </template>
          </p>
        </div>
      </div>
    </div>

    <div class="border-t border-[var(--border)] bg-[var(--bg-canvas)] px-5 py-3.5">
      <div class="mx-auto flex max-w-3xl items-center gap-2">
        <input ref="fileInputEl" type="file" multiple class="hidden" @change="onPickFiles" />
        <button
          type="button"
          class="inline-flex shrink-0 items-center gap-1.5 rounded-lg border border-[var(--border-strong)] px-2.5 py-1.5 text-xs text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
          @click="fileInputEl?.click()"
        >
          <svg
            aria-hidden="true"
            class="h-3.5 w-3.5"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="1.75"
            stroke-linecap="round"
            stroke-linejoin="round"
          >
            <path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48" />
          </svg>
          <span>传文件</span>
        </button>

        <label for="dm-text-input" class="sr-only">私密文本消息</label>
        <input
          id="dm-text-input"
          v-model="textInput"
          placeholder="发送私密文本（不写入时间线）..."
          class="min-w-0 flex-1 rounded-lg border border-[var(--border-strong)] bg-[var(--bg-surface)] px-3 py-1.5 text-xs text-[var(--text-primary)] placeholder:text-[var(--text-tertiary)] focus:border-[var(--accent)] focus:outline-none"
          @keydown.enter="onTextKeydown"
        />

        <button
          type="button"
          class="shrink-0 rounded-lg bg-[var(--btn-primary-bg)] px-3.5 py-1.5 text-xs font-medium text-[var(--btn-primary-text)] transition-colors hover:bg-[var(--btn-primary-hover)]"
          @click="submitText"
        >
          发送
        </button>
      </div>
    </div>
  </section>
</template>
