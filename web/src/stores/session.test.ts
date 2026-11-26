import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createSessionStore } from './session'

class MockWebSocket {
  static instances: MockWebSocket[] = []
  readyState = 0
  sent: Array<{ type: string; payload: unknown }> = []
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null

  constructor(public readonly url: string) {
    MockWebSocket.instances.push(this)
  }

  open(): void {
    this.readyState = 1
    this.onopen?.()
  }

  send(data: string): void {
    this.sent.push(JSON.parse(data) as { type: string; payload: unknown })
  }

  close(): void {
    if (this.readyState === 3) return
    this.readyState = 3
    this.onclose?.()
  }
}

function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const mockCheckStorageQuota = vi.fn()

vi.mock('../lib/opfsSink', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/opfsSink')>()
  return {
    ...actual,
    checkStorageQuota: (...args: Parameters<typeof actual.checkStorageQuota>) => mockCheckStorageQuota(...args)
  }
})

describe('session store', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    MockWebSocket.instances = []
    window.history.replaceState({}, '', '/')
    let seq = 0
    URL.createObjectURL = vi.fn(() => `blob:preview-${++seq}`)
    URL.revokeObjectURL = vi.fn()
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
    mockCheckStorageQuota.mockReset()
    mockCheckStorageQuota.mockResolvedValue({ ok: true, availableBytes: 100_000_000 })
  })

  it('handles welcome, space.state, room URL sync and idempotent timeline deduplication', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(new Blob(['img']), { status: 200 }))
    const store = createSessionStore({ fetcher })

    store.handleServerEvent('welcome', {
      peerId: 'p-self',
      sessionToken: 'tok-self',
      self: { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      iceServers: [],
      limits: { maxTimelineFileMB: 20, maxRelayMB: 100 }
    })

    expect(store.state.sessionToken).toBe('tok-self')
    expect(store.state.limits.maxRelayMB).toBe(100)

    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
      peers: [
        { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
        { id: 'p-other', name: 'iPhone', deviceType: 'mobile', os: 'iOS', browser: 'Safari' }
      ],
      timeline: []
    })

    expect(window.location.search).toBe('?room=482917')
    expect(store.state.peers).toHaveLength(2)

    const imgItem = {
      id: 'item-1',
      kind: 'image',
      senderId: 'p-self',
      senderName: 'Mac Chrome',
      blobId: 'blob-1',
      fileName: 'shot.png',
      mime: 'image/png',
      size: 3,
      createdAt: 1000,
      expiresAt: 2000
    }
    store.handleServerEvent('timeline.item', { item: imgItem })
    store.handleServerEvent('timeline.item', { item: imgItem }) // 幂等去重
    expect(store.state.timeline).toHaveLength(1)

    await vi.waitFor(() => {
      expect(store.state.blobPreviewUrls['blob-1']).toBe('blob:preview-1')
    })

    store.handleServerEvent('timeline.deleted', { itemId: 'item-1' })
    expect(store.state.timeline).toHaveLength(0)
    expect(store.state.blobPreviewUrls['blob-1']).toBeUndefined()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-1')

    store.destroy()
  })

  it('revokes and clears ObjectURLs on timeline.cleared, space.state switch, and destroy', async () => {
    const fetcher = vi.fn().mockImplementation(async () => new Response(new Blob(['img']), { status: 200 }))
    const store = createSessionStore({ fetcher })

    store.handleServerEvent('welcome', {
      peerId: 'p-self',
      sessionToken: 'tok-self',
      self: { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      iceServers: [],
      limits: { maxTimelineFileMB: 20, maxRelayMB: 100 }
    })

    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
      peers: [],
      timeline: [
        {
          id: 'item-1',
          kind: 'image',
          senderId: 'p-self',
          senderName: 'Mac Chrome',
          blobId: 'blob-1',
          fileName: 'a.png',
          mime: 'image/png',
          size: 3,
          createdAt: 1000,
          expiresAt: 2000
        }
      ]
    })

    await vi.waitFor(() => {
      expect(store.state.blobPreviewUrls['blob-1']).toBe('blob:preview-1')
    })

    store.handleServerEvent('timeline.cleared', {})
    expect(store.state.timeline).toHaveLength(0)
    expect(store.state.blobPreviewUrls).toEqual({})
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-1')

    store.handleServerEvent('timeline.item', {
      item: {
        id: 'item-2',
        kind: 'image',
        senderId: 'p-self',
        senderName: 'Mac Chrome',
        blobId: 'blob-2',
        fileName: 'b.png',
        mime: 'image/png',
        size: 3,
        createdAt: 1001,
        expiresAt: 2001
      }
    })

    await vi.waitFor(() => {
      expect(store.state.blobPreviewUrls['blob-2']).toBe('blob:preview-2')
    })

    store.handleServerEvent('space.state', {
      space: { kind: 'lan', sharedTimelineEnabled: false },
      peers: [],
      timeline: []
    })
    expect(window.location.search).toBe('')
    expect(store.state.blobPreviewUrls).toEqual({})
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-2')

    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '123456', sharedTimelineEnabled: true },
      peers: [],
      timeline: [
        {
          id: 'item-3',
          kind: 'image',
          senderId: 'p-self',
          senderName: 'Mac Chrome',
          blobId: 'blob-3',
          fileName: 'c.png',
          mime: 'image/png',
          size: 3,
          createdAt: 1002,
          expiresAt: 2002
        }
      ]
    })

    await vi.waitFor(() => {
      expect(store.state.blobPreviewUrls['blob-3']).toBe('blob:preview-3')
    })

    store.destroy()
    expect(store.state.blobPreviewUrls).toEqual({})
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-3')
  })

  it('checks storage quota and warns via toast before accepting incoming transfer, and revokes received URL on dismissTransfer', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(new Uint8Array([1, 2, 3]), { status: 200 }))
    const store = createSessionStore({ fetcher })

    store.handleServerEvent('welcome', {
      peerId: 'p-self',
      sessionToken: 'tok-self',
      self: { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      iceServers: [],
      limits: { maxTimelineFileMB: 20, maxRelayMB: 100 }
    })

    store.handleServerEvent('space.state', {
      space: { kind: 'lan', sharedTimelineEnabled: false },
      peers: [
        { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
        { id: 'p-other', name: 'iPhone', deviceType: 'mobile', os: 'iOS', browser: 'Safari' }
      ],
      timeline: []
    })

    store.handleServerEvent('rtc.signal', {
      fromPeerId: 'p-other',
      signalType: 'file.offer',
      transferId: 'tx-1',
      data: {
        files: [{ name: 'big.bin', size: 3, mime: 'application/octet-stream' }]
      }
    })

    expect(store.state.transfers).toHaveLength(1)
    expect(store.state.transfers[0].state).toBe('pending')

    mockCheckStorageQuota.mockResolvedValueOnce({ ok: false, availableBytes: 100 })
    await store.acceptIncoming('tx-1')

    expect(mockCheckStorageQuota).toHaveBeenCalledWith(3)
    expect(store.state.toast).not.toBeNull()
    expect(store.state.toast?.kind).toBe('error')
    expect(store.state.toast?.message).toContain('存储空间不足')
    expect(store.state.transfers[0].state).toBe('connecting')

    store.handleServerEvent('rtc.signal', {
      fromPeerId: 'p-other',
      signalType: 'relay.start',
      transferId: 'tx-1',
      data: {}
    })

    await vi.waitFor(() => {
      expect(store.state.transfers[0].state).toBe('completed')
    })

    const receivedUrl = store.state.transfers[0].receivedFiles[0]?.objectUrl
    expect(receivedUrl).toBe('blob:preview-1')

    store.dismissTransfer('tx-1')
    expect(store.state.transfers).toHaveLength(0)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-1')

    store.destroy()
  })

  it('handles peer lifecycle, dm.text, ROOM_NOT_FOUND URL reset, and uploadTimelineFile guards', async () => {
    const uploadedItem = {
      id: 'item-up-1',
      kind: 'file',
      senderId: 'p-self',
      senderName: 'Mac Chrome',
      blobId: 'blob-up-1',
      fileName: 'doc.txt',
      mime: 'text/plain',
      size: 5,
      createdAt: 1000,
      expiresAt: 2000
    }
    const fetcher = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ item: uploadedItem }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      })
    )
    const store = createSessionStore({ fetcher })

    store.handleServerEvent('welcome', {
      peerId: 'p-self',
      sessionToken: 'tok-self',
      self: { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      iceServers: [],
      limits: { maxTimelineFileMB: 1, maxRelayMB: 100 }
    })

    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '654321', sharedTimelineEnabled: true },
      peers: [{ id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' }],
      timeline: []
    })
    expect(window.location.search).toBe('?room=654321')

    const otherPeer = { id: 'p-2', name: 'Pixel', deviceType: 'mobile', os: 'Android', browser: 'Chrome' }
    store.handleServerEvent('peer.joined', { peer: otherPeer })
    store.selectPeer('p-2')
    expect(store.state.peers).toHaveLength(2)
    expect(store.state.selectedPeerId).toBe('p-2')

    store.handleServerEvent('peer.updated', { peer: { ...otherPeer, name: 'Pixel 9' } })
    expect(store.state.peers.find((p) => p.id === 'p-2')?.name).toBe('Pixel 9')

    store.handleServerEvent('dm.text', {
      id: 'dm-1',
      fromPeerId: 'p-2',
      fromName: 'Pixel 9',
      text: 'hi',
      createdAt: 1234
    })
    expect(store.state.dmsByPeer['p-2']).toHaveLength(1)

    store.handleServerEvent('peer.left', { peerId: 'p-2' })
    expect(store.state.selectedPeerId).toBeNull()
    expect(store.state.peers).toHaveLength(1)

    const hugeFile = new File([new Uint8Array(2 * 1024 * 1024)], 'huge.bin', { type: 'application/octet-stream' })
    expect(await store.uploadTimelineFile(hugeFile)).toBe(false)
    expect(store.state.toast?.kind).toBe('error')

    const okFile = new File(['hello'], 'doc.txt', { type: 'text/plain' })
    expect(await store.uploadTimelineFile(okFile)).toBe(true)
    expect(store.state.timeline).toHaveLength(1)
    expect(store.state.toast?.kind).toBe('success')

    store.handleServerEvent('error', { code: 'ROOM_NOT_FOUND', message: '房间不存在' })
    expect(window.location.search).toBe('')
    expect(store.state.toast?.kind).toBe('error')

    store.destroy()
  })

  it('disposes transfers, aborts inflight relay, revokes received ObjectURLs and isolates DMs on space switch', async () => {
    let relaySignal: AbortSignal | null | undefined
    const inflightRelay = createDeferred<Response>()
    const fetcher = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/api/relay/tx-done/')) {
        return Promise.resolve(new Response(new Uint8Array([1, 2, 3]), { status: 200 }))
      }
      if (url.includes('/api/relay/tx-active/')) {
        relaySignal = init?.signal
        return inflightRelay.promise
      }
      return Promise.resolve(new Response('not found', { status: 404 }))
    })
    const store = createSessionStore({ fetcher })

    store.handleServerEvent('welcome', {
      peerId: 'p-self',
      sessionToken: 'tok-self',
      self: { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      iceServers: [],
      limits: { maxTimelineFileMB: 20, maxRelayMB: 100 }
    })

    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
      peers: [
        { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
        { id: 'p-other', name: 'iPhone', deviceType: 'mobile', os: 'iOS', browser: 'Safari' }
      ],
      timeline: []
    })

    // 1. 完成一个接收任务，产出 receivedFiles[0].objectUrl
    store.handleServerEvent('rtc.signal', {
      fromPeerId: 'p-other',
      signalType: 'file.offer',
      transferId: 'tx-done',
      data: { files: [{ name: 'done.bin', size: 3, mime: 'application/octet-stream' }] }
    })
    await store.acceptIncoming('tx-done')
    store.handleServerEvent('rtc.signal', {
      fromPeerId: 'p-other',
      signalType: 'relay.start',
      transferId: 'tx-done',
      data: {}
    })
    await vi.waitFor(() => {
      expect(store.state.transfers.find((t) => t.transferId === 'tx-done')?.state).toBe('completed')
    })

    // 2. 启动一个在途 relay 接收任务
    store.handleServerEvent('rtc.signal', {
      fromPeerId: 'p-other',
      signalType: 'file.offer',
      transferId: 'tx-active',
      data: { files: [{ name: 'stream.bin', size: 5, mime: 'application/octet-stream' }] }
    })
    await store.acceptIncoming('tx-active')
    store.handleServerEvent('rtc.signal', {
      fromPeerId: 'p-other',
      signalType: 'relay.start',
      transferId: 'tx-active',
      data: {}
    })
    await vi.waitFor(() => {
      expect(relaySignal).toBeDefined()
    })
    expect(relaySignal?.aborted).toBe(false)

    // 3. 记录一条同空间私信
    store.handleServerEvent('dm.text', {
      id: 'dm-room-1',
      fromPeerId: 'p-other',
      fromName: 'iPhone',
      text: 'room secret',
      createdAt: 100
    })

    // 4. 同一空间重复下发 space.state（例如 WS 重连）不应销毁进行中传输或私信
    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
      peers: [
        { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
        { id: 'p-other', name: 'iPhone', deviceType: 'mobile', os: 'iOS', browser: 'Safari' }
      ],
      timeline: []
    })
    expect(store.state.transfers).toHaveLength(2)
    expect(store.state.dmsByPeer['p-other']).toHaveLength(1)
    expect(relaySignal?.aborted).toBe(false)

    // 5. 真正切换空间（切回 lan）应释放全部传输、中止在途请求、释放 ObjectURL 并隔离私信
    store.handleServerEvent('space.state', {
      space: { kind: 'lan', sharedTimelineEnabled: false },
      peers: [{ id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' }],
      timeline: []
    })
    expect(relaySignal?.aborted).toBe(true)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-1')
    expect(store.state.transfers).toEqual([])
    expect(store.state.dmsByPeer).toEqual({})

    // 释放挂起的 Promise 防止测试结束时未决
    inflightRelay.reject(new DOMException('aborted', 'AbortError'))

    // 6. 验证 destroy 清理 state.transfers 且之后 showToast 不再生效
    store.handleServerEvent('rtc.signal', {
      fromPeerId: 'p-other',
      signalType: 'file.offer',
      transferId: 'tx-lan',
      data: { files: [{ name: 'lan.bin', size: 1, mime: 'application/octet-stream' }] }
    })
    expect(store.state.transfers).toHaveLength(1)
    store.destroy()
    expect(store.state.transfers).toEqual([])
    store.state.toast = null
    store.showToast('after destroy', 'error')
    expect(store.state.toast).toBeNull()
  })

  it('handles uploadTimelineFile network/JSON failures and discards stale uploads across space switches', async () => {
    const deferredFetch = createDeferred<Response>()
    const fetcher = vi
      .fn()
      .mockRejectedValueOnce(new Error('network down'))
      .mockResolvedValueOnce(new Response('{invalid-json', { status: 200 }))
      .mockReturnValueOnce(deferredFetch.promise)

    const store = createSessionStore({ fetcher })
    store.handleServerEvent('welcome', {
      peerId: 'p-self',
      sessionToken: 'tok-self',
      self: { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      iceServers: [],
      limits: { maxTimelineFileMB: 10, maxRelayMB: 100 }
    })
    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
      peers: [],
      timeline: []
    })

    const file = new File(['hello'], 'note.txt', { type: 'text/plain' })

    // 1. 网络异常
    expect(await store.uploadTimelineFile(file)).toBe(false)
    expect(store.state.toast).toMatchObject({ kind: 'error', message: '上传至时间线失败' })

    // 2. 非法 JSON 响应
    expect(await store.uploadTimelineFile(file)).toBe(false)
    expect(store.state.toast).toMatchObject({ kind: 'error', message: '上传至时间线失败' })

    // 3. 上传期间切换空间，过期的上传完成结果应被丢弃
    store.state.toast = null
    const pendingUpload = store.uploadTimelineFile(file)
    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '999888', sharedTimelineEnabled: true },
      peers: [],
      timeline: []
    })
    deferredFetch.resolve(
      new Response(
        JSON.stringify({
          item: {
            id: 'stale-item',
            kind: 'file',
            senderId: 'p-self',
            senderName: 'Mac Chrome',
            blobId: 'blob-stale',
            fileName: 'note.txt',
            mime: 'text/plain',
            size: 5,
            createdAt: 100,
            expiresAt: 200
          }
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      )
    )
    expect(await pendingUpload).toBe(false)
    expect(store.state.timeline).toHaveLength(0)
    expect(store.state.toast).toBeNull()

    store.destroy()
  })

  it('does not prefetch kind=file previews and revokes :dl ObjectURL if item deleted or space switched mid-download', async () => {
    const deferredDl1 = createDeferred<Response>()
    const deferredDl2 = createDeferred<Response>()
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(new Response(new Blob(['ok']), { status: 200 }))
      .mockReturnValueOnce(deferredDl1.promise)
      .mockReturnValueOnce(deferredDl2.promise)

    const store = createSessionStore({ fetcher })
    store.handleServerEvent('welcome', {
      peerId: 'p-self',
      sessionToken: 'tok-self',
      self: { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      iceServers: [],
      limits: { maxTimelineFileMB: 10, maxRelayMB: 100 }
    })

    const fileItem1 = {
      id: 'item-file-1',
      kind: 'file' as const,
      senderId: 'p-self',
      senderName: 'Mac Chrome',
      blobId: 'blob-file-1',
      fileName: 'archive.zip',
      mime: 'application/zip',
      size: 2,
      createdAt: 100,
      expiresAt: 200
    }
    const fileItem2 = {
      id: 'item-file-2',
      kind: 'file' as const,
      senderId: 'p-self',
      senderName: 'Mac Chrome',
      blobId: 'blob-file-2',
      fileName: 'doc.pdf',
      mime: 'application/pdf',
      size: 2,
      createdAt: 101,
      expiresAt: 201
    }
    const fileItem3 = {
      id: 'item-file-3',
      kind: 'file' as const,
      senderId: 'p-self',
      senderName: 'Mac Chrome',
      blobId: 'blob-file-3',
      fileName: 'video.mp4',
      mime: 'video/mp4',
      size: 3,
      createdAt: 102,
      expiresAt: 202
    }

    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
      peers: [],
      timeline: [fileItem1, fileItem2, fileItem3]
    })

    // kind === 'file' 不应预拉取预览 ObjectURL
    await Promise.resolve()
    expect(fetcher).not.toHaveBeenCalled()
    expect(store.state.blobPreviewUrls).toEqual({})

    // 1. 正常下载触发浏览器保存
    await store.downloadTimelineBlob(fileItem1)
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(HTMLAnchorElement.prototype.click).toHaveBeenCalledTimes(1)

    // 2. 下载期间条目被删除：不触发下载并立即释放 :dl ObjectURL
    const clickSpy = vi.mocked(HTMLAnchorElement.prototype.click)
    clickSpy.mockClear()
    const dlDeleted = store.downloadTimelineBlob(fileItem2)
    store.handleServerEvent('timeline.deleted', { itemId: 'item-file-2' })
    deferredDl1.resolve(new Response(new Blob(['pdf']), { status: 200 }))
    await dlDeleted
    expect(clickSpy).not.toHaveBeenCalled()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-2')

    // 3. 下载期间切换空间：不触发下载并立即释放 :dl ObjectURL
    const dlSwitched = store.downloadTimelineBlob(fileItem3)
    store.handleServerEvent('space.state', {
      space: { kind: 'lan', sharedTimelineEnabled: false },
      peers: [],
      timeline: []
    })
    deferredDl2.resolve(new Response(new Blob(['vid']), { status: 200 }))
    await dlSwitched
    expect(clickSpy).not.toHaveBeenCalled()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-3')

    store.destroy()
  })

  it('handles room/timeline/DM WS commands and routes autoAcceptInRoom through quota-checked acceptIncoming', async () => {
    const store = createSessionStore({
      WebSocketImpl: MockWebSocket as unknown as typeof WebSocket
    })
    store.start()
    expect(MockWebSocket.instances).toHaveLength(1)
    const sock = MockWebSocket.instances[0]
    sock.open()

    // 清空 hello 握手消息便于断言
    sock.sent = []

    store.handleServerEvent('welcome', {
      peerId: 'p-self',
      sessionToken: 'tok-self',
      self: { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      iceServers: [],
      limits: { maxTimelineFileMB: 10, maxRelayMB: 100 }
    })
    store.handleServerEvent('space.state', {
      space: { kind: 'lan', sharedTimelineEnabled: false },
      peers: [
        { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
        { id: 'p-other', name: 'iPhone', deviceType: 'mobile', os: 'iOS', browser: 'Safari' }
      ],
      timeline: []
    })

    // 1. lan 空间下发送时间线文本应被拦截，空文本直接返回 false
    expect(store.postTimelineText('   ')).toBe(false)
    expect(store.postTimelineText('hello lan')).toBe(false)
    expect(store.state.toast?.kind).toBe('error')
    expect(sock.sent).toHaveLength(0)

    // 2. createRoom / joinRoom / returnToLan
    store.createRoom()
    expect(store.state.roomModalOpen).toBe(true)
    expect(sock.sent.at(-1)).toEqual({ type: 'room.create', payload: {} })

    expect(store.joinRoom('12ab')).toBe(false)
    expect(store.state.toast?.message).toContain('6 位数字')
    expect(store.joinRoom('482-917')).toBe(true)
    expect(store.state.roomModalOpen).toBe(false)
    expect(sock.sent.at(-1)).toEqual({ type: 'space.join', payload: { kind: 'room', code: '482917' } })

    // 切换到 room 空间
    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
      peers: [
        { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
        { id: 'p-other', name: 'iPhone', deviceType: 'mobile', os: 'iOS', browser: 'Safari' }
      ],
      timeline: []
    })

    // 3. room 开启共享时间线后可发送文本；sendDM 发送私信并记录 outgoing 消息
    expect(store.postTimelineText('hello room')).toBe(true)
    expect(sock.sent.at(-1)).toEqual({ type: 'timeline.post', payload: { text: 'hello room' } })

    expect(store.sendDM('p-other', '   ')).toBe(false)
    expect(store.sendDM('p-other', 'ping')).toBe(true)
    expect(sock.sent.at(-1)).toEqual({ type: 'dm.text', payload: { toPeerId: 'p-other', text: 'ping' } })
    expect(store.state.dmsByPeer['p-other']).toHaveLength(1)
    expect(store.state.dmsByPeer['p-other'][0]).toMatchObject({ text: 'ping', outgoing: true })

    // 4. room 内开启 autoAcceptInRoom 时，收到 file.offer 应经过 session.acceptIncoming 执行配额校验并提示
    store.state.autoAcceptInRoom = true
    mockCheckStorageQuota.mockResolvedValueOnce({ ok: false, availableBytes: 50 })
    store.handleServerEvent('rtc.signal', {
      fromPeerId: 'p-other',
      signalType: 'file.offer',
      transferId: 'tx-auto',
      data: { files: [{ name: 'auto.bin', size: 1024, mime: 'application/octet-stream' }] }
    })

    await vi.waitFor(() => {
      expect(mockCheckStorageQuota).toHaveBeenCalledWith(1024)
      expect(store.state.transfers[0]?.state).toBe('connecting')
    })
    expect(store.state.toast).toMatchObject({ kind: 'error' })
    expect(store.state.toast?.message).toContain('存储空间不足')
    expect(sock.sent.at(-1)).toEqual({
      type: 'rtc.signal',
      payload: { toPeerId: 'p-other', signalType: 'file.accept', transferId: 'tx-auto', data: {} }
    })

    // 5. acceptIncoming 等待配额期间若切换空间，应放弃发送 file.accept
    const deferredQuota = createDeferred<{ ok: boolean; availableBytes: number | null }>()
    mockCheckStorageQuota.mockReturnValueOnce(deferredQuota.promise)
    store.state.autoAcceptInRoom = false
    store.handleServerEvent('rtc.signal', {
      fromPeerId: 'p-other',
      signalType: 'file.offer',
      transferId: 'tx-race',
      data: { files: [{ name: 'race.bin', size: 2048, mime: 'application/octet-stream' }] }
    })
    const acceptPromise = store.acceptIncoming('tx-race')
    const sentCountBeforeSwitch = sock.sent.length
    store.returnToLan()
    expect(sock.sent.at(-1)).toEqual({ type: 'space.join', payload: { kind: 'lan' } })
    store.handleServerEvent('space.state', {
      space: { kind: 'lan', sharedTimelineEnabled: false },
      peers: [{ id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' }],
      timeline: []
    })
    store.state.toast = null
    deferredQuota.resolve({ ok: false, availableBytes: 10 })
    await acceptPromise
    expect(store.state.toast).toBeNull()
    expect(sock.sent.filter((m) => m.type === 'rtc.signal').length).toBe(
      sock.sent.slice(0, sentCountBeforeSwitch).filter((m) => m.type === 'rtc.signal').length
    )

    store.destroy()
  })
})
