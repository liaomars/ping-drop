<script setup lang="ts">
import type { TransferTask } from '../lib/rtcTransfer'
import { formatBytes } from '../lib/device'

defineProps<{
  transfers: TransferTask[]
  isRoom: boolean
  autoAcceptInRoom: boolean
  mobileBarVisible?: boolean
}>()

const emit = defineEmits<{
  accept: [transferId: string]
  reject: [transferId: string]
  cancel: [transferId: string]
  dismiss: [transferId: string]
  'download-file': [url: string, name: string]
  'update:autoAcceptInRoom': [enabled: boolean]
}>()

function progressPct(task: TransferTask): number {
  if (task.totalBytes <= 0) return 0
  return Math.min(100, Math.round((task.bytesTransferred / task.totalBytes) * 100))
}
</script>

<template>
  <aside
    v-if="transfers.length > 0 || isRoom"
    aria-label="文件传输任务"
    class="fixed right-4 left-4 z-30 mx-auto max-w-3xl space-y-2 md:right-4 md:bottom-4 md:left-auto md:w-[24rem]"
    :class="mobileBarVisible ? 'bottom-20' : 'bottom-4'"
  >
    <div
      v-if="isRoom"
      class="flex items-center justify-between rounded-xl border border-[var(--border)] bg-[var(--bg-surface)]/95 px-3.5 py-2 text-xs text-[var(--text-secondary)] backdrop-blur-md"
      style="box-shadow: var(--shadow-popover)"
    >
      <label for="auto-accept-room-checkbox" class="cursor-pointer select-none">私人房间文件自动接收</label>
      <input
        id="auto-accept-room-checkbox"
        type="checkbox"
        :checked="autoAcceptInRoom"
        @change="emit('update:autoAcceptInRoom', ($event.target as HTMLInputElement).checked)"
      />
    </div>

    <div
      v-for="task in transfers"
      :key="task.transferId"
      class="space-y-2 rounded-2xl border border-[var(--border)] bg-[var(--bg-surface)]/95 p-3.5 text-xs backdrop-blur-md"
      style="box-shadow: var(--shadow-popover)"
    >
      <div class="flex items-center justify-between gap-2">
        <div class="truncate font-medium text-[var(--text-primary)]">
          {{ task.direction === 'send' ? `发送给 ${task.peerName}` : `来自 ${task.peerName}` }}
        </div>
        <div class="flex shrink-0 items-center gap-2">
          <span class="rounded-md bg-[var(--bg-subtle)] px-1.5 py-0.5 text-[10px] font-medium text-[var(--text-secondary)]">
            {{ task.channel === 'relay' ? '服务器中转' : 'P2P 直连' }}
          </span>
          <button
            v-if="task.state === 'completed' || task.state === 'rejected' || task.state === 'cancelled' || task.state === 'failed'"
            :data-testid="`dismiss-transfer-${task.transferId}`"
            type="button"
            class="text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
            @click="emit('dismiss', task.transferId)"
          >
            关闭
          </button>
        </div>
      </div>

      <div class="truncate tabular-nums text-[var(--text-secondary)]">
        {{ task.files.map((f) => f.name).join(', ') }} · {{ formatBytes(task.totalBytes) }}
      </div>

      <div v-if="task.state === 'pending' && task.direction === 'receive'" class="flex gap-2">
        <button
          type="button"
          class="flex-1 rounded-lg bg-[var(--btn-primary-bg)] py-1.5 font-medium text-[var(--btn-primary-text)] transition-colors hover:bg-[var(--btn-primary-hover)]"
          @click="emit('accept', task.transferId)"
        >接收</button>
        <button
          type="button"
          class="flex-1 rounded-lg border border-[var(--border-strong)] py-1.5 text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
          @click="emit('reject', task.transferId)"
        >拒绝</button>
      </div>

      <div v-else-if="task.state === 'pending' && task.direction === 'send'" class="flex items-center justify-between text-[var(--text-secondary)]">
        <span>等待对方确认接收...</span>
        <button type="button" class="text-[var(--danger)] hover:underline" @click="emit('cancel', task.transferId)">取消</button>
      </div>

      <div v-else-if="task.state === 'connecting' || task.state === 'transferring'" class="space-y-1.5">
        <div
          role="progressbar"
          aria-valuemin="0"
          aria-valuemax="100"
          :aria-valuenow="progressPct(task)"
          class="h-1 overflow-hidden rounded-full bg-[var(--bg-subtle)]"
        >
          <div class="h-full bg-[var(--accent)] transition-all" :style="{ width: `${progressPct(task)}%` }" />
        </div>
        <div class="flex items-center justify-between tabular-nums text-[11px] text-[var(--text-secondary)]">
          <span>{{ progressPct(task) }}% · {{ formatBytes(task.speedBps) }}/s</span>
          <button type="button" class="text-[var(--danger)] hover:underline" @click="emit('cancel', task.transferId)">取消</button>
        </div>
        <div v-if="task.channel === 'relay'" class="text-[11px] text-[var(--warning)]">
          直连受限，已切换服务器中转
        </div>
      </div>

      <div v-else-if="task.state === 'rejected'" class="text-[var(--warning)]">
        {{ task.direction === 'send' ? '对方已拒绝接收' : '已拒绝接收' }}
      </div>

      <div v-else-if="task.state === 'completed' && task.direction === 'send'" class="text-[var(--success)]">
        发送完成
      </div>

      <div v-if="task.state === 'completed' && task.receivedFiles.length > 0" class="space-y-1.5">
        <div
          v-for="(rec, idx) in task.receivedFiles"
          :key="rec.fileIndex"
          class="space-y-1 rounded-lg bg-[var(--bg-subtle)] px-2.5 py-2"
        >
          <div class="flex items-center justify-between gap-2">
            <span class="truncate text-[var(--text-primary)]">
              {{ rec.name }} ({{ rec.storageMode === 'opfs' ? 'OPFS' : '内存' }})
            </span>
            <button
              :data-testid="`save-file-${idx}`"
              type="button"
              class="shrink-0 rounded-md bg-[var(--btn-primary-bg)] px-2.5 py-0.5 text-[11px] font-medium text-[var(--btn-primary-text)] hover:bg-[var(--btn-primary-hover)]"
              @click="emit('download-file', rec.objectUrl, rec.name)"
            >
              保存 / 重新下载
            </button>
          </div>
          <p
            v-if="rec.storageMode === 'memory'"
            :data-testid="`memory-warning-${task.transferId}-${idx}`"
            class="text-[11px] text-[var(--warning)]"
          >
            当前环境不支持 OPFS，文件暂存于内存，请及时保存且勿刷新页面
          </p>
        </div>
      </div>

      <div v-if="task.errorMessage" class="text-[var(--danger)]">{{ task.errorMessage }}</div>
    </div>
  </aside>
</template>
