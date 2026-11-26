<script setup lang="ts">
import { ref, watch } from 'vue'
import type { SpaceSummary, TimelineItem } from '../lib/protocol'
import { formatBytes, parseTextSegments } from '../lib/device'

const props = defineProps<{
  space: SpaceSummary
  timeline: TimelineItem[]
  blobPreviewUrls: Record<string, string>
  maxTimelineFileMB: number
}>()

const emit = defineEmits<{
  'post-text': [text: string]
  'upload-file': [file: File]
  'delete-item': [itemId: string]
  'clear-timeline': []
  'download-item': [item: TimelineItem]
  'copy-text': [text: string]
  'create-room': []
}>()

const draftText = ref('')
const confirmClearOpen = ref(false)
const fileInputEl = ref<HTMLInputElement | null>(null)

watch(
  () => [props.space.kind, props.space.code, props.timeline.length] as const,
  ([, , len]) => {
    if (len === 0) {
      confirmClearOpen.value = false
    }
  }
)

watch(
  () => [props.space.kind, props.space.code] as const,
  () => {
    confirmClearOpen.value = false
  }
)

function submitText(): void {
  if (!draftText.value.trim()) return
  emit('post-text', draftText.value)
  draftText.value = ''
}

function onPaste(ev: ClipboardEvent): void {
  const files = Array.from(ev.clipboardData?.files ?? [])
  if (files.length > 0) {
    ev.preventDefault()
    for (const file of files) {
      emit('upload-file', file)
    }
  }
}

function onPickFiles(ev: Event): void {
  const input = ev.target as HTMLInputElement
  for (const file of Array.from(input.files ?? [])) {
    emit('upload-file', file)
  }
  input.value = ''
}

function onDropFiles(ev: DragEvent): void {
  ev.preventDefault()
  for (const file of Array.from(ev.dataTransfer?.files ?? [])) {
    emit('upload-file', file)
  }
}

function confirmClear(): void {
  confirmClearOpen.value = false
  emit('clear-timeline')
}
</script>

<template>
  <section
    data-testid="timeline-panel"
    class="flex flex-col gap-3 rounded-xl border border-slate-800 bg-slate-900/60 p-4"
    @dragover.prevent
    @drop.stop="onDropFiles"
  >
    <div class="flex items-center justify-between">
      <div>
        <h2 class="text-sm font-semibold text-slate-100">共享时间线（文本 / 图片 / ≤{{ maxTimelineFileMB }}MB 小文件）</h2>
        <p class="text-xs text-slate-400">支持离线后取，按空间隔离并在 TTL 到期后自动清理</p>
      </div>
      <button
        v-if="space.sharedTimelineEnabled && timeline.length > 0"
        data-testid="timeline-clear-btn"
        type="button"
        class="rounded border border-rose-500/40 px-2 py-1 text-xs text-rose-300 hover:bg-rose-500/10"
        @click="confirmClearOpen = true"
      >
        清空
      </button>
    </div>

    <div
      v-if="confirmClearOpen"
      role="alertdialog"
      aria-label="确认清空共享时间线"
      class="flex items-center justify-between rounded-lg border border-rose-500/40 bg-rose-950/40 px-3 py-2 text-xs text-rose-200"
    >
      <span>确认清空当前空间的全部共享时间线内容吗？此操作不可撤销。</span>
      <div class="flex gap-2">
        <button
          data-testid="timeline-clear-confirm"
          type="button"
          class="rounded bg-rose-600 px-2.5 py-1 font-medium text-white"
          @click="confirmClear"
        >
          确认清空
        </button>
        <button
          type="button"
          class="rounded border border-slate-700 px-2.5 py-1 text-slate-300"
          @click="confirmClearOpen = false"
        >
          取消
        </button>
      </div>
    </div>

    <div
      v-if="!space.sharedTimelineEnabled"
      class="space-y-3 rounded-xl border border-dashed border-slate-700 bg-slate-950/60 p-6 text-center"
    >
      <p class="text-sm font-medium text-slate-200">默认局域网空间已关闭共享时间线</p>
      <p class="text-xs text-slate-400">
        为防止公司或公共 Wi-Fi 下误泄露剪贴板，局域网默认仅开放设备雷达点对点互传。开启 6 位码私人房间即可使用跨设备图文暂存。
      </p>
      <button
        data-testid="timeline-enable-room-btn"
        type="button"
        class="rounded-md bg-sky-600 px-4 py-1.5 text-xs font-medium text-white hover:bg-sky-500"
        @click="emit('create-room')"
      >
        一键开启 6 位码私人房间
      </button>
    </div>

    <template v-else>
      <div class="hidden gap-2 md:flex">
        <label for="timeline-draft-input" class="sr-only">发送文字到共享时间线</label>
        <textarea
          id="timeline-draft-input"
          v-model="draftText"
          rows="2"
          placeholder="粘贴文字、截图（Ctrl/Cmd+V）或拖拽小文件到此处..."
          class="flex-1 resize-none rounded-lg border border-slate-700 bg-slate-950 px-3 py-2 text-xs text-slate-100 outline-none focus:border-sky-500"
          @paste="onPaste"
          @keydown.ctrl.enter.prevent="submitText"
          @keydown.meta.enter.prevent="submitText"
        />
        <div class="flex flex-col gap-1.5">
          <button
            type="button"
            class="rounded-md bg-sky-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-sky-500"
            @click="submitText"
          >
            发送
          </button>
          <input ref="fileInputEl" type="file" multiple class="hidden" @change="onPickFiles" />
          <button
            type="button"
            class="rounded-md border border-slate-700 px-3 py-1.5 text-xs text-slate-200 hover:border-slate-500"
            @click="fileInputEl?.click()"
          >
            传文件/图
          </button>
        </div>
      </div>

      <div class="max-h-[28rem] space-y-2.5 overflow-y-auto">
        <div v-if="timeline.length === 0" class="rounded-lg border border-dashed border-slate-800 p-6 text-center text-xs text-slate-500">
          暂无暂存内容，可发送文字或上传 ≤{{ maxTimelineFileMB }} MB 图片与文件
        </div>

        <article
          v-for="item in timeline"
          :key="item.id"
          class="rounded-lg border border-slate-800 bg-slate-950/75 p-3 text-xs"
        >
          <div class="mb-2 flex items-center justify-between text-[11px] text-slate-400">
            <span>{{ item.senderName }}</span>
            <div class="flex items-center gap-2">
              <button
                v-if="item.kind === 'text' && item.text"
                data-testid="timeline-copy-btn"
                type="button"
                class="text-sky-400 hover:underline"
                @click="emit('copy-text', item.text)"
              >
                复制
              </button>
              <button
                v-if="item.blobId"
                type="button"
                class="text-emerald-400 hover:underline"
                @click="emit('download-item', item)"
              >
                下载
              </button>
              <button type="button" class="text-slate-500 hover:text-rose-400" @click="emit('delete-item', item.id)">
                删除
              </button>
            </div>
          </div>

          <p v-if="item.kind === 'text' && item.text" class="whitespace-pre-wrap break-words text-slate-100">
            <template v-for="(seg, idx) in parseTextSegments(item.text)" :key="idx">
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

          <div v-else-if="item.kind === 'image' && item.blobId" class="space-y-1.5">
            <img
              v-if="blobPreviewUrls[item.blobId]"
              :src="blobPreviewUrls[item.blobId]"
              :alt="item.fileName ?? 'image'"
              class="max-h-64 rounded-md border border-slate-800 object-contain"
            />
            <div v-else class="text-slate-500">图片加载中...</div>
            <div class="text-[11px] text-slate-400">{{ item.fileName }} ({{ formatBytes(item.size ?? 0) }})</div>
          </div>

          <div v-else-if="item.kind === 'file'" class="flex items-center justify-between rounded-md bg-slate-900 px-3 py-2">
            <div>
              <div class="font-medium text-slate-100">{{ item.fileName }}</div>
              <div class="text-[11px] text-slate-400">{{ formatBytes(item.size ?? 0) }}</div>
            </div>
            <button
              type="button"
              class="rounded bg-emerald-600/20 px-2.5 py-1 text-emerald-300 hover:bg-emerald-600/30"
              @click="emit('download-item', item)"
            >
              安全下载
            </button>
          </div>
        </article>
      </div>
    </template>
  </section>
</template>
