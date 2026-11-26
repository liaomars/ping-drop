<script setup lang="ts">
import { ref, watch } from 'vue'
import QRCode from 'qrcode'
import type { PeerInfo, SpaceSummary } from '../lib/protocol'
import type { WsStatus } from '../lib/wsClient'
import { formatRoomCode, normalizeRoomCode } from '../lib/device'

const props = defineProps<{
  self: PeerInfo | null
  space: SpaceSummary
  wsStatus: WsStatus
  roomModalOpen: boolean
}>()

const emit = defineEmits<{
  'update:nickname': [name: string]
  'create-room': []
  'join-room': [code: string]
  'return-lan': []
  'toggle-room-modal': [open: boolean]
  'copy-text': [text: string]
}>()

const editingName = ref(false)
const nameInput = ref('')
const codeInput = ref('')
const qrDataUrl = ref('')
let qrSeq = 0

watch(
  () => [props.space.code, props.roomModalOpen] as const,
  async ([code, open]) => {
    const seq = ++qrSeq
    qrDataUrl.value = ''
    if (!open || !code) {
      return
    }
    const shareUrl = `${window.location.origin}/?room=${code}`
    const url = await QRCode.toDataURL(shareUrl, { width: 180, margin: 1 }).catch(() => '')
    if (seq === qrSeq && props.roomModalOpen && props.space.code === code) {
      qrDataUrl.value = url
    }
  },
  { immediate: true }
)

function startEditName(): void {
  nameInput.value = props.self?.name ?? ''
  editingName.value = true
}

function submitName(): void {
  editingName.value = false
  if (nameInput.value.trim()) {
    emit('update:nickname', nameInput.value.trim())
  }
}

function onNameKeydown(ev: KeyboardEvent): void {
  if (ev.isComposing || ev.keyCode === 229) return
  ev.preventDefault()
  submitName()
}

function onCodeInput(ev: Event): void {
  const input = ev.target as HTMLInputElement
  const clean = normalizeRoomCode(input.value)
  codeInput.value = clean
  if (input.value !== clean) {
    input.value = clean
  }
}

function submitJoin(): void {
  const clean = normalizeRoomCode(codeInput.value)
  if (clean.length === 6) {
    emit('join-room', clean)
    codeInput.value = ''
  }
}

function onCodeKeydown(ev: KeyboardEvent): void {
  if (ev.isComposing || ev.keyCode === 229) return
  ev.preventDefault()
  submitJoin()
}

function copyInviteLink(): void {
  if (!props.space.code) return
  emit('copy-text', `${window.location.origin}/?room=${props.space.code}`)
}
</script>

<template>
  <header class="border-b border-slate-800 bg-slate-900/90 px-4 py-3 backdrop-blur">
    <div class="mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3">
        <h1 class="text-lg font-bold tracking-tight text-sky-400">PingDrop</h1>
        <span
          class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium"
          :class="wsStatus === 'open' ? 'bg-emerald-500/15 text-emerald-300' : 'bg-amber-500/15 text-amber-300'"
        >
          <span class="h-1.5 w-1.5 rounded-full" :class="wsStatus === 'open' ? 'bg-emerald-400' : 'bg-amber-400'" />
          {{ wsStatus === 'open' ? '已连接' : '重连中' }}
        </span>
        <span class="rounded-md bg-slate-800 px-2.5 py-1 text-xs text-slate-200">
          {{ space.kind === 'room' && space.code ? `房间 ${formatRoomCode(space.code)}` : '默认局域网' }}
        </span>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <div v-if="editingName" class="flex items-center gap-1">
          <label for="nickname-edit-input" class="sr-only">本机设备名称</label>
          <input
            id="nickname-edit-input"
            v-model="nameInput"
            data-testid="name-edit-input"
            maxlength="32"
            class="w-36 rounded-md border border-slate-700 bg-slate-950 px-2 py-1 text-xs text-slate-100 outline-none focus:border-sky-500"
            @keydown.enter="onNameKeydown"
          />
          <button
            type="button"
            class="rounded-md bg-sky-600 px-2 py-1 text-xs font-medium text-white hover:bg-sky-500"
            @click="submitName"
          >
            保存
          </button>
        </div>
        <button
          v-else
          data-testid="edit-name-btn"
          type="button"
          class="rounded-md border border-slate-700 px-2.5 py-1 text-xs text-slate-200 hover:border-slate-500"
          @click="startEditName"
        >
          本机：{{ self?.name ?? '初始化中...' }}
        </button>

        <button
          v-if="space.kind === 'room'"
          data-testid="return-lan-btn"
          type="button"
          class="rounded-md border border-slate-700 px-2.5 py-1 text-xs text-slate-300 hover:border-slate-500"
          @click="emit('return-lan')"
        >
          返回局域网
        </button>

        <button
          type="button"
          class="rounded-md bg-sky-600 px-3 py-1 text-xs font-medium text-white hover:bg-sky-500"
          :aria-expanded="roomModalOpen"
          @click="emit('toggle-room-modal', !roomModalOpen)"
        >
          跨网配对
        </button>
      </div>
    </div>

    <div
      v-if="roomModalOpen"
      class="mx-auto mt-3 max-w-6xl rounded-xl border border-slate-800 bg-slate-950 p-4 shadow-xl"
    >
      <div class="grid gap-4 md:grid-cols-2">
        <div class="space-y-3">
          <div class="flex items-center justify-between">
            <h2 class="text-sm font-semibold text-slate-100">创建 / 分享 6 位私人房间</h2>
            <button
              data-testid="create-room-btn"
              type="button"
              class="rounded-md bg-sky-600 px-2.5 py-1 text-xs font-medium text-white hover:bg-sky-500"
              @click="emit('create-room')"
            >
              生成新配对码
            </button>
          </div>
          <div v-if="space.kind === 'room' && space.code" class="flex items-center gap-4">
            <img v-if="qrDataUrl" :src="qrDataUrl" alt="配对二维码" class="h-28 w-28 rounded-lg bg-white p-1" />
            <div class="space-y-2">
              <div class="font-mono text-2xl font-bold tracking-widest text-sky-300">
                {{ formatRoomCode(space.code) }}
              </div>
              <button
                data-testid="copy-invite-btn"
                type="button"
                class="rounded-md border border-slate-700 px-2.5 py-1 text-xs text-slate-200 hover:border-sky-500"
                @click="copyInviteLink"
              >
                复制直达链接
              </button>
            </div>
          </div>
          <p v-else class="text-xs text-slate-400">
            点击「生成新配对码」创建独立加密空间，手机 5G 或异地设备输入 6 位数字即可互通与暂存图文文件。
          </p>
        </div>

        <div class="space-y-2">
          <h2 class="text-sm font-semibold text-slate-100">输入 6 位配对码加入</h2>
          <div class="flex gap-2">
            <label for="room-code-join-input" class="sr-only">6 位配对码</label>
            <input
              id="room-code-join-input"
              data-testid="room-code-input"
              :value="codeInput"
              inputmode="numeric"
              placeholder="例如 482917"
              class="flex-1 rounded-md border border-slate-700 bg-slate-900 px-3 py-1.5 font-mono text-sm text-slate-100 outline-none focus:border-sky-500"
              @input="onCodeInput"
              @keydown.enter="onCodeKeydown"
            />
            <button
              data-testid="room-join-submit"
              type="button"
              class="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-500 disabled:opacity-40"
              :disabled="codeInput.length !== 6"
              @click="submitJoin"
            >
              加入
            </button>
            <button
              type="button"
              class="rounded-md border border-slate-700 px-3 py-1.5 text-xs text-slate-300"
              @click="emit('toggle-room-modal', false)"
            >
              关闭
            </button>
          </div>
        </div>
      </div>
    </div>
  </header>
</template>
