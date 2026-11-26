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

async function onGlobalDrop(ev: DragEvent): Promise<void> {
  ev.preventDefault()
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
    class="flex min-h-screen flex-col bg-slate-950 text-slate-100"
    @dragover.prevent
    @drop="onGlobalDrop"
  >
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
        class="pointer-events-auto rounded-lg border px-3.5 py-2 text-xs shadow-xl"
        :class="
          state.toast.kind === 'error'
            ? 'border-rose-500/50 bg-rose-950/90 text-rose-100'
            : state.toast.kind === 'success'
              ? 'border-emerald-500/50 bg-emerald-950/90 text-emerald-100'
              : 'border-slate-700 bg-slate-900/95 text-slate-100'
        "
      >
        {{ state.toast.message }}
      </div>
    </div>

    <div class="mx-auto flex w-full max-w-6xl flex-1 flex-col gap-4 p-4 pb-24">
      <div class="grid grid-cols-2 gap-2 md:hidden" role="tablist" aria-label="功能视图切换">
        <button
          data-testid="tab-radar"
          type="button"
          role="tab"
          :aria-selected="activeTab === 'radar'"
          class="rounded-lg py-2 text-xs font-medium"
          :class="activeTab === 'radar' ? 'bg-sky-600 text-white' : 'bg-slate-900 text-slate-400'"
          @click="activeTab = 'radar'"
        >
          附近设备
        </button>
        <button
          data-testid="tab-timeline"
          type="button"
          role="tab"
          :aria-selected="activeTab === 'timeline'"
          class="rounded-lg py-2 text-xs font-medium"
          :class="activeTab === 'timeline' ? 'bg-sky-600 text-white' : 'bg-slate-900 text-slate-400'"
          @click="activeTab = 'timeline'"
        >
          共享时间线
        </button>
      </div>

      <main class="grid flex-1 gap-4 md:grid-cols-12">
        <div class="space-y-4 md:col-span-5" :class="activeTab === 'radar' ? 'block' : 'hidden md:block'">
          <DeviceRadar
            :peers="state.peers"
            :self-id="state.self?.id ?? null"
            :selected-peer-id="state.selectedPeerId"
            @select-peer="store.selectPeer"
            @drop-files="store.sendFiles"
          />
          <DirectPanel
            v-if="selectedPeer"
            :key="selectedPeer.id"
            :peer="selectedPeer"
            :messages="state.dmsByPeer[selectedPeer.id] ?? []"
            @close="store.selectPeer(null)"
            @send-text="store.sendDM"
            @send-files="store.sendFiles"
          />
        </div>

        <div class="md:col-span-7" :class="activeTab === 'timeline' ? 'block' : 'hidden md:block'">
          <TimelinePanel
            :space="state.space"
            :timeline="state.timeline"
            :blob-preview-urls="state.blobPreviewUrls"
            :max-timeline-file-m-b="state.limits.maxTimelineFileMB"
            @post-text="store.postTimelineText"
            @upload-file="store.uploadTimelineFile"
            @delete-item="store.deleteTimelineItem"
            @clear-timeline="store.clearTimeline"
            @download-item="store.downloadTimelineBlob"
            @copy-text="handleCopy"
            @create-room="store.createRoom"
          />
        </div>
      </main>
    </div>

    <div
      v-if="showMobileQuickBar"
      data-testid="mobile-quick-bar"
      class="fixed right-0 bottom-0 left-0 z-20 border-t border-slate-800 bg-slate-950/95 p-3 md:hidden"
    >
      <div class="flex items-center gap-2">
        <label for="mobile-quick-timeline-input" class="sr-only">发送文字到共享时间线</label>
        <input
          id="mobile-quick-timeline-input"
          v-model="mobileDraft"
          data-testid="mobile-quick-input"
          placeholder="发送文字到共享时间线..."
          class="min-w-0 flex-1 rounded-md border border-slate-700 bg-slate-900 px-3 py-2 text-xs text-slate-100"
          @keydown.enter="onMobileDraftKeydown"
        />
        <input ref="mobileFileInput" type="file" multiple class="hidden" @change="onMobilePickFiles" />
        <button
          type="button"
          class="rounded-md border border-slate-700 px-2.5 py-2 text-xs text-slate-200"
          @click="mobileFileInput?.click()"
        >
          文件/图
        </button>
        <button
          data-testid="mobile-quick-submit"
          type="button"
          class="rounded-md bg-sky-600 px-3 py-2 text-xs font-medium text-white"
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
