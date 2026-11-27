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
    class="flex flex-col bg-[var(--bg-canvas)]"
    @dragover.prevent
    @drop.stop="onDropFiles"
  >
    <div class="flex items-center justify-between border-b border-[var(--border)] px-5 py-3.5">
      <div class="flex items-baseline gap-2.5">
        <h2 class="text-sm font-semibold text-[var(--text-primary)]">共享时间线</h2>
        <span class="tabular-nums text-xs text-[var(--text-tertiary)]">≤ {{ maxTimelineFileMB }} MB</span>
      </div>
      <button
        v-if="space.sharedTimelineEnabled && timeline.length > 0"
        data-testid="timeline-clear-btn"
        type="button"
        class="rounded-md px-2 py-1 text-xs text-[var(--text-secondary)] transition-colors hover:bg-[var(--danger-bg)] hover:text-[var(--danger)]"
        @click="confirmClearOpen = true"
      >
        清空
      </button>
    </div>

    <div
      v-if="confirmClearOpen"
      role="alertdialog"
      aria-label="确认清空共享时间线"
      class="flex flex-wrap items-center justify-between gap-2 border-b border-[var(--border)] px-5 py-2.5 text-xs"
      style="background-color: var(--danger-bg); color: var(--danger)"
    >
      <span>确认清空当前空间的全部共享时间线内容吗？此操作不可撤销。</span>
      <div class="flex items-center gap-2">
        <button
          data-testid="timeline-clear-confirm"
          type="button"
          class="rounded-md px-2.5 py-1 font-medium text-white"
          style="background-color: var(--danger)"
          @click="confirmClear"
        >
          确认清空
        </button>
        <button
          type="button"
          class="rounded-md border border-[var(--border-strong)] bg-[var(--bg-surface)] px-2.5 py-1 text-[var(--text-primary)]"
          @click="confirmClearOpen = false"
        >
          取消
        </button>
      </div>
    </div>

    <div v-if="!space.sharedTimelineEnabled" class="flex flex-1 flex-col items-center justify-center px-6 py-16 text-center">
      <div class="mb-3 flex h-10 w-10 items-center justify-center rounded-full bg-[var(--bg-subtle)] text-[var(--text-secondary)]">
        <svg
          aria-hidden="true"
          class="h-5 w-5"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.6"
          stroke-linecap="round"
          stroke-linejoin="round"
        >
          <rect x="4" y="11" width="16" height="10" rx="2" />
          <path d="M8 11V7a4 4 0 0 1 8 0v4" />
        </svg>
      </div>
      <p class="text-sm font-medium text-[var(--text-primary)]">局域网默认关闭共享时间线</p>
      <p class="mt-1.5 max-w-sm text-xs leading-relaxed text-[var(--text-secondary)]">
        保护公共网络剪贴板隐私，开启 6 位码私人房间即可跨端暂存图文。
      </p>
      <button
        data-testid="timeline-enable-room-btn"
        type="button"
        class="mt-5 rounded-lg bg-[var(--btn-primary-bg)] px-4 py-2 text-xs font-medium text-[var(--btn-primary-text)] transition-colors hover:bg-[var(--btn-primary-hover)]"
        @click="emit('create-room')"
      >
        开启 6 位码私人房间
      </button>
    </div>

    <template v-else>
      <div class="native-scroll flex-1 overflow-y-auto px-5 py-5">
        <div class="mx-auto flex max-w-3xl flex-col gap-5">
          <div class="hidden rounded-xl border border-[var(--border-strong)] bg-[var(--bg-surface)] transition-colors focus-within:border-[var(--accent)] md:block">
            <label for="timeline-draft-input" class="sr-only">发送文字到共享时间线</label>
            <textarea
              id="timeline-draft-input"
              v-model="draftText"
              rows="3"
              placeholder="输入或粘贴文字、截图（⌘V），或直接拖入文件..."
              class="w-full resize-none bg-transparent px-3.5 pt-3 pb-2 text-sm leading-relaxed text-[var(--text-primary)] placeholder:text-[var(--text-tertiary)] focus:outline-none"
              @paste="onPaste"
              @keydown.ctrl.enter.prevent="submitText"
              @keydown.meta.enter.prevent="submitText"
            />
            <div class="flex items-center justify-between border-t border-[var(--border)] px-3 py-2">
              <div class="flex items-center gap-3">
                <input ref="fileInputEl" type="file" multiple class="hidden" @change="onPickFiles" />
                <button
                  type="button"
                  class="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
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
                  <span>添加文件或图片</span>
                </button>
                <span class="text-[11px] text-[var(--text-tertiary)]">⌘Enter 发送</span>
              </div>

              <button
                type="button"
                class="rounded-lg bg-[var(--btn-primary-bg)] px-3.5 py-1 text-xs font-medium text-[var(--btn-primary-text)] transition-colors hover:bg-[var(--btn-primary-hover)]"
                @click="submitText"
              >
                发送
              </button>
            </div>
          </div>

          <div v-if="timeline.length === 0" class="py-14 text-center text-xs text-[var(--text-tertiary)]">
            暂无暂存内容，发送文字或上传 ≤{{ maxTimelineFileMB }} MB 图片与文件
          </div>

          <div v-else class="space-y-3">
            <article
              v-for="item in timeline"
              :key="item.id"
              class="group rounded-xl border border-[var(--border)] bg-[var(--bg-surface)] px-4 py-3.5 transition-colors hover:border-[var(--border-strong)]"
            >
              <div class="mb-2 flex items-center justify-between text-[11px] text-[var(--text-tertiary)]">
                <span class="font-medium text-[var(--text-secondary)]">{{ item.senderName }}</span>
                <div class="flex items-center gap-3 transition-opacity md:opacity-60 md:group-hover:opacity-100 md:group-focus-within:opacity-100">
                  <button
                    v-if="item.kind === 'text' && item.text"
                    data-testid="timeline-copy-btn"
                    type="button"
                    class="hover:text-[var(--text-primary)]"
                    @click="emit('copy-text', item.text)"
                  >
                    复制
                  </button>
                  <button
                    v-if="item.blobId"
                    type="button"
                    class="hover:text-[var(--text-primary)]"
                    @click="emit('download-item', item)"
                  >
                    下载
                  </button>
                  <button
                    type="button"
                    class="hover:text-[var(--danger)]"
                    @click="emit('delete-item', item.id)"
                  >
                    删除
                  </button>
                </div>
              </div>

              <p v-if="item.kind === 'text' && item.text" class="whitespace-pre-wrap break-words text-sm leading-relaxed text-[var(--text-primary)]">
                <template v-for="(seg, idx) in parseTextSegments(item.text)" :key="idx">
                  <a
                    v-if="seg.kind === 'link'"
                    :href="seg.href"
                    target="_blank"
                    rel="noopener noreferrer"
                    class="text-[var(--accent)] underline underline-offset-2"
                  >{{ seg.text }}</a>
                  <span v-else>{{ seg.text }}</span>
                </template>
              </p>

              <div v-else-if="item.kind === 'image' && item.blobId" class="space-y-2">
                <img
                  v-if="blobPreviewUrls[item.blobId]"
                  :src="blobPreviewUrls[item.blobId]"
                  :alt="item.fileName ?? 'image'"
                  class="max-h-72 rounded-lg border border-[var(--border)] object-contain"
                />
                <div v-else class="text-xs text-[var(--text-tertiary)]">图片加载中...</div>
                <div class="tabular-nums text-[11px] text-[var(--text-tertiary)]">
                  {{ item.fileName }} · {{ formatBytes(item.size ?? 0) }}
                </div>
              </div>

              <div
                v-else-if="item.kind === 'file'"
                class="flex items-center justify-between gap-3 rounded-lg bg-[var(--bg-subtle)] px-3 py-2.5"
              >
                <div class="flex min-w-0 items-center gap-2.5">
                  <svg
                    aria-hidden="true"
                    class="h-4 w-4 shrink-0 text-[var(--text-secondary)]"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.6"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                  >
                    <path d="M14.5 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7.5L14.5 2z" />
                    <polyline points="14 2 14 8 20 8" />
                  </svg>
                  <div class="min-w-0">
                    <div class="truncate text-xs font-medium text-[var(--text-primary)]">{{ item.fileName }}</div>
                    <div class="tabular-nums text-[11px] text-[var(--text-tertiary)]">{{ formatBytes(item.size ?? 0) }}</div>
                  </div>
                </div>
                <button
                  type="button"
                  class="shrink-0 rounded-md border border-[var(--border-strong)] bg-[var(--bg-surface)] px-2.5 py-1 text-xs font-medium text-[var(--text-primary)] transition-colors hover:bg-[var(--bg-hover)]"
                  @click="emit('download-item', item)"
                >
                  下载
                </button>
              </div>
            </article>
          </div>
        </div>
      </div>
    </template>
  </section>
</template>
