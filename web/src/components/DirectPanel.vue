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
    class="flex flex-col gap-3 rounded-xl border border-sky-500/40 bg-slate-900/80 p-4"
    @dragover.prevent
    @drop.stop="onDropFiles"
  >
    <div class="flex items-center justify-between border-b border-slate-800 pb-2">
      <div>
        <h3 class="text-sm font-semibold text-slate-100">点对点直传 · {{ peer.name }}</h3>
        <p class="text-xs text-slate-400">私聊文本不落内存暂存，文件优先 WebRTC P2P 直传</p>
      </div>
      <button type="button" class="text-xs text-slate-400 hover:text-slate-200" @click="emit('close')">关闭</button>
    </div>

    <div class="max-h-56 space-y-2 overflow-y-auto rounded-lg bg-slate-950/80 p-3">
      <div v-if="messages.length === 0" class="text-center text-xs text-slate-500">暂无点对点消息记录</div>
      <div
        v-for="msg in messages"
        :key="msg.id"
        class="rounded-lg px-3 py-2 text-xs"
        :class="msg.outgoing ? 'ml-auto max-w-[85%] bg-sky-600/25 text-sky-100' : 'mr-auto max-w-[85%] bg-slate-800 text-slate-100'"
      >
        <div class="mb-0.5 text-[11px] text-slate-400">{{ msg.outgoing ? '我' : msg.fromName }}</div>
        <p class="whitespace-pre-wrap break-words">
          <template v-for="(seg, idx) in parseTextSegments(msg.text)" :key="idx">
            <a
              v-if="seg.kind === 'link'"
              :href="seg.href"
              target="_blank"
              rel="noopener noreferrer"
              class="text-sky-300 underline"
            >{{ seg.text }}</a>
            <span v-else>{{ seg.text }}</span>
          </template>
        </p>
      </div>
    </div>

    <div class="flex flex-wrap items-center gap-2">
      <label for="dm-text-input" class="sr-only">私密文本消息</label>
      <input
        id="dm-text-input"
        v-model="textInput"
        placeholder="发送私密文本（不写入时间线）..."
        class="min-w-0 flex-1 rounded-md border border-slate-700 bg-slate-950 px-3 py-1.5 text-xs text-slate-100 outline-none focus:border-sky-500"
        @keydown.enter="onTextKeydown"
      />
      <button
        type="button"
        class="rounded-md bg-sky-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-sky-500"
        @click="submitText"
      >
        发文本
      </button>
      <input ref="fileInputEl" type="file" multiple class="hidden" @change="onPickFiles" />
      <button
        type="button"
        class="rounded-md border border-emerald-500/50 bg-emerald-500/10 px-3 py-1.5 text-xs font-medium text-emerald-300 hover:bg-emerald-500/20"
        @click="fileInputEl?.click()"
      >
        选文件/拖拽直传
      </button>
    </div>
  </section>
</template>
