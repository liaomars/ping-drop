<script setup lang="ts">
import { computed } from 'vue'
import type { PeerInfo } from '../lib/protocol'

const props = defineProps<{
  peers: PeerInfo[]
  selfId: string | null
  selectedPeerId: string | null
}>()

const emit = defineEmits<{
  'select-peer': [peerId: string]
  'drop-files': [peerId: string, files: File[]]
}>()

const otherPeers = computed(() => props.peers.filter((p) => p.id !== props.selfId))

function onDrop(peerId: string, ev: DragEvent): void {
  ev.preventDefault()
  const files = Array.from(ev.dataTransfer?.files ?? [])
  if (files.length > 0) {
    emit('drop-files', peerId, files)
  }
}
</script>

<template>
  <section class="flex flex-col gap-3 rounded-xl border border-slate-800 bg-slate-900/60 p-4">
    <div class="flex items-center justify-between">
      <h2 class="text-sm font-semibold text-slate-100">附近 / 房间在线设备 ({{ otherPeers.length }})</h2>
      <span class="text-xs text-slate-400">点击设备发私信或传大文件</span>
    </div>

    <div v-if="otherPeers.length === 0" class="rounded-lg border border-dashed border-slate-800 p-6 text-center text-xs text-slate-400">
      当前空间暂无其他在线设备。可在另一台设备打开同页或使用顶部「跨网配对」加入房间。
    </div>

    <div class="grid gap-2.5 sm:grid-cols-2 md:grid-cols-1">
      <button
        v-for="peer in otherPeers"
        :key="peer.id"
        :data-testid="`peer-card-${peer.id}`"
        type="button"
        :aria-pressed="selectedPeerId === peer.id"
        class="flex items-center justify-between rounded-lg border p-3 text-left transition"
        :class="
          selectedPeerId === peer.id
            ? 'border-sky-500 bg-sky-500/10'
            : 'border-slate-800 bg-slate-950/70 hover:border-slate-600'
        "
        @click="emit('select-peer', peer.id)"
        @dragover.prevent
        @drop.stop="onDrop(peer.id, $event)"
      >
        <div>
          <div class="text-sm font-medium text-slate-100">{{ peer.name }}</div>
          <div class="text-xs text-slate-400">{{ peer.os }} · {{ peer.browser }}</div>
        </div>
        <span class="rounded bg-slate-800 px-2 py-0.5 text-xs text-sky-300">
          {{ peer.deviceType === 'mobile' ? '手机' : '桌面' }}
        </span>
      </button>
    </div>
  </section>
</template>
