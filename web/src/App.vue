<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { createSessionStore } from './stores/session'
import { copyText } from './lib/device'
import RoomBar from './components/RoomBar.vue'
import DeviceRadar from './components/DeviceRadar.vue'
import DirectPanel from './components/DirectPanel.vue'
import TimelinePanel from './components/TimelinePanel.vue'
import TransferTray from './components/TransferTray.vue'

const store = createSessionStore()
const state = store.state
const activeTab = ref<'radar' | 'timeline'>('radar')
const mobileDraft = ref('')
const mobileFileInput = ref<HTMLInputElement | null>(null)
const dragDepth = ref(0)

const selectedPeer = computed(
  () => state.peers.find((p) => p.id === state.selectedPeerId) ?? null
)

const showMobileQuickBar = computed(
  () => activeTab.value === 'timeline' && state.space.sharedTimelineEnabled
)

onMounted(() => {
  void store.start()
})

onUnmounted(() => {
  store.destroy()
})

async function handleCopy(text: string): Promise<void> {
  const ok = await copyText(text)
  store.showToast(ok ? '已复制到剪贴板' : '复制失败，请手动选择复制', ok ? 'success' : 'error')
}

function submitMobileDraft(): void {
  if (store.postTimelineText(mobileDraft.value)) {
    mobileDraft.value = ''
  }
}

function onMobileDraftKeydown(ev: KeyboardEvent): void {
  if (ev.isComposing || ev.keyCode === 229) return
  ev.preventDefault()
  submitMobileDraft()
}

function onMobilePickFiles(ev: Event): void {
  const input = ev.target as HTMLInputElement
  for (const file of Array.from(input.files ?? [])) {
    void store.uploadTimelineFile(file)
  }
  input.value = ''
}

function onGlobalDragEnter(ev: DragEvent): void {
  if (ev.dataTransfer?.types && !Array.from(ev.dataTransfer.types).includes('Files')) {
    return
  }
  dragDepth.value += 1
}

function onGlobalDragLeave(): void {
  dragDepth.value = Math.max(0, dragDepth.value - 1)
}

async function onGlobalDrop(ev: DragEvent): Promise<void> {
  ev.preventDefault()
  dragDepth.value = 0
  const files = Array.from(ev.dataTransfer?.files ?? [])
  if (files.length === 0) return

  if (selectedPeer.value) {
    store.sendFiles(selectedPeer.value.id, files)
    return
  }

  if (!state.space.sharedTimelineEnabled) {
    store.showToast('当前局域网默认关闭共享时间线，请选择在线设备 P2P/中转直传，或开启 6 位码房间', 'error')
    return
  }

  const maxBytes = state.limits.maxTimelineFileMB * 1024 * 1024
  const eligible = files.filter((file) => file.size <= maxBytes)
  const hasOversized = files.some((file) => file.size > maxBytes)

  if (eligible.length > 0) {
    await Promise.all(eligible.map((f) => store.uploadTimelineFile(f)))
  }

  if (hasOversized) {
    store.showToast(
      `文件超出时间线 ${state.limits.maxTimelineFileMB} MB 上限，请选择在线设备进行 P2P/中转直传`,
      'error'
    )
  }
}
</script>

<template>
  <div
    data-testid="app-root"
    class="relative flex min-h-screen flex-col bg-[var(--bg-canvas)] text-[var(--text-primary)] md:h-dvh md:overflow-hidden"
    @dragenter="onGlobalDragEnter"
    @dragleave="onGlobalDragLeave"
    @dragover.prevent
    @drop="onGlobalDrop"
  >
    <div
      v-if="dragDepth > 0"
      aria-hidden="true"
      class="pointer-events-none fixed inset-2 z-50 flex items-start justify-center rounded-2xl border-2 border-dashed border-[var(--accent)] bg-[var(--accent)]/[0.03] pt-16"
    >
      <span
        class="rounded-full border border-[var(--border)] bg-[var(--bg-surface)] px-3.5 py-1.5 text-xs font-medium text-[var(--text-primary)]"
        style="box-shadow: var(--shadow-popover)"
      >
        {{ selectedPeer ? `松开以直传给 ${selectedPeer.name}` : '松开以上传到共享时间线' }}
      </span>
    </div>

    <RoomBar
      :self="state.self"
      :space="state.space"
      :ws-status="state.wsStatus"
      :room-modal-open="state.roomModalOpen"
      @update:nickname="store.updateNickname"
      @create-room="store.createRoom"
      @join-room="store.joinRoom"
      @return-lan="store.returnToLan"
      @toggle-room-modal="state.roomModalOpen = $event"
      @copy-text="handleCopy"
    />

    <div
      role="region"
      aria-label="系统提示"
      aria-live="polite"
      class="pointer-events-none fixed top-16 right-4 z-40"
    >
      <div
        v-if="state.toast"
        :role="state.toast.kind === 'error' ? 'alert' : 'status'"
        class="pointer-events-auto flex items-center gap-2 rounded-full border border-[var(--border)] bg-[var(--bg-surface)]/95 px-3.5 py-2 text-xs text-[var(--text-primary)] backdrop-blur-md"
        style="box-shadow: var(--shadow-popover)"
      >
        <span
          class="h-1.5 w-1.5 shrink-0 rounded-full"
          :style="{
            backgroundColor:
              state.toast.kind === 'error'
                ? 'var(--danger)'
                : state.toast.kind === 'success'
                  ? 'var(--success)'
                  : 'var(--accent)'
          }"
        />
        <span>{{ state.toast.message }}</span>
      </div>
    </div>

    <div class="border-b border-[var(--border)] bg-[var(--bg-canvas)] px-4 py-2 md:hidden">
      <div class="grid grid-cols-2 rounded-lg bg-[var(--bg-subtle)] p-0.5" role="tablist" aria-label="功能视图切换">
        <button
          data-testid="tab-radar"
          type="button"
          role="tab"
          :aria-selected="activeTab === 'radar'"
          class="rounded-[7px] py-1.5 text-xs font-medium transition-colors"
          :class="
            activeTab === 'radar'
              ? 'bg-[var(--bg-surface)] text-[var(--text-primary)] shadow-xs'
              : 'text-[var(--text-secondary)]'
          "
          @click="activeTab = 'radar'"
        >
          附近设备
        </button>
        <button
          data-testid="tab-timeline"
          type="button"
          role="tab"
          :aria-selected="activeTab === 'timeline'"
          class="rounded-[7px] py-1.5 text-xs font-medium transition-colors"
          :class="
            activeTab === 'timeline'
              ? 'bg-[var(--bg-surface)] text-[var(--text-primary)] shadow-xs'
              : 'text-[var(--text-secondary)]'
          "
          @click="activeTab = 'timeline'"
        >
          共享时间线
        </button>
      </div>
    </div>

    <main class="flex min-h-0 flex-1 flex-col md:flex-row">
      <aside
        class="flex flex-col md:w-72 md:shrink-0 md:border-r md:border-[var(--border)] md:bg-[var(--bg-sidebar)] lg:w-80"
        :class="activeTab === 'radar' ? 'flex-1 md:flex-initial' : 'hidden md:flex'"
      >
        <div class="hidden border-b border-[var(--border)] p-2.5 md:block">
          <button
            type="button"
            class="flex w-full items-center justify-between rounded-lg px-2.5 py-2 text-left text-[13px] font-medium transition-colors"
            :class="
              !selectedPeer
                ? 'bg-[var(--bg-active)] text-[var(--accent)]'
                : 'text-[var(--text-primary)] hover:bg-[var(--bg-hover)]'
            "
            @click="store.selectPeer(null)"
          >
            <span class="flex items-center gap-2.5">
              <svg
                aria-hidden="true"
                class="h-4 w-4 shrink-0"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                stroke-width="1.75"
                stroke-linecap="round"
                stroke-linejoin="round"
              >
                <rect x="4" y="3" width="16" height="18" rx="2" />
                <path d="M8 7h8" />
                <path d="M8 11h8" />
                <path d="M8 15h5" />
              </svg>
              <span>共享时间线</span>
            </span>
            <span
              class="tabular-nums text-[11px]"
              :class="!selectedPeer ? 'text-[var(--accent)]' : 'text-[var(--text-tertiary)]'"
            >
              {{ state.space.sharedTimelineEnabled ? state.timeline.length : '已关闭' }}
            </span>
          </button>
        </div>

        <div
          class="native-scroll flex-1 overflow-y-auto p-3"
          :class="selectedPeer ? 'hidden md:block' : 'block'"
        >
          <DeviceRadar
            :peers="state.peers"
            :self-id="state.self?.id ?? null"
            :selected-peer-id="state.selectedPeerId"
            @select-peer="store.selectPeer"
            @drop-files="store.sendFiles"
          />
        </div>
      </aside>

      <section
        class="flex min-h-0 min-w-0 flex-1 flex-col bg-[var(--bg-canvas)]"
        :class="activeTab === 'timeline' || selectedPeer ? 'flex' : 'hidden md:flex'"
      >
        <DirectPanel
          v-if="selectedPeer"
          :key="selectedPeer.id"
          :peer="selectedPeer"
          :messages="state.dmsByPeer[selectedPeer.id] ?? []"
          class="min-h-0 flex-1"
          :class="activeTab === 'radar' ? 'flex' : 'hidden md:flex'"
          @close="store.selectPeer(null)"
          @send-text="store.sendDM"
          @send-files="store.sendFiles"
        />

        <TimelinePanel
          :space="state.space"
          :timeline="state.timeline"
          :blob-preview-urls="state.blobPreviewUrls"
          :max-timeline-file-m-b="state.limits.maxTimelineFileMB"
          class="min-h-0 flex-1"
          :class="[
            selectedPeer ? 'md:hidden' : 'md:flex',
            activeTab === 'timeline' ? 'flex' : 'hidden',
            showMobileQuickBar ? 'pb-16 md:pb-0' : ''
          ]"
          @post-text="store.postTimelineText"
          @upload-file="store.uploadTimelineFile"
          @delete-item="store.deleteTimelineItem"
          @clear-timeline="store.clearTimeline"
          @download-item="store.downloadTimelineBlob"
          @copy-text="handleCopy"
          @create-room="store.createRoom"
        />
      </section>
    </main>

    <div
      v-if="showMobileQuickBar"
      data-testid="mobile-quick-bar"
      class="fixed right-0 bottom-0 left-0 z-20 border-t border-[var(--border)] bg-[var(--bg-surface)]/95 px-3 py-2.5 backdrop-blur-md md:hidden"
    >
      <div class="flex items-center gap-2">
        <label for="mobile-quick-timeline-input" class="sr-only">发送文字到共享时间线</label>
        <input
          id="mobile-quick-timeline-input"
          v-model="mobileDraft"
          data-testid="mobile-quick-input"
          placeholder="发送文字到共享时间线..."
          class="min-w-0 flex-1 rounded-lg border border-[var(--border-strong)] bg-[var(--bg-canvas)] px-3 py-1.5 text-xs text-[var(--text-primary)] placeholder:text-[var(--text-tertiary)] focus:border-[var(--accent)] focus:outline-none"
          @keydown.enter="onMobileDraftKeydown"
        />
        <input ref="mobileFileInput" type="file" multiple class="hidden" @change="onMobilePickFiles" />
        <button
          type="button"
          class="rounded-lg border border-[var(--border-strong)] px-2.5 py-1.5 text-xs text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]"
          @click="mobileFileInput?.click()"
        >
          文件/图
        </button>
        <button
          data-testid="mobile-quick-submit"
          type="button"
          class="rounded-lg bg-[var(--btn-primary-bg)] px-3 py-1.5 text-xs font-medium text-[var(--btn-primary-text)] hover:bg-[var(--btn-primary-hover)]"
          @click="submitMobileDraft"
        >
          发送
        </button>
      </div>
    </div>

    <TransferTray
      :transfers="state.transfers"
      :is-room="state.space.kind === 'room'"
      :auto-accept-in-room="state.autoAcceptInRoom"
      :mobile-bar-visible="showMobileQuickBar"
      @update:auto-accept-in-room="state.autoAcceptInRoom = $event"
      @accept="store.acceptTransfer"
      @reject="store.rejectTransfer"
      @cancel="store.cancelTransfer"
      @dismiss="store.dismissTransfer"
      @download-file="store.triggerSavedFileDownload"
    />
  </div>
</template>
