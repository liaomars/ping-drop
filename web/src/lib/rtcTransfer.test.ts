import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { resolveChunkSize, TransferManager, type TransferTask } from './rtcTransfer'
import type { SignalType } from './protocol'

type EventHandler = (ev?: unknown) => void

class MockDataChannel {
  binaryType: BinaryType = 'arraybuffer'
  bufferedAmount = 0
  bufferedAmountLowThreshold = 0
  readyState: RTCDataChannelState = 'connecting'
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((ev: { data: string | ArrayBuffer }) => void) | null = null
  onbufferedamountlow: (() => void) | null = null
  sent: Array<string | ArrayBuffer> = []
  onSend?: (data: string | ArrayBuffer, dc: MockDataChannel) => void
  private listeners = new Map<string, Set<EventHandler>>()

  addEventListener(type: string, cb: EventHandler): void {
    let set = this.listeners.get(type)
    if (!set) {
      set = new Set()
      this.listeners.set(type, set)
    }
    set.add(cb)
  }

  removeEventListener(type: string, cb: EventHandler): void {
    this.listeners.get(type)?.delete(cb)
  }

  emitEvent(type: string): void {
    if (type === 'bufferedamountlow') {
      this.onbufferedamountlow?.()
    } else if (type === 'open') {
      this.readyState = 'open'
      this.onopen?.()
    } else if (type === 'close') {
      this.readyState = 'closed'
      this.onclose?.()
    }
    const set = this.listeners.get(type)
    if (set) {
      for (const cb of [...set]) {
        cb({ type })
      }
    }
  }

  send(data: string | ArrayBuffer): void {
    this.sent.push(data)
    this.onSend?.(data, this)
  }

  close(): void {
    this.readyState = 'closed'
    this.emitEvent('close')
  }
}

class MockPeerConnection {
  static instances: MockPeerConnection[] = []

  connectionState: RTCPeerConnectionState = 'new'
  iceConnectionState: RTCIceConnectionState = 'new'
  sctp: { maxMessageSize: number } | null = { maxMessageSize: 16 * 1024 }
  remoteDescriptionSet = false
  addedIceCandidates: RTCIceCandidateInit[] = []
  closed = false
  createdDataChannel: MockDataChannel | null = null
  remoteDataChannel: MockDataChannel | null = null

  onicecandidate: ((ev: { candidate: RTCIceCandidate | null }) => void) | null = null
  onconnectionstatechange: (() => void) | null = null
  oniceconnectionstatechange: (() => void) | null = null
  ondatachannel: ((ev: { channel: RTCDataChannel }) => void) | null = null

  constructor() {
    MockPeerConnection.instances.push(this)
  }

  createDataChannel(): RTCDataChannel {
    const dc = new MockDataChannel()
    this.createdDataChannel = dc
    return dc as unknown as RTCDataChannel
  }

  async createOffer(): Promise<RTCSessionDescriptionInit> {
    return { type: 'offer', sdp: 'v=0-mock-offer' }
  }

  async createAnswer(): Promise<RTCSessionDescriptionInit> {
    return { type: 'answer', sdp: 'v=0-mock-answer' }
  }

  async setLocalDescription(): Promise<void> {}

  async setRemoteDescription(): Promise<void> {
    this.remoteDescriptionSet = true
  }

  async addIceCandidate(candidate: RTCIceCandidateInit): Promise<void> {
    if (!this.remoteDescriptionSet) {
      throw new Error('InvalidStateError: remoteDescription not set')
    }
    this.addedIceCandidates.push(candidate)
  }

  triggerDataChannel(dc: MockDataChannel): void {
    this.remoteDataChannel = dc
    this.ondatachannel?.({ channel: dc as unknown as RTCDataChannel })
  }

  close(): void {
    this.closed = true
    this.connectionState = 'closed'
  }
}

/** 构造可双向联动的数据通道，用于真实模拟 WebRTC 端到端发送与接收 */
function linkMockChannels(senderDc: MockDataChannel, receiverDc: MockDataChannel): void {
  senderDc.onSend = (data) => {
    const payload =
      typeof data === 'string'
        ? data
        : data.slice(0)
    receiverDc.onmessage?.({ data: payload })
  }
}

async function readBlobBytes(blob: Blob): Promise<Uint8Array> {
  if (typeof blob.arrayBuffer === 'function') {
    return new Uint8Array(await blob.arrayBuffer())
  }
  return new Promise<Uint8Array>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      if (reader.result instanceof ArrayBuffer) {
        resolve(new Uint8Array(reader.result))
      } else {
        reject(new Error('unexpected FileReader result'))
      }
    }
    reader.onerror = () => reject(reader.error ?? new Error('FileReader error'))
    reader.readAsArrayBuffer(blob)
  })
}

describe('rtcTransfer', () => {
  const capturedBlobs: Blob[] = []
  const originalWorker = globalThis.Worker
  const originalStorageDesc = Object.getOwnPropertyDescriptor(navigator, 'storage')
  let clickSpy: ReturnType<typeof vi.fn>

  beforeEach(() => {
    vi.useFakeTimers()
    MockPeerConnection.instances = []
    capturedBlobs.length = 0

    URL.createObjectURL = vi.fn((obj: Blob | MediaSource) => {
      if (obj instanceof Blob) {
        capturedBlobs.push(obj)
      }
      return `blob:mock-${capturedBlobs.length}`
    })
    URL.revokeObjectURL = vi.fn()

    clickSpy = vi.fn()
    const origCreateElement = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tagName: string) => {
      const el = origCreateElement(tagName)
      if (tagName === 'a') {
        el.click = clickSpy
      }
      return el
    })

    vi.stubGlobal('RTCPeerConnection', MockPeerConnection)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    globalThis.Worker = originalWorker
    if (originalStorageDesc) {
      Object.defineProperty(navigator, 'storage', originalStorageDesc)
    } else {
      delete (navigator as unknown as Record<string, unknown>).storage
    }
  })

  it('negotiates dynamic chunk size safely within [16KB, 64KB]', () => {
    expect(resolveChunkSize(undefined)).toBe(16 * 1024)
    expect(resolveChunkSize(null)).toBe(16 * 1024)
    expect(resolveChunkSize(0)).toBe(16 * 1024)
    expect(resolveChunkSize(-1024)).toBe(16 * 1024)
    expect(resolveChunkSize(Number.NaN)).toBe(16 * 1024)
    expect(resolveChunkSize(8 * 1024)).toBe(16 * 1024)
    expect(resolveChunkSize(32 * 1024)).toBe(32 * 1024)
    expect(resolveChunkSize(256 * 1024)).toBe(64 * 1024)
  })

  it('streams chunks over WebRTC with backpressure pause/resume and sequentially reassembles on receiver', async () => {
    const senderUpdates: TransferTask[] = []
    const receiverUpdates: TransferTask[] = []
    let senderMgr!: TransferManager
    let receiverMgr!: TransferManager

    const forwardSignal = (
      fromId: string,
      fromName: string,
      target: () => TransferManager,
      signalType: SignalType,
      transferId: string,
      data?: unknown
    ) => {
      queueMicrotask(() => {
        void target().handleSignal(fromId, fromName, signalType, transferId, data)
      })
    }

    senderMgr = new TransferManager({
      sendSignal: (_toPeerId, signalType, transferId, data) => {
        forwardSignal('peer-a', 'Laptop', () => receiverMgr, signalType, transferId, data)
      },
      getSessionToken: () => 'tok-a',
      getIceServers: () => [],
      getMaxRelayMB: () => 100,
      onUpdate: (t) => senderUpdates.push(t)
    })

    receiverMgr = new TransferManager({
      sendSignal: (_toPeerId, signalType, transferId, data) => {
        forwardSignal('peer-b', 'Phone', () => senderMgr, signalType, transferId, data)
      },
      getSessionToken: () => 'tok-b',
      getIceServers: () => [],
      getMaxRelayMB: () => 100,
      onUpdate: (t) => receiverUpdates.push(t)
    })

    // 40 KB 测试文件（16KB + 16KB + 8KB 三片）
    const payload = new Uint8Array(40 * 1024)
    for (let i = 0; i < payload.length; i += 1) {
      payload[i] = i % 251
    }
    const file = new File([payload], 'archive.bin', { type: 'application/octet-stream' })

    const task = senderMgr.startOutgoing('peer-b', 'Phone', [file])
    // 投递 file.offer 到接收端
    await vi.advanceTimersByTimeAsync(0)
    expect(receiverUpdates.at(-1)?.state).toBe('pending')

    // 接收端同意接收，触发 file.accept -> rtc.offer -> rtc.answer
    receiverMgr.acceptIncoming(task.transferId)
    await vi.advanceTimersByTimeAsync(0)

    const senderPc = MockPeerConnection.instances[0]
    const receiverPc = MockPeerConnection.instances[1]
    expect(senderPc).toBeDefined()
    expect(receiverPc).toBeDefined()

    const senderDc = senderPc.createdDataChannel!
    const receiverDc = new MockDataChannel()
    receiverPc.triggerDataChannel(receiverDc)
    linkMockChannels(senderDc, receiverDc)

    // 第一片发送后模拟发送缓冲区积压 >= 1MB 触发背压暂停
    let chunksSent = 0
    const origOnSend = senderDc.onSend!
    senderDc.onSend = (data, dc) => {
      origOnSend(data, dc)
      if (data instanceof ArrayBuffer) {
        chunksSent += 1
        if (chunksSent === 1) {
          dc.bufferedAmount = 1024 * 1024
        }
      }
    }

    // 打开通道开始发送，应当在发完第 1 片后停下来等待 bufferedamountlow
    senderDc.emitEvent('open')
    await vi.advanceTimersByTimeAsync(10)

    expect(chunksSent).toBe(1)
    expect(senderUpdates.at(-1)?.state).toBe('transferring')

    // 释放缓冲区并触发 bufferedamountlow 恢复发送
    senderDc.bufferedAmount = 0
    senderDc.emitEvent('bufferedamountlow')
    await vi.advanceTimersByTimeAsync(10)

    const lastSender = senderUpdates.at(-1)!
    const lastReceiver = receiverUpdates.at(-1)!
    expect(lastSender.state).toBe('completed')
    expect(lastSender.bytesTransferred).toBe(payload.byteLength)
    expect(lastReceiver.state).toBe('completed')
    expect(lastReceiver.bytesTransferred).toBe(payload.byteLength)
    expect(lastReceiver.receivedFiles).toHaveLength(1)
    expect(lastReceiver.receivedFiles[0]).toMatchObject({
      fileIndex: 0,
      name: 'archive.bin',
      size: payload.byteLength,
      mime: 'application/octet-stream',
      storageMode: 'memory'
    })
    expect(clickSpy).toHaveBeenCalledTimes(1)

    // 验证重组后的文件内容与原始字节完全一致
    vi.useRealTimers()
    const reassembled = await readBlobBytes(capturedBlobs[0])
    expect(reassembled).toEqual(payload)

    receiverMgr.releaseCompletedTask(task.transferId)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith(lastReceiver.receivedFiles[0].objectUrl)
  })

  it('serializes synchronous burst of WebRTC messages and fails cleanly on byte size mismatch', async () => {
    const receiverUpdates: TransferTask[] = []
    const mgr = new TransferManager({
      sendSignal: vi.fn(),
      getSessionToken: () => 'tok-rx',
      getIceServers: () => [],
      getMaxRelayMB: () => 100,
      onUpdate: (t) => receiverUpdates.push(t)
    })

    await mgr.handleSignal(
      'peer-a',
      'Laptop',
      'file.offer',
      'tx-mismatch',
      { files: [{ name: 'bad.bin', size: 10, mime: 'application/octet-stream' }] },
      true
    )
    await mgr.handleSignal('peer-a', 'Laptop', 'rtc.offer', 'tx-mismatch', { type: 'offer', sdp: 'v=0' })

    const receiverPc = MockPeerConnection.instances[0]
    const dc = new MockDataChannel()
    receiverPc.triggerDataChannel(dc)

    // 同步连续派发 file.start -> 4 字节分片 -> file.end(声明 10 字节)
    dc.onmessage?.({
      data: JSON.stringify({
        type: 'file.start',
        transferId: 'tx-mismatch',
        fileIndex: 0,
        name: 'bad.bin',
        size: 10,
        mime: 'application/octet-stream'
      })
    })
    dc.onmessage?.({ data: new Uint8Array([1, 2, 3, 4]).buffer })
    dc.onmessage?.({
      data: JSON.stringify({
        type: 'file.end',
        transferId: 'tx-mismatch',
        fileIndex: 0,
        totalBytes: 10
      })
    })

    await vi.advanceTimersByTimeAsync(10)

    const finalTask = receiverUpdates.at(-1)!
    expect(finalTask.state).toBe('failed')
    expect(finalTask.errorMessage).toContain('byte size mismatch')
    expect(finalTask.receivedFiles).toHaveLength(0)
    expect(clickSpy).not.toHaveBeenCalled()
    expect(receiverPc.closed).toBe(true)
  })

  it('buffers ICE candidates arriving before remoteDescription is set', async () => {
    const mgr = new TransferManager({
      sendSignal: vi.fn(),
      getSessionToken: () => 'tok',
      getIceServers: () => [],
      getMaxRelayMB: () => 50,
      onUpdate: vi.fn()
    })

    const file = new File([new Uint8Array([1, 2, 3])], 'ice.bin')
    const task = mgr.startOutgoing('peer-b', 'Phone', [file])
    await mgr.handleSignal('peer-b', 'Phone', 'file.accept', task.transferId, {})

    const senderPc = MockPeerConnection.instances[0]
    const ice: RTCIceCandidateInit = { candidate: 'candidate:1 1 udp 1 127.0.0.1 5000 typ host', sdpMid: '0', sdpMLineIndex: 0 }

    // rtc.answer 尚未到达前收到 rtc.ice
    await mgr.handleSignal('peer-b', 'Phone', 'rtc.ice', task.transferId, ice)
    expect(senderPc.addedIceCandidates).toHaveLength(0)

    await mgr.handleSignal('peer-b', 'Phone', 'rtc.answer', task.transferId, { type: 'answer', sdp: 'v=0-ans' })
    expect(senderPc.addedIceCandidates).toEqual([ice])
  })

  it('cleans up resources on file.reject and file.cancel', async () => {
    const senderSignals: Array<{ type: SignalType; id: string }> = []
    const senderUpdates: TransferTask[] = []
    const receiverSignals: Array<{ type: SignalType; id: string }> = []
    const receiverUpdates: TransferTask[] = []

    const senderMgr = new TransferManager({
      sendSignal: (_to, type, id) => senderSignals.push({ type, id }),
      getSessionToken: () => 'tok-a',
      getIceServers: () => [],
      getMaxRelayMB: () => 100,
      onUpdate: (t) => senderUpdates.push(t)
    })

    const receiverMgr = new TransferManager({
      sendSignal: (_to, type, id) => receiverSignals.push({ type, id }),
      getSessionToken: () => 'tok-b',
      getIceServers: () => [],
      getMaxRelayMB: () => 100,
      onUpdate: (t) => receiverUpdates.push(t)
    })

    // 1. 测试拒绝接收 file.reject
    const f1 = new File([new Uint8Array([1, 2])], 'a.txt')
    const t1 = senderMgr.startOutgoing('peer-b', 'Phone', [f1])
    await receiverMgr.handleSignal('peer-a', 'Laptop', 'file.offer', t1.transferId, {
      files: [{ name: 'a.txt', size: 2, mime: 'text/plain' }]
    })
    receiverMgr.rejectIncoming(t1.transferId)
    expect(receiverSignals.at(-1)?.type).toBe('file.reject')
    expect(receiverUpdates.at(-1)?.state).toBe('rejected')

    await senderMgr.handleSignal('peer-b', 'Phone', 'file.reject', t1.transferId, {})
    expect(senderUpdates.at(-1)?.state).toBe('rejected')

    // 2. 测试传输中取消 file.cancel 并释放 PC/DC 资源
    const f2 = new File([new Uint8Array(32 * 1024)], 'b.bin')
    const t2 = senderMgr.startOutgoing('peer-b', 'Phone', [f2])
    await senderMgr.handleSignal('peer-b', 'Phone', 'file.accept', t2.transferId, {})
    const pc = MockPeerConnection.instances[0]
    const dc = pc.createdDataChannel!
    dc.bufferedAmount = 1024 * 1024 // 让发送端处于背压等待中
    dc.emitEvent('open')
    await vi.advanceTimersByTimeAsync(0)

    senderMgr.cancelTransfer(t2.transferId)
    expect(senderUpdates.at(-1)?.state).toBe('cancelled')
    expect(senderSignals.some((s) => s.type === 'file.cancel' && s.id === t2.transferId)).toBe(true)
    expect(pc.closed).toBe(true)
    expect(dc.readyState).toBe('closed')

    // 对端收到 file.cancel
    await receiverMgr.handleSignal('peer-a', 'Laptop', 'file.offer', 'tx-c2', {
      files: [{ name: 'b.bin', size: 32 * 1024, mime: 'application/octet-stream' }]
    }, true)
    await receiverMgr.handleSignal('peer-a', 'Laptop', 'file.cancel', 'tx-c2', { reason: 'user_cancelled' })
    expect(receiverUpdates.at(-1)?.state).toBe('cancelled')
    expect(receiverUpdates.at(-1)?.errorMessage).toBe('对端已取消传输')

    senderMgr.disposeAll()
    receiverMgr.disposeAll()
  })

  it('auto-fallbacks to HTTPS streaming relay after 8s WebRTC connect timeout for both sender POST and receiver GET', async () => {
    const signals: Array<{ to: string; type: SignalType; id: string }> = []
    const senderFetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ bytes: 5 }), { status: 200 }))
    const receiverFetcher = vi.fn().mockImplementation(async () => {
      const stream = new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(new Uint8Array([10, 20]))
          controller.enqueue(new Uint8Array([30, 40, 50]))
          controller.close()
        }
      })
      return new Response(stream, { status: 200 })
    })
    const senderUpdates: string[] = []
    const receiverUpdates: TransferTask[] = []

    const senderMgr = new TransferManager({
      sendSignal: (toPeerId, signalType, transferId) => {
        signals.push({ to: toPeerId, type: signalType, id: transferId })
      },
      getSessionToken: () => 'tok-sender',
      getIceServers: () => [],
      getMaxRelayMB: () => 100,
      onUpdate: (t) => senderUpdates.push(`${t.channel}:${t.state}`),
      fetcher: senderFetcher,
      connectTimeoutMs: 8000
    })

    const receiverMgr = new TransferManager({
      sendSignal: vi.fn(),
      getSessionToken: () => 'tok-receiver',
      getIceServers: () => [],
      getMaxRelayMB: () => 100,
      onUpdate: (t) => receiverUpdates.push(t),
      fetcher: receiverFetcher
    })

    const file = new File([new Uint8Array([10, 20, 30, 40, 50])], 'hello.txt', { type: 'text/plain' })
    const task = senderMgr.startOutgoing('peer-b', 'Phone', [file])
    expect(signals[0].type).toBe('file.offer')

    await receiverMgr.handleSignal(
      'peer-a',
      'Laptop',
      'file.offer',
      task.transferId,
      { files: [{ name: 'hello.txt', size: 5, mime: 'text/plain' }] },
      true
    )

    // 模拟接收方同意
    await senderMgr.handleSignal('peer-b', 'Phone', 'file.accept', task.transferId, {})
    expect(signals.some((s) => s.type === 'rtc.offer')).toBe(true)

    // 推进 8 秒触发 WebRTC 超时并自动发送 relay.start 走 HTTPS 流式中转
    await vi.advanceTimersByTimeAsync(8000)
    expect(signals.some((s) => s.type === 'relay.start')).toBe(true)
    expect(senderFetcher).toHaveBeenCalledWith(
      `/api/relay/${task.transferId}/0`,
      expect.objectContaining({
        method: 'POST',
        headers: expect.objectContaining({ Authorization: 'Bearer tok-sender' }),
        body: file
      })
    )
    expect(senderUpdates).toContain('relay:completed')

    // 接收方收到 relay.start 后发起 GET 流式拉取并触发保存
    await receiverMgr.handleSignal('peer-a', 'Laptop', 'relay.start', task.transferId, {})
    expect(receiverFetcher).toHaveBeenCalledWith(
      `/api/relay/${task.transferId}/0`,
      expect.objectContaining({
        method: 'GET',
        headers: expect.objectContaining({ Authorization: 'Bearer tok-receiver' })
      })
    )
    const rxFinal = receiverUpdates.at(-1)!
    expect(rxFinal.channel).toBe('relay')
    expect(rxFinal.state).toBe('completed')
    expect(rxFinal.bytesTransferred).toBe(5)
    expect(rxFinal.receivedFiles).toHaveLength(1)
    expect(clickSpy).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
    const reassembled = await readBlobBytes(capturedBlobs[0])
    expect(reassembled).toEqual(new Uint8Array([10, 20, 30, 40, 50]))
  })

  it('fails with clear error when maxRelayMB === 0 or file exceeds relay limit', async () => {
    // 1. maxRelayMB === 0 服务端禁用中转
    const fetcherDisabled = vi.fn()
    const signalsDisabled: SignalType[] = []
    const updatesDisabled: TransferTask[] = []

    const mgrDisabled = new TransferManager({
      sendSignal: (_to, type) => signalsDisabled.push(type),
      getSessionToken: () => 'tok-1',
      getIceServers: () => [],
      getMaxRelayMB: () => 0,
      onUpdate: (t) => updatesDisabled.push(t),
      fetcher: fetcherDisabled,
      connectTimeoutMs: 8000
    })

    const smallFile = new File([new Uint8Array([1, 2, 3])], 'small.bin')
    const tx1 = mgrDisabled.startOutgoing('peer-b', 'Phone', [smallFile])
    await mgrDisabled.handleSignal('peer-b', 'Phone', 'file.accept', tx1.transferId, {})
    await vi.advanceTimersByTimeAsync(8000)

    expect(signalsDisabled).not.toContain('relay.start')
    expect(fetcherDisabled).not.toHaveBeenCalled()
    expect(updatesDisabled.at(-1)?.state).toBe('failed')
    expect(updatesDisabled.at(-1)?.errorMessage).toContain('服务端已禁用中转')

    // 2. 文件大小超出 maxRelayMB 限额（例如限额 1MB，文件 2MB）
    const fetcherLimit = vi.fn()
    const signalsLimit: SignalType[] = []
    const updatesLimit: TransferTask[] = []

    const mgrLimit = new TransferManager({
      sendSignal: (_to, type) => signalsLimit.push(type),
      getSessionToken: () => 'tok-2',
      getIceServers: () => [],
      getMaxRelayMB: () => 1,
      onUpdate: (t) => updatesLimit.push(t),
      fetcher: fetcherLimit,
      connectTimeoutMs: 8000
    })

    const largeFile = new File([new Uint8Array(2 * 1024 * 1024)], 'huge.bin')
    const tx2 = mgrLimit.startOutgoing('peer-b', 'Phone', [largeFile])
    await mgrLimit.handleSignal('peer-b', 'Phone', 'file.accept', tx2.transferId, {})

    // 模拟 ICE 失败触发即时降级检测
    const pc = MockPeerConnection.instances.at(-1)!
    pc.iceConnectionState = 'failed'
    pc.oniceconnectionstatechange?.()
    await vi.advanceTimersByTimeAsync(0)

    expect(signalsLimit).not.toContain('relay.start')
    expect(fetcherLimit).not.toHaveBeenCalled()
    expect(updatesLimit.at(-1)?.state).toBe('failed')
    expect(updatesLimit.at(-1)?.errorMessage).toContain('1 MB 中转限额')
  })

  it('serializes rapid synchronous burst into async OpfsWorkerSink without dropping chunks', async () => {
    const writtenChunks: Uint8Array[] = []
    class AsyncDelayOpfsWorker {
      onmessage: ((ev: MessageEvent<{ id: number; ok: boolean; file?: File; size?: number }>) => void) | null = null
      onerror: ((ev: ErrorEvent) => void) | null = null
      terminated = false

      postMessage(data: { id: number; cmd: string; chunk?: ArrayBuffer }): void {
        if (data.cmd === 'write' && data.chunk) {
          writtenChunks.push(new Uint8Array(data.chunk))
        }
        // 故意用 setTimeout 制造多轮异步延迟，验证 receiveQueue 严格串行化
        setTimeout(() => {
          if (data.cmd === 'finalize') {
            const total = writtenChunks.reduce((acc, c) => acc + c.byteLength, 0)
            const merged = new Uint8Array(total)
            let off = 0
            for (const c of writtenChunks) {
              merged.set(c, off)
              off += c.byteLength
            }
            const file = new File([merged], 'streamed.bin', { type: 'application/octet-stream' })
            this.onmessage?.({ data: { id: data.id, ok: true, file, size: total } } as MessageEvent)
          } else {
            this.onmessage?.({ data: { id: data.id, ok: true } } as MessageEvent)
          }
        }, 5)
      }

      terminate(): void {
        this.terminated = true
      }
    }

    globalThis.Worker = AsyncDelayOpfsWorker as unknown as typeof Worker
    Object.defineProperty(navigator, 'storage', {
      configurable: true,
      value: {
        getDirectory: vi.fn().mockResolvedValue({
          getDirectoryHandle: vi.fn().mockResolvedValue({ removeEntry: vi.fn().mockResolvedValue(undefined) })
        })
      }
    })

    const updates: TransferTask[] = []
    const mgr = new TransferManager({
      sendSignal: vi.fn(),
      getSessionToken: () => 'tok-opfs',
      getIceServers: () => [],
      getMaxRelayMB: () => 100,
      onUpdate: (t) => updates.push(t)
    })

    await mgr.handleSignal(
      'peer-a',
      'Laptop',
      'file.offer',
      'tx-opfs-burst',
      { files: [{ name: 'streamed.bin', size: 6, mime: 'application/octet-stream' }] },
      true
    )
    await mgr.handleSignal('peer-a', 'Laptop', 'rtc.offer', 'tx-opfs-burst', { type: 'offer', sdp: 'v=0' })

    const dc = new MockDataChannel()
    MockPeerConnection.instances[0].triggerDataChannel(dc)

    // 同一同步调用栈内连发 file.start -> 两片二进制分片 -> file.end
    dc.onmessage?.({
      data: JSON.stringify({
        type: 'file.start',
        transferId: 'tx-opfs-burst',
        fileIndex: 0,
        name: 'streamed.bin',
        size: 6,
        mime: 'application/octet-stream'
      })
    })
    dc.onmessage?.({ data: new Uint8Array([1, 2, 3]).buffer })
    dc.onmessage?.({ data: new Uint8Array([4, 5, 6]).buffer })
    dc.onmessage?.({
      data: JSON.stringify({
        type: 'file.end',
        transferId: 'tx-opfs-burst',
        fileIndex: 0,
        totalBytes: 6
      })
    })

    await vi.advanceTimersByTimeAsync(50)

    const finalTask = updates.at(-1)!
    expect(finalTask.state).toBe('completed')
    expect(finalTask.receivedFiles).toHaveLength(1)
    expect(finalTask.receivedFiles[0]).toMatchObject({
      name: 'streamed.bin',
      size: 6,
      storageMode: 'opfs'
    })
    expect(clickSpy).toHaveBeenCalledTimes(1)
  })

  it('supports backpressure via onbufferedamountlow property and marks relay download failure cleanly', async () => {
    // 1. 无 addEventListener 的精简 DataChannel Mock，验证 onbufferedamountlow 兜底分支与 0 字节文件发送
    const minimalDc = {
      binaryType: 'arraybuffer' as BinaryType,
      bufferedAmount: 0,
      bufferedAmountLowThreshold: 0,
      onopen: null as (() => void) | null,
      onclose: null as (() => void) | null,
      onmessage: null as ((ev: { data: string | ArrayBuffer }) => void) | null,
      onbufferedamountlow: null as (() => void) | null,
      sent: [] as Array<string | ArrayBuffer>,
      send(data: string | ArrayBuffer) {
        this.sent.push(data)
        if (data instanceof ArrayBuffer) {
          this.bufferedAmount = 1024 * 1024
        }
      },
      close() {}
    }

    const origCreateDc = MockPeerConnection.prototype.createDataChannel
    MockPeerConnection.prototype.createDataChannel = () => minimalDc as unknown as RTCDataChannel

    try {
      const updates: TransferTask[] = []
      const mgr = new TransferManager({
        sendSignal: vi.fn(),
        getSessionToken: () => 'tok-min',
        getIceServers: () => [],
        getMaxRelayMB: () => 50,
        onUpdate: (t) => updates.push(t)
      })

      const emptyFile = new File([], 'empty.txt', { type: 'text/plain' })
      const nonEmptyFile = new File([new Uint8Array(20 * 1024)], 'part.bin')
      const tx = mgr.startOutgoing('peer-b', 'Phone', [emptyFile, nonEmptyFile])
      await mgr.handleSignal('peer-b', 'Phone', 'file.accept', tx.transferId, {})

      minimalDc.onopen?.()
      await vi.advanceTimersByTimeAsync(10)

      // 第一片 16KB 发送后触发 bufferedAmount >= 1MB 暂停
      expect(updates.at(-1)?.state).toBe('transferring')
      minimalDc.bufferedAmount = 0
      minimalDc.onbufferedamountlow?.()
      await vi.advanceTimersByTimeAsync(10)

      // 第二片 4KB 发送后再次置 bufferedAmount = 1MB 并发送 file.end，此时应等待缓冲区排空为 0 后才标记 completed
      expect(updates.at(-1)?.state).toBe('transferring')
      expect(minimalDc.bufferedAmountLowThreshold).toBe(0)
      minimalDc.bufferedAmount = 0
      minimalDc.onbufferedamountlow?.()
      await vi.advanceTimersByTimeAsync(10)

      expect(updates.at(-1)?.state).toBe('completed')
      await vi.advanceTimersByTimeAsync(2000)
    } finally {
      MockPeerConnection.prototype.createDataChannel = origCreateDc
    }

    // 2. 服务端中转下载 HTTP 非 200 错误时，receiver 应清理活跃 sink 并标记 failed
    const errFetcher = vi.fn().mockResolvedValue(new Response('bad gateway', { status: 502 }))
    const rxUpdates: TransferTask[] = []
    const rxMgr = new TransferManager({
      sendSignal: vi.fn(),
      getSessionToken: () => 'tok-rx',
      getIceServers: () => [],
      getMaxRelayMB: () => 50,
      onUpdate: (t) => rxUpdates.push(t),
      fetcher: errFetcher
    })

    await rxMgr.handleSignal(
      'peer-a',
      'Laptop',
      'file.offer',
      'tx-relay-err',
      { files: [{ name: 'fail.bin', size: 10, mime: 'application/octet-stream' }] },
      true
    )
    await rxMgr.handleSignal('peer-a', 'Laptop', 'relay.start', 'tx-relay-err', {})
    expect(rxUpdates.at(-1)?.state).toBe('failed')
    expect(rxUpdates.at(-1)?.errorMessage).toContain('relay download failed: 502')
  })

  it('waits for DataChannel bufferedAmount to drain to 0 before marking completed and waits grace period before closing PC', async () => {
    // 1. bufferedAmount 未排空前不关闭 PC，排空后等待对端关闭通道（或在 2s 宽限期后正常收尾）
    const updates: TransferTask[] = []
    const mgr = new TransferManager({
      sendSignal: vi.fn(),
      getSessionToken: () => 'tok-drain',
      getIceServers: () => [],
      getMaxRelayMB: () => 100,
      onUpdate: (t) => updates.push(t)
    })

    const f1 = new File([new Uint8Array([1, 2, 3, 4, 5])], 'drain.bin')
    const t1 = mgr.startOutgoing('peer-b', 'Phone', [f1])
    await mgr.handleSignal('peer-b', 'Phone', 'file.accept', t1.transferId, {})

    const pc1 = MockPeerConnection.instances[0]
    const dc1 = pc1.createdDataChannel!
    dc1.onSend = (data, dc) => {
      if (typeof data === 'string' && JSON.parse(data).type === 'file.end') {
        dc.bufferedAmount = 512
      }
    }

    dc1.emitEvent('open')
    await vi.advanceTimersByTimeAsync(10)

    expect(dc1.bufferedAmountLowThreshold).toBe(0)
    expect(updates.at(-1)?.state).toBe('transferring')
    expect(pc1.closed).toBe(false)

    // 尚未完全降为 0 时继续等待
    dc1.bufferedAmount = 128
    dc1.emitEvent('bufferedamountlow')
    await vi.advanceTimersByTimeAsync(10)
    expect(updates.at(-1)?.state).toBe('transferring')
    expect(pc1.closed).toBe(false)

    // 完全排空为 0 后标记 completed，并在 2s 宽限期内等待接收方关闭 DC
    dc1.bufferedAmount = 0
    dc1.emitEvent('bufferedamountlow')
    await vi.advanceTimersByTimeAsync(10)
    expect(updates.at(-1)?.state).toBe('completed')
    expect(pc1.closed).toBe(false)

    // 模拟接收端处理完 file.end 后关闭 DataChannel，不应误判失败且立即关闭 PC
    dc1.close()
    await vi.advanceTimersByTimeAsync(0)
    expect(updates.at(-1)?.state).toBe('completed')
    expect(pc1.closed).toBe(true)

    // 2. 若接收端未显式关闭 DC，2s 宽限期到期后亦应正常关闭 PC
    const t2 = mgr.startOutgoing('peer-b', 'Phone', [f1])
    await mgr.handleSignal('peer-b', 'Phone', 'file.accept', t2.transferId, {})
    const pc2 = MockPeerConnection.instances[1]
    const dc2 = pc2.createdDataChannel!
    dc2.emitEvent('open')
    await vi.advanceTimersByTimeAsync(10)

    expect(updates.at(-1)?.state).toBe('completed')
    expect(pc2.closed).toBe(false)
    await vi.advanceTimersByTimeAsync(2000)
    expect(pc2.closed).toBe(true)

    // 3. 若对端卡死导致 bufferedAmount 15s 内未排空，应超时失败并关闭 PC
    const t3 = mgr.startOutgoing('peer-b', 'Phone', [f1])
    await mgr.handleSignal('peer-b', 'Phone', 'file.accept', t3.transferId, {})
    const pc3 = MockPeerConnection.instances[2]
    const dc3 = pc3.createdDataChannel!
    dc3.onSend = (data, dc) => {
      if (typeof data === 'string' && JSON.parse(data).type === 'file.end') {
        dc.bufferedAmount = 1024
      }
    }
    dc3.emitEvent('open')
    await vi.advanceTimersByTimeAsync(10)
    expect(dc3.bufferedAmountLowThreshold).toBe(0)
    expect(updates.at(-1)?.state).toBe('transferring')

    await vi.advanceTimersByTimeAsync(14980)
    expect(updates.at(-1)?.state).toBe('transferring')
    expect(pc3.closed).toBe(false)

    await vi.advanceTimersByTimeAsync(20)
    expect(updates.at(-1)?.state).toBe('failed')
    expect(pc3.closed).toBe(true)
  })

  it('ignores duplicate rtc.offer without leaking RTCPeerConnection and aborts signaling if cancelled during setLocalDescription', async () => {
    const signals: Array<{ type: SignalType; id: string }> = []
    const mgr = new TransferManager({
      sendSignal: (_to, type, id) => signals.push({ type, id }),
      getSessionToken: () => 'tok-dup',
      getIceServers: () => [],
      getMaxRelayMB: () => 100,
      onUpdate: vi.fn()
    })

    // 1. 接收端在首条 rtc.offer 协商期间及协商完成后收到重复 rtc.offer，只创建 1 个 PC 并只回复 1 次 rtc.answer
    let resolveRemote!: () => void
    const origSetRemote = MockPeerConnection.prototype.setRemoteDescription
    MockPeerConnection.prototype.setRemoteDescription = () =>
      new Promise<void>((resolve) => {
        resolveRemote = resolve
      })

    try {
      await mgr.handleSignal(
        'peer-a',
        'Laptop',
        'file.offer',
        'tx-dup-offer',
        { files: [{ name: 'dup.bin', size: 4, mime: 'application/octet-stream' }] },
        true
      )

      const firstOfferPromise = mgr.handleSignal('peer-a', 'Laptop', 'rtc.offer', 'tx-dup-offer', {
        type: 'offer',
        sdp: 'v=0-1'
      })
      const dupOfferDuringHandshake = mgr.handleSignal('peer-a', 'Laptop', 'rtc.offer', 'tx-dup-offer', {
        type: 'offer',
        sdp: 'v=0-2'
      })

      expect(MockPeerConnection.instances).toHaveLength(1)
      resolveRemote()
      await Promise.all([firstOfferPromise, dupOfferDuringHandshake])

      // 协商完成且已有 PC 时再来重复 offer 同样忽略
      await mgr.handleSignal('peer-a', 'Laptop', 'rtc.offer', 'tx-dup-offer', {
        type: 'offer',
        sdp: 'v=0-3'
      })
      expect(MockPeerConnection.instances).toHaveLength(1)
      expect(signals.filter((s) => s.type === 'rtc.answer' && s.id === 'tx-dup-offer')).toHaveLength(1)
    } finally {
      MockPeerConnection.prototype.setRemoteDescription = origSetRemote
    }

    // 2. setLocalDescription 异步等待期间取消传输，不应再发送 rtc.offer / rtc.answer
    let resolveLocalSender!: () => void
    const origSetLocal = MockPeerConnection.prototype.setLocalDescription
    MockPeerConnection.prototype.setLocalDescription = () =>
      new Promise<void>((resolve) => {
        resolveLocalSender = resolve
      })

    try {
      const f = new File([new Uint8Array([1, 2])], 'cancel.bin')
      const txSend = mgr.startOutgoing('peer-b', 'Phone', [f])
      const acceptPromise = mgr.handleSignal('peer-b', 'Phone', 'file.accept', txSend.transferId, {})
      await vi.advanceTimersByTimeAsync(0)
      mgr.cancelTransfer(txSend.transferId)
      resolveLocalSender()
      await acceptPromise
      expect(signals.some((s) => s.type === 'rtc.offer' && s.id === txSend.transferId)).toBe(false)

      let resolveLocalReceiver!: () => void
      MockPeerConnection.prototype.setLocalDescription = () =>
        new Promise<void>((resolve) => {
          resolveLocalReceiver = resolve
        })
      await mgr.handleSignal(
        'peer-a',
        'Laptop',
        'file.offer',
        'tx-rx-abort',
        { files: [{ name: 'abort.bin', size: 4, mime: 'application/octet-stream' }] },
        true
      )
      const rxOfferPromise = mgr.handleSignal('peer-a', 'Laptop', 'rtc.offer', 'tx-rx-abort', {
        type: 'offer',
        sdp: 'v=0-rx'
      })
      await vi.advanceTimersByTimeAsync(0)
      mgr.cancelTransfer('tx-rx-abort')
      resolveLocalReceiver()
      await rxOfferPromise
      expect(signals.some((s) => s.type === 'rtc.answer' && s.id === 'tx-rx-abort')).toBe(false)
    } finally {
      MockPeerConnection.prototype.setLocalDescription = origSetLocal
    }
  })

  it('ignores relay.start while pending and validates blank transferId, malformed answer SDP and offered metadata sizes', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(new Uint8Array([1, 2, 3]), { status: 200 }))
    const rxUpdates: TransferTask[] = []
    const rxMgr = new TransferManager({
      sendSignal: vi.fn(),
      getSessionToken: () => 'tok-guard',
      getIceServers: () => [],
      getMaxRelayMB: () => 50,
      onUpdate: (t) => rxUpdates.push(t),
      fetcher
    })

    // 1. 空白 transferId 应直接忽略
    await rxMgr.handleSignal('peer-a', 'Laptop', 'file.offer', '   ', {
      files: [{ name: 'ignored.bin', size: 3, mime: 'application/octet-stream' }]
    })
    expect(rxUpdates).toHaveLength(0)

    // 2. pending 状态收到 relay.start 不得触发中转下载
    await rxMgr.handleSignal('peer-a', 'Laptop', 'file.offer', 'tx-pending-relay', {
      files: [{ name: 'secret.bin', size: 3, mime: 'application/octet-stream' }]
    })
    expect(rxUpdates.at(-1)?.state).toBe('pending')

    await rxMgr.handleSignal('peer-a', 'Laptop', 'relay.start', 'tx-pending-relay', {})
    expect(fetcher).not.toHaveBeenCalled()
    expect(rxUpdates.at(-1)?.state).toBe('pending')
    expect(rxUpdates.at(-1)?.channel).toBe('webrtc')

    // 3. file.start 声明的 size 与 file.offer 不一致时快速失败
    rxMgr.acceptIncoming('tx-pending-relay')
    await rxMgr.handleSignal('peer-a', 'Laptop', 'rtc.offer', 'tx-pending-relay', { type: 'offer', sdp: 'v=0' })
    const rxPc = MockPeerConnection.instances.at(-1)!
    const rxDc = new MockDataChannel()
    rxPc.triggerDataChannel(rxDc)
    rxDc.onmessage?.({
      data: JSON.stringify({
        type: 'file.start',
        transferId: 'tx-pending-relay',
        fileIndex: 0,
        name: 'secret.bin',
        size: 999,
        mime: 'application/octet-stream'
      })
    })
    await vi.advanceTimersByTimeAsync(10)
    expect(rxUpdates.at(-1)?.state).toBe('failed')
    expect(rxUpdates.at(-1)?.errorMessage).toContain('size mismatch')

    // 3b. file.end 声明的 totalBytes 与 file.offer 不一致时快速失败
    await rxMgr.handleSignal(
      'peer-a',
      'Laptop',
      'file.offer',
      'tx-end-mismatch',
      { files: [{ name: 'endbad.bin', size: 3, mime: 'application/octet-stream' }] },
      true
    )
    await rxMgr.handleSignal('peer-a', 'Laptop', 'rtc.offer', 'tx-end-mismatch', { type: 'offer', sdp: 'v=0' })
    const rxPc2 = MockPeerConnection.instances.at(-1)!
    const rxDc2 = new MockDataChannel()
    rxPc2.triggerDataChannel(rxDc2)
    rxDc2.onmessage?.({
      data: JSON.stringify({
        type: 'file.start',
        transferId: 'tx-end-mismatch',
        fileIndex: 0,
        name: 'endbad.bin',
        size: 3,
        mime: 'application/octet-stream'
      })
    })
    rxDc2.onmessage?.({ data: new Uint8Array([1, 2]).buffer })
    rxDc2.onmessage?.({
      data: JSON.stringify({
        type: 'file.end',
        transferId: 'tx-end-mismatch',
        fileIndex: 0,
        totalBytes: 2
      })
    })
    await vi.advanceTimersByTimeAsync(10)
    expect(rxUpdates.at(-1)?.state).toBe('failed')
    expect(rxUpdates.at(-1)?.errorMessage).toContain('file.end size mismatch')

    // 4. rtc.answer 携带非法 SDP 导致 setRemoteDescription 抛错时应清理 PC 并降级（或按限额失败）而不抛出未捕获异常
    const origSetRemote = MockPeerConnection.prototype.setRemoteDescription
    MockPeerConnection.prototype.setRemoteDescription = async () => {
      throw new Error('InvalidAccessError: malformed SDP')
    }
    try {
      const txUpdates: TransferTask[] = []
      const txSignals: SignalType[] = []
      const txMgr = new TransferManager({
        sendSignal: (_to, type) => txSignals.push(type),
        getSessionToken: () => 'tok-sdp',
        getIceServers: () => [],
        getMaxRelayMB: () => 0,
        onUpdate: (t) => txUpdates.push(t)
      })
      const f = new File([new Uint8Array([1, 2])], 'sdp.bin')
      const tx = txMgr.startOutgoing('peer-b', 'Phone', [f])
      await txMgr.handleSignal('peer-b', 'Phone', 'file.accept', tx.transferId, {})
      const txPc = MockPeerConnection.instances.at(-1)!
      await txMgr.handleSignal('peer-b', 'Phone', 'rtc.ice', tx.transferId, {
        candidate: 'candidate:1 1 udp 1 127.0.0.1 5000 typ host'
      })
      await txMgr.handleSignal('peer-b', 'Phone', 'rtc.answer', tx.transferId, { type: 'answer', sdp: 'bad-sdp' })
      expect(txPc.closed).toBe(true)
      expect(txPc.addedIceCandidates).toHaveLength(0)
      expect(txUpdates.at(-1)?.state).toBe('failed')
      expect(txSignals).not.toContain('relay.start')
    } finally {
      MockPeerConnection.prototype.setRemoteDescription = origSetRemote
    }

    // 5. !res.body 降级分支在 arrayBuffer() 返回前被取消时应中止写入与完成
    let resolveArrayBuf!: (buf: ArrayBuffer) => void
    const bodylessFetcher = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      body: null,
      arrayBuffer: () =>
        new Promise<ArrayBuffer>((resolve) => {
          resolveArrayBuf = resolve
        })
    } as unknown as Response)
    const abortUpdates: TransferTask[] = []
    const abortMgr = new TransferManager({
      sendSignal: vi.fn(),
      getSessionToken: () => 'tok-ab',
      getIceServers: () => [],
      getMaxRelayMB: () => 50,
      onUpdate: (t) => abortUpdates.push(t),
      fetcher: bodylessFetcher
    })
    await abortMgr.handleSignal(
      'peer-a',
      'Laptop',
      'file.offer',
      'tx-bodyless-abort',
      { files: [{ name: 'ab.bin', size: 3, mime: 'application/octet-stream' }] },
      true
    )
    const relayPromise = abortMgr.handleSignal('peer-a', 'Laptop', 'relay.start', 'tx-bodyless-abort', {})
    await vi.advanceTimersByTimeAsync(0)
    abortMgr.cancelTransfer('tx-bodyless-abort')
    resolveArrayBuf(new Uint8Array([1, 2, 3]).buffer)
    await relayPromise
    expect(abortUpdates.at(-1)?.state).toBe('cancelled')
    expect(abortUpdates.at(-1)?.receivedFiles).toHaveLength(0)
  })
})
