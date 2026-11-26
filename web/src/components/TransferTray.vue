<script setup lang="ts">
import type { ReceivedFileRecord, TransferTask } from '../lib/rtcTransfer'
import { formatBytes } from '../lib/device'

const LARGE_MEMORY_FILE_BYTES = 20 * 1024 * 1024

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

function isLargeMemoryFile(rec: ReceivedFileRecord): boolean {
  return rec.storageMode === 'memory' && rec.size >= LARGE_MEMORY_FILE_BYTES
}
</script>

<template>
  <aside
    v-if="transfers.length > 0 || isRoom"
    aria-label="文件传输任务"
    class="fixed right-4 left-4 z-30 mx-auto max-w-3xl space-y-2 md:right-4 md:bottom-4 md:left-auto md:w-[26rem]"
    :class="mobileBarVisible ? 'bottom-20' : 'bottom-4'"
  >
    <div
      v-if="isRoom"
      class="flex items-center justify-between rounded-lg border border-slate-800 bg-slate-900/95 px-3 py-1.5 text-xs text-slate-300 shadow"
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
      class="space-y-2 rounded-xl border border-slate-700 bg-slate-900/95 p-3 text-xs shadow-2xl"
    >
      <div class="flex items-center justify-between gap-2">
        <div class="font-medium text-slate-100">
          {{ task.direction === 'send' ? `发送给 ${task.peerName}` : `来自 ${task.peerName}` }}
        </div>
        <div class="flex items-center gap-1.5">
          <span class="rounded bg-slate-800 px-1.5 py-0.5 text-[10px] text-sky-300">
            {{ task.channel === 'relay' ? '服务器中转' : 'P2P 直连' }}
          </span>
          <button
            v-if="task.state === 'completed' || task.state === 'rejected' || task.state === 'cancelled' || task.state === 'failed'"
            :data-testid="`dismiss-transfer-${task.transferId}`"
            type="button"
            class="text-slate-400 hover:text-slate-200"
            @click="emit('dismiss', task.transferId)"
          >
            关闭
          </button>
        </div>
      </div>

      <div class="text-slate-400">
        {{ task.files.map((f) => f.name).join(', ') }} · {{ formatBytes(task.totalBytes) }}
      </div>

      <div v-if="task.state === 'pending' && task.direction === 'receive'" class="flex gap-2">
        <button
          type="button"
          class="flex-1 rounded-md bg-emerald-600 py-1.5 font-medium text-white hover:bg-emerald-500"
          @click="emit('accept', task.transferId)"
        >
          接收
        </button>
        <button
          type="button"
          class="flex-1 rounded-md border border-slate-700 py-1.5 text-slate-300 hover:border-slate-500"
          @click="emit('reject', task.transferId)"
        >
          拒绝
        </button>
      </div>

      <div v-else-if="task.state === 'pending' && task.direction === 'send'" class="flex items-center justify-between text-slate-400">
        <span>等待对方确认接收...</span>
        <button type="button" class="text-rose-400 hover:underline" @click="emit('cancel', task.transferId)">
          取消
        </button>
      </div>

      <div v-else-if="task.state === 'connecting' || task.state === 'transferring'" class="space-y-1.5">
        <div
          role="progressbar"
          aria-valuemin="0"
          aria-valuemax="100"
          :aria-valuenow="progressPct(task)"
          class="h-1.5 overflow-hidden rounded-full bg-slate-800"
        >
          <div class="h-full bg-sky-500 transition-all" :style="{ width: `${progressPct(task)}%` }" />
        </div>
        <div class="flex items-center justify-between text-[11px] text-slate-400">
          <span>{{ progressPct(task) }}% · {{ formatBytes(task.speedBps) }}/s</span>
          <button type="button" class="text-rose-400 hover:underline" @click="emit('cancel', task.transferId)">
            取消
          </button>
        </div>
        <div v-if="task.channel === 'relay'" class="text-[11px] text-amber-300">
          直连受限，已切换服务器中转
        </div>
      </div>

      <div v-else-if="task.state === 'rejected'" class="text-amber-300">
        {{ task.direction === 'send' ? '对方已拒绝接收' : '已拒绝接收' }}
      </div>

      <div v-else-if="task.state === 'completed' && task.direction === 'send'" class="text-emerald-300">
        发送完成
      </div>

      <div v-if="task.state === 'completed' && task.receivedFiles.length > 0" class="space-y-1">
        <div
          v-for="(rec, idx) in task.receivedFiles"
          :key="rec.fileIndex"
          class="space-y-1 rounded bg-slate-950 px-2.5 py-1.5"
        >
          <div class="flex items-center justify-between">
            <span class="truncate text-slate-200">
              {{ rec.name }} ({{ rec.storageMode === 'opfs' ? 'OPFS' : '内存' }})
            </span>
            <button
              :data-testid="`save-file-${idx}`"
              type="button"
              class="ml-2 shrink-0 rounded bg-sky-600 px-2 py-0.5 text-[11px] font-medium text-white"
              @click="emit('download-file', rec.objectUrl, rec.name)"
            >
              保存 / 重新下载
            </button>
          </div>
          <p
            v-if="isLargeMemoryFile(rec)"
            :data-testid="`memory-warning-${task.transferId}-${idx}`"
            class="text-[11px] text-amber-300"
          >
            当前文件通过内存暂存（当前环境不支持 OPFS），体积较大，请立即保存并避免刷新页面
          </p>
        </div>
      </div>

      <div v-if="task.errorMessage" class="text-rose-400">{{ task.errorMessage }}</div>
    </div>
  </aside>
</template>
