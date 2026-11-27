<script setup lang="ts">
import { computed, ref } from 'vue'
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

const dragOverPeerId = ref<string | null>(null)
const otherPeers = computed(() => props.peers.filter((p) => p.id !== props.selfId))

function onDragOver(peerId: string): void {
  dragOverPeerId.value = peerId
}

function onDragLeave(peerId: string): void {
  if (dragOverPeerId.value === peerId) {
    dragOverPeerId.value = null
  }
}

function onDrop(peerId: string, ev: DragEvent): void {
  ev.preventDefault()
  dragOverPeerId.value = null
  const files = Array.from(ev.dataTransfer?.files ?? [])
  if (files.length > 0) {
    emit('drop-files', peerId, files)
  }
}
</script>

<template>
  <section class="flex flex-col gap-2">
    <div class="flex items-center justify-between px-2">
      <h2 class="text-[11px] font-medium tracking-wide text-[var(--text-tertiary)]">
        在线设备 ({{ otherPeers.length }})
      </h2>
    </div>

    <div v-if="otherPeers.length === 0" class="px-2 py-8 text-center">
      <p class="text-xs font-medium text-[var(--text-secondary)]">暂无其他在线设备</p>
      <p class="mt-1 text-[11px] leading-relaxed text-[var(--text-tertiary)]">
        同局域网打开本页，或使用右上角「跨网配对」
      </p>
    </div>

    <div v-else class="space-y-0.5">
      <button
        v-for="peer in otherPeers"
        :key="peer.id"
        :data-testid="`peer-card-${peer.id}`"
        type="button"
        :aria-pressed="selectedPeerId === peer.id"
        class="flex w-full items-center gap-2.5 rounded-lg border px-2.5 py-2 text-left transition-colors"
        :class="[
          dragOverPeerId === peer.id
            ? 'border-[var(--accent)] bg-[var(--bg-active)]'
            : selectedPeerId === peer.id
              ? 'border-transparent bg-[var(--bg-active)]'
              : 'border-transparent hover:bg-[var(--bg-hover)]'
        ]"
        @click="emit('select-peer', peer.id)"
        @dragover.prevent="onDragOver(peer.id)"
        @dragleave="onDragLeave(peer.id)"
        @drop.stop="onDrop(peer.id, $event)"
      >
        <span
          class="flex h-8 w-8 shrink-0 items-center justify-center rounded-full"
          :class="
            selectedPeerId === peer.id
              ? 'bg-[var(--accent)] text-white'
              : 'bg-[var(--bg-subtle)] text-[var(--text-secondary)]'
          "
        >
          <svg
            v-if="peer.deviceType === 'mobile'"
            aria-hidden="true"
            class="h-4 w-4"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="1.6"
            stroke-linecap="round"
            stroke-linejoin="round"
          >
            <rect x="7" y="2" width="10" height="20" rx="2" />
            <path d="M12 18h.01" />
          </svg>
          <svg
            v-else
            aria-hidden="true"
            class="h-4 w-4"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="1.6"
            stroke-linecap="round"
            stroke-linejoin="round"
          >
            <rect x="3" y="4" width="18" height="12" rx="2" />
            <path d="M2 20h20" />
          </svg>
        </span>

        <div class="min-w-0 flex-1">
          <div
            class="truncate text-[13px] font-medium"
            :class="selectedPeerId === peer.id ? 'text-[var(--accent)]' : 'text-[var(--text-primary)]'"
          >
            {{ peer.name }}
          </div>
          <div class="truncate text-[11px] text-[var(--text-tertiary)]">{{ peer.os }} · {{ peer.browser }}</div>
        </div>

        <span
          v-if="selectedPeerId === peer.id"
          class="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--accent)]"
          aria-hidden="true"
        />
      </button>
    </div>
  </section>
</template>
