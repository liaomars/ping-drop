import type { FileMeta, SignalType } from './protocol'
import { createChunkSink, triggerBrowserDownload, type ChunkSink } from './opfsSink'

/** 传输底层通道类型：WebRTC 点对点直连或服务端流式中转 */
export type TransferChannel = 'webrtc' | 'relay'

/** 单个传输任务生命周期状态 */
export type TransferState =
  | 'pending'
  | 'connecting'
  | 'transferring'
  | 'completed'
  | 'rejected'
  | 'cancelled'
  | 'failed'

/** 接收端已落盘或组包完成的单文件记录 */
export interface ReceivedFileRecord {
  fileIndex: number
  name: string
  size: number
  mime: string
  objectUrl: string
  storageMode: 'opfs' | 'memory'
}

/** 暴露给前端状态树与 UI 展示的任务快照 */
export interface TransferTask {
  transferId: string
  direction: 'send' | 'receive'
  peerId: string
  peerName: string
  files: FileMeta[]
  channel: TransferChannel
  state: TransferState
  currentFileIndex: number
  bytesTransferred: number
  totalBytes: number
  speedBps: number
  errorMessage?: string
  receivedFiles: ReceivedFileRecord[]
}

/** TransferManager 外部依赖注入接口 */
export interface TransferManagerDeps {
  /** 通过 WebSocket 向指定对端发送信令 */
  sendSignal: (toPeerId: string, signalType: SignalType, transferId: string, data?: unknown) => void
  /** 获取当前会话的 Bearer 鉴权令牌 */
  getSessionToken: () => string
  /** 获取服务端下发的 STUN/TURN ICE 服务器配置 */
  getIceServers: () => RTCIceServer[]
  /** 获取服务端允许的单文件最大中转限额（MB；0 表示禁用中转） */
  getMaxRelayMB: () => number
  /** 任务状态、进度或速率发生变化时的回调 */
  onUpdate: (task: TransferTask) => void
  /** 可选自定义 fetch 实现，便于单元测试注入 Mock */
  fetcher?: typeof fetch
  /** WebRTC 建连超时毫秒数，默认 8000ms */
  connectTimeoutMs?: number
}

const MAX_CHUNK_BYTES = 64 * 1024
const FALLBACK_CHUNK_BYTES = 16 * 1024
const BUFFER_LOW_THRESHOLD = 256 * 1024
const BUFFER_HIGH_THRESHOLD = 1024 * 1024
const DEFAULT_CONNECT_TIMEOUT_MS = 8_000

interface InternalSession {
  task: TransferTask
  rawFiles: File[]
  pc: RTCPeerConnection | null
  dc: RTCDataChannel | null
  connectTimer: ReturnType<typeof setTimeout> | null
  abortController: AbortController
  activeSink: ChunkSink | null
  activeExpectedSize: number
  activeFileMeta: FileMeta | null
  activeFileIndex: number
  activeReceivedBytes: number
  speedWindowStart: number
  speedWindowBytes: number
  fallbackTriggered: boolean
  remoteDescriptionSet: boolean
  pendingIceCandidates: RTCIceCandidateInit[]
  receiveQueue: Promise<void>
}

interface ControlPacket {
  type?: string
  fileIndex?: number
  name?: string
  size?: number
  mime?: string
  totalBytes?: number
}

/**
 * 按对端协商的 SCTP maxMessageSize 动态计算安全分片大小（钳制在 16 KB ~ 64 KB）。
 */
export function resolveChunkSize(maxMessageSize?: number | null): number {
  if (!maxMessageSize || !Number.isFinite(maxMessageSize) || maxMessageSize <= 0) {
    return FALLBACK_CHUNK_BYTES
  }
  return Math.max(FALLBACK_CHUNK_BYTES, Math.min(MAX_CHUNK_BYTES, Math.floor(maxMessageSize)))
}

/**
 * 管理每次传输独立的 RTCPeerConnection 生命周期、背压分片流式收发，以及 8 秒建连超时自动降级 HTTPS 流式中转。
 */
export class TransferManager {
  private readonly deps: TransferManagerDeps
  private readonly fetcher: typeof fetch
  private readonly connectTimeoutMs: number
  private readonly sessions = new Map<string, InternalSession>()

  /** 创建 TransferManager 实例 */
  constructor(deps: TransferManagerDeps) {
    this.deps = deps
    this.fetcher = deps.fetcher ?? globalThis.fetch.bind(globalThis)
    this.connectTimeoutMs = deps.connectTimeoutMs ?? DEFAULT_CONNECT_TIMEOUT_MS
  }

  /** 发送方发起文件传输邀请（先由对端确认，再建连传输） */
  startOutgoing(peerId: string, peerName: string, files: File[]): TransferTask {
    const transferId = createTransferId()
    const metas: FileMeta[] = files.map((f) => ({
      name: f.name || 'file.bin',
      size: f.size,
      mime: f.type || 'application/octet-stream'
    }))
    const totalBytes = metas.reduce((acc, f) => acc + f.size, 0)

    const session = this.createSession({
      transferId,
      direction: 'send',
      peerId,
      peerName,
      files: metas,
      totalBytes,
      rawFiles: files
    })
    this.sessions.set(transferId, session)
    this.emit(session)
    this.deps.sendSignal(peerId, 'file.offer', transferId, { files: metas })
    return this.snapshot(session.task)
  }

  /** 接收方同意接收文件邀请 */
  acceptIncoming(transferId: string): void {
    const s = this.sessions.get(transferId)
    if (!s || s.task.direction !== 'receive' || s.task.state !== 'pending') {
      return
    }
    s.task.state = 'connecting'
    this.emit(s)
    this.deps.sendSignal(s.task.peerId, 'file.accept', transferId, {})
  }

  /** 接收方拒绝接收文件邀请 */
  rejectIncoming(transferId: string): void {
    const s = this.sessions.get(transferId)
    if (!s || s.task.direction !== 'receive' || s.task.state !== 'pending') {
      return
    }
    s.task.state = 'rejected'
    this.emit(s)
    this.deps.sendSignal(s.task.peerId, 'file.reject', transferId, {})
  }

  /** 任一方取消传输并中止底层 WebRTC 连接、HTTP 中转请求与本地接收槽 */
  cancelTransfer(transferId: string): void {
    const s = this.sessions.get(transferId)
    if (!s || this.isTerminalState(s.task.state)) {
      return
    }
    this.cleanupResources(s)
    s.task.state = 'cancelled'
    s.task.speedBps = 0
    s.task.errorMessage = '已取消传输'
    this.emit(s)
    this.deps.sendSignal(s.task.peerId, 'file.cancel', transferId, { reason: 'user_cancelled' })
  }

  /** 处理来自 WebSocket 的 rtc.signal 对端透传信令 */
  async handleSignal(
    fromPeerId: string,
    fromPeerName: string,
    signalType: SignalType,
    transferId: string,
    data: unknown,
    autoAccept = false
  ): Promise<void> {
    if (signalType === 'file.offer') {
      if (this.sessions.has(transferId)) {
        return
      }
      const payload = (data ?? {}) as { files?: FileMeta[] }
      const files = Array.isArray(payload.files) ? payload.files : []
      const totalBytes = files.reduce((acc, f) => acc + (f.size ?? 0), 0)
      const session = this.createSession({
        transferId,
        direction: 'receive',
        peerId: fromPeerId,
        peerName: fromPeerName,
        files,
        totalBytes,
        rawFiles: []
      })
      this.sessions.set(transferId, session)
      this.emit(session)
      if (autoAccept) {
        this.acceptIncoming(transferId)
      }
      return
    }

    const s = this.sessions.get(transferId)
    if (!s) {
      return
    }

    switch (signalType) {
      case 'file.accept':
        if (s.task.direction === 'send' && s.task.state === 'pending') {
          await this.startSenderWebRTC(s)
        }
        break
      case 'file.reject':
        if (!this.isTerminalState(s.task.state)) {
          this.cleanupResources(s)
          s.task.state = 'rejected'
          s.task.speedBps = 0
          this.emit(s)
        }
        break
      case 'file.cancel':
        if (!this.isTerminalState(s.task.state)) {
          this.cleanupResources(s)
          s.task.state = 'cancelled'
          s.task.speedBps = 0
          s.task.errorMessage = '对端已取消传输'
          this.emit(s)
        }
        break
      case 'rtc.offer':
        if (s.task.direction === 'receive' && s.task.state === 'connecting' && !s.fallbackTriggered && data) {
          await this.handleReceiverOffer(s, data as RTCSessionDescriptionInit)
        }
        break
      case 'rtc.answer':
        if (
          s.task.direction === 'send' &&
          s.pc &&
          s.task.state === 'connecting' &&
          !s.fallbackTriggered &&
          data
        ) {
          await s.pc.setRemoteDescription(data as RTCSessionDescriptionInit)
          s.remoteDescriptionSet = true
          await this.flushPendingIceCandidates(s)
        }
        break
      case 'rtc.ice':
        if (!this.isTerminalState(s.task.state) && !s.fallbackTriggered && data) {
          const candidate = data as RTCIceCandidateInit
          if (s.pc && s.remoteDescriptionSet) {
            await s.pc.addIceCandidate(candidate).catch(() => undefined)
          } else {
            s.pendingIceCandidates.push(candidate)
          }
        }
        break
      case 'relay.start':
        if (s.task.direction === 'receive' && !this.isTerminalState(s.task.state) && !s.fallbackTriggered) {
          s.fallbackTriggered = true
          this.cleanupPeerConnection(s)
          await this.abortActiveSink(s)
          await this.runReceiverRelay(s)
        }
        break
    }
  }

  /** 释放已完成任务关联的本地下载 ObjectURL 并移除会话记录 */
  releaseCompletedTask(transferId: string): void {
    const s = this.sessions.get(transferId)
    if (!s) {
      return
    }
    for (const rec of s.task.receivedFiles) {
      URL.revokeObjectURL(rec.objectUrl)
    }
    this.sessions.delete(transferId)
  }

  /** 页面卸载或切换空间时中止所有进行中的传输并回收资源 */
  disposeAll(): void {
    for (const s of this.sessions.values()) {
      this.cleanupResources(s)
      for (const rec of s.task.receivedFiles) {
        URL.revokeObjectURL(rec.objectUrl)
      }
    }
    this.sessions.clear()
  }

  private createSession(opts: {
    transferId: string
    direction: 'send' | 'receive'
    peerId: string
    peerName: string
    files: FileMeta[]
    totalBytes: number
    rawFiles: File[]
  }): InternalSession {
    return {
      task: {
        transferId: opts.transferId,
        direction: opts.direction,
        peerId: opts.peerId,
        peerName: opts.peerName,
        files: opts.files,
        channel: 'webrtc',
        state: 'pending',
        currentFileIndex: 0,
        bytesTransferred: 0,
        totalBytes: opts.totalBytes,
        speedBps: 0,
        receivedFiles: []
      },
      rawFiles: opts.rawFiles,
      pc: null,
      dc: null,
      connectTimer: null,
      abortController: new AbortController(),
      activeSink: null,
      activeExpectedSize: 0,
      activeFileMeta: null,
      activeFileIndex: 0,
      activeReceivedBytes: 0,
      speedWindowStart: performance.now(),
      speedWindowBytes: 0,
      fallbackTriggered: false,
      remoteDescriptionSet: false,
      pendingIceCandidates: [],
      receiveQueue: Promise.resolve()
    }
  }

  private async startSenderWebRTC(s: InternalSession): Promise<void> {
    s.task.state = 'connecting'
    this.emit(s)

    try {
      const pc = new RTCPeerConnection({ iceServers: this.deps.getIceServers() })
      s.pc = pc
      const dc = pc.createDataChannel('pingdrop-transfer', { ordered: true })
      dc.binaryType = 'arraybuffer'
      dc.bufferedAmountLowThreshold = BUFFER_LOW_THRESHOLD
      s.dc = dc

      pc.onicecandidate = (ev) => {
        if (ev.candidate) {
          const init = typeof ev.candidate.toJSON === 'function' ? ev.candidate.toJSON() : ev.candidate
          this.deps.sendSignal(s.task.peerId, 'rtc.ice', s.task.transferId, init)
        }
      }

      pc.oniceconnectionstatechange = () => {
        if (pc.iceConnectionState === 'failed' && s.task.state === 'connecting') {
          void this.handleWebRTCConnectFailure(s)
        }
      }

      pc.onconnectionstatechange = () => {
        if (pc.connectionState === 'failed' && s.task.state === 'connecting') {
          void this.handleWebRTCConnectFailure(s)
        }
      }

      dc.onopen = () => {
        if (s.fallbackTriggered || s.abortController.signal.aborted || s.task.state !== 'connecting') {
          return
        }
        this.clearConnectTimer(s)
        void this.streamOutgoingWebRTC(s, pc, dc)
      }

      s.connectTimer = setTimeout(() => {
        if (s.task.state === 'connecting') {
          void this.handleWebRTCConnectFailure(s)
        }
      }, this.connectTimeoutMs)

      const offer = await pc.createOffer()
      if (s.abortController.signal.aborted || s.fallbackTriggered) {
        return
      }
      await pc.setLocalDescription(offer)
      this.deps.sendSignal(s.task.peerId, 'rtc.offer', s.task.transferId, offer)
    } catch {
      if (s.task.state === 'connecting' && !s.abortController.signal.aborted) {
        await this.handleWebRTCConnectFailure(s)
      }
    }
  }

  private async handleReceiverOffer(s: InternalSession, offer: RTCSessionDescriptionInit): Promise<void> {
    try {
      const pc = new RTCPeerConnection({ iceServers: this.deps.getIceServers() })
      s.pc = pc

      pc.onicecandidate = (ev) => {
        if (ev.candidate) {
          const init = typeof ev.candidate.toJSON === 'function' ? ev.candidate.toJSON() : ev.candidate
          this.deps.sendSignal(s.task.peerId, 'rtc.ice', s.task.transferId, init)
        }
      }

      pc.ondatachannel = (ev) => {
        if (s.abortController.signal.aborted || s.fallbackTriggered || this.isTerminalState(s.task.state)) {
          return
        }
        const dc = ev.channel
        dc.binaryType = 'arraybuffer'
        s.dc = dc
        s.task.state = 'transferring'
        this.emit(s)

        dc.onmessage = (msgEv) => {
          // 串行排空接收消息，防止 file.start / 异步写盘 / file.end 并发竞态
          s.receiveQueue = s.receiveQueue
            .then(async () => {
              if (s.abortController.signal.aborted || s.fallbackTriggered || this.isTerminalState(s.task.state)) {
                return
              }
              await this.handleReceiverDataMessage(s, msgEv.data)
            })
            .catch(async (err) => {
              if (s.abortController.signal.aborted || s.fallbackTriggered || this.isTerminalState(s.task.state)) {
                return
              }
              await this.failReceiverSession(s, err instanceof Error ? err.message : 'WebRTC 接收异常')
            })
        }

        dc.onclose = () => {
          s.receiveQueue = s.receiveQueue.then(async () => {
            if (s.abortController.signal.aborted || s.fallbackTriggered || this.isTerminalState(s.task.state)) {
              return
            }
            if (s.task.channel === 'webrtc' && s.task.receivedFiles.length < s.task.files.length) {
              await this.failReceiverSession(s, 'WebRTC 连接已断开，接收未完成')
            }
          })
        }
      }

      await pc.setRemoteDescription(offer)
      s.remoteDescriptionSet = true
      await this.flushPendingIceCandidates(s)
      const answer = await pc.createAnswer()
      if (s.abortController.signal.aborted || s.fallbackTriggered) {
        return
      }
      await pc.setLocalDescription(answer)
      this.deps.sendSignal(s.task.peerId, 'rtc.answer', s.task.transferId, answer)
    } catch (err) {
      if (!s.abortController.signal.aborted && !s.fallbackTriggered && !this.isTerminalState(s.task.state)) {
        await this.failReceiverSession(s, err instanceof Error ? err.message : 'WebRTC 应答协商失败')
      }
    }
  }

  private async streamOutgoingWebRTC(s: InternalSession, pc: RTCPeerConnection, dc: RTCDataChannel): Promise<void> {
    s.task.state = 'transferring'
    this.emit(s)
    const chunkSize = resolveChunkSize(pc.sctp?.maxMessageSize)

    dc.onclose = () => {
      if (!s.abortController.signal.aborted && s.task.state === 'transferring') {
        s.task.state = 'failed'
        s.task.speedBps = 0
        s.task.errorMessage = 'WebRTC 连接已断开，发送未完成'
        s.abortController.abort()
        this.emit(s)
      }
    }

    try {
      for (let i = 0; i < s.rawFiles.length; i += 1) {
        if (s.abortController.signal.aborted) {
          return
        }
        const file = s.rawFiles[i]
        s.task.currentFileIndex = i
        dc.send(
          JSON.stringify({
            type: 'file.start',
            transferId: s.task.transferId,
            fileIndex: i,
            name: file.name,
            size: file.size,
            mime: file.type || 'application/octet-stream'
          })
        )

        let offset = 0
        while (offset < file.size) {
          if (s.abortController.signal.aborted) {
            return
          }
          if (dc.bufferedAmount >= BUFFER_HIGH_THRESHOLD) {
            await waitBufferedLow(dc, s.abortController.signal)
          }
          if (s.abortController.signal.aborted) {
            return
          }
          const slice = await readBlobAsArrayBuffer(file.slice(offset, offset + chunkSize))
          if (s.abortController.signal.aborted) {
            return
          }
          if (slice.byteLength === 0) {
            break
          }
          dc.send(slice)
          offset += slice.byteLength
          this.advanceProgress(s, slice.byteLength)
        }

        if (s.abortController.signal.aborted) {
          return
        }
        dc.send(
          JSON.stringify({
            type: 'file.end',
            transferId: s.task.transferId,
            fileIndex: i,
            totalBytes: file.size
          })
        )
      }

      if (s.abortController.signal.aborted) {
        return
      }
      s.task.state = 'completed'
      s.task.speedBps = 0
      this.emit(s)
    } catch (err) {
      if (!s.abortController.signal.aborted && !this.isTerminalState(s.task.state)) {
        s.task.state = 'failed'
        s.task.speedBps = 0
        s.task.errorMessage = err instanceof Error ? err.message : 'WebRTC 发送异常'
        this.emit(s)
      }
    } finally {
      this.cleanupPeerConnection(s)
    }
  }

  private async handleReceiverDataMessage(s: InternalSession, data: unknown): Promise<void> {
    if (typeof data === 'string') {
      const ctrl = JSON.parse(data) as ControlPacket
      if (ctrl.type === 'file.start') {
        await this.abortActiveSink(s)
        s.activeFileIndex = ctrl.fileIndex ?? 0
        s.task.currentFileIndex = s.activeFileIndex
        s.activeExpectedSize = ctrl.size ?? 0
        s.activeReceivedBytes = 0
        s.activeFileMeta = {
          name: ctrl.name ?? 'file.bin',
          size: s.activeExpectedSize,
          mime: ctrl.mime ?? 'application/octet-stream'
        }
        const sink = await createChunkSink(s.task.transferId, s.activeFileIndex, s.activeFileMeta.name)
        if (s.abortController.signal.aborted || s.fallbackTriggered) {
          await sink.abort().catch(() => undefined)
          return
        }
        s.activeSink = sink
        return
      }

      if (ctrl.type === 'file.end') {
        if (!s.activeSink || !s.activeFileMeta) {
          throw new Error('unexpected file.end without active file')
        }
        const expected = ctrl.totalBytes ?? s.activeExpectedSize
        if (s.activeReceivedBytes !== expected) {
          throw new Error(`byte size mismatch: expected ${expected}, got ${s.activeReceivedBytes}`)
        }
        const sink = s.activeSink
        const meta = s.activeFileMeta
        const fileIndex = s.activeFileIndex
        const url = await sink.finalize(meta.mime, expected)
        const mode = sink.storageMode
        s.activeSink = null
        s.activeFileMeta = null
        if (s.abortController.signal.aborted || s.fallbackTriggered) {
          URL.revokeObjectURL(url)
          return
        }
        s.task.receivedFiles.push({
          fileIndex,
          name: meta.name,
          size: expected,
          mime: meta.mime,
          objectUrl: url,
          storageMode: mode
        })
        triggerBrowserDownload(url, meta.name)
        if (s.task.receivedFiles.length >= s.task.files.length) {
          s.task.state = 'completed'
          s.task.speedBps = 0
          this.cleanupPeerConnection(s)
        }
        this.emit(s)
      }
      return
    }

    if (!s.activeSink) {
      throw new Error('received binary chunk without active file')
    }
    const chunk = await toUint8Array(data)
    await s.activeSink.write(chunk)
    s.activeReceivedBytes += chunk.byteLength
    this.advanceProgress(s, chunk.byteLength)
  }

  private async failReceiverSession(s: InternalSession, errorMessage: string): Promise<void> {
    s.abortController.abort()
    this.cleanupPeerConnection(s)
    await this.abortActiveSink(s)
    s.task.state = 'failed'
    s.task.speedBps = 0
    s.task.errorMessage = errorMessage
    this.emit(s)
  }

  private async handleWebRTCConnectFailure(s: InternalSession): Promise<void> {
    if (s.fallbackTriggered || s.abortController.signal.aborted || s.task.state !== 'connecting') {
      return
    }
    s.fallbackTriggered = true
    this.cleanupPeerConnection(s)

    const maxRelayMB = this.deps.getMaxRelayMB()
    const maxRelayBytes = Number.isFinite(maxRelayMB) && maxRelayMB > 0 ? maxRelayMB * 1024 * 1024 : 0
    const canRelay = maxRelayBytes > 0 && s.rawFiles.every((f) => f.size <= maxRelayBytes)
    if (!canRelay) {
      s.task.state = 'failed'
      s.task.speedBps = 0
      s.task.errorMessage =
        maxRelayMB <= 0
          ? '跨网直连受当前网络 NAT 限制，服务端已禁用中转，建议切至同一 Wi-Fi 或配置 TURN'
          : `跨网直连受限，且文件超出服务器 ${maxRelayMB} MB 中转限额`
      this.emit(s)
      return
    }

    s.task.channel = 'relay'
    s.task.state = 'transferring'
    this.emit(s)
    this.deps.sendSignal(s.task.peerId, 'relay.start', s.task.transferId, {})
    await this.runSenderRelay(s)
  }

  private async runSenderRelay(s: InternalSession): Promise<void> {
    try {
      for (let i = 0; i < s.rawFiles.length; i += 1) {
        if (s.abortController.signal.aborted) {
          return
        }
        const file = s.rawFiles[i]
        s.task.currentFileIndex = i
        const res = await this.fetcher(`/api/relay/${encodeURIComponent(s.task.transferId)}/${i}`, {
          method: 'POST',
          headers: {
            Authorization: `Bearer ${this.deps.getSessionToken()}`,
            'Content-Type': 'application/octet-stream'
          },
          body: file,
          signal: s.abortController.signal
        })
        if (!res.ok) {
          throw new Error(`relay upload failed: ${res.status}`)
        }
        this.advanceProgress(s, file.size)
      }
      if (s.abortController.signal.aborted) {
        return
      }
      s.task.state = 'completed'
      s.task.speedBps = 0
      this.emit(s)
    } catch (err) {
      if (!s.abortController.signal.aborted && !this.isTerminalState(s.task.state)) {
        s.task.state = 'failed'
        s.task.speedBps = 0
        s.task.errorMessage = err instanceof Error ? err.message : '服务器中转上传失败'
        this.emit(s)
      }
    }
  }

  private async runReceiverRelay(s: InternalSession): Promise<void> {
    s.task.channel = 'relay'
    s.task.state = 'transferring'
    this.emit(s)

    try {
      for (let i = 0; i < s.task.files.length; i += 1) {
        if (s.abortController.signal.aborted) {
          return
        }
        const meta = s.task.files[i]
        s.task.currentFileIndex = i
        const sink = await createChunkSink(s.task.transferId, i, meta.name)
        if (s.abortController.signal.aborted) {
          await sink.abort().catch(() => undefined)
          return
        }
        s.activeSink = sink

        const res = await this.fetcher(`/api/relay/${encodeURIComponent(s.task.transferId)}/${i}`, {
          method: 'GET',
          headers: { Authorization: `Bearer ${this.deps.getSessionToken()}` },
          signal: s.abortController.signal
        })
        if (!res.ok) {
          throw new Error(`relay download failed: ${res.status}`)
        }

        if (res.body) {
          const reader = res.body.getReader()
          while (true) {
            const { done, value } = await reader.read()
            if (done) break
            if (s.abortController.signal.aborted) {
              await reader.cancel().catch(() => undefined)
              return
            }
            if (value) {
              await sink.write(value)
              this.advanceProgress(s, value.byteLength)
            }
          }
        } else {
          const buf = new Uint8Array(await res.arrayBuffer())
          await sink.write(buf)
          this.advanceProgress(s, buf.byteLength)
        }

        const url = await sink.finalize(meta.mime, meta.size)
        s.activeSink = null
        if (s.abortController.signal.aborted) {
          URL.revokeObjectURL(url)
          return
        }
        s.task.receivedFiles.push({
          fileIndex: i,
          name: meta.name,
          size: meta.size,
          mime: meta.mime,
          objectUrl: url,
          storageMode: sink.storageMode
        })
        triggerBrowserDownload(url, meta.name)
        this.emit(s)
      }

      if (s.abortController.signal.aborted) {
        return
      }
      s.task.state = 'completed'
      s.task.speedBps = 0
      this.emit(s)
    } catch (err) {
      await this.abortActiveSink(s)
      if (!s.abortController.signal.aborted && !this.isTerminalState(s.task.state)) {
        s.task.state = 'failed'
        s.task.speedBps = 0
        s.task.errorMessage = err instanceof Error ? err.message : '服务器中转下载失败'
        this.emit(s)
      }
    }
  }

  private async flushPendingIceCandidates(s: InternalSession): Promise<void> {
    if (!s.pc || !s.remoteDescriptionSet || s.pendingIceCandidates.length === 0) {
      return
    }
    const candidates = s.pendingIceCandidates.splice(0, s.pendingIceCandidates.length)
    for (const candidate of candidates) {
      await s.pc.addIceCandidate(candidate).catch(() => undefined)
    }
  }

  private advanceProgress(s: InternalSession, deltaBytes: number): void {
    s.task.bytesTransferred += deltaBytes
    s.speedWindowBytes += deltaBytes
    const now = performance.now()
    const elapsedSec = (now - s.speedWindowStart) / 1000
    if (elapsedSec >= 0.4) {
      s.task.speedBps = Math.round(s.speedWindowBytes / elapsedSec)
      s.speedWindowStart = now
      s.speedWindowBytes = 0
    }
    this.emit(s)
  }

  private clearConnectTimer(s: InternalSession): void {
    if (s.connectTimer !== null) {
      clearTimeout(s.connectTimer)
      s.connectTimer = null
    }
  }

  private cleanupPeerConnection(s: InternalSession): void {
    this.clearConnectTimer(s)
    if (s.dc) {
      s.dc.onopen = null
      s.dc.onclose = null
      s.dc.onmessage = null
      s.dc.onbufferedamountlow = null
      s.dc.close()
      s.dc = null
    }
    if (s.pc) {
      s.pc.onicecandidate = null
      s.pc.oniceconnectionstatechange = null
      s.pc.onconnectionstatechange = null
      s.pc.ondatachannel = null
      s.pc.close()
      s.pc = null
    }
    s.pendingIceCandidates.length = 0
    s.remoteDescriptionSet = false
  }

  private async abortActiveSink(s: InternalSession): Promise<void> {
    if (s.activeSink) {
      const sink = s.activeSink
      s.activeSink = null
      s.activeFileMeta = null
      await sink.abort().catch(() => undefined)
    }
  }

  private cleanupResources(s: InternalSession): void {
    s.abortController.abort()
    this.cleanupPeerConnection(s)
    if (s.activeSink) {
      const sink = s.activeSink
      s.activeSink = null
      s.activeFileMeta = null
      void sink.abort().catch(() => undefined)
    }
  }

  private isTerminalState(state: TransferState): boolean {
    return state === 'completed' || state === 'rejected' || state === 'cancelled' || state === 'failed'
  }

  private snapshot(task: TransferTask): TransferTask {
    return {
      ...task,
      files: [...task.files],
      receivedFiles: [...task.receivedFiles]
    }
  }

  private emit(s: InternalSession): void {
    this.deps.onUpdate(this.snapshot(s.task))
  }
}

function waitBufferedLow(dc: RTCDataChannel, signal: AbortSignal): Promise<void> {
  if (signal.aborted) {
    return Promise.reject(new Error('aborted'))
  }
  return new Promise((resolve, reject) => {
    let settled = false
    const hasEventTarget = typeof dc.addEventListener === 'function' && typeof dc.removeEventListener === 'function'
    const prevOnLow = dc.onbufferedamountlow

    const cleanup = () => {
      signal.removeEventListener('abort', onAbort)
      if (hasEventTarget) {
        dc.removeEventListener('bufferedamountlow', onLow)
        dc.removeEventListener('close', onClose)
      }
      if (dc.onbufferedamountlow === onLowProp) {
        dc.onbufferedamountlow = prevOnLow
      }
    }

    const onLow = () => {
      if (settled) return
      settled = true
      cleanup()
      resolve()
    }

    const onLowProp = (ev: Event) => {
      prevOnLow?.call(dc, ev)
      onLow()
    }

    const onClose = () => {
      if (settled) return
      settled = true
      cleanup()
      reject(new Error('datachannel closed'))
    }

    const onAbort = () => {
      if (settled) return
      settled = true
      cleanup()
      reject(new Error('aborted'))
    }

    if (hasEventTarget) {
      dc.addEventListener('bufferedamountlow', onLow)
      dc.addEventListener('close', onClose)
    } else {
      dc.onbufferedamountlow = onLowProp as (this: RTCDataChannel, ev: Event) => unknown
    }
    signal.addEventListener('abort', onAbort)
  })
}

async function toUint8Array(data: unknown): Promise<Uint8Array> {
  if (data instanceof ArrayBuffer) {
    return new Uint8Array(data)
  }
  if (ArrayBuffer.isView(data)) {
    return new Uint8Array(data.buffer, data.byteOffset, data.byteLength)
  }
  if (typeof Blob !== 'undefined' && data instanceof Blob) {
    return new Uint8Array(await readBlobAsArrayBuffer(data))
  }
  throw new Error('unsupported WebRTC binary payload')
}

async function readBlobAsArrayBuffer(blob: Blob): Promise<ArrayBuffer> {
  if (typeof blob.arrayBuffer === 'function') {
    return blob.arrayBuffer()
  }
  return new Promise<ArrayBuffer>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      if (reader.result instanceof ArrayBuffer) {
        resolve(reader.result)
      } else {
        reject(new Error('failed to read blob as ArrayBuffer'))
      }
    }
    reader.onerror = () => reject(reader.error ?? new Error('failed to read blob'))
    reader.readAsArrayBuffer(blob)
  })
}

function createTransferId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID().replace(/-/g, '')
  }
  return `tx-${Date.now()}-${Math.random().toString(16).slice(2, 10)}`
}
