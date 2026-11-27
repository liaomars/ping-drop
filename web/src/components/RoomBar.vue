<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
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

function onWindowKeydown(ev: KeyboardEvent): void {
  if (ev.key === 'Escape' && props.roomModalOpen) {
    emit('toggle-room-modal', false)
  }
}

onMounted(() => {
  window.addEventListener('keydown', onWindowKeydown)
})

onUnmounted(() => {
  window.removeEventListener('keydown', onWindowKeydown)
})

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
  <header class="relative z-30 border-b border-[var(--border)] bg-[var(--bg-canvas)]/90 px-4 py-2.5 backdrop-blur-md">
    <div class="flex min-h-8 flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3">
        <h1 class="text-[15px] font-semibold tracking-tight text-[var(--text-primary)]">PingDrop</h1>

        <span class="inline-flex items-center gap-1.5 text-xs text-[var(--text-secondary)]">
          <span
            class="h-2 w-2 rounded-full"
            :style="{ backgroundColor: wsStatus === 'open' ? 'var(--success)' : 'var(--warning)' }"
          />
          <span>{{ wsStatus === 'open' ? '已连接' : '重连中' }}</span>
        </span>

        <span class="h-3 w-px bg-[var(--border-strong)]" aria-hidden="true" />

        <span
          class="text-xs"
          :class="
            space.kind === 'room' && space.code
              ? 'font-mono font-medium tracking-wide text-[var(--text-primary)] tabular-nums'
              : 'text-[var(--text-secondary)]'
          "
        >
          {{ space.kind === 'room' && space.code ? `房间 ${formatRoomCode(space.code)}` : '默认局域网' }}
        </span>
      </div>

      <div class="flex flex-wrap items-center gap-1.5">
        <div v-if="editingName" class="flex items-center gap-1.5">
          <label for="nickname-edit-input" class="sr-only">本机设备名称</label>
          <input
            id="nickname-edit-input"
            v-model="nameInput"
            data-testid="name-edit-input"
            maxlength="32"
            class="w-36 rounded-lg border border-[var(--border-strong)] bg-[var(--bg-canvas)] px-2.5 py-1 text-xs text-[var(--text-primary)] focus:border-[var(--accent)] focus:outline-none"
            @keydown.enter="onNameKeydown"
          />
          <button
            type="button"
            class="rounded-lg bg-[var(--btn-primary-bg)] px-2.5 py-1 text-xs font-medium text-[var(--btn-primary-text)] hover:bg-[var(--btn-primary-hover)]"
            @click="submitName"
          >
            保存
          </button>
        </div>
        <button
          v-else
          data-testid="edit-name-btn"
          type="button"
          class="inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-xs text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
          @click="startEditName"
        >
          <span>本机：{{ self?.name ?? '初始化中...' }}</span>
          <svg
            aria-hidden="true"
            class="h-3 w-3 opacity-60"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="1.75"
            stroke-linecap="round"
            stroke-linejoin="round"
          >
            <path d="M12 20h9" />
            <path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z" />
          </svg>
        </button>

        <button
          v-if="space.kind === 'room'"
          data-testid="return-lan-btn"
          type="button"
          class="rounded-lg px-2.5 py-1.5 text-xs text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
          @click="emit('return-lan')"
        >
          返回局域网
        </button>

        <button
          type="button"
          class="rounded-lg bg-[var(--btn-primary-bg)] px-3 py-1.5 text-xs font-medium text-[var(--btn-primary-text)] transition-colors hover:bg-[var(--btn-primary-hover)]"
          :aria-expanded="roomModalOpen"
          @click="emit('toggle-room-modal', !roomModalOpen)"
        >
          跨网配对
        </button>
      </div>
    </div>

    <div
      v-if="roomModalOpen"
      class="mx-auto mt-3 max-w-4xl rounded-2xl border border-[var(--border)] bg-[var(--bg-surface)] p-5"
      style="box-shadow: var(--shadow-popover)"
    >
      <div class="grid gap-6 md:grid-cols-2">
        <div class="space-y-3">
          <div class="flex items-center justify-between gap-2">
            <h2 class="text-xs font-semibold text-[var(--text-primary)]">分享私人房间</h2>
            <button
              data-testid="create-room-btn"
              type="button"
              class="rounded-lg border border-[var(--border-strong)] px-2.5 py-1 text-xs font-medium text-[var(--text-primary)] transition-colors hover:bg-[var(--bg-hover)]"
              @click="emit('create-room')"
            >
              生成新配对码
            </button>
          </div>
          <div v-if="space.kind === 'room' && space.code" class="flex items-center gap-4">
            <img
              v-if="qrDataUrl"
              :src="qrDataUrl"
              alt="配对二维码"
              class="h-24 w-24 rounded-xl border border-[var(--border)] bg-white p-1.5"
            />
            <div class="space-y-2">
              <div class="font-mono text-2xl font-semibold tracking-[0.18em] text-[var(--text-primary)] tabular-nums">
                {{ formatRoomCode(space.code) }}
              </div>
              <button
                data-testid="copy-invite-btn"
                type="button"
                class="rounded-lg border border-[var(--border-strong)] px-2.5 py-1 text-xs text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
                @click="copyInviteLink"
              >
                复制直达链接
              </button>
            </div>
          </div>
          <p v-else class="text-xs leading-relaxed text-[var(--text-secondary)]">
            生成 6 位配对码创建独立空间，异地设备输入配对码或扫码即可互通。
          </p>
        </div>

        <div class="space-y-3 border-t border-[var(--border)] pt-4 md:border-t-0 md:border-l md:pt-0 md:pl-6">
          <h2 class="text-xs font-semibold text-[var(--text-primary)]">输入 6 位码加入</h2>
          <div class="flex gap-2">
            <label for="room-code-join-input" class="sr-only">6 位配对码</label>
            <input
              id="room-code-join-input"
              data-testid="room-code-input"
              :value="codeInput"
              inputmode="numeric"
              placeholder="000 000"
              class="min-w-0 flex-1 rounded-lg border border-[var(--border-strong)] bg-[var(--bg-canvas)] px-3 py-1.5 font-mono text-sm tracking-widest text-[var(--text-primary)] tabular-nums placeholder:text-[var(--text-tertiary)] focus:border-[var(--accent)] focus:outline-none"
              @input="onCodeInput"
              @keydown.enter="onCodeKeydown"
            />
            <button
              data-testid="room-join-submit"
              type="button"
              class="rounded-lg bg-[var(--btn-primary-bg)] px-3.5 py-1.5 text-xs font-medium text-[var(--btn-primary-text)] transition-colors hover:bg-[var(--btn-primary-hover)] disabled:opacity-35"
              :disabled="codeInput.length !== 6"
              @click="submitJoin"
            >
              加入
            </button>
            <button
              type="button"
              class="rounded-lg px-2.5 py-1.5 text-xs text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
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
