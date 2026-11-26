import { reactive } from 'vue'
import type {
  DMMessage,
  ErrorCode,
  Limits,
  PeerInfo,
  RTCSignalEventPayload,
  SpaceStatePayload,
  SpaceSummary,
  TimelineItem,
  WelcomePayload
} from '../lib/protocol'
import {
  detectDeviceProfile,
  formatBytes,
  isValidRoomCode,
  loadNickname,
  normalizeRoomCode,
  readRoomCodeFromLocation,
  saveNickname,
  syncRoomUrl
} from '../lib/device'
import { BlobUrlCache } from '../lib/blobUrl'
import { checkStorageQuota, cleanupStaleOpfsTempFiles, triggerBrowserDownload } from '../lib/opfsSink'
import { TransferManager, type TransferTask } from '../lib/rtcTransfer'
import { WsClient, type WsStatus } from '../lib/wsClient'

/** 页面顶部轻量提示条结构 */
export interface ToastNotice {
  id: number
  kind: 'info' | 'success' | 'error'
  message: string
}

/** 前端单一响应式会话状态树 */
export interface SessionState {
  wsStatus: WsStatus
  self: PeerInfo | null
  sessionToken: string
  iceServers: RTCIceServer[]
  limits: Limits
  space: SpaceSummary
  peers: PeerInfo[]
  timeline: TimelineItem[]
  blobPreviewUrls: Record<string, string>
  dmsByPeer: Record<string, DMMessage[]>
  transfers: TransferTask[]
  selectedPeerId: string | null
  autoAcceptInRoom: boolean
  roomModalOpen: boolean
  toast: ToastNotice | null
}

/** 创建 SessionStore 时的可选依赖注入配置 */
export interface SessionStoreOptions {
  wsUrl?: string
  fetcher?: typeof fetch
  WebSocketImpl?: typeof WebSocket
}

/** 创建单一响应式会话状态源，协调 WS 信令、鉴权预览缓存、OPFS 与文件传输引擎 */
export function createSessionStore(opts: SessionStoreOptions = {}) {
  const fetcher = opts.fetcher ?? (typeof window !== 'undefined' ? fetch.bind(window) : fetch)
  const blobCache = new BlobUrlCache(fetcher)
  let toastSeq = 0
  let toastTimer: ReturnType<typeof setTimeout> | null = null
  let destroyed = false

  const state = reactive<SessionState>({
    wsStatus: 'closed',
    self: null,
    sessionToken: '',
    iceServers: [],
    limits: { maxTimelineFileMB: 20, maxRelayMB: 100 },
    space: { kind: 'lan', sharedTimelineEnabled: false },
    peers: [],
    timeline: [],
    blobPreviewUrls: {},
    dmsByPeer: {},
    transfers: [],
    selectedPeerId: null,
    autoAcceptInRoom: false,
    roomModalOpen: false,
    toast: null
  })

  /** 显示短时 Toast 提示并在 3.2 秒后自动清除 */
  function showToast(message: string, kind: ToastNotice['kind'] = 'info'): void {
    toastSeq += 1
    state.toast = { id: toastSeq, kind, message }
    if (toastTimer !== null) clearTimeout(toastTimer)
    toastTimer = setTimeout(() => {
      state.toast = null
    }, 3200)
  }

  const wsUrl =
    opts.wsUrl ??
    (typeof window !== 'undefined'
      ? `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/ws`
      : 'ws://127.0.0.1:8080/ws')

  const ws = new WsClient({
    url: wsUrl,
    WebSocketImpl: opts.WebSocketImpl,
    getHello: () => {
      const profile = detectDeviceProfile()
      const roomCode = state.space.kind === 'room' ? state.space.code : readRoomCodeFromLocation() ?? undefined
      const saved = loadNickname()
      return {
        name: saved || undefined,
        ...profile,
        roomCode
      }
    },
    onStatus: (status) => {
      state.wsStatus = status
    },
    onEvent: (type, payload) => {
      handleServerEvent(type, payload)
    }
  })

  const transfers = new TransferManager({
    sendSignal: (toPeerId, signalType, transferId, data) => {
      ws.send('rtc.signal', { toPeerId, signalType, transferId, data })
    },
    getSessionToken: () => state.sessionToken,
    getIceServers: () => state.iceServers,
    getMaxRelayMB: () => state.limits.maxRelayMB,
    fetcher,
    onUpdate: (task) => {
      const idx = state.transfers.findIndex((t) => t.transferId === task.transferId)
      if (idx === -1) {
        state.transfers.unshift(task)
      } else {
        state.transfers[idx] = task
      }
    }
  })

  /** 清理遗留 OPFS 临时文件并启动 WebSocket 会话连接 */
  function start(): void {
    void cleanupStaleOpfsTempFiles()
    ws.connect()
  }

  /** 销毁会话、关闭连接并释放所有本地 ObjectURL 与传输资源 */
  function destroy(): void {
    destroyed = true
    ws.close()
    transfers.disposeAll()
    blobCache.clear()
    state.blobPreviewUrls = {}
    if (toastTimer !== null) {
      clearTimeout(toastTimer)
      toastTimer = null
    }
  }

  function clearPreviewUrls(): void {
    blobCache.clear()
    state.blobPreviewUrls = {}
  }

  function ensurePreviewUrls(items: TimelineItem[]): void {
    if (!state.sessionToken || destroyed) return
    for (const item of items) {
      if (item.kind === 'image' && item.blobId && !state.blobPreviewUrls[item.blobId]) {
        const blobId = item.blobId
        const itemId = item.id
        void blobCache
          .fetchObjectUrl(blobId, state.sessionToken, false)
          .then((url) => {
            if (!destroyed && state.timeline.some((x) => x.id === itemId && x.blobId === blobId)) {
              state.blobPreviewUrls[blobId] = url
            } else {
              blobCache.revoke(blobId)
            }
          })
          .catch(() => undefined)
      }
    }
  }

  /** 分发并处理来自服务端的 WebSocket 业务事件 */
  function handleServerEvent(type: string, payload: unknown): void {
    switch (type) {
      case 'welcome': {
        const p = payload as WelcomePayload
        state.self = p.self
        state.sessionToken = p.sessionToken
        state.iceServers = p.iceServers ?? []
        state.limits = p.limits ?? state.limits
        break
      }
      case 'space.state': {
        const p = payload as SpaceStatePayload
        clearPreviewUrls()
        state.space = p.space
        state.peers = p.peers ?? []
        state.timeline = p.timeline ?? []
        if (p.space.kind === 'room' && p.space.code) {
          syncRoomUrl(p.space.code)
        } else {
          syncRoomUrl(null)
        }
        if (state.selectedPeerId && !state.peers.some((peer) => peer.id === state.selectedPeerId)) {
          state.selectedPeerId = null
        }
        ensurePreviewUrls(state.timeline)
        break
      }
      case 'peer.joined': {
        const { peer } = payload as { peer: PeerInfo }
        if (!state.peers.some((x) => x.id === peer.id)) {
          state.peers.push(peer)
        }
        break
      }
      case 'peer.left': {
        const { peerId } = payload as { peerId: string }
        state.peers = state.peers.filter((x) => x.id !== peerId)
        if (state.selectedPeerId === peerId) {
          state.selectedPeerId = null
        }
        break
      }
      case 'peer.updated': {
        const { peer } = payload as { peer: PeerInfo }
        const idx = state.peers.findIndex((x) => x.id === peer.id)
        if (idx !== -1) state.peers[idx] = peer
        if (state.self?.id === peer.id) state.self = peer
        break
      }
      case 'timeline.item': {
        const { item } = payload as { item: TimelineItem }
        if (!state.timeline.some((x) => x.id === item.id)) {
          state.timeline.push(item)
          ensurePreviewUrls([item])
        }
        break
      }
      case 'timeline.deleted': {
        const { itemId } = payload as { itemId: string }
        const target = state.timeline.find((x) => x.id === itemId)
        if (target?.blobId) {
          blobCache.revoke(target.blobId)
          delete state.blobPreviewUrls[target.blobId]
        }
        state.timeline = state.timeline.filter((x) => x.id !== itemId)
        break
      }
      case 'timeline.cleared': {
        clearPreviewUrls()
        state.timeline = []
        break
      }
      case 'dm.text': {
        const msg = payload as DMMessage
        const list = state.dmsByPeer[msg.fromPeerId] ?? []
        list.push(msg)
        state.dmsByPeer[msg.fromPeerId] = list
        showToast(`收到来自 ${msg.fromName} 的私信`, 'info')
        break
      }
      case 'rtc.signal': {
        const sig = payload as RTCSignalEventPayload
        const fromPeer = state.peers.find((x) => x.id === sig.fromPeerId)
        const auto = state.space.kind === 'room' && state.autoAcceptInRoom
        void transfers.handleSignal(
          sig.fromPeerId,
          fromPeer?.name ?? '对端设备',
          sig.signalType,
          sig.transferId,
          sig.data,
          auto
        )
        break
      }
      case 'error': {
        const err = payload as { code: ErrorCode; message: string }
        if (err.code === 'ROOM_NOT_FOUND') {
          syncRoomUrl(null)
        }
        showToast(err.message || '操作失败', 'error')
        break
      }
    }
  }

  /** 更新本地持久化昵称并向服务端同步 hello */
  function updateNickname(name: string): void {
    const clean = name.trim().slice(0, 32)
    if (!clean) return
    saveNickname(clean)
    if (state.self) state.self.name = clean
    const profile = detectDeviceProfile()
    ws.send('hello', { name: clean, ...profile })
  }

  /** 请求创建新的 6 位数字配对码房间并打开房间弹窗 */
  function createRoom(): void {
    ws.send('room.create', {})
    state.roomModalOpen = true
  }

  /** 校验并加入指定 6 位数字配对码房间 */
  function joinRoom(rawCode: string): boolean {
    const code = normalizeRoomCode(rawCode)
    if (!isValidRoomCode(code)) {
      showToast('请输入 6 位数字配对码', 'error')
      return false
    }
    ws.send('space.join', { kind: 'room', code })
    state.roomModalOpen = false
    return true
  }

  /** 切回默认局域网空间 */
  function returnToLan(): void {
    ws.send('space.join', { kind: 'lan' })
    state.roomModalOpen = false
  }

  /** 向当前共享时间线发送文本消息 */
  function postTimelineText(text: string): boolean {
    const clean = text.trim()
    if (!clean) return false
    if (!state.space.sharedTimelineEnabled) {
      showToast('当前局域网默认关闭共享时间线，请开启 6 位码房间', 'error')
      return false
    }
    ws.send('timeline.post', { text })
    return true
  }

  /** 上传图片或小文件至当前空间的共享时间线 */
  async function uploadTimelineFile(file: File): Promise<boolean> {
    if (!state.space.sharedTimelineEnabled) {
      showToast('当前局域网默认关闭共享时间线，请开启 6 位码房间', 'error')
      return false
    }
    const maxBytes = state.limits.maxTimelineFileMB * 1024 * 1024
    if (file.size > maxBytes) {
      showToast(`文件超出时间线 ${state.limits.maxTimelineFileMB} MB 上限`, 'error')
      return false
    }

    const fd = new FormData()
    fd.append('file', file)
    const res = await fetcher('/api/blobs', {
      method: 'POST',
      headers: { Authorization: `Bearer ${state.sessionToken}` },
      body: fd
    })
    if (!res.ok) {
      showToast('上传至时间线失败', 'error')
      return false
    }
    const data = (await res.json()) as { item?: TimelineItem }
    if (data.item && !state.timeline.some((x) => x.id === data.item?.id)) {
      state.timeline.push(data.item)
      ensurePreviewUrls([data.item])
    }
    showToast('已添加到共享时间线', 'success')
    return true
  }

  /** 删除指定共享时间线条目 */
  function deleteTimelineItem(itemId: string): void {
    ws.send('timeline.delete', { itemId })
  }

  /** 清空当前空间的共享时间线 */
  function clearTimeline(): void {
    ws.send('timeline.clear', {})
  }

  /** 通过鉴权 Blob 接口下载时间线文件并触发浏览器保存 */
  async function downloadTimelineBlob(item: TimelineItem): Promise<void> {
    if (!item.blobId) return
    try {
      const url = await blobCache.fetchObjectUrl(item.blobId, state.sessionToken, true)
      triggerBrowserDownload(url, item.fileName ?? 'download.bin')
    } catch {
      showToast('文件已过期或无权限下载', 'error')
    }
  }

  /** 向指定在线设备发送点对点私信 */
  function sendDM(toPeerId: string, text: string): boolean {
    if (!text.trim()) return false
    const ok = ws.send('dm.text', { toPeerId, text })
    if (ok && state.self) {
      const list = state.dmsByPeer[toPeerId] ?? []
      list.push({
        id: `local-${Date.now()}`,
        fromPeerId: state.self.id,
        fromName: state.self.name,
        text,
        createdAt: Date.now(),
        outgoing: true
      })
      state.dmsByPeer[toPeerId] = list
    }
    return ok
  }

  /** 向指定在线设备发起文件传输 */
  function sendFiles(toPeerId: string, files: File[]): void {
    if (files.length === 0) return
    const peer = state.peers.find((x) => x.id === toPeerId)
    transfers.startOutgoing(toPeerId, peer?.name ?? '对端设备', files)
  }

  /** 接收文件前校验浏览器存储配额，空间不足时弹出警告后再继续接收 */
  async function acceptIncoming(transferId: string): Promise<void> {
    const task = state.transfers.find((t) => t.transferId === transferId)
    if (task) {
      const quota = await checkStorageQuota(task.totalBytes)
      if (!quota.ok) {
        showToast(
          quota.availableBytes !== null
            ? `浏览器可用存储空间不足（剩余约 ${formatBytes(quota.availableBytes)}），可能无法完整保存文件`
            : '浏览器可用存储空间不足，可能无法完整保存文件',
          'error'
        )
      }
    }
    transfers.acceptIncoming(transferId)
  }

  /** 移除传输任务卡片并释放关联的本地接收文件 ObjectURL */
  function dismissTransfer(transferId: string): void {
    transfers.releaseCompletedTask(transferId)
    state.transfers = state.transfers.filter((t) => t.transferId !== transferId)
  }

  return {
    state,
    start,
    destroy,
    handleServerEvent,
    updateNickname,
    createRoom,
    joinRoom,
    returnToLan,
    postTimelineText,
    uploadTimelineFile,
    deleteTimelineItem,
    clearTimeline,
    downloadTimelineBlob,
    sendDM,
    sendFiles,
    acceptIncoming,
    acceptTransfer: acceptIncoming,
    rejectTransfer: (id: string) => transfers.rejectIncoming(id),
    cancelTransfer: (id: string) => transfers.cancelTransfer(id),
    dismissTransfer,
    triggerSavedFileDownload: (url: string, name: string) => triggerBrowserDownload(url, name),
    selectPeer: (id: string | null) => {
      state.selectedPeerId = id
    },
    showToast
  }
}

/** 前端单一响应式会话状态源类型 */
export type SessionStore = ReturnType<typeof createSessionStore>
